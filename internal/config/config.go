// Package config 负责解析 CLI 参数与环境变量（优先级：CLI > 环境变量 > 默认值）。
package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 运行期配置。
type Config struct {
	Host          string
	Port          int
	DataDir       string
	PluginsDir    string
	MCPDir        string
	LogLevel      string
	Dev           bool
	NoBrowser     bool
	AdminPassword string
	// PublicBaseURL 对外访问地址（如 https://gw.example.com）。
	// 为空时按请求的 Host / X-Forwarded-* 推导，适合反向代理场景；
	// 容器或 NAT 后端的内部调用拿不到真实对外地址时，需要显式配置。
	PublicBaseURL string
	// ImageHost 是否开启内置图床（上传接口）；默认开启。
	ImageHost bool
	// ImageHostPublic 图床公网可达性声明：auto | on | off。
	// auto 按 PublicBaseURL 的 host 字面量判断；on 表示由用户断言公网可达。
	ImageHostPublic string
	// ImageUploadMaxMB 单张上传上限（MB）。
	ImageUploadMaxMB int
	// ImageTTLSeconds 生图结果保留秒数。
	ImageTTLSeconds int
	// UploadTTLSeconds 上传图片保留秒数。
	UploadTTLSeconds int
}

// DefaultPort 默认监听端口。
const DefaultPort = 8787

// 图床与图片保留的默认值。
const (
	DefaultImageUploadMaxMB = 100
	DefaultImageTTLSeconds  = 3600
	DefaultUploadTTLSeconds = 3600
	// DefaultImageHostPublic 默认按对外地址自动判断图床是否公网可达。
	DefaultImageHostPublic = "auto"
)

// 日志级别：默认 info，可在面板中热切换（无需重启）。
const (
	DefaultLogLevel = "info"
)

// levelVar 承载全局日志级别，配合 Leveler 让 slog 的过滤阈值可运行期调整。
var levelVar = new(slog.LevelVar)

// Leveler 返回可供 slog.Handler 使用的动态级别，随 ApplyLogLevel 变化。
func Leveler() slog.Leveler { return levelVar }

// ParseLogLevel 解析日志级别字符串；无法识别时回退为 info。
func ParseLogLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ApplyLogLevel 立即生效地设置全局日志级别，返回是否被识别。
func ApplyLogLevel(name string) bool {
	trimmed := strings.TrimSpace(name)
	levelVar.Set(ParseLogLevel(trimmed))
	switch strings.ToLower(trimmed) {
	case "debug", "info", "warn", "warning", "error":
		return true
	default:
		return false
	}
}

