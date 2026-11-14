// Package imagestore 把图片保存到网关数据目录，并以只读静态路由对外提供。
//
// 设计目标：
//   - 文件名使用高熵随机串，不可枚举；知道链接即可访问（不额外签名）。
//   - 写入使用临时文件 + rename，避免读到半截图片。
//   - 到期文件由启动时与定时任务清理，保留期可配置（默认 1 小时）。
package imagestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultRetention 图片默认保留时长。
	DefaultRetention = time.Hour
	// MaxImageBytes 生图结果单张保存上限。
	MaxImageBytes = 64 << 20
	// DefaultUploadMaxBytes 上传图片默认上限。
	DefaultUploadMaxBytes = 100 << 20
	// URLPrefix 生图结果的对外访问前缀。
	URLPrefix = "/v1/images/files/"
	// UploadURLPrefix 上传图片的对外访问前缀。
	UploadURLPrefix = "/v1/uploads/"

	tmpPrefix = ".upload-"
)

// 上传相关错误，供 HTTP 层映射为 413 / 415。
var (
	ErrTooLarge        = errors.New("图片超过大小上限")
	ErrEmptyImage      = errors.New("图片内容为空")
	ErrUnsupportedType = errors.New("不支持的图片类型")
)

// namePattern 只接受本包生成的文件名，杜绝路径穿越与任意文件读取。
var namePattern = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|webp|gif|bin)$`)

var extByType = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

var typeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".bin":  "application/octet-stream",
}

// Store 图片存储。
type Store struct {
	dir       string
	prefix    string
	retention time.Duration
	maxBytes  int64
}

// Saved 一次写入的结果。
type Saved struct {
	Name        string
	Bytes       int64
	ContentType string
}

// Open 打开生图结果目录（默认前缀与 64 MB 上限）。
func Open(dir string, retention time.Duration) (*Store, error) {
	return OpenWith(dir, URLPrefix, retention, MaxImageBytes)
}

// OpenWith 打开（必要时创建）图片目录，并先清理一批过期文件。
func OpenWith(dir, prefix string, retention time.Duration, maxBytes int64) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("图片目录为空")
	}
	if !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") {
		return nil, fmt.Errorf("图片访问前缀非法: %q", prefix)
	}
	if retention <= 0 {
		retention = DefaultRetention
	}
	if maxBytes <= 0 {
		maxBytes = MaxImageBytes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建图片目录失败: %w", err)
	}
	s := &Store{dir: dir, prefix: prefix, retention: retention, maxBytes: maxBytes}
	s.Cleanup(time.Now())
	return s, nil
}

// Dir 返回图片目录。
func (s *Store) Dir() string { return s.dir }

// Prefix 返回对外访问前缀。
func (s *Store) Prefix() string { return s.prefix }

// Retention 返回保留时长。
func (s *Store) Retention() time.Duration { return s.retention }

// MaxBytes 返回单张上限。
func (s *Store) MaxBytes() int64 { return s.maxBytes }

// URL 把文件名拼成对外访问路径。
func (s *Store) URL(name string) string { return s.prefix + name }

// Save 保存图片内容并返回文件名（不含路径）。contentType 为空时按内容嗅探。
func (s *Store) Save(data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", ErrEmptyImage
	}
	if int64(len(data)) > s.maxBytes {
		return "", fmt.Errorf("%w（上限 %d MB）", ErrTooLarge, s.maxBytes>>20)
	}
	ext := extFor(contentType, data)
	name, err := randomName(ext)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(s.dir, tmpPrefix+"*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("写入图片失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("写入图片失败: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("保存图片失败: %w", err)
	}
	return name, nil
}

// SaveReader 流式保存图片，不会把整个文件读进内存。
//
// maxBytes <= 0 时使用 Store 配置的上限；requireImage 为 true 时只接受
// PNG/JPEG/WebP/GIF，并按文件头判断（忽略调用方声明的 Content-Type）。
func (s *Store) SaveReader(r io.Reader, maxBytes int64, requireImage bool) (Saved, error) {
	if maxBytes <= 0 {
		maxBytes = s.maxBytes
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(r, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return Saved{}, fmt.Errorf("读取图片失败: %w", err)
	}
	if n == 0 {
		return Saved{}, ErrEmptyImage
	}
	head = head[:n]
	if int64(n) > maxBytes {
		return Saved{}, fmt.Errorf("%w（上限 %d MB）", ErrTooLarge, maxBytes>>20)
	}

	ext := extFor("", head)
	if requireImage && ext == ".bin" {
		return Saved{}, ErrUnsupportedType
	}
	name, err := randomName(ext)
	if err != nil {
		return Saved{}, err
	}

	tmp, err := os.CreateTemp(s.dir, tmpPrefix+"*")
	if err != nil {
		return Saved{}, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(head); err != nil {
		cleanup()
		return Saved{}, fmt.Errorf("写入图片失败: %w", err)
	}
	written := int64(n)
	// 多读 1 字节用于判断是否超限
	copied, err := io.Copy(tmp, io.LimitReader(r, maxBytes-written+1))
	if err != nil {
		cleanup()
		return Saved{}, fmt.Errorf("写入图片失败: %w", err)
	}
	written += copied
	if written > maxBytes {
		cleanup()
		return Saved{}, fmt.Errorf("%w（上限 %d MB）", ErrTooLarge, maxBytes>>20)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return Saved{}, fmt.Errorf("写入图片失败: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return Saved{}, fmt.Errorf("保存图片失败: %w", err)
	}

	ct, ok := typeByExt[ext]
	if !ok {
		ct = "application/octet-stream"
	}
	return Saved{Name: name, Bytes: written, ContentType: ct}, nil
}

// Load 读取已保存的图片，返回原始字节与推测的内容类型。
// 供网关在「图床不可公网访问」时把自家链接转成 base64。
func (s *Store) Load(name string) ([]byte, string, error) {
	if !namePattern.MatchString(name) {
		return nil, "", os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return nil, "", err
	}
	ext := strings.ToLower(filepath.Ext(name))
	ct, ok := typeByExt[ext]
	if !ok {
		ct = "application/octet-stream"
	}
	return data, ct, nil
}

// Delete 删除指定文件；文件名非法或不存在时返回 os.ErrNotExist。
func (s *Store) Delete(name string) error {
	if !namePattern.MatchString(name) {
		return os.ErrNotExist
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
		return err
	}
	return nil
}

// ServeHTTP 提供 GET/HEAD 静态访问。
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, s.prefix)
	if !namePattern.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(name))
	ct, ok := typeByExt[ext]
	if !ok {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", s.cacheMaxAge()))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// cacheMaxAge 让浏览器缓存时间不超过保留期，避免服务端已删除、客户端仍展示。
func (s *Store) cacheMaxAge() int {
	age := int(s.retention / time.Second / 2)
	if age > 3600 {
		age = 3600
	}
	if age < 0 {
		age = 0
	}
	return age
}

// Cleanup 删除超过保留期的图片与遗留临时文件。
func (s *Store) Cleanup(now time.Time) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		slog.Warn("[imagestore] 读取图片目录失败", "dir", s.dir, "err", err)
		return
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		// 遗留临时文件（进程异常退出）用更短的窗口清理
		ttl := s.retention
		if strings.HasPrefix(name, tmpPrefix) {
			ttl = s.retention
			if ttl > time.Hour {
				ttl = time.Hour
			}
			if ttl < 10*time.Minute {
				ttl = 10 * time.Minute
			}
		}
		if now.Sub(info.ModTime()) <= ttl {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err == nil {
			removed++
		}
	}
	if removed > 0 {
		slog.Info("[imagestore] 已清理过期图片", "count", removed, "dir", s.dir)
	}
}

// CleanupLoop 按保留期的一半（1 分钟 ~ 1 小时）清理过期图片，直到 ctx 结束。
func (s *Store) CleanupLoop(ctx context.Context) {
	interval := s.retention / 2
	if interval > time.Hour {
		interval = time.Hour
	}
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Cleanup(time.Now())
		}
	}
}

// URL 把文件名拼成生图结果的对外访问路径。
func URL(name string) string { return URLPrefix + name }

// extFor 根据 Content-Type 或文件内容确定扩展名。
func extFor(contentType string, data []byte) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if ext, ok := extByType[base]; ok {
		return ext
	}
	sniffed := http.DetectContentType(data)
	if ext, ok := extByType[sniffed]; ok {
		return ext
	}
	// 部分上游返回的 WebP 会被 DetectContentType 识别为 octet-stream，按文件头兜底
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp"
	}
	return ".bin"
}

// randomName 生成 128 位随机文件名。
func randomName(ext string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成文件名失败: %w", err)
	}
	return hex.EncodeToString(buf) + ext, nil
}
