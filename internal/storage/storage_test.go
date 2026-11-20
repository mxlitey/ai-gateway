package storage

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestNewRequiresCredentials 验证凭证不全时视为未配置。
func TestNewRequiresCredentials(t *testing.T) {
	if s := New(Options{Endpoint: "https://s3.us-east-1.amazonaws.com"}); s != nil {
		t.Fatal("缺少桶名/凭证时应返回 nil")
	}
	s := New(Options{
		Endpoint:  "https://s3.us-east-1.amazonaws.com",
		Bucket:    "aigw",
		AccessKey: "AK",
		SecretKey: "SK",
	})
	if s == nil {
		t.Fatal("凭证齐全时应返回可用的 Store")
	}
	if s.TTL() != 30*time.Minute {
		t.Fatalf("未指定 TTL 时应回落到 30 分钟，实际 %v", s.TTL())
	}
}

// TestTicketVirtualHosted 验证默认使用 virtual-hosted style，桶名进主机名。
func TestTicketVirtualHosted(t *testing.T) {
	s := New(Options{
		Endpoint:   "https://s3.us-east-1.amazonaws.com",
		Bucket:     "aigw",
		AccessKey:  "AK",
		SecretKey:  "SK",
		Prefix:     "aigw",
		PublicBase: "https://cdn.example.com/",
		PresignTTL: 1800 * time.Second,
	})
	ticket, err := s.Ticket("aigw/uploads/2026-11-20/abc.png", 0)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	u, err := url.Parse(ticket.UploadURL)
	if err != nil {
		t.Fatalf("预签名 URL 非法: %v", err)
	}
	if u.Host != "aigw.s3.us-east-1.amazonaws.com" {
		t.Fatalf("主机名应含桶名，实际 %s", u.Host)
	}
	if u.Path != "/aigw/uploads/2026-11-20/abc.png" {
		t.Fatalf("路径应为对象键，实际 %s", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Expires") != "1800" {
		t.Fatalf("签名参数缺失: %s", u.RawQuery)
	}
	if q.Get("X-Amz-Signature") == "" {
		t.Fatal("缺少 X-Amz-Signature")
	}
	if ticket.Method != "PUT" || ticket.ExpiresIn != 1800 {
		t.Fatalf("凭证描述不符: %+v", ticket)
	}
	if !strings.HasPrefix(ticket.DownloadURL, "https://aigw.s3.us-east-1.amazonaws.com/") {
		t.Fatalf("下载地址不符: %s", ticket.DownloadURL)
	}
	if ticket.PublicURL != "https://cdn.example.com/aigw/uploads/2026-11-20/abc.png" {
		t.Fatalf("公开地址不符: %s", ticket.PublicURL)
	}
}

// TestTicketPathStyle 验证 path-style 时桶名进路径（MinIO 场景）。
func TestTicketPathStyle(t *testing.T) {
	s := New(Options{
		Endpoint:       "http://127.0.0.1:9000",
		Bucket:         "aigw",
		AccessKey:      "AK",
		SecretKey:      "SK",
		ForcePathStyle: true,
		PresignTTL:     time.Minute,
	})
	ticket, err := s.Ticket("uploads/a b.png", 0)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	u, err := url.Parse(ticket.UploadURL)
	if err != nil {
		t.Fatalf("预签名 URL 非法: %v", err)
	}
	if u.Host != "127.0.0.1:9000" {
		t.Fatalf("path-style 主机名不应含桶名，实际 %s", u.Host)
	}
	if u.EscapedPath() != "/aigw/uploads/a%20b.png" {
		t.Fatalf("path-style 路径不符: %s", u.EscapedPath())
	}
}

// TestNewUploadKey 验证对象键带前缀与日期，扩展名白名单生效。
func TestNewUploadKey(t *testing.T) {
	s := New(Options{
		Endpoint:  "https://s3.us-east-1.amazonaws.com",
		Bucket:    "aigw",
		AccessKey: "AK",
		SecretKey: "SK",
		Prefix:    "aigw",
	})
	key := s.NewUploadKey(".PNG")
	if !strings.HasPrefix(key, "aigw/uploads/") || !strings.HasSuffix(key, ".png") {
		t.Fatalf("对象键不符: %s", key)
	}
	if bad := s.NewUploadKey("svg"); strings.Contains(bad, ".") {
		t.Fatalf("不支持的扩展名应被丢弃: %s", bad)
	}
}

// TestConfigValidate 验证非法输入被拒。
func TestConfigValidate(t *testing.T) {
	for _, c := range []Config{
		{Driver: "gcs"},
		{ForcePathStyle: "yes"},
		{TTLSeconds: -1},
		{TTLSeconds: 604801},
	} {
		if err := c.Validate(); err == nil {
			t.Fatalf("应拒绝非法设置 %+v", c)
		}
	}
	if err := (Config{Driver: "s3", ForcePathStyle: "on", TTLSeconds: 1800}).Validate(); err != nil {
		t.Fatalf("合法设置被拒: %v", err)
	}
}

// TestConfigOptions 验证面板配置转句柄参数：归一化与未启用时的零值。
func TestConfigOptions(t *testing.T) {
	if opts := (Config{Driver: "none"}).Options(); New(opts) != nil {
		t.Fatal("driver=none 应关闭对象存储")
	}
	if opts := (Config{}).Options(); New(opts) != nil {
		t.Fatal("空配置应关闭对象存储")
	}

	cfg := Config{
		Driver: "S3", Endpoint: "https://panel.example.com/", Bucket: "panel",
		AccessKey: "AK", SecretKey: "SK", Prefix: "/aigw/",
		PublicBase: "https://cdn.example.com/", ForcePathStyle: "on", TTLSeconds: 600,
	}
	opts := cfg.Options()
	s := New(opts)
	if s == nil {
		t.Fatal("driver=s3 且必填项齐全时应启用")
	}
	if opts.Endpoint != "https://panel.example.com" || opts.Prefix != "aigw" ||
		opts.PublicBase != "https://cdn.example.com" {
		t.Fatalf("归一化不符: %+v", opts)
	}
	if !opts.ForcePathStyle || s.TTL() != 10*time.Minute {
		t.Fatalf("path-style / TTL 不符: %+v", opts)
	}
}

// TestConfigOptionsIncomplete 验证启用但必填项不全时不启用。
func TestConfigOptionsIncomplete(t *testing.T) {
	opts := (Config{Driver: "s3", Endpoint: "https://panel.example.com", Bucket: "b"}).Options()
	if New(opts) != nil {
		t.Fatal("必填项不全时不应启用")
	}
}
