package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Python 运行时支持（见 docs/plugin-spec.md §11）。
//
// 解释器一律来自系统 Python（py -3 → python3 → python；跳过 Windows 应用商店的占位程序）。
// 插件声明的 requirements 会在启动前用 pip 装进插件私有的 deps 目录
// （官方源优先、失败后按序退避到国内镜像，成功源按指纹缓存 24 小时）。
//
// Docker 镜像的基础镜像本身自带 python3 / pip；Windows 桌面版由用户自行安装 Python。

const (
	// pyDepsSubdir 插件私有依赖目录（相对插件数据目录）。
	pyDepsSubdir = "deps"
	// pyIndexEnvVar 追加的 pip 源列表（逗号分隔，空 = 内置默认列表）。
	pyIndexEnvVar = "AIGW_PIP_INDEX_URLS"

	// pyInstallerTimeout 依赖安装的超时。
	pyInstallerTimeout = 15 * time.Minute
)

// pyDefaultIndexURLs pip 源默认顺序：官方源优先，失败后按序退避到国内镜像。
var pyDefaultIndexURLs = []string{
	"https://pypi.org/simple",
	"https://pypi.tuna.tsinghua.edu.cn/simple",
	"https://mirrors.aliyun.com/pypi/simple/",
	"https://mirrors.cloud.tencent.com/pypi/simple",
	"https://pypi.mirrors.ustc.edu.cn/simple",
}

// pyInterpreter 解析出的 Python 解释器。
type pyInterpreter struct {
	// Path 解释器可执行文件路径。
	Path string
	// Args 前置参数（Windows 的 py 启动器需要 "-3"）。
	Args []string
	// Version 形如 "3.12.7"（探测失败时为空）。
	Version string
}

// Command 返回「解释器 + 前置参数 + 追加参数」的完整 argv。
func (p *pyInterpreter) Command(args ...string) (string, []string) {
	full := make([]string, 0, len(p.Args)+len(args))
	full = append(full, p.Args...)
	full = append(full, args...)
	return p.Path, full
}

// pyEnv 返回 Python 运行时的额外环境变量配置。
type pyEnv struct {
	// IndexURLs 自定义 pip 源（空 = 内置默认列表）。
	IndexURLs []string
}

// ---- 解释器探测 ----

// systemPythonCandidates 返回系统 Python 的探测顺序。
//
// Windows 上 py 启动器最可靠：直接叫 python 往往命中应用商店占位程序
// （%LOCALAPPDATA%\Microsoft\WindowsApps\python.exe），它会弹商店并把进程挂住。
func systemPythonCandidates() [][]string {
	if runtime.GOOS == "windows" {
		return [][]string{
			{"py", "-3"},
			{"python3"},
			{"python"},
		}
	}
	return [][]string{
		{"python3"},
		{"python"},
	}
}

