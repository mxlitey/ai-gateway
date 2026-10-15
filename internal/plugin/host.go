package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

// 插件运行状态。
const (
	StatusDiscovered = "discovered"
	StatusStarting   = "starting"
	StatusRunning    = "running"
	StatusUnhealthy  = "unhealthy"
	StatusRestarting = "restarting"
	StatusFailed     = "failed"
	StatusStopped    = "stopped"
)

const (
	probeInterval      = 2 * time.Second // 探活间隔
	probeTimeout       = 2 * time.Second // 单次探活超时
	unhealthyThreshold = 3               // 连续失败多少次判定不健康
	maxFailures        = 5               // 连续失败多少次后放弃自动重启
	stopGracePeriod    = 5 * time.Second // 优雅停止等待时长
	defaultChannelKey  = "gw-local"      // 插件没有账号时使用的占位渠道密钥
)

type instance struct {
	manifest *Manifest
	dir      string
	port     int

	status    string
	lastErr   string
	lastSeen  int64
	failures  int
	enabled   bool
	autostart bool

	active bool
	cmd    *exec.Cmd
	proc   *procHandle
	exited chan struct{}
	stop   chan struct{}
	done   chan struct{}
}

// requestStop 请求监管循环停止（可重复调用）。
func (inst *instance) requestStop() {
	if inst.stop == nil {
		return
	}
	select {
	case <-inst.stop:
	default:
		close(inst.stop)
	}
}

// Host 插件宿主。
type Host struct {
	cfg    *config.Config
	store  *store.Store
	log    *slog.Logger
	client *http.Client

	mu       sync.Mutex
	items    map[string]*instance
	shutdown bool
}

// New 创建插件宿主。
func New(cfg *config.Config, st *store.Store, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	return &Host{
		cfg:    cfg,
		store:  st,
		log:    logger,
		items:  map[string]*instance{},
		client: &http.Client{Transport: &http.Transport{DisableCompression: true}},
	}
}

// ---------- 发现与注册 ----------

// Discover 扫描插件目录，注册/更新插件，并按开关启动。
func (h *Host) Discover() error {
	entries, err := os.ReadDir(h.cfg.PluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			h.log.Info("插件目录不存在，跳过扫描", "dir", h.cfg.PluginsDir)
			return nil
		}
		return fmt.Errorf("读取插件目录失败: %w", err)
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(h.cfg.PluginsDir, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, manifestFileName)); err != nil {
			continue // 不是插件目录
		}

		manifest, err := LoadManifest(dir)
		if err != nil {
			h.log.Error("加载插件清单失败", "dir", dir, "err", err)
			continue
		}
		// 目录仍在、清单可读就算「插件仍存在」：即使校验不通过也不能删它的渠道
		if pid := strings.TrimSpace(manifest.ID); pid != "" {
			seen[pid] = true
		}
		if err := Validate(dir, manifest); err != nil {
			h.log.Error("插件清单校验失败", "plugin", manifest.ID, "err", err)
			h.registerInvalid(dir, manifest, err)
			continue
		}
		h.register(manifest)
	}

	// 磁盘上已不存在的插件：停止进程、删除渠道与记录，避免面板里残留幽灵条目
	for _, pluginID := range h.knownPluginIDs(seen) {
		dir := h.pluginDirOf(pluginID)
		if dir == "" || pluginDirExists(dir) {
			continue
		}
		h.log.Info("插件目录已移除，清理记录", "plugin", pluginID)
		_ = h.stopInternal(pluginID, true)

		h.mu.Lock()
		delete(h.items, pluginID)
		h.mu.Unlock()

		// 插件已被移除，它注册的渠道不能只是停用，否则会在「渠道管理」里
		// 留下无法删除的幽灵条目，这里直接清掉。
		h.removePluginChannels(pluginID)
		if err := h.store.DeletePlugin(pluginID); err != nil {
			h.log.Error("删除插件记录失败", "plugin", pluginID, "err", err)
		}
	}
	// 兜底：清理磁盘上已不存在、但数据库/内存里也没有记录的插件渠道
	h.purgeOrphanChannels(seen)
	return nil
}

