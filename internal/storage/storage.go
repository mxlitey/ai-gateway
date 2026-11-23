// Package storage 提供对象存储的预签名能力。
//
// 网关自身不中转文件字节：上传与下载都由客户端直连对象存储完成，
// 网关只负责签发短期有效的预签名 URL。
package storage

import (
	"errors"
	"path"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/id"
)

// ErrNotConfigured 未配置对象存储时返回，接口层据此回 501。
var ErrNotConfigured = errors.New("未配置对象存储")

// Options 对象存储的初始化参数。
type Options struct {
	// Endpoint 服务端点，如 https://s3.us-east-1.amazonaws.com 。
	Endpoint string
	// Region 签名区域。
	Region string
	// Bucket 桶名。
	Bucket string
	// AccessKey / SecretKey 预签名凭证。
	AccessKey string
	SecretKey string
	// Prefix 对象键公共前缀，可为空。
	Prefix string
	// PublicBase 公开访问前缀（CDN 或公开桶）；为空时只能用预签名 GET 读取。
	PublicBase string
	// ForcePathStyle 使用 path-style 访问（MinIO 等）。
	ForcePathStyle bool
	// PresignTTL 预签名默认有效期。
	PresignTTL time.Duration
}

// Ticket 一次直传凭证：客户端按 Method 把字节 PUT 到 UploadURL 即完成上传。
type Ticket struct {
	Driver  string            `json:"driver"`
	Bucket  string            `json:"bucket"`
	Key     string            `json:"key"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	// UploadURL 预签名上传地址。
	UploadURL string `json:"upload_url"`
	// DownloadURL 预签名下载地址（私有桶必填）。
	DownloadURL string `json:"download_url"`
	// PublicURL 公开访问地址；未配置公开前缀时为空。
	PublicURL string `json:"public_url,omitempty"`
	// ExpiresAt 过期时间（Unix 秒）。
	ExpiresAt int64 `json:"expires_at"`
	// ExpiresIn 有效期（秒）。
	ExpiresIn int64 `json:"expires_in"`
}

// Store 对象存储句柄。零值表示未配置，所有方法返回 ErrNotConfigured。
type Store struct {
	opts Options
}

// New 构造对象存储句柄；opts 不完整时返回 nil（视为未配置）。
func New(opts Options) *Store {
	if strings.TrimSpace(opts.Endpoint) == "" ||
		strings.TrimSpace(opts.Bucket) == "" ||
		strings.TrimSpace(opts.AccessKey) == "" ||
		strings.TrimSpace(opts.SecretKey) == "" {
		return nil
	}
	opts.Region = strings.TrimSpace(opts.Region)
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	opts.Prefix = strings.Trim(strings.TrimSpace(opts.Prefix), "/")
	opts.PublicBase = strings.TrimRight(strings.TrimSpace(opts.PublicBase), "/")
	if opts.PresignTTL <= 0 {
		opts.PresignTTL = 30 * time.Minute
	}
	return &Store{opts: opts}
}

// TTL 预签名默认有效期。
func (s *Store) TTL() time.Duration {
	if s == nil {
		return 0
	}
	return s.opts.PresignTTL
}

// Bucket 桶名。
func (s *Store) Bucket() string {
	if s == nil {
		return ""
	}
	return s.opts.Bucket
}

// NewUploadKey 生成一个上传统用的对象键，如 aigw/uploads/2026-11-20/ab12….png 。
func (s *Store) NewUploadKey(ext string) string {
	ext = normalizeExt(ext)
	name := "uploads/" + time.Now().UTC().Format("2006-01-02") + "/" + id.Hex(12)
	if ext != "" {
		name += "." + ext
	}
	if s == nil || s.opts.Prefix == "" {
		return name
	}
	return s.opts.Prefix + "/" + name
}

// PublicURL 返回对象的公开访问地址；未配置公开前缀时返回空串。
func (s *Store) PublicURL(key string) string {
	if s == nil || s.opts.PublicBase == "" || key == "" {
		return ""
	}
	return s.opts.PublicBase + "/" + escapePath(strings.TrimPrefix(key, "/"))
}

// Ticket 为给定对象键签发上传、下载两类预签名 URL。
func (s *Store) Ticket(key string, ttl time.Duration) (Ticket, error) {
	if s == nil {
		return Ticket{}, ErrNotConfigured
	}
	key = strings.TrimPrefix(strings.TrimSpace(key), "/")
	if key == "" {
		return Ticket{}, errors.New("对象键不能为空")
	}
	if ttl <= 0 {
		ttl = s.opts.PresignTTL
	}
	put, err := s.presign("PUT", key, ttl)
	if err != nil {
		return Ticket{}, err
	}
	get, err := s.presign("GET", key, ttl)
	if err != nil {
		return Ticket{}, err
	}
	expiresAt := time.Now().Add(ttl).Unix()
	return Ticket{
		Driver:      "s3",
		Bucket:      s.opts.Bucket,
		Key:         key,
		Method:      "PUT",
		Headers:     map[string]string{},
		UploadURL:   put,
		DownloadURL: get,
		PublicURL:   s.PublicURL(key),
		ExpiresAt:   expiresAt,
		ExpiresIn:   int64(ttl / time.Second),
	}, nil
}

// PresignGet 签发下载用的预签名 URL。
func (s *Store) PresignGet(key string, ttl time.Duration) (string, int64, error) {
	if s == nil {
		return "", 0, ErrNotConfigured
	}
	if ttl <= 0 {
		ttl = s.opts.PresignTTL
	}
	u, err := s.presign("GET", strings.TrimPrefix(strings.TrimSpace(key), "/"), ttl)
	if err != nil {
		return "", 0, err
	}
	return u, time.Now().Add(ttl).Unix(), nil
}

// normalizeExt 归一化文件扩展名，只保留常见图片类型。
func normalizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	switch ext {
	case "png", "jpg", "jpeg", "webp", "gif":
		return ext
	}
	return ""
}

// escapePath 对路径逐段做 AWS 百分号编码，保留分隔符；返回值不带前导斜杠。
func escapePath(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	segs := strings.Split(path.Clean("/" + p)[1:], "/")
	for i, seg := range segs {
		segs[i] = escape(seg)
	}
	return strings.Join(segs, "/")
}