// isStorePlaceholder 判断是否为 Windows 应用商店的 python 占位程序。
func isStorePlaceholder(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, `\windowsapps\`) && strings.HasSuffix(lower, ".exe")
}

// probeInterpreter 执行 `-c` 脚本确认解释器可用且为主版本 3。
func probeInterpreter(bin string, pre []string) (*pyInterpreter, error) {
	args := append(append([]string{}, pre...), "-c", "import sys;sys.stdout.write('%d.%d.%d'%sys.version_info[:3])")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(string(out))
	if !strings.HasPrefix(version, "3.") {
		return nil, fmt.Errorf("需要 Python 3，实际为 %s", version)
	}
	return &pyInterpreter{Path: bin, Args: pre, Version: version}, nil
}

// findSystemPython 在 PATH 中寻找可用的 Python 3。
func findSystemPython() *pyInterpreter {
	for _, cand := range systemPythonCandidates() {
		bin := cand[0]
		path, err := exec.LookPath(bin)
		if err != nil || isStorePlaceholder(path) {
			continue
		}
		if interp, err := probeInterpreter(path, cand[1:]); err == nil {
			return interp
		}
	}
	return nil
}

// logger 返回宿主日志器；未初始化时回退到默认 logger（便于在未装配的实例上做探测）。
func (h *Host) logger() *slog.Logger {
	if h.log != nil {
		return h.log
	}
	return slog.Default()
}

// resolvePython 解析解释器；系统里没有 Python 3 时返回带指引的错误。
func (h *Host) resolvePython() (*pyInterpreter, error) {
	if interp := findSystemPython(); interp != nil {
		return interp, nil
	}
	return nil, fmt.Errorf("未找到 Python 3：请先安装 Python 3（Windows 建议从 python.org 安装并勾选 Add to PATH；" +
		"Docker 镜像已内置），安装后重试启动插件")
}

// ---- pip 依赖安装 ----

// pyPipLock pip 源命中缓存：同一份依赖 24 小时内直接复用命中的源。
type pyPipLock struct {
	RequirementsHash string `json:"requirements_hash"`
	IndexURL         string `json:"index_url"`
	UpdatedAt        int64  `json:"updated_at"`
	// Version 解释器版本，版本变了需要重装依赖。
	Version string `json:"version"`
}

const pyPipLockTTL = 24 * time.Hour

// ensurePythonDeps 确保插件声明的依赖已装进私有 deps 目录。
func (h *Host) ensurePythonDeps(ctx context.Context, inst *instance, interp *pyInterpreter, dataDir string, env pyEnv) (string, error) {
	depsDir := filepath.Join(dataDir, pyDepsSubdir)
	reqHash := requirementsHash(inst.manifest.Requirements)
	lockPath := filepath.Join(depsDir, ".pip.lock")

	if samePyDeps(lockPath, reqHash, interp.Version) {
		return depsDir, nil
	}
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		return "", fmt.Errorf("创建依赖目录失败: %w", err)
	}
	reqFile := filepath.Join(depsDir, "requirements.txt")
	if err := os.WriteFile(reqFile, []byte(strings.Join(inst.manifest.Requirements, "\n")+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("写入 requirements.txt 失败: %w", err)
	}
	if err := h.ensurePip(ctx, interp); err != nil {
		return "", err
	}

	sources := pipSources(env)
	if cached := cachedPipSource(lockPath, reqHash, interp.Version); cached != "" {
		sources = append([]string{cached}, sources...)
	}

	var failures []string
	for _, src := range sources {
		err := h.pipInstall(ctx, interp, depsDir, reqFile, src)
		if err == nil {
			writePipLock(lockPath, pyPipLock{
				RequirementsHash: reqHash,
				IndexURL:         src,
				UpdatedAt:        time.Now().UnixMilli(),
				Version:          interp.Version,
			})
			h.logger().Info("Python 依赖安装完成", "plugin", inst.manifest.ID, "source", src)
			return depsDir, nil
		}
		h.logger().Warn("pip 源不可用，尝试下一个", "plugin", inst.manifest.ID, "source", src, "err", err)
		failures = append(failures, fmt.Sprintf("  - %-40s %s", hostOf(src), trimOneLine(err.Error(), 120)))
	}

	return "", fmt.Errorf("依赖安装失败：%s\n%s\n可尝试：在「设置 → pip 源」填写内网源，或把依赖放到插件的 vendor 目录离线安装",
		strings.Join(inst.manifest.Requirements, ", "), strings.Join(failures, "\n"))
}

// pipSources 返回 pip 源尝试顺序：自定义优先，其次官方，最后内置国内镜像。
func pipSources(env pyEnv) []string {
	custom := append([]string{}, env.IndexURLs...)
	if len(custom) == 0 {
		custom = splitList(os.Getenv(pyIndexEnvVar))
	}
	out := dedupeStrings(custom)
	// 自定义源一般已包含官方/镜像顺序，不再追加默认列表，避免把内网源的用户拖到公网
	if len(out) > 0 {
		return out
	}
	return dedupeStrings(pyDefaultIndexURLs)
}

// ensurePip 确认 pip 可用；缺失时尝试用 ensurepip 自举。
func (h *Host) ensurePip(ctx context.Context, interp *pyInterpreter) error {
	if pipAvailable(ctx, interp) {
		return nil
	}
	bin, args := interp.Command("-m", "ensurepip", "--upgrade")
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Python（%s）缺少 pip 且自举失败：%s\n"+
			"请先安装 pip（Debian/Ubuntu: apt install python3-pip；Windows: 重装 Python 时勾选 pip）",
			interp.Version, trimOneLine(lastLine(out)+err.Error(), 200))
	}
	return nil
}

// pipAvailable 探测 pip 是否可用（`python -m pip --version`）。
func pipAvailable(ctx context.Context, interp *pyInterpreter) bool {
	bin, args := interp.Command("-m", "pip", "--version")
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	return cmd.Run() == nil
}

// pipInstall 用指定源安装 requirements；仅使用一个 --index-url，避免多源混合解析。
func (h *Host) pipInstall(ctx context.Context, interp *pyInterpreter, depsDir, reqFile, indexURL string) error {
	runCtx, cancel := context.WithTimeout(ctx, pyInstallerTimeout)
	defer cancel()

	bin, args := interp.Command(
		"-m", "pip", "install",
		"--disable-pip-version-check",
		"--no-input",
		"--timeout", "15",
		"--retries", "1",
		"--target", depsDir,
		"--index-url", indexURL,
		"-r", reqFile,
	)
	cmd := exec.CommandContext(runCtx, bin, args...)
	hideWindow(cmd)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return errors.New(trimOneLine(lastLine(out)+" "+err.Error(), 300))
}

// ---- pip 缓存 ----

func requirementsHash(reqs []string) string {
	sum := sha256.Sum256([]byte(strings.Join(reqs, "\n")))
	return hex.EncodeToString(sum[:])
}

func readPipLock(path string) (pyPipLock, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return pyPipLock{}, false
	}
	var lock pyPipLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return pyPipLock{}, false
	}
	return lock, true
}

// samePyDeps 判断已安装的依赖是否与当前声明一致且未过期。
func samePyDeps(lockPath, reqHash, version string) bool {
	lock, ok := readPipLock(lockPath)
	if !ok {
		return false
	}
	if lock.RequirementsHash != reqHash || lock.Version != version {
		return false
	}
	return time.Since(time.UnixMilli(lock.UpdatedAt)) < pyPipLockTTL
}

// cachedPipSource 返回 24 小时内成功使用过的源。
func cachedPipSource(lockPath, reqHash, version string) string {
	lock, ok := readPipLock(lockPath)
	if !ok || lock.RequirementsHash != reqHash || lock.Version != version {
		return ""
	}
	if time.Since(time.UnixMilli(lock.UpdatedAt)) >= pyPipLockTTL {
		return ""
	}
	return lock.IndexURL
}

func writePipLock(path string, lock pyPipLock) {
	raw, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// ---- 小工具 ----

func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// hostOf 从 URL 中取出主机名，用于错误提示。
func hostOf(rawURL string) string {
	v := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
	if i := strings.IndexAny(v, "/?#"); i >= 0 {
		v = v[:i]
	}
	return v
}

// lastLine 取输出的最后一行非空内容（pip 的结论通常在末尾）。
func lastLine(out []byte) string {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(string(lines[i])); s != "" {
			return s
		}
	}
	return ""
}

func trimOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// 运行期设置的读取入口：插件宿主在拉起 Python 插件时读取一次。
func (h *Host) pythonEnv() pyEnv {
	var out pyEnv
	if h.store == nil {
		return out
	}
	if v, ok, err := h.store.GetSetting(store.SettingPipIndexURLs); err == nil && ok {
		out.IndexURLs = splitList(v)
	}
	return out
}

// PythonStatus Python 运行时的探测结果，供面板展示。
type PythonStatus struct {
	// Found 是否已找到可用解释器。
	Found bool `json:"found"`
	// Path 解释器路径。
	Path string `json:"path"`
	// Version 版本号（形如 3.12.7）。
	Version string `json:"version"`
	// IndexURLs 生效的 pip 源尝试顺序。
	IndexURLs []string `json:"index_urls"`
	// Error 未找到解释器时的原因。
	Error string `json:"error"`
}

// PythonStatus 返回当前 Python 运行时探测结果。
func (h *Host) PythonStatus() PythonStatus {
	env := h.pythonEnv()
	out := PythonStatus{IndexURLs: pipSources(env)}
	interp, err := h.resolvePython()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Found = true
	out.Path = interp.Path
	out.Version = interp.Version
	return out
}