// knownPluginIDs 返回内存与数据库中记录的全部插件 id，跳过本次已扫描到的。
func (h *Host) knownPluginIDs(seen map[string]bool) []string {
	ids := map[string]bool{}

	h.mu.Lock()
	for pid := range h.items {
		ids[pid] = true
	}
	h.mu.Unlock()

	if rows, err := h.store.ListPlugins(); err == nil {
		for _, r := range rows {
			ids[r.ID] = true
		}
	}

	out := make([]string, 0, len(ids))
	for pid := range ids {
		if seen[pid] {
			continue
		}
		out = append(out, pid)
	}
	return out
}

// pluginDirOf 返回插件目录：优先内存实例，其次数据库记录。
func (h *Host) pluginDirOf(pluginID string) string {
	h.mu.Lock()
	if inst := h.items[pluginID]; inst != nil {
		dir := inst.dir
		h.mu.Unlock()
		return dir
	}
	h.mu.Unlock()

	if row, err := h.store.GetPlugin(pluginID); err == nil && row != nil {
		return row.Dir
	}
	return ""
}

// pluginDirExists 判断目录是否仍是一个插件目录。
func pluginDirExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, manifestFileName))
	return err == nil
}

func (h *Host) register(manifest *Manifest) {
	// 已注册且目录/版本未变则跳过
	h.mu.Lock()
	if existing, ok := h.items[manifest.ID]; ok &&
		existing.dir == manifest.Dir && existing.manifest.Version == manifest.Version {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	// 端口由插件作者在 plugin.json 里指定，宿主不再代为分配
	port := manifest.Port

	inst := &instance{
		manifest:  manifest,
		dir:       manifest.Dir,
		port:      port,
		status:    StatusDiscovered,
		enabled:   true,
		autostart: true,
	}
	// 沿用用户持久化的开关设置
	if row, err := h.store.GetPlugin(manifest.ID); err == nil && row != nil {
		inst.enabled = row.Enabled
		inst.autostart = row.Autostart
	}

	h.mu.Lock()
	h.items[manifest.ID] = inst
	shuttingDown := h.shutdown
	h.mu.Unlock()

	if err := h.store.UpsertPlugin(&model.Plugin{
		ID:          manifest.ID,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Dir:         manifest.Dir,
		Port:        port,
		Enabled:     inst.enabled,
		Autostart:   inst.autostart,
		LastStatus:  StatusDiscovered,
		InstalledAt: time.Now().UnixMilli(),
	}); err != nil {
		h.log.Error("写入插件记录失败", "plugin", manifest.ID, "err", err)
	}
	h.setStatus(inst, StatusDiscovered, "")

	if !shuttingDown && inst.enabled && inst.autostart {
		_ = h.startInternal(manifest.ID, false)
	}
}

func (h *Host) registerInvalid(dir string, manifest *Manifest, cause error) {
	pluginID := strings.TrimSpace(manifest.ID)
	if pluginID == "" {
		pluginID = filepath.Base(dir)
	}

	inst := &instance{
		manifest: manifest,
		dir:      dir,
		status:   StatusFailed,
		lastErr:  cause.Error(),
		enabled:  false,
	}

	h.mu.Lock()
	h.items[pluginID] = inst
	h.mu.Unlock()

	if err := h.store.UpsertPlugin(&model.Plugin{
		ID:          pluginID,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Dir:         dir,
		Enabled:     false,
		Autostart:   false,
		LastStatus:  StatusFailed,
		LastError:   cause.Error(),
		InstalledAt: time.Now().UnixMilli(),
	}); err != nil {
		h.log.Error("写入插件记录失败", "plugin", pluginID, "err", err)
	}
	_ = h.store.SavePluginFlags(pluginID, false, false)
	_ = h.store.SavePluginStatus(pluginID, StatusFailed, cause.Error())
}

// ---------- 对外操作 ----------

// List 返回插件列表（DB 记录 + 内存运行状态）。
func (h *Host) List() []model.Plugin {
	rows, err := h.store.ListPlugins()
	if err != nil {
		h.log.Error("读取插件列表失败", "err", err)
		rows = nil
	}
	byID := make(map[string]model.Plugin, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]model.Plugin, 0, len(h.items)+len(byID))
	for pid, inst := range h.items {
		p := byID[pid]
		p.ID = pid
		p.Name = inst.manifest.Name
		p.Version = inst.manifest.Version
		p.Dir = inst.dir
		p.Port = inst.port
		p.Enabled = inst.enabled
		p.Autostart = inst.autostart
		p.LastStatus = inst.status
		p.LastError = inst.lastErr
		p.LastSeenAt = inst.lastSeen
		p.Console = inst.manifest.HasConsole()
		p.ConsoleTitle = inst.manifest.Name
		p.ConsoleBasePath = inst.manifest.ConsoleBasePath()
		p.Capabilities = inst.manifest.KnownCapabilities()
		if p.InstalledAt == 0 {
			p.InstalledAt = time.Now().UnixMilli()
		}
		out = append(out, p)
		delete(byID, pid)
	}
	// 仅存在于 DB 的残留记录
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Remove 卸载插件：停止进程、停用其渠道、删除磁盘目录与数据库记录。
func (h *Host) Remove(pluginID string) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("缺少插件 id")
	}

	dir := h.pluginDirOf(pluginID)
	_ = h.stopInternal(pluginID, true)

	h.mu.Lock()
	delete(h.items, pluginID)
	h.mu.Unlock()

	// 卸载插件时连同它注册的渠道一起删除，避免渠道页残留无法删除的条目
	h.removePluginChannels(pluginID)

	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("删除插件目录失败: %w", err)
		}
	}
	if err := h.store.DeletePlugin(pluginID); err != nil {
		return fmt.Errorf("删除插件记录失败: %w", err)
	}
	h.log.Info("已卸载插件", "plugin", pluginID, "dir", dir)
	return nil
}

