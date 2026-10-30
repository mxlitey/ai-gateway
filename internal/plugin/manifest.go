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

// 控制台能力：插件在清单里声明后，面板才显示对应的列与按钮（见 docs/plugin-spec.md §6.4）。
const (
	CapCheckin = "checkin" // 账号列表显示「今日签到」列与签到按钮
	CapQuota   = "quota"   // 账号列表显示「套餐 / 额度」列与刷新额度按钮
)

// Manifest 插件清单（plugin.json）。
//
// 一个插件固定对应一个渠道（渠道名 = 插件 name），并可选提供一个统一风格的控制台。
// 插件一律以 Python 解释器启动：entry 必须是插件目录内的 .py 脚本，
// 同一份源码可在 Windows / macOS / Linux（含 Docker）通用（见 docs/plugin-spec.md §2）。
type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	// Entry 入口脚本，必须是插件目录内的 .py 文件（相对路径，不允许绝对路径或 .. 逃逸）。
	Entry string            `json:"entry"`
	Args  []string          `json:"args"`
	Env   map[string]string `json:"env"`
	// Port 由插件作者自己指定，宿主不再代为分配
	Port int `json:"port"`
	// Console 是否提供控制台（实现 /console/* 约定接口）
	Console bool `json:"console"`
	// Capabilities 控制台能力，取值见 CapCheckin / CapQuota
	Capabilities []string `json:"capabilities"`
	// StartupTimeoutMS 启动后等待探活通过的最长时间
	StartupTimeoutMS int `json:"startup_timeout_ms"`
	// Requirements 需要预先安装的 Python 依赖（格式同 requirements.txt）。
	Requirements []string `json:"requirements"`
	// PythonVersion 期望的 Python 版本（可选，仅用于便携解释器下载与提示）。
	PythonVersion string `json:"python_version"`
	// CallbackPaths 插件自行监听、且必须由浏览器直达的回调路径（如 TRAE 的 /authorize）。
	// 仅在这些路径上允许「粘贴回调链接」；Docker 中回调端口无法从宿主机访问时使用。
	CallbackPaths []string `json:"callback_paths"`

	// ID 由插件目录名推导（加载时填充），无需在 plugin.json 里声明
	ID string `json:"-"`
	// Dir 为插件目录绝对路径（加载时填充）
	Dir string `json:"-"`
}

const (
	// 插件只监听回环地址，上游根路径、探活路径、控制台前缀都是固定约定。
	pluginAPIPrefix       = "/v1"
	healthPath            = "/health"
	consolePath           = "/console"
	defaultStartupTimeout = 20000
	manifestFileName      = "plugin.json"
	// scriptSuffix 插件入口脚本的扩展名：网关只支持 Python 插件。
	scriptSuffix = ".py"
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
	m.ID = filepath.Base(filepath.Clean(dir))
	m.applyDefaults()
	return &m, nil
}

// HasRequirements 是否声明了需要安装的 Python 依赖。
func (m *Manifest) HasRequirements() bool { return len(m.Requirements) > 0 }

func (m *Manifest) applyDefaults() {
	if m.StartupTimeoutMS <= 0 {
		m.StartupTimeoutMS = defaultStartupTimeout
	}
}

// EntryPath 返回 entry 的绝对路径。
func (m *Manifest) EntryPath() string {
	return filepath.Join(m.Dir, filepath.FromSlash(strings.TrimSpace(m.Entry)))
}

// UpstreamBaseURL 返回插件渠道的上游根地址，固定 http://127.0.0.1:<port>/v1。
func UpstreamBaseURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + pluginAPIPrefix
}

// HasConsole 是否提供控制台。
func (m *Manifest) HasConsole() bool { return m.Console }

// ConsoleBasePath 控制台接口前缀，固定 /console；未声明控制台时返回空串。
func (m *Manifest) ConsoleBasePath() string {
	if !m.Console {
		return ""
	}
	return consolePath
}

// HasCapability 判断是否声明了某个控制台能力（大小写不敏感）。
func (m *Manifest) HasCapability(name string) bool {
	for _, c := range m.Capabilities {
		if strings.EqualFold(strings.TrimSpace(c), name) {
			return true
		}
	}
	return false
}

// KnownCapabilities 返回去重后、宿主认识的能力列表（未知值忽略，便于向前兼容）。
func (m *Manifest) KnownCapabilities() []string {
	out := make([]string, 0, 2)
	for _, name := range []string{CapCheckin, CapQuota} {
		if m.HasCapability(name) {
			out = append(out, name)
		}
	}
	return out
}

// Validate 校验清单与入口脚本；dir 为插件目录。
func Validate(dir string, m *Manifest) error {
	id := strings.TrimSpace(m.ID)
	if id == "" {
		id = filepath.Base(filepath.Clean(dir))
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("插件目录名非法（需 2-32 位小写字母、数字、- 或 _）: %q", id)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name 不能为空")
	}
	if m.Port <= 0 || m.Port > 65535 {
		return fmt.Errorf("port 非法（需 1-65535，由插件自己指定）: %d", m.Port)
	}

	// 入口必须是插件目录内的 .py 脚本：网关只支持 Python 插件（见 docs/plugin-spec.md §2）。
	entry := strings.TrimSpace(m.Entry)
	if entry == "" {
		return fmt.Errorf("entry 不能为空（须是插件目录内的 .py 脚本）")
	}
	// 指向插件目录之外的路径一律拒绝：既防相对路径逃逸（../x），也防越界的绝对路径。
	if filepath.IsAbs(entry) || !IsInside(dir, m.EntryPath()) {
		return fmt.Errorf("entry 逃出了插件目录（须是相对插件目录的路径）: %s", entry)
	}
	if !strings.EqualFold(filepath.Ext(entry), scriptSuffix) {
		return fmt.Errorf("entry 必须是 .py 脚本（网关只支持 Python 插件）: %s", entry)
	}
	info, statErr := os.Stat(m.EntryPath())
	if statErr != nil {
		return fmt.Errorf("entry 脚本不存在: %s", entry)
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
	Port int
	Data string
}

// substitute 替换 args / env 中的占位符。
//
//	{{port}}
func substitute(s string, v substVars) string {
	return strings.NewReplacer("{{port}}", strconv.Itoa(v.Port)).Replace(s)
}
