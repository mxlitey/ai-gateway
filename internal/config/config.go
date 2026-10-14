// Package config 负责解析 CLI 参数与环境变量（优先级：CLI > 环境变量 > 默认值）。
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config 运行期配置。
type Config struct {
	Host          string
	Port          int
	DataDir       string
	PluginsDir    string
	LogLevel      string
	Dev           bool
	NoBrowser     bool
	AdminPassword string
}

// DefaultPort 默认监听端口。
const DefaultPort = 8787

// Load 解析参数。args 为去掉了子命令后的参数列表。
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("ai-gateway", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	c := &Config{}
	fs.StringVar(&c.Host, "host", envOr("AIGW_HOST", "127.0.0.1"), "监听地址")
	fs.IntVar(&c.Port, "port", envIntOr("AIGW_PORT", DefaultPort), "监听端口")
	fs.StringVar(&c.DataDir, "data-dir", envOr("AIGW_DATA_DIR", "./data"), "数据目录")
	fs.StringVar(&c.PluginsDir, "plugins-dir", envOr("AIGW_PLUGINS_DIR", "./plugins"), "插件目录")
	fs.StringVar(&c.LogLevel, "log-level", envOr("AIGW_LOG_LEVEL", "info"), "日志级别 debug|info|warn|error")
	fs.BoolVar(&c.Dev, "dev", false, "开发模式")
	fs.BoolVar(&c.NoBrowser, "no-browser", false, "启动后不自动打开浏览器")
	fs.StringVar(&c.AdminPassword, "admin-password", envOr("AIGW_ADMIN_PASSWORD", ""), "首次初始化管理员密码")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.Port <= 0 || c.Port > 65535 {
		return nil, fmt.Errorf("端口非法: %d", c.Port)
	}

	var err error
	if c.DataDir, err = filepath.Abs(c.DataDir); err != nil {
		return nil, fmt.Errorf("解析数据目录失败: %w", err)
	}
	if c.PluginsDir, err = filepath.Abs(c.PluginsDir); err != nil {
		return nil, fmt.Errorf("解析插件目录失败: %w", err)
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