// Start 启动插件（并持久化「启用」）。
func (h *Host) Start(pluginID string) error {
	return h.startInternal(pluginID, true)
}

// Stop 停止插件（并持久化「停用」）。
func (h *Host) Stop(pluginID string) error {
	return h.stopInternal(pluginID, false)
}

// Restart 重启插件（不改动启用开关）。
func (h *Host) Restart(pluginID string) error {
	if err := h.stopInternal(pluginID, true); err != nil {
		return err
	}
	return h.startInternal(pluginID, true)
}

// ConsoleTarget 返回插件控制台的反向代理基地址（形如 http://127.0.0.1:20800/console）。
// 插件未声明控制台、未运行或未分配端口时返回错误。
func (h *Host) ConsoleTarget(pluginID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	inst, ok := h.items[pluginID]
	if !ok {
		return "", fmt.Errorf("插件不存在: %s", pluginID)
	}
	if !inst.manifest.HasConsole() {
		return "", fmt.Errorf("插件未声明控制台: %s", pluginID)
	}
	if inst.status != StatusRunning || inst.port == 0 {
		return "", fmt.Errorf("插件当前状态为 %s，请先启动后再使用控制台", inst.status)
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", inst.port, inst.manifest.ConsoleBasePath()), nil
}

// SetFlags 修改启用 / 自启开关，并按需要启停。
func (h *Host) SetFlags(pluginID string, enabled, autostart *bool) error {
	h.mu.Lock()
	inst, ok := h.items[pluginID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("插件不存在: %s", pluginID)
	}
	if enabled != nil {
		inst.enabled = *enabled
	}
	if autostart != nil {
		inst.autostart = *autostart
	}
	next := *inst
	h.mu.Unlock()

	if err := h.store.SavePluginFlags(pluginID, next.enabled, next.autostart); err != nil {
		return err
	}
	if next.enabled {
		return h.startInternal(pluginID, false)
	}
	return h.stopInternal(pluginID, true)
}

// Shutdown 停止所有插件进程（幂等）。
func (h *Host) Shutdown() {
	h.mu.Lock()
	if h.shutdown {
		h.mu.Unlock()
		return
	}
	h.shutdown = true
	ids := make([]string, 0, len(h.items))
	for pid := range h.items {
		ids = append(ids, pid)
	}
	h.mu.Unlock()

	for _, pid := range ids {
		_ = h.stopInternal(pid, true)
	}

	// 兜底：强杀仍在运行的残留进程，并关闭作业对象回收整棵进程树
	h.mu.Lock()
	defer h.mu.Unlock()
	for pid, inst := range h.items {
		if inst.proc != nil {
			inst.proc.release()
		}
		if inst.cmd == nil || inst.cmd.Process == nil {
			continue
		}
		select {
		case <-inst.exited:
		default:
			h.log.Warn("发现残留插件进程，强制结束", "plugin", pid)
			_ = inst.cmd.Process.Kill()
		}
	}
}

// ---------- 内部：启停 ----------

func (h *Host) startInternal(pluginID string, persist bool) error {
	h.mu.Lock()
	inst, ok := h.items[pluginID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("插件不存在: %s", pluginID)
	}
	if inst.status == StatusFailed && inst.port == 0 {
		h.mu.Unlock()
		return fmt.Errorf("插件清单非法，无法启动: %s", pluginID)
	}
	if inst.active {
		h.mu.Unlock()
		return nil
	}
	inst.enabled = true
	inst.failures = 0
	inst.stop = make(chan struct{})
	inst.done = make(chan struct{})
	inst.active = true
	autostart := inst.autostart
	port := inst.port
	h.mu.Unlock()

	if persist {
		if err := h.store.SavePluginFlags(pluginID, true, autostart); err != nil {
			h.log.Error("保存插件开关失败", "plugin", pluginID, "err", err)
		}
	}

	h.log.Info("启动插件", "plugin", pluginID, "port", port)
	go h.supervise(inst)
	return nil
}

func (h *Host) stopInternal(pluginID string, keepEnabled bool) error {
	h.mu.Lock()
	inst, ok := h.items[pluginID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("插件不存在: %s", pluginID)
	}
	if !keepEnabled {
		inst.enabled = false
	}
	autostart := inst.autostart
	active := inst.active
	done := inst.done
	inst.requestStop()
	h.mu.Unlock()

	if !keepEnabled {
		if err := h.store.SavePluginFlags(pluginID, false, autostart); err != nil {
			h.log.Error("保存插件开关失败", "plugin", pluginID, "err", err)
		}
	}

	if active && done != nil {
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			h.log.Warn("等待插件停止超时，强制结束", "plugin", pluginID)
			h.forceKill(inst)
		}
	} else {
		h.stopProcess(inst)
		h.setStatus(inst, StatusStopped, "")
		h.syncChannels(inst, false)
	}
	return nil
}

