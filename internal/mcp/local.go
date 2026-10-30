package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// 本地 MCP 服务器：mcp 目录下的每个子目录放一份 mcp.json + Python 入口脚本。
// 网关启动时扫描、自动登记为「本地」服务器，拉起 Python 进程并监听 127.0.0.1 的随机端口，
// 对上层来说它与远程 Streamable HTTP 服务器没有任何区别。
const (
	// localManifestFile 每个本地服务器目录里的清单文件名。
	localManifestFile = "mcp.json"
	// localDefaultEntry 清单未指定 entry 时的入口脚本。
	localDefaultEntry = "main.py"
	// localDefaultPath 清单未指定 path 时的 Streamable HTTP 路径。
	localDefaultPath = "/mcp"
	// localDefaultStartupMS 清单未指定时的启动就绪超时。
	localDefaultStartupMS = 30000
	// localRestartMaxDelay 崩溃重启的最大退避间隔。
	localRestartMaxDelay = time.Minute
	// localPipTimeout 依赖安装的整体超时。
	localPipTimeout = 5 * time.Minute
)

// localManifest mcp.json 的内容。
type localManifest struct {
	Name             string            `json:"name"`
	Alias            string            `json:"alias"`
	Entry            string            `json:"entry"`
	Path             string            `json:"path"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	Requirements     string            `json:"requirements"`
	StartupTimeoutMS int               `json:"startup_timeout_ms"`

	// dir 子目录名；root 子目录绝对路径。
	dir  string
	root string
}

// localManager 负责扫描 mcp 目录、自动登记服务器并托管 Python 进程。
type localManager struct {
	cfg     *config.Config
	store   *store.Store
	svc     *Service
	log     *slog.Logger
	dataDir string

	mu       sync.Mutex
	procs    map[string]*localProc
	stopping bool

	pyMu    sync.Mutex
	pyFound *localPython
}

// localProc 一台本地服务器的进程句柄。
type localProc struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	stop bool
}

func (p *localProc) setStop() {
	p.mu.Lock()
	p.stop = true
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (p *localProc) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stop
}

// localPython 解析到的 Python 解释器（bin 后接 pre 参数，如 py -3）。
type localPython struct {
	bin string
	pre []string
}

func newLocalManager(cfg *config.Config, st *store.Store, svc *Service) *localManager {
	if cfg == nil {
		return nil
	}
	return &localManager{
		cfg:     cfg,
		store:   st,
		svc:     svc,
		log:     slog.Default(),
		dataDir: cfg.DataDir,
		procs:   map[string]*localProc{},
	}
}

// StartLocal 扫描 mcp 目录并异步拉起各本地服务器；不阻塞网关启动。
func (svc *Service) StartLocal() {
	if svc.local == nil {
		return
	}
	svc.local.start()
}

// StopLocal 结束所有本地 MCP 进程。
func (svc *Service) StopLocal() {
	if svc.local == nil {
		return
	}
	svc.local.stopAll()
}

// start 同步完成目录扫描与登记，进程启动放到后台，避免拖慢网关就绪。
func (lm *localManager) start() {
	manifests, err := scanLocalManifests(lm.cfg.MCPDir)
	if err != nil {
		lm.log.Warn("本地 MCP 目录扫描失败", "dir", lm.cfg.MCPDir, "err", err)
		return
	}
	servers, err := lm.store.ListMCPServers()
	if err != nil {
		lm.log.Warn("读取 MCP 服务器失败", "err", err)
		return
	}
	bySource := map[string]*model.MCPServer{}
	for i := range servers {
		if servers[i].IsLocal() {
			bySource[servers[i].Source] = &servers[i]
		}
	}

	seen := map[string]bool{}
	stale := false
	for _, mf := range manifests {
		source := model.LocalSourcePrefix + mf.dir
		seen[source] = true
		srv := lm.ensureServer(mf, bySource[source])
		if srv == nil || !srv.Enabled {
			continue
		}
		lm.spawn(srv, mf)
	}
	// 目录被删除的本地服务器：移除登记，历史调用日志保留在 mcp_call_logs
	for i := range servers {
		if !servers[i].IsLocal() || seen[servers[i].Source] {
			continue
		}
		if err := lm.store.DeleteMCPServer(servers[i].ID); err != nil {
			lm.log.Warn("移除失效的本地 MCP 服务器失败", "server", servers[i].Name, "err", err)
			continue
		}
		lm.svc.ForgetTools(servers[i].ID)
		stale = true
	}
	if stale {
		lm.svc.RebuildRoutes()
	}
}

// ensureServer 登记或同步本地服务器：只覆盖展示名/别名/来源，保留用户改过的启用状态、
// 超时、用量上限等设置。
func (lm *localManager) ensureServer(mf localManifest, existing *model.MCPServer) *model.MCPServer {
	name := strings.TrimSpace(mf.Name)
	if name == "" {
		name = mf.dir
	}
	if existing != nil {
		if existing.Name != name {
			existing.Name = name
			if err := lm.store.UpdateMCPServer(existing); err != nil {
				lm.log.Warn("同步本地 MCP 名称失败", "server", name, "err", err)
			}
		}
		return existing
	}

	alias := normalizeAlias(mf.Alias)
	if alias == "" {
		alias = normalizeAlias(name)
	}
	if alias == "" {
		alias = "local"
	}
	// 与手工服务器的命名空间去重
	if used, err := lm.store.UsedMCPAliases(); err == nil {
		base := alias
		for i := 2; used[alias]; i++ {
			alias = fmt.Sprintf("%s-%d", base, i)
		}
	}
	order, err := lm.store.NextMCPServerSortOrder()
	if err != nil {
		lm.log.Warn("读取 MCP 排序失败", "err", err)
	}
	srv := &model.MCPServer{
		ID:             id.UUID(),
		Name:           name,
		Alias:          alias,
		Headers:        []model.Header{},
		Keys:           []model.MCPKey{},
		KeyStrategy:    model.KeyStrategyRotate,
		DailyLimit:     -1,
		ToolLimits:     map[string]int{},
		BillableMode:   model.MCPBillableSuccess,
		TimeoutMS:      localDefaultStartupMS,
		Enabled:        true,
		Source:         model.LocalSourcePrefix + mf.dir,
		SortOrder:      order,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := lm.store.CreateMCPServer(srv); err != nil {
		lm.log.Warn("登记本地 MCP 服务器失败", "server", name, "err", err)
		return nil
	}
	lm.log.Info("已登记本地 MCP 服务器", "server", name, "dir", mf.dir)
	return srv
}

// spawn 在后台托管一台本地服务器：分配端口、装依赖、拉起进程、等待就绪，退出后按退避重启。
func (lm *localManager) spawn(srv *model.MCPServer, mf localManifest) {
	p := &localProc{}
	lm.mu.Lock()
	if old := lm.procs[mf.dir]; old != nil {
		old.setStop()
	}
	lm.procs[mf.dir] = p
	lm.mu.Unlock()

	go lm.run(srv, mf, p)
}

func (lm *localManager) run(srv *model.MCPServer, mf localManifest, p *localProc) {
	backoff := time.Second
	for {
		if lm.isStopping() || p.stopped() {
			return
		}
		py, err := lm.findPython()
		if err != nil {
			lm.svc.setState(srv.ID, "error", err.Error())
			lm.log.Warn("本地 MCP 找不到可用的 Python", "server", srv.Name, "err", err)
			if !lm.sleep(backoff) || p.stopped() {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		if err := lm.ensureDeps(py, mf); err != nil {
			lm.svc.setState(srv.ID, "error", err.Error())
			lm.log.Warn("本地 MCP 依赖安装失败", "server", srv.Name, "err", err)
			if !lm.sleep(backoff) || p.stopped() {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		port, err := freePort()
		if err != nil {
			lm.svc.setState(srv.ID, "error", err.Error())
			if !lm.sleep(backoff) || p.stopped() {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		cmd, err := lm.launch(py, mf, port)
		if err != nil {
			lm.svc.setState(srv.ID, "error", "启动本地 MCP 进程失败: "+err.Error())
			lm.log.Warn("启动本地 MCP 进程失败", "server", srv.Name, "err", err)
			if !lm.sleep(backoff) || p.stopped() {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		p.mu.Lock()
		p.cmd = cmd
		p.mu.Unlock()

		// 端口在启动时已确定，立刻回写地址：面板与工具调用不必等就绪探测完成
		url := localURL(port, mf.Path)
		srv.URL = url
		lm.setURL(srv.ID, url)
		if err := waitLocalReady(url, time.Duration(mf.StartupTimeoutMS)*time.Millisecond); err != nil {
			p.mu.Lock()
			stopped := p.stop
			p.mu.Unlock()
			if !stopped {
				lm.svc.setState(srv.ID, "error", "启动超时: "+err.Error())
				lm.log.Warn("本地 MCP 启动未就绪", "server", srv.Name, "url", url, "err", err)
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			if stopped || lm.isStopping() {
				return
			}
			if !lm.sleep(backoff) || p.stopped() {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// 就绪：记录状态即可，地址已在启动时写过
		lm.svc.setState(srv.ID, "ok", "")
		lm.log.Info("本地 MCP 已就绪", "server", srv.Name, "url", url, "pid", cmd.Process.Pid)
		lm.refreshToolsAsync(srv.ID)
		backoff = time.Second

		_ = cmd.Wait()
		if lm.isStopping() || p.stopped() {
			return
		}
		lm.svc.setState(srv.ID, "error", "本地 MCP 进程已退出")
		lm.log.Warn("本地 MCP 进程退出，准备重启", "server", srv.Name)
		if !lm.sleep(backoff) || p.stopped() {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// refreshToolsAsync 进程就绪后自动拉一次工具列表，省去面板上的手动刷新。
func (lm *localManager) refreshToolsAsync(serverID string) {
	go func() {
		latest, err := lm.store.GetMCPServer(serverID)
		if err != nil || latest == nil || !latest.Enabled {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := lm.svc.RefreshTools(ctx, latest); err != nil {
			lm.log.Warn("本地 MCP 工具列表拉取失败", "server", latest.Name, "err", err)
		}
	}()
}

// setURL 把实际监听地址写回数据库。按 ID 重新读取，避免用启动时的快照覆盖面板上的改动。
func (lm *localManager) setURL(serverID, url string) {
	if serverID == "" {
		return
	}
	latest, err := lm.store.GetMCPServer(serverID)
	if err != nil {
		lm.log.Warn("读取本地 MCP 记录失败", "err", err)
		return
	}
	if latest == nil || latest.URL == url {
		return
	}
	latest.URL = url
	if err := lm.store.UpdateMCPServer(latest); err != nil {
		lm.log.Warn("回写本地 MCP 地址失败", "server", latest.Name, "err", err)
	}
}

func (lm *localManager) isStopping() bool {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	return lm.stopping
}

// sleep 返回 false 表示已被要求停止。
func (lm *localManager) sleep(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if lm.isStopping() {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > localRestartMaxDelay {
		return localRestartMaxDelay
	}
	return d
}

func (lm *localManager) stopAll() {
	lm.mu.Lock()
	lm.stopping = true
	procs := make([]*localProc, 0, len(lm.procs))
	for _, p := range lm.procs {
		procs = append(procs, p)
	}
	lm.mu.Unlock()
	for _, p := range procs {
		p.setStop()
	}
}

// ---- 目录扫描 ----

// scanLocalManifests 读取 mcp 目录下所有含 mcp.json 的一级子目录。
func scanLocalManifests(root string) ([]localManifest, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []localManifest{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, localManifestFile))
		if err != nil {
			continue
		}
		var mf localManifest
		if err := json.Unmarshal(raw, &mf); err != nil {
			slog.Warn("本地 MCP 清单解析失败", "dir", e.Name(), "err", err)
			continue
		}
		mf.dir = e.Name()
		mf.root = dir
		if mf.Entry == "" {
			mf.Entry = localDefaultEntry
		}
		if mf.Path == "" {
			mf.Path = localDefaultPath
		}
		if !strings.HasPrefix(mf.Path, "/") {
			mf.Path = "/" + mf.Path
		}
		if mf.Requirements == "" {
			mf.Requirements = "requirements.txt"
		}
		if mf.StartupTimeoutMS <= 0 {
			mf.StartupTimeoutMS = localDefaultStartupMS
		}
		entry, ok := safeLocalJoin(dir, mf.Entry)
		if !ok {
			slog.Warn("本地 MCP 入口路径不合法", "dir", e.Name(), "entry", mf.Entry)
			continue
		}
		if info, err := os.Stat(entry); err != nil || info.IsDir() {
			slog.Warn("本地 MCP 入口脚本不存在", "dir", e.Name(), "entry", mf.Entry)
			continue
		}
		out = append(out, mf)
	}
	return out, nil
}

// safeLocalJoin 把清单里的相对路径限制在服务器目录内，拒绝越界路径。
func safeLocalJoin(root, rel string) (string, bool) {
	if filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(root, clean), true
}

// ---- Python 与依赖 ----

// findPython 依次尝试 AIGW_MCP_PYTHON、便携 Python、系统 Python，并缓存结果。
func (lm *localManager) findPython() (*localPython, error) {
	lm.pyMu.Lock()
	defer lm.pyMu.Unlock()
	if lm.pyFound != nil {
		return lm.pyFound, nil
	}
	if v := strings.TrimSpace(os.Getenv("AIGW_MCP_PYTHON")); v != "" {
		if py, err := probePython(v, nil); err == nil {
			lm.pyFound = py
			return py, nil
		}
	}
	root := filepath.Join(lm.dataDir, "runtime", "python")
	if entries, err := os.ReadDir(root); err == nil {
		versions := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				versions = append(versions, e.Name())
			}
		}
		sort.Slice(versions, func(i, j int) bool { return pyVersionLess(versions[j], versions[i]) })
		for _, v := range versions {
			for _, p := range portablePythonPaths(root, v) {
				if info, err := os.Stat(p); err != nil || info.IsDir() {
					continue
				}
				if py, err := probePython(p, nil); err == nil {
					lm.pyFound = py
					return py, nil
				}
			}
		}
	}
	for _, cand := range systemPythonCandidates() {
		path, err := exec.LookPath(cand[0])
		if err != nil || isStorePython(path) {
			continue
		}
		if py, err := probePython(path, cand[1:]); err == nil {
			lm.pyFound = py
			return py, nil
		}
	}
	return nil, fmt.Errorf("未找到可用的 Python 3，请安装 Python 或使用插件页的便携运行时")
}

func systemPythonCandidates() [][]string {
	if runtime.GOOS == "windows" {
		return [][]string{{"py", "-3"}, {"python3"}, {"python"}}
	}
	return [][]string{{"python3"}, {"python"}}
}

func isStorePython(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, `\windowsapps\`) && strings.HasSuffix(lower, ".exe")
}

func portablePythonPaths(root, version string) []string {
	dir := filepath.Join(root, version)
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(dir, "python", "python.exe"), filepath.Join(dir, "python.exe")}
	}
	return []string{filepath.Join(dir, "bin", "python3"), filepath.Join(dir, "python", "bin", "python3")}
}

// pyVersionLess 按数值比较 Python 版本号（3.9 小于 3.12）。
func pyVersionLess(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

func probePython(bin string, pre []string) (*localPython, error) {
	args := append(append([]string{}, pre...), "-c", "import sys;sys.stdout.write('%d.%d.%d'%sys.version_info[:3])")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideLocalWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(string(out))
	if !strings.HasPrefix(version, "3.") {
		return nil, fmt.Errorf("需要 Python 3，实际为 %s", version)
	}
	return &localPython{bin: bin, pre: pre}, nil
}

// ensureDeps 按 requirements.txt 安装依赖到 data/mcp/<dir>/deps。
// requirements 不存在时直接跳过；清单未变化则复用已装好的目录。
func (lm *localManager) ensureDeps(py *localPython, mf localManifest) error {
	req, ok := safeLocalJoin(mf.root, mf.Requirements)
	if !ok {
		return fmt.Errorf("requirements 路径不合法: %s", mf.Requirements)
	}
	info, err := os.Stat(req)
	if err != nil || info.IsDir() || !hasRequirements(req) {
		return nil
	}
	deps := filepath.Join(lm.dataDir, "mcp", mf.dir, "deps")
	if err := os.MkdirAll(deps, 0o755); err != nil {
		return err
	}
	stamp := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	if raw, err := os.ReadFile(filepath.Join(deps, ".stamp")); err == nil && strings.TrimSpace(string(raw)) == stamp {
		return nil
	}

	sources := []string{
		"https://pypi.org/simple",
		"https://pypi.tuna.tsinghua.edu.cn/simple",
		"https://mirrors.aliyun.com/pypi/simple/",
		"https://mirrors.cloud.tencent.com/pypi/simple",
		"https://pypi.mirrors.ustc.edu.cn/simple",
	}
	if v := strings.TrimSpace(os.Getenv("AIGW_PIP_INDEX_URLS")); v != "" {
		parts := []string{}
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			sources = parts
		}
	}

	var lastErr error
	for _, src := range sources {
		ctx, cancel := context.WithTimeout(context.Background(), localPipTimeout)
		args := append(append([]string{}, py.pre...),
			"-m", "pip", "install", "--disable-pip-version-check", "--no-input",
			"--target", deps, "-r", req, "--index-url", src,
		)
		cmd := exec.CommandContext(ctx, py.bin, args...)
		cmd.Dir = mf.root
		hideLocalWindow(cmd)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			_ = os.WriteFile(filepath.Join(deps, ".stamp"), []byte(stamp), 0o644)
			return nil
		}
		lastErr = fmt.Errorf("%s: %v (%s)", src, err, tailString(string(out), 300))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的 pip 源")
	}
	return fmt.Errorf("安装 requirements.txt 失败: %w", lastErr)
}

// hasRequirements 判断 requirements 文件里是否有真正的依赖行（跳过空行与注释）。
func hasRequirements(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}
	return false
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---- 进程启动与就绪探测 ----

func (lm *localManager) launch(py *localPython, mf localManifest, port int) (*exec.Cmd, error) {
	entry, ok := safeLocalJoin(mf.root, mf.Entry)
	if !ok {
		return nil, fmt.Errorf("入口路径不合法")
	}
	args := append([]string{}, py.pre...)
	args = append(args, entry)
	for _, a := range mf.Args {
		args = append(args, templatePort(a, port))
	}

	env := os.Environ()
	env = append(env,
		"PYTHONUNBUFFERED=1",
		fmt.Sprintf("AIGW_MCP_PORT=%d", port),
		fmt.Sprintf("PORT=%d", port),
	)
	deps := filepath.Join(lm.dataDir, "mcp", mf.dir, "deps")
	pp := deps + string(os.PathListSeparator) + mf.root
	if old := strings.TrimSpace(os.Getenv("PYTHONPATH")); old != "" {
		pp += string(os.PathListSeparator) + old
	}
	env = append(env, "PYTHONPATH="+pp)
	// 私有可写目录：运行期状态写这里，不要写进 mcp 目录（升级/换镜像会覆盖）
	dataPath := filepath.Join(lm.dataDir, "mcp", mf.dir)
	_ = os.MkdirAll(dataPath, 0o755)
	env = append(env, "AIGW_MCP_DATA="+dataPath)
	for k, v := range mf.Env {
		env = append(env, k+"="+templatePort(v, port))
	}

	cmd := exec.Command(py.bin, args...)
	cmd.Dir = mf.root
	cmd.Env = env
	hideLocalWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func templatePort(s string, port int) string {
	return strings.ReplaceAll(s, "{{port}}", strconv.Itoa(port))
}

func localURL(port int, path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// waitLocalReady 轮询 MCP initialize，直到上游可响应或超时。
func waitLocalReady(url string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = localDefaultStartupMS * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		if err := probeLocalMCP(client, url); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("等待超时")
	}
	return lastErr
}

func probeLocalMCP(client *http.Client, url string) error {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ai-gateway","version":"1.0.0"}}}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
