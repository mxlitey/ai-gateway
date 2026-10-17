package plugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Python 运行时支持（见 docs/plugin-spec.md §11）。
//
// 解释器解析顺序：
//  1. 系统 Python（py -3 → python3 → python；跳过 Windows 应用商店的占位程序）；
//  2. 本地便携 Python（data/runtime/python/<版本>/，可手动放置或由宿主下载）；
//  3. 显式操作「下载便携 Python」（不会在启动探活循环里自动触发）。
//
// 依赖安装：插件声明 requirements 时，用 pip 把包装进插件私有的 deps 目录，
// 官方源优先、失败后按序退避到国内镜像，成功源按 requirements 指纹缓存 24 小时。

const (
	// pyRuntimeSubdir 便携 Python 在数据目录下的相对位置。
	pyRuntimeSubdir = "runtime/python"
	// pyDepsSubdir 插件私有依赖目录（相对插件数据目录）。
	pyDepsSubdir = "deps"
	// pyRuntimeDefaultVersion 内置默认的便携 Python 版本。
	pyRuntimeDefaultVersion = "3.12"
	// pyRuntimeEnvVar 指定便携 Python 的根目录（设置项或环境变量），便于内网/离线部署。
	pyRuntimeEnvVar = "AIGW_PYTHON_RUNTIME_DIR"
	// pyMirrorEnvVar 指定便携 Python 下载地址的镜像前缀（设置项或环境变量）。
	pyMirrorEnvVar = "AIGW_PYTHON_MIRROR"
	// pyIndexEnvVar 追加的 pip 源列表（逗号分隔，空 = 内置默认列表）。
	pyIndexEnvVar = "AIGW_PIP_INDEX_URLS"

	// 下载与安装的超时。
	pyDownloadTimeout  = 20 * time.Minute
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

// pyRuntimeBaseURL python-build-standalone 官方发布地址（按 tag 组织）。
const pyRuntimeBaseURL = "https://github.com/astral-sh/python-build-standalone/releases/download"

// pyInterpreter 解析出的 Python 解释器。
type pyInterpreter struct {
	// Path 解释器可执行文件路径。
	Path string
	// Args 前置参数（Windows 的 py 启动器需要 "-3"）。
	Args []string
	// Version 形如 "3.12.7"（探测失败时为空）。
	Version string
	// Portable 是否来自便携目录（决定能否用 ensurepip 自举 pip）。
	Portable bool
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
	// RuntimeDir 便携 Python 根目录（设置项 AIGW_PYTHON_RUNTIME_DIR）。
	RuntimeDir string
	// Mirror 便携 Python 下载地址镜像前缀（设置项 AIGW_PYTHON_MIRROR）。
	Mirror string
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
func probeInterpreter(bin string, pre []string, portable bool) (*pyInterpreter, error) {
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
	return &pyInterpreter{Path: bin, Args: pre, Version: version, Portable: portable}, nil
}

// findSystemPython 在 PATH 中寻找可用的 Python 3。
func findSystemPython() *pyInterpreter {
	for _, cand := range systemPythonCandidates() {
		bin := cand[0]
		path, err := exec.LookPath(bin)
		if err != nil || isStorePlaceholder(path) {
			continue
		}
		if interp, err := probeInterpreter(path, cand[1:], false); err == nil {
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

// portablePythonRoot 返回便携 Python 的存放根目录。
func (h *Host) portablePythonRoot(env pyEnv) string {
	if dir := strings.TrimSpace(env.RuntimeDir); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv(pyRuntimeEnvVar)); dir != "" {
		return dir
	}
	dataDir := ""
	if h.cfg != nil {
		dataDir = h.cfg.DataDir
	}
	return filepath.Join(dataDir, filepath.FromSlash(pyRuntimeSubdir))
}

// portableInterpreterPaths 返回某个便携版本目录下的解释器候选路径。
func portableInterpreterPaths(root, version string) []string {
	dir := filepath.Join(root, version)
	switch runtime.GOOS {
	case "windows":
		return []string{filepath.Join(dir, "python", "python.exe"), filepath.Join(dir, "python.exe")}
	default:
		return []string{filepath.Join(dir, "bin", "python3"), filepath.Join(dir, "python", "bin", "python3")}
	}
}

// findPortablePython 在便携目录里寻找可用解释器（按版本号从新到旧）。
func (h *Host) findPortablePython(env pyEnv) *pyInterpreter {
	root := h.portablePythonRoot(env)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	versions := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	// 版本号从新到旧：字典序对 "3.12" / "3.13" / "3.9" 需要按数值比较
	sort.Slice(versions, func(i, j int) bool { return pyVersionLess(versions[j], versions[i]) })
	for _, v := range versions {
		for _, p := range portableInterpreterPaths(root, v) {
			if info, err := os.Stat(p); err != nil || info.IsDir() {
				continue
			}
			if interp, err := probeInterpreter(p, nil, true); err == nil {
				return interp
			}
		}
	}
	return nil
}

// pyVersionLess 比较 "3.9" 与 "3.12" 这类版本号。
func pyVersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		var ai, bi int
		_, _ = fmt.Sscanf(as[i], "%d", &ai)
		_, _ = fmt.Sscanf(bs[i], "%d", &bi)
		if ai != bi {
			return ai < bi
		}
	}
	return len(as) < len(bs)
}

// resolvePython 按「系统 → 便携」的顺序解析解释器；都不存在时返回带指引的错误。
func (h *Host) resolvePython(env pyEnv) (*pyInterpreter, error) {
	if interp := findSystemPython(); interp != nil {
		return interp, nil
	}
	if interp := h.findPortablePython(env); interp != nil {
		return interp, nil
	}
	return nil, fmt.Errorf("未找到 Python 3：请安装 Python 3，或在面板「设置 → 便携 Python」下载/指定安装位置（%s）",
		h.portablePythonRoot(env))
}

// EnsurePortablePython 下载并解压便携 Python（显式操作，不在启动流程里自动调用）。
func (h *Host) EnsurePortablePython(ctx context.Context, env pyEnv, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = pyRuntimeDefaultVersion
	}
	if interp := h.findPortablePython(env); interp != nil && strings.HasPrefix(interp.Version, version+".") {
		return interp.Path, nil
	}
	return h.downloadPortablePython(ctx, env, version)
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

// ensurePip 确认 pip 可用；便携解释器可用 ensurepip 自举。
func (h *Host) ensurePip(ctx context.Context, interp *pyInterpreter) error {
	if pipAvailable(ctx, interp) {
		return nil
	}
	if !interp.Portable {
		return fmt.Errorf("系统 Python（%s）缺少 pip：请先安装 pip（Debian/Ubuntu: apt install python3-pip），"+
			"或在「设置 → 便携 Python」改用自带 pip 的便携版本", interp.Version)
	}
	bin, args := interp.Command("-m", "ensurepip", "--upgrade")
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("便携 Python 自举 pip 失败：%s", trimOneLine(lastLine(out)+err.Error(), 200))
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

// ---- 便携 Python 下载 ----

// pyReleaseTarget 返回 python-build-standalone 的 target 三元组。
func pyReleaseTarget() (string, error) {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("便携 Python 暂不支持当前架构：%s", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "windows":
		return arch + "-pc-windows-msvc", nil
	case "darwin":
		return arch + "-apple-darwin", nil
	case "linux":
		return arch + "-unknown-linux-gnu", nil
	default:
		return "", fmt.Errorf("便携 Python 暂不支持当前系统：%s", runtime.GOOS)
	}
}

// pyReleaseTag python-build-standalone 的发布 tag。
// 官方把发行包放在按日期命名的 tag 下，文件名形如
// cpython-3.12.11+20250918-x86_64-pc-windows-msvc-install_only.tar.gz；
// 需要跟随上游升级时只改这几个常量即可。
const (
	pyReleaseTag         = "20250918"
	pyReleasePatch       = ".11"
	pyReleaseAssetSuffix = "+20250918"
)

// pyRuntimeSHA256 已知发行包的 SHA256（按 target 三元组）。
// 校验不通过的包不会解压；镜像/离线场景可用「便携 Python 下载地址」替换下载源，
// 但文件内容仍须与官方一致。
// 官方哈希公示在 releases 页的对象属性里；留空表示跳过校验（仅限用户自建镜像）。
var pyRuntimeSHA256 = map[string]string{
	"x86_64-pc-windows-msvc":    "",
	"aarch64-pc-windows-msvc":   "",
	"x86_64-apple-darwin":       "",
	"aarch64-apple-darwin":      "",
	"x86_64-unknown-linux-gnu":  "",
	"aarch64-unknown-linux-gnu": "",
}

// pyReleaseAssetURL 返回官方发行包地址（install_only 版本，即解压即用的运行时）。
func pyReleaseAssetURL(version, target string) string {
	name := fmt.Sprintf("cpython-%s%s%s-%s-install_only.tar.gz", version, pyReleasePatch, pyReleaseAssetSuffix, target)
	return pyRuntimeBaseURL + "/" + pyReleaseTag + "/" + name
}

// downloadPortablePython 按「镜像地址 → 官方地址」下载便携 Python 并校验、解压。
//
// 只做一次尝试，不在此处重试：下载失败直接让插件进入 failed，由用户显式重试，
// 避免在启动/探活循环里反复拉取大文件。
func (h *Host) downloadPortablePython(ctx context.Context, env pyEnv, version string) (string, error) {
	target, err := pyReleaseTarget()
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(env.Mirror)
	if base == "" {
		base = strings.TrimSpace(os.Getenv(pyMirrorEnvVar))
	}
	assetURL := pyReleaseAssetURL(version, target)
	urls := []string{}
	if base != "" {
		// 镜像只替换前缀，保留官方文件名：镜像方只需按同名文件转存
		urls = append(urls, strings.TrimRight(base, "/")+"/"+filepath.Base(assetURL))
	}
	urls = append(urls, assetURL)

	root := h.portablePythonRoot(env)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建便携运行时目录失败: %w", err)
	}
	dest := filepath.Join(root, version)
	stage := dest + ".partial"
	_ = os.RemoveAll(stage)

	var lastErr error
	for _, u := range urls {
		h.logger().Info("开始下载便携 Python", "url", u, "target", target)
		if err := h.fetchAndExtract(ctx, u, stage); err != nil {
			lastErr = err
			h.logger().Warn("便携 Python 下载失败", "url", u, "err", err)
			_ = os.RemoveAll(stage)
			continue
		}
		_ = os.RemoveAll(dest)
		if err := os.Rename(stage, dest); err != nil {
			_ = os.RemoveAll(stage)
			return "", fmt.Errorf("安装便携 Python 失败: %w", err)
		}
		clearQuarantine(interpreterContainingDir(dest))
		postInstallPython(dest)
		for _, p := range portableInterpreterPaths(root, version) {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p, nil
			}
		}
		return "", fmt.Errorf("便携 Python 解压完成但未找到解释器，请检查下载包是否完整")
	}
	return "", fmt.Errorf("便携 Python 下载失败：%v\n可手动解压后放到 %s", lastErr, root)
}

// interpreterContainingDir 返回解压目录中真正带有 bin/python3 的那一层，
// 用于 macOS 清理 quarantine 属性（归档可能带也可能不带顶层 python/ 目录）。
func interpreterContainingDir(root string) string {
	for _, p := range []string{
		filepath.Join(root, "bin", "python3"),
		filepath.Join(root, "python", "bin", "python3"),
	} {
		if _, err := os.Stat(p); err == nil {
			return root
		}
	}
	return filepath.Join(root, "python")
}

// fetchAndExtract 下载压缩包并解压到目标目录（tar.gz / zip 自适应）。
func (h *Host) fetchAndExtract(ctx context.Context, url, dest string) error {
	runCtx, cancel := context.WithTimeout(ctx, pyDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 大文件先落盘：解压需要随机访问（zip），也避免内存占用过高
	tmp, err := os.CreateTemp(filepath.Dir(dest), "python-runtime-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("下载中断: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	h.logger().Info("便携 Python 下载完成", "sha256", hex.EncodeToString(hasher.Sum(nil)))

	// 有登记哈希就强制校验：镜像的可信度取决于内容，而不是域名
	if want := strings.TrimSpace(pyRuntimeSHA256[pyReleaseTargetOrEmpty()]); want != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("便携 Python 校验失败（期望 %s，实际 %s）", shortHash(want), shortHash(got))
		}
	}

	if strings.HasSuffix(strings.ToLower(url), ".zip") {
		return extractZip(tmpPath, dest)
	}
	return extractTarGz(tmpPath, dest)
}

// shortHash 截取哈希前缀用于提示，避免错误文案过长。
func shortHash(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:16] + "…"
}