// ---------- 内部：监管循环 ----------

func (h *Host) supervise(inst *instance) {
	defer func() {
		h.mu.Lock()
		inst.active = false
		done := inst.done
		h.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()

	backoffs := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

	for {
		if h.stopRequested(inst) {
			h.finishStopped(inst)
			return
		}

		h.setStatus(inst, StatusStarting, "")
		if err := h.spawn(inst); err != nil {
			inst.failures++
			h.setStatus(inst, StatusFailed, err.Error())
			h.syncChannels(inst, false)
			if inst.failures >= maxFailures {
				return
			}
			if !h.sleepOrStop(inst, backoffs[minInt(inst.failures-1, len(backoffs)-1)]) {
				continue
			}
			continue
		}

		if !h.waitReady(inst) {
			h.stopProcess(inst)
			if h.stopRequested(inst) {
				h.finishStopped(inst)
				return
			}
			inst.failures++
			h.setStatus(inst, StatusUnhealthy, "启动超时，未通过探活")
			h.syncChannels(inst, false)
			if inst.failures >= maxFailures {
				h.setStatus(inst, StatusFailed, "启动反复失败，已停止自动重启")
				return
			}
			h.setStatus(inst, StatusRestarting, "")
			h.sleepOrStop(inst, backoffs[minInt(inst.failures-1, len(backoffs)-1)])
			continue
		}

		inst.failures = 0
		h.setStatus(inst, StatusRunning, "")
		h.syncChannels(inst, true)

		reason := h.watch(inst)
		if h.stopRequested(inst) {
			h.stopProcess(inst)
			h.finishStopped(inst)
			return
		}

		h.stopProcess(inst)
		inst.failures++
		h.setStatus(inst, StatusUnhealthy, reason)
		h.syncChannels(inst, false)
		if inst.failures >= maxFailures {
			h.setStatus(inst, StatusFailed, reason+"；已停止自动重启")
			return
		}
		h.setStatus(inst, StatusRestarting, "")
		h.sleepOrStop(inst, backoffs[minInt(inst.failures-1, len(backoffs)-1)])
	}
}

func (h *Host) finishStopped(inst *instance) {
	h.setStatus(inst, StatusStopped, "")
	h.syncChannels(inst, false)
}

func (h *Host) stopRequested(inst *instance) bool {
	select {
	case <-inst.stop:
		return true
	default:
		return false
	}
}

// sleepOrStop 等待 d；期间收到停止请求返回 false。
func (h *Host) sleepOrStop(inst *instance, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-inst.stop:
		return false
	case <-timer.C:
		return true
	}
}