// Load 解析参数。args 为去掉了子命令后的参数列表。
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("ai-gateway", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	c := &Config{}
	fs.StringVar(&c.Host, "host", envOr("AIGW_HOST", "127.0.0.1"), "监听地址")
	fs.IntVar(&c.Port, "port", envIntOr("AIGW_PORT", DefaultPort), "监听端口")
	fs.StringVar(&c.DataDir, "data-dir", envOr("AIGW_DATA_DIR", "./data"), "数据目录")
	fs.StringVar(&c.PluginsDir, "plugins-dir", envOr("AIGW_PLUGINS_DIR", "./plugins"), "插件目录")
	fs.StringVar(&c.MCPDir, "mcp-dir", envOr("AIGW_MCP_DIR", "./mcp"), "本地 MCP 服务器目录")
	fs.StringVar(&c.LogLevel, "log-level", envOr("AIGW_LOG_LEVEL", "info"), "日志级别 debug|info|warn|error")
	fs.BoolVar(&c.Dev, "dev", false, "开发模式")
	fs.BoolVar(&c.NoBrowser, "no-browser", false, "启动后不自动打开浏览器")
	fs.StringVar(&c.AdminPassword, "admin-password", envOr("AIGW_ADMIN_PASSWORD", ""), "首次初始化管理员密码")
	fs.StringVar(&c.PublicBaseURL, "public-url", envOr("AIGW_PUBLIC_BASE_URL", ""), "对外访问地址，如 https://gw.example.com（生图返回的链接用它拼接）")
	fs.BoolVar(&c.ImageHost, "image-host", envBoolOr("AIGW_IMAGE_HOST", true), "开启内置图床（图片上传接口）")
	fs.StringVar(&c.ImageHostPublic, "image-host-public", envOr("AIGW_IMAGE_HOST_PUBLIC", DefaultImageHostPublic), "图床公网可达性声明 auto|on|off")
	fs.IntVar(&c.ImageUploadMaxMB, "image-max-mb", envIntOr("AIGW_IMAGE_HOST_MAX_MB", DefaultImageUploadMaxMB), "图床上传单张大小上限（MB）")
	fs.IntVar(&c.ImageTTLSeconds, "image-ttl", envIntOr("AIGW_IMAGE_TTL_SECONDS", DefaultImageTTLSeconds), "生图结果保留秒数")
	fs.IntVar(&c.UploadTTLSeconds, "upload-ttl", envIntOr("AIGW_UPLOAD_TTL_SECONDS", DefaultUploadTTLSeconds), "上传图片保留秒数")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.Port <= 0 || c.Port > 65535 {
		return nil, fmt.Errorf("端口非法: %d", c.Port)
	}
	if c.ImageUploadMaxMB <= 0 || c.ImageUploadMaxMB > 4096 {
		return nil, fmt.Errorf("图床上传大小上限非法: %d MB", c.ImageUploadMaxMB)
	}
	if c.ImageTTLSeconds <= 0 {
		c.ImageTTLSeconds = DefaultImageTTLSeconds
	}
	if c.UploadTTLSeconds <= 0 {
		c.UploadTTLSeconds = DefaultUploadTTLSeconds
	}
	c.ImageHostPublic = strings.ToLower(strings.TrimSpace(c.ImageHostPublic))
	switch c.ImageHostPublic {
	case "auto", "on", "off":
	case "":
		c.ImageHostPublic = DefaultImageHostPublic
	default:
		return nil, fmt.Errorf("图床公网声明非法: %q（可选 auto|on|off）", c.ImageHostPublic)
	}
	if c.ImageHostPublic == "on" && strings.TrimSpace(c.PublicBaseURL) == "" {
		return nil, fmt.Errorf("AIGW_IMAGE_HOST_PUBLIC=on 需要同时配置 AIGW_PUBLIC_BASE_URL")
	}

	var err error
	if c.DataDir, err = filepath.Abs(c.DataDir); err != nil {
		return nil, fmt.Errorf("解析数据目录失败: %w", err)
	}
	if c.PluginsDir, err = filepath.Abs(c.PluginsDir); err != nil {
		return nil, fmt.Errorf("解析插件目录失败: %w", err)
	}
	if c.MCPDir, err = filepath.Abs(c.MCPDir); err != nil {
		return nil, fmt.Errorf("解析本地 MCP 目录失败: %w", err)
	}
	if c.PublicBaseURL != "" {
		c.PublicBaseURL = strings.TrimRight(strings.TrimSpace(c.PublicBaseURL), "/")
	}
	return c, nil
}

// DBPath 返回 SQLite 文件路径。
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "ai-gateway.db")
}

// LogDir 返回日志目录。
func (c *Config) LogDir() string {
	return filepath.Join(c.DataDir, "logs")
}

// ImageTTL 生图结果保留时长。
func (c *Config) ImageTTL() time.Duration {
	return time.Duration(c.ImageTTLSeconds) * time.Second
}

// UploadTTL 上传图片保留时长。
func (c *Config) UploadTTL() time.Duration {
	return time.Duration(c.UploadTTLSeconds) * time.Second
}

// ImageUploadMaxBytes 图床上传单张上限（字节）。
func (c *Config) ImageUploadMaxBytes() int64 {
	return int64(c.ImageUploadMaxMB) << 20
}

// ImageHostPublicMode 返回图床公网可达性声明：auto|on|off。
func (c *Config) ImageHostPublicMode() string {
	if c.ImageHostPublic == "" {
		return DefaultImageHostPublic
	}
	return c.ImageHostPublic
}

// PluginDataDir 返回某插件的私有数据目录。
func (c *Config) PluginDataDir(pluginID string) string {
	return filepath.Join(c.DataDir, "plugins", pluginID)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBoolOr(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
