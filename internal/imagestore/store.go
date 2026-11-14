// Package imagestore 把生图结果保存到网关数据目录，并以只读静态路由对外提供。
//
// 设计目标：
//   - 文件名使用高熵随机串，不可枚举；知道链接即可访问（不额外签名）。
//   - 写入使用临时文件 + rename，避免读到半截图片。
//   - 到期文件由启动时与定时任务清理，默认保留 7 天。
package imagestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	DefaultRetention = 7 * 24 * time.Hour
	// MaxImageBytes 单张图片保存上限。
	MaxImageBytes = 64 << 20
	// URLPrefix 图片对外的访问前缀。
	URLPrefix = "/v1/images/files/"

	cleanupInterval = time.Hour
	tmpPrefix       = ".upload-"
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
	retention time.Duration
}

// Open 打开（必要时创建）图片目录，并先清理一批过期文件。
func Open(dir string, retention time.Duration) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("图片目录为空")
	}
	if retention <= 0 {
		retention = DefaultRetention
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建图片目录失败: %w", err)
	}
	s := &Store{dir: dir, retention: retention}
	s.Cleanup(time.Now())
	return s, nil
}

// Dir 返回图片目录。
func (s *Store) Dir() string { return s.dir }

// Save 保存图片内容并返回文件名（不含路径）。contentType 为空时按内容嗅探。
func (s *Store) Save(data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("图片内容为空")
	}
	if len(data) > MaxImageBytes {
		return "", fmt.Errorf("图片超过 %d MB", MaxImageBytes>>20)
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

// ServeHTTP 提供 GET/HEAD /v1/images/files/{name}。
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, URLPrefix)
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
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, name, fi.ModTime(), f)
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
			ttl = time.Hour
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

// CleanupLoop 按小时清理过期图片，直到 ctx 结束。
func (s *Store) CleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
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

// URL 把文件名拼成对外访问路径。
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