// ---------- 内部：进程 ----------

func (h *Host) spawn(inst *instance) error {
	// 端口由插件自己在 plugin.json 里指定，占用即视为配置错误，直接给出可操作的提示
	if !portAvailable(inst.port) {
		return fmt.Errorf("端口 %d 已被占用，请在 plugin.json 里换一个端口", inst.port)
	}
	vars := h.vars(inst)
	if err := os.MkdirAll(vars.Data, 0o755); err != nil {
		return fmt.Errorf("创建插件数据目录失败: %w", err)
	}

	entry := inst.manifest.EntryPath()
	args := make([]string, 0, len(inst.manifest.Args))
	for _, a := range inst.manifest.Args {
		args = append(args, substitute(a, vars))
	}
	// Windows 上 .cmd/.bat 需要经命令解释器转发
	entry, args = wrapCommand(entry, args)

	cmd := exec.Command(entry, args...)
	cmd.Dir = inst.dir

	env := os.Environ()
	for k, v := range inst.manifest.Env {
		env = append(env, k+"="+substitute(v, vars))
	}
	env = append(env,
		"AI_GATEWAY_PLUGIN_ID="+inst.manifest.ID,
		"AI_GATEWAY_PLUGIN_PORT="+strconv.Itoa(inst.port),
		"AI_GATEWAY_PLUGIN_DIR="+inst.dir,
		"AI_GATEWAY_PLUGIN_DATA="+vars.Data,
	)
	cmd.Env = env

	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("创建输出管道失败: %w", err)
	}
	cmd.Stdout = pipeWriter
	cmd.Stderr = pipeWriter
	applyProcAttr(cmd)

	proc := newProcHandle()
	if err := cmd.Start(); err != nil {
		proc.release()
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
		return fmt.Errorf("启动插件进程失败: %w", err)
	}
	// 把进程树纳入作业对象：网关退出时整棵进程树会被一并结束
	if err := proc.attach(cmd); err != nil {
		h.log.Warn("进程树托管失败，插件子进程可能不会随网关退出而结束",
			"plugin", inst.manifest.ID, "err", err)
		proc.release()
		proc = nil
	}
	// 父进程不再持有写端，子进程退出后读端会 EOF
	_ = pipeWriter.Close()

	exited := make(chan struct{})
	inst.cmd = cmd
	inst.proc = proc
	inst.exited = exited

	go h.pipeLogs(inst, pipeReader)
	go func() {
		waitErr := cmd.Wait()
		if proc != nil {
			proc.release()
		}
		close(exited)
		if waitErr != nil {
			h.log.Debug("插件进程退出", "plugin", inst.manifest.ID, "err", waitErr)
		}
	}()
	return nil
}

func (h *Host) pipeLogs(inst *instance, r io.Reader) {
	defer func() {
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
		}
	}()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		h.log.Info("[plugin:"+inst.manifest.ID+"] "+line)
	}
}

