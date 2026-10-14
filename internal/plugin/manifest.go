// Package plugin 实现进程级插件宿主：发现、校验、监管与渠道注册。
package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ChannelSpec 插件声明的渠道。
type ChannelSpec struct {
	Name         string            `json:"name"`
	PublicModels []string          `json:"public_models"`
	ModelMap     map[string]string `json:"model_map"`
}

// ConsoleSpec 插件控制台：声明后宿主会在插件页里嵌一套统一风格的管理界面。
// 插件只需实现宿主约定的 REST 接口（见 docs/plugin-spec.md §6），无需自带前端。
type ConsoleSpec struct {
	// Title 控制台标题，缺省用插件名
	Title string `json:"title"`
	// BasePath 插件进程内的控制台接口前缀，默认 /console
	BasePath string `json:"base_path"`
}

// Manifest 插件清单（plugin.json）。
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Entry       string `json:"entry"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Port        int               `json:"port"`
	// HealthPath 为 nil 表示使用默认 /health；显式给空串表示不做 HTTP 探活（退回 TCP 连通性）
	HealthPath       *string       `json:"health_path"`
	HealthTimeoutMS  int           `json:"health_timeout_ms"`
	StartupTimeoutMS int           `json:"startup_timeout_ms"`
	BaseURL          string        `json:"base_url"`
	UIPath           string        `json:"ui_path"`
	APIKeyEnv        string        `json:"api_key_env"`
	// ModelsPath 为插件自述的模型清单端点（如 /v1/models）。
	// 宿主不自动拉取：渠道模型由用户在「渠道管理」里手动获取并勾选，此处仅作声明。
	ModelsPath       string        `json:"models_path"`
	Channels         []ChannelSpec `json:"channels"`
	// Console 为 nil 表示该插件不提供控制台
	Console *ConsoleSpec `json:"console"`

	// Dir 为插件目录绝对路径（加载时填充）
	Dir string `json:"-"`
}

const (
	defaultHealthPath     = "/health"
	defaultHealthTimeout  = 2000
	defaultStartupTimeout = 20000
	defaultConsolePath    = "/console"
	manifestFileName      = "plugin.json"
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,31}$`)

// LoadManifest 读取并解析插件目录下的 plugin.json。
func LoadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", manifestFileName, err)
	}

	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", manifestFileName, err)
	}
	m.Dir = dir
	m.applyDefaults()
	return &m, nil
}

func (m *Manifest) applyDefaults() {
	if m.HealthTimeoutMS <= 0 {
		m.HealthTimeoutMS = defaultHealthTimeout
	}
	if m.StartupTimeoutMS <= 0 {
		m.StartupTimeoutMS = defaultStartupTimeout
	}
	if m.Port < 0 || m.Port > 65535 {
		m.Port = 0
	}
}

// HealthURLPath 返回探活路径；返回空串表示不做 HTTP 探活。
func (m *Manifest) HealthURLPath() string {
	if m.HealthPath == nil {
		return defaultHealthPath
	}
	return strings.TrimSpace(*m.HealthPath)
}

// EntryPath 返回 entry 的绝对路径。
func (m *Manifest) EntryPath() string {
	return filepath.Join(m.Dir, filepath.FromSlash(strings.TrimSpace(m.Entry)))
}

// HasConsole 是否声明了控制台。
func (m *Manifest) HasConsole() bool { return m.Console != nil }

// ConsoleBasePath 返回插件进程内的控制台接口前缀（规范化：带前导斜杠、无尾斜杠）。
// 未声明控制台时返回空串。
func (m *Manifest) ConsoleBasePath() string {
	if m.Console == nil {
		return ""
	}
	p := strings.TrimSpace(m.Console.BasePath)
	if p == "" {
		return defaultConsolePath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

// ConsoleTitleStr 返回控制台标题，缺省用插件名。
func (m *Manifest) ConsoleTitleStr() string {
	if m.Console == nil {
		return ""
	}
	if t := strings.TrimSpace(m.Console.Title); t != "" {
		return t
	}
	return m.Name
}

// Validate 校验清单与可执行文件；dir 为插件目录。
func Validate(dir string, m *Manifest) error {
	id := strings.TrimSpace(m.ID)
	if !idRe.MatchString(id) {
		return fmt.Errorf("id 非法（需 2-32 位小写字母、数字、- 或 _）: %q", m.ID)
	}
	if filepath.Base(filepath.Clean(dir)) != id {
		return fmt.Errorf("id(%s) 必须与目录名一致", id)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name 不能为空")
	}
	if strings.TrimSpace(m.BaseURL) == "" {
		return fmt.Errorf("base_url 不能为空")
	}
	for i, c := range m.Channels {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("channels[%d].name 不能为空", i)
		}
	}

	entry := strings.TrimSpace(m.Entry)
	if entry == "" {
		return fmt.Errorf("entry 不能为空")
	}
	if filepath.IsAbs(entry) {
		return fmt.Errorf("entry 不能是绝对路径")
	}
	full := m.EntryPath()
	if !IsInside(dir, full) {
		return fmt.Errorf("entry 逃出了插件目录: %s", entry)
	}
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("entry 文件不存在: %s", entry)
	}
	if info.IsDir() {
		return fmt.Errorf("entry 指向的是目录: %s", entry)
	}
	return nil
}

// IsInside 判断 target 是否位于 dir 内（含自身）。
func IsInside(dir, target string) bool {
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dirAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// substVars 占位符取值。
type substVars struct {
	Port      int
	Data      string
	APIKey    string
	PluginDir string
}

// substitute 替换 args / env / base_url 中的占位符。
//
//	{{port}} {{data}} {{api_key}} {{plugin_dir}}
func substitute(s string, v substVars) string {
	return strings.NewReplacer(
		"{{port}}", strconv.Itoa(v.Port),
		"{{data}}", v.Data,
		"{{api_key}}", v.APIKey,
		"{{plugin_dir}}", v.PluginDir,
	).Replace(s)
}

// ChannelSpecs 返回实际要注册的渠道列表（未声明时按插件名生成一个）。
func (m *Manifest) ChannelSpecs() []ChannelSpec {
	if len(m.Channels) == 0 {
		return []ChannelSpec{{Name: m.Name}}
	}
	return m.Channels
}