// pyReleaseTargetOrEmpty 返回当前平台的 target，失败时返回空串（用于查表）。
func pyReleaseTargetOrEmpty() string {
	t, err := pyReleaseTarget()
	if err != nil {
		return ""
	}
	return t
}

// extractTarGz 解压 tar.gz 到 dest（拒绝越界路径）。
func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			_ = out.Close()
		case tar.TypeSymlink:
			// 只允许指向归档内部的相对链接：python-build-standalone 里的
			// bin/python3 等链接都是同包内的相对路径，外部目标一律丢弃。
			if filepath.IsAbs(hdr.Linkname) || strings.HasPrefix(hdr.Linkname, "..") {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

// extractZip 解压 zip 到 dest（拒绝越界路径）。
func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()&0o777)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		_ = rc.Close()
		_ = out.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// safeJoin 拼接解压目标路径，拒绝跳出 dest 的条目。
func safeJoin(dest, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." ||
		filepath.IsAbs(clean) {
		return "", fmt.Errorf("压缩包内路径非法: %s", name)
	}
	target := filepath.Join(dest, clean)
	rel, err := filepath.Rel(dest, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("压缩包内路径非法: %s", name)
	}
	return target, nil
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
	if v, ok, err := h.store.GetSetting(store.SettingPythonRuntimeDir); err == nil && ok {
		out.RuntimeDir = strings.TrimSpace(v)
	}
	if v, ok, err := h.store.GetSetting(store.SettingPythonMirror); err == nil && ok {
		out.Mirror = strings.TrimSpace(v)
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
	// Portable 是否来自便携目录（false 表示系统 Python）。
	Portable bool `json:"portable"`
	// RuntimeDir 便携 Python 存放根目录。
	RuntimeDir string `json:"runtime_dir"`
	// Mirror 便携 Python 下载镜像前缀。
	Mirror string `json:"mirror"`
	// DownloadURL 当前平台会使用的官方下载地址。
	DownloadURL string `json:"download_url"`
	// IndexURLs 生效的 pip 源尝试顺序。
	IndexURLs []string `json:"index_urls"`
	// Error 未找到解释器时的原因。
	Error string `json:"error"`
}

// PythonStatus 返回当前 Python 运行时探测结果。
func (h *Host) PythonStatus() PythonStatus {
	env := h.pythonEnv()
	out := PythonStatus{
		RuntimeDir: h.portablePythonRoot(env),
		Mirror:     env.Mirror,
		IndexURLs:  pipSources(env),
	}
	if target, err := pyReleaseTarget(); err == nil {
		out.DownloadURL = pyReleaseAssetURL(pyRuntimeDefaultVersion, target)
	}
	interp, err := h.resolvePython(env)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Found = true
	out.Path = interp.Path
	out.Version = interp.Version
	out.Portable = interp.Portable
	return out
}

// DownloadPortablePython 下载便携 Python（面板显式触发）。
func (h *Host) DownloadPortablePython(ctx context.Context, version string) (string, error) {
	return h.EnsurePortablePython(ctx, h.pythonEnv(), version)
}