func (h *Host) waitReady(inst *instance) bool {
	deadline := time.Now().Add(time.Duration(inst.manifest.StartupTimeoutMS) * time.Millisecond)
	for {
		if h.stopRequested(inst) || inst.exited == nil {
			return false
		}
		select {
		case <-inst.exited:
			return false
		default:
		}
		if time.Now().After(deadline) {
			return false
		}
		if h.probe(inst) {
			h.touch(inst)
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// watch 巡检运行中的插件，返回异常原因（空串表示收到停止请求）。
func (h *Host) watch(inst *instance) string {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	// 每隔 accountSyncTicks 次探活（约 30s）把插件账号同步进渠道密钥，
	// 插件内部新增/删除账号后无需人工介入即可反映到渠道里。
	const accountSyncTicks = 15

	ticks := 0
	fails := 0
	for {
		select {
		case <-inst.stop:
			return ""
		case <-inst.exited:
			return "插件进程已退出"
		case <-ticker.C:
			ticks++
			if ticks%accountSyncTicks == 0 {
				go h.refreshAccountKeys(inst)
			}
			if h.probe(inst) {
				fails = 0
				h.touch(inst)
				continue
			}
			fails++
			if fails >= unhealthyThreshold {
				return fmt.Sprintf("连续 %d 次探活失败", fails)
			}
		}
	}
}

// probe 探活：请求固定的 /health，状态码 <500 视为存活。
func (h *Host) probe(inst *instance) bool {
	addr := "127.0.0.1:" + strconv.Itoa(inst.port)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+healthPath, nil)
	if err != nil {
		return false
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode < 500
}

// stopProcess 先尝试优雅停止，超时后强杀。
func (h *Host) stopProcess(inst *instance) {
	if inst.cmd == nil || inst.cmd.Process == nil || inst.exited == nil {
		return
	}
	select {
	case <-inst.exited:
		return
	default:
	}

	if err := gracefulStop(inst.cmd); err != nil {
		h.log.Debug("优雅停止不可用，将直接结束进程", "plugin", inst.manifest.ID, "err", err)
	}

	timer := time.NewTimer(stopGracePeriod)
	defer timer.Stop()
	select {
	case <-inst.exited:
		return
	case <-timer.C:
	}

	h.forceKill(inst)
}

func (h *Host) forceKill(inst *instance) {
	if inst.cmd == nil || inst.cmd.Process == nil {
		return
	}
	select {
	case <-inst.exited:
		return
	default:
	}
	_ = inst.cmd.Process.Kill()
	// 关闭作业对象：连同插件派生的子进程一起结束
	if inst.proc != nil {
		inst.proc.release()
	}
	select {
	case <-inst.exited:
	case <-time.After(2 * time.Second):
	}
}

// ---------- 内部：状态与辅助 ----------

func (h *Host) setStatus(inst *instance, status, message string) {
	h.mu.Lock()
	changed := inst.status != status || inst.lastErr != message
	inst.status = status
	inst.lastErr = message
	h.mu.Unlock()

	if !changed {
		return
	}
	h.log.Info("插件状态变化", "plugin", inst.manifest.ID, "status", status, "message", message)
	if err := h.store.SavePluginStatus(inst.manifest.ID, status, message); err != nil {
		h.log.Error("保存插件状态失败", "plugin", inst.manifest.ID, "err", err)
	}
}

func (h *Host) touch(inst *instance) {
	h.mu.Lock()
	inst.lastSeen = time.Now().UnixMilli()
	h.mu.Unlock()
}

func (h *Host) vars(inst *instance) substVars {
	return substVars{
		Port: inst.port,
		Data: h.cfg.PluginDataDir(inst.manifest.ID),
	}
}

// pluginAccount 是插件控制台 GET /accounts 返回的账号中，账号同步需要的字段。
type pluginAccount struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	ExpiresAt int64  `json:"expires_at"`
}

// alive 判断账号登录态是否仍有效（0 表示不过期）。
func (a pluginAccount) alive() bool {
	return a.ExpiresAt == 0 || a.ExpiresAt > time.Now().UnixMilli()
}

// fetchAccounts 读取插件控制台声明的账号列表；ok=false 表示读不到（未声明控制台、未运行或请求失败）。
func (h *Host) fetchAccounts(inst *instance) (accounts []pluginAccount, ok bool) {
	if !inst.manifest.HasConsole() || inst.port == 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d%s/accounts", inst.port, inst.manifest.ConsoleBasePath())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	var doc struct {
		Accounts []pluginAccount `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false
	}
	return doc.Accounts, true
}

// pluginAccountKeys 把插件账号同步成渠道密钥：Key = 账号 id（网关按它选号）、Label = 账号名。
//
// 网关转发时按 Authorization: Bearer <账号 id> 调用插件，插件按收到的账号 id 选号；
// 「轮询 / 故障转移」「启用停用」「先后顺序」全部由网关的渠道密钥策略管理。
// ok=false 表示该插件不适用账号模式（没声明控制台、没在运行，或账号接口读取失败），
// 调用方应回退成占位密钥 defaultChannelKey。
//
// existing 里的密钥（用户已设置的启用状态与顺序）优先保留，只增删账号条目。
func (h *Host) pluginAccountKeys(inst *instance, existing []model.ChannelKey) (keys []model.ChannelKey, ok bool) {
	accounts, ok := h.fetchAccounts(inst)
	if !ok {
		return nil, false
	}

	byID := make(map[string]pluginAccount, len(accounts))
	for _, a := range accounts {
		if strings.TrimSpace(a.ID) != "" {
			byID[a.ID] = a
		}
	}

	out := make([]model.ChannelKey, 0, len(byID))
	seen := make(map[string]bool, len(byID))
	for _, k := range existing {
		a, found := byID[k.Key]
		if !found {
			continue // 账号已被删除
		}
		seen[k.Key] = true
		out = append(out, model.ChannelKey{Key: k.Key, Enabled: k.Enabled, Label: strings.TrimSpace(a.Name)})
	}
	for _, a := range accounts {
		id := strings.TrimSpace(a.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, model.ChannelKey{
			Key:     id,
			Enabled: a.Enabled && a.alive(),
			Label:   strings.TrimSpace(a.Name),
		})
	}
	return out, true
}

// portAvailable 判断端口是否可绑定。
func portAvailable(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- 内部：渠道同步 ----------

// syncChannels 按插件状态注册/停用其渠道（source = plugin:<id>，渠道名 = 插件名）。
// 不加锁，避免与监管循环互锁。
func (h *Host) syncChannels(inst *instance, running bool) {
	source := "plugin:" + inst.manifest.ID
	name := inst.manifest.Name
	baseURL := UpstreamBaseURL(inst.port)

	channels, err := h.store.ListChannels()
	if err != nil {
		h.log.Error("读取渠道失败，跳过插件渠道同步", "plugin", inst.manifest.ID, "err", err)
		return
	}

	var found *model.Channel
	for i := range channels {
		if channels[i].Source == source {
			found = &channels[i]
			break
		}
	}

	if !running {
		if found != nil && found.Enabled {
			found.Enabled = false
			if err := h.store.UpdateChannel(found); err != nil {
				h.log.Error("停用插件渠道失败", "plugin", inst.manifest.ID, "err", err)
			}
		}
		return
	}

	// 渠道模型不由清单声明：新建时留空，之后由用户在「渠道管理」里手动获取并勾选维护。
	key := model.ChannelKey{Key: defaultChannelKey, Enabled: true}

	if found == nil {
		order, err := h.store.NextChannelSortOrder()
		if err != nil {
			h.log.Error("读取渠道排序失败", "plugin", inst.manifest.ID, "err", err)
			order = 0
		}
		keys := []model.ChannelKey{key}
		if ak, ok := h.pluginAccountKeys(inst, nil); ok {
			keys = ak
		}
		created := &model.Channel{
			ID:            id.UUID(),
			Name:          name,
			BaseURL:       baseURL,
			Keys:          keys,
			Models:        []string{},
			ModelMap:      map[string]string{},
			ModelPriority: map[string]int{},
			Headers:       []model.Header{},
			Enabled:       true,
			Source:        source,
			SortOrder:     order,
			CreatedAt:     time.Now().UnixMilli(),
		}
		if err := h.store.CreateChannel(created); err != nil {
			h.log.Error("创建插件渠道失败", "plugin", inst.manifest.ID, "err", err)
		} else {
			h.log.Info("已注册插件渠道", "plugin", inst.manifest.ID, "channel", name)
		}
		return
	}

	nextKeys := []model.ChannelKey{key}
	if ak, ok := h.pluginAccountKeys(inst, found.Keys); ok {
		nextKeys = ak
	}
	found.Name = name
	found.BaseURL = baseURL
	found.Keys = nextKeys
	found.Enabled = true
	if err := h.store.UpdateChannel(found); err != nil {
		h.log.Error("更新插件渠道失败", "plugin", inst.manifest.ID, "err", err)
	}
}

// SyncChannels 重新把插件控制台的账号同步成渠道密钥。
// 供控制台账号变更后立即同步；插件不在运行时返回错误。
func (h *Host) SyncChannels(pluginID string) error {
	h.mu.Lock()
	inst, ok := h.items[pluginID]
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("插件不存在: %s", pluginID)
	}
	if inst.status != StatusRunning {
		return fmt.Errorf("插件当前状态为 %s，请先启动后再同步账号", inst.status)
	}
	h.syncChannels(inst, true)
	return nil
}

// refreshAccountKeys 周期性把插件账号同步进渠道密钥（只更新 keys，不改动启用状态等其他字段）。
// 账号集合没变化时直接跳过，避免无谓写库；插件运行期间由 watch 循环定时调用。
func (h *Host) refreshAccountKeys(inst *instance) {
	if !inst.manifest.HasConsole() {
		return
	}
	channels, err := h.store.ListChannels()
	if err != nil {
		return
	}
	source := "plugin:" + inst.manifest.ID

	for i := range channels {
		ch := channels[i]
		if ch.Source != source {
			continue
		}
		keys, ok := h.pluginAccountKeys(inst, ch.Keys)
		if !ok || sameChannelKeys(ch.Keys, keys) {
			return
		}
		ch.Keys = keys
		if err := h.store.UpdateChannel(&ch); err != nil {
			h.log.Error("同步插件账号到渠道密钥失败", "plugin", inst.manifest.ID, "err", err)
		} else {
			h.log.Info("插件账号已同步到渠道密钥", "plugin", inst.manifest.ID, "channel", ch.Name, "keys", len(keys))
		}
		return
	}
}

// sameChannelKeys 判断两组渠道密钥是否完全一致（值、启用状态、展示名、顺序）。
func sameChannelKeys(a, b []model.ChannelKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// removePluginChannels 删除指定插件注册的全部渠道（source = plugin:<id>）。
// 仅在插件被卸载、或磁盘目录已消失时调用；插件只是停止运行时应走 syncChannels 停用。
func (h *Host) removePluginChannels(pluginID string) {
	source := "plugin:" + pluginID
	channels, err := h.store.ListChannels()
	if err != nil {
		h.log.Error("读取渠道失败，无法清理插件渠道", "plugin", pluginID, "err", err)
		return
	}
	for i := range channels {
		if channels[i].Source != source {
			continue
		}
		if err := h.store.DeleteChannel(channels[i].ID); err != nil {
			h.log.Error("删除插件渠道失败", "plugin", pluginID, "err", err)
			continue
		}
		h.log.Info("已删除插件渠道", "plugin", pluginID, "channel", channels[i].Name)
	}
}

// purgeOrphanChannels 清理「来源插件在本次扫描中已不存在」的渠道。
// 用户手动删掉插件目录后，若数据库记录也一并丢失，removePluginChannels 无从触发，
// 渠道页会残留一条提示「由插件注册，无法删除」的条目，这里按 source 前缀兜底清理。
func (h *Host) purgeOrphanChannels(seen map[string]bool) {
	channels, err := h.store.ListChannels()
	if err != nil {
		h.log.Error("读取渠道失败，跳过残留渠道清理", "err", err)
		return
	}
	for i := range channels {
		source := channels[i].Source
		if !strings.HasPrefix(source, "plugin:") {
			continue
		}
		pluginID := strings.TrimPrefix(source, "plugin:")
		if pluginID == "" || seen[pluginID] {
			continue
		}
		if err := h.store.DeleteChannel(channels[i].ID); err != nil {
			h.log.Error("清理插件残留渠道失败", "plugin", pluginID, "err", err)
			continue
		}
		h.log.Info("已清理插件残留渠道", "plugin", pluginID, "channel", channels[i].Name)
	}
}

// ValidateDir 插件清单校验入口（供测试与外部校验使用）。
func ValidateDir(dir string) (*Manifest, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifest(abs)
	if err != nil {
		return nil, err
	}
	if err := Validate(abs, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}