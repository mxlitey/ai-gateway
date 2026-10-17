// Package plugin 实现进程级插件宿主：发现、校验、监管与渠道注册。
package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Entry       string            `json:"entry"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	// Entries 按平台声明各自的入口，键为 "goos"（如 windows）或 "goos/goarch"（如 linux/amd64）。
	// 值为字符串时表示可执行文件路径（沿用共享的 args）；值为数组时表示完整命令（自带参数）。
	// 命中优先级：goos/goarch > goos > entry。仅声明 entry 时行为与旧版完全一致。
	Entries map[string]json.RawMessage `json:"entries"`
	// Port 由插件作者自己指定，宿主不再代为分配
	Port int `json:"port"`
	// Console 是否提供控制台（实现 /console/* 约定接口）
	Console bool `json:"console"`
	// Capabilities 控制台能力，取值见 CapCheckin / CapQuota
	Capabilities []string `json:"capabilities"`
	// StartupTimeoutMS 启动后等待探活通过的最长时间
	StartupTimeoutMS int `json:"startup_timeout_ms"`

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

func (m *Manifest) applyDefaults() {
	if m.StartupTimeoutMS <= 0 {
		m.StartupTimeoutMS = defaultStartupTimeout
	}
}

// EntryPath 返回 entry 的绝对路径。
func (m *Manifest) EntryPath() string {
	return filepath.Join(m.Dir, filepath.FromSlash(strings.TrimSpace(m.Entry)))
}

// resolveEntry 按平台解析出本次运行应使用的入口命令与参数。
//
// 查找顺序：entries["<goos>/<goarch>"] → entries["<goos>"] → 旧字段 entry。
// 值与 entry 同为「相对插件目录的路径」时沿用共享的 Args；
// 值为数组时视为完整命令（首项为可执行文件，其余为参数），不再套用共享 Args。
// 路径类值均相对插件目录解析为绝对路径；解释器名（如 node、cmd.exe）保持原样交给 PATH 查找。
func (m *Manifest) resolveEntry() (string, []string, error) {
	raw, ok := m.lookupPlatformEntry()
	if !ok {
		// 回落到旧字段 entry
		return m.EntryPath(), copyArgs(m.Args), nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		path := strings.TrimSpace(asString)
		if path == "" {
			return "", nil, fmt.Errorf("entries 中 %q 的平台入口为空", platformKey())
		}
		return m.resolveExecPath(path), copyArgs(m.Args), nil
	}

	var asArray []string
	if err := json.Unmarshal(raw, &asArray); err != nil {
		return "", nil, fmt.Errorf("entries 的平台入口既不是字符串也不是数组: %s", string(raw))
	}
	if len(asArray) == 0 || strings.TrimSpace(asArray[0]) == "" {
		return "", nil, fmt.Errorf("entries 的平台入口命令为空")
	}
	cmd := m.resolveExecPath(strings.TrimSpace(asArray[0]))
	return cmd, asArray[1:], nil
}

// lookupPlatformEntry 在 entries 里按 "goos/goarch" → "goos" 的顺序查找。
func (m *Manifest) lookupPlatformEntry() (json.RawMessage, bool) {
	if len(m.Entries) == 0 {
		return nil, false
	}
	arch := runtime.GOARCH
	// 每个 OS/架构都登记一份别名：amd64 也接受 x86_64，arm64 也接受 aarch64
	if raw, ok := m.Entries[strings.ToLower(runtime.GOOS)+"/"+strings.ToLower(arch)]; ok {
		return raw, true
	}
	if alias := archAlias(arch); alias != "" {
		if raw, ok := m.Entries[strings.ToLower(runtime.GOOS)+"/"+alias]; ok {
			return raw, true
		}
	}
	if raw, ok := m.Entries[strings.ToLower(runtime.GOOS)]; ok {
		return raw, true
	}
	return nil, false
}

// archAlias 返回架构的常见别名，便于插件作者用 x86_64 / aarch64 书写。
func archAlias(arch string) string {
	switch strings.ToLower(arch) {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return ""
	}
}

// platformKey 返回当前平台标识，用于错误提示。
func platformKey() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// resolveExecPath 判断给定命令是路径还是裸命令名：
// 含路径分隔符或 . / .. 前缀时按插件目录解析为绝对路径，否则原样返回交给 PATH 查找。
func (m *Manifest) resolveExecPath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return filepath.Join(m.Dir, filepath.FromSlash(s))
	}
	if strings.ContainsAny(s, `/\`) {
		return filepath.Join(m.Dir, filepath.FromSlash(s))
	}
	return s
}

// copyArgs 复制一份参数切片，避免调用方就地替换占位符时改动清单本身。
func copyArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	return out
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

// Validate 校验清单与可执行文件；dir 为插件目录。
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

	// 入口只校验「当前平台实际会用到的那一个」：
	// entries 里为其它平台声明的入口在本机不存在属正常（用户可能只随包携带一个平台）。
	entry, _, err := m.resolveEntry()
	if err != nil {
		return err
	}
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return fmt.Errorf("entry 不能为空")
	}
	// 指向插件目录之外的路径一律拒绝：既防相对路径逃逸（../x），也防越界的绝对路径。
	// 裸命令名（node、cmd.exe）交由 PATH 查找，不做目录归属校验。
	if !looksLikeCommand(entry) {
		if !IsInside(dir, entry) {
			return fmt.Errorf("entry 逃出了插件目录: %s", filepath.Base(entry))
		}
		info, statErr := os.Stat(entry)
		if statErr != nil {
			return fmt.Errorf("当前平台（%s）的 entry 文件不存在: %s", platformKey(), filepath.Base(entry))
		}
		if info.IsDir() {
			return fmt.Errorf("entry 指向的是目录: %s", filepath.Base(entry))
		}
	}
	return nil
}

// looksLikeCommand 判断是否是不含路径分隔符的裸命令名（交给 PATH 查找，无需本地文件存在）。
func looksLikeCommand(s string) bool {
	return !filepath.IsAbs(s) && !strings.ContainsAny(s, `/\`)
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