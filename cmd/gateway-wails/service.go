package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/czg95/ai-gateway-v2/internal/app"
	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/netinfo"
	"github.com/czg95/ai-gateway-v2/internal/store"
	"github.com/czg95/ai-gateway-v2/internal/version"
)

// portSettingKey 端口持久化在数据目录的 settings 表里。
const portSettingKey = "meta.ui_port"

// lanSettingKey 访问范围（仅本机 / 局域网）持久化在 settings 表里。
const lanSettingKey = "meta.ui_lan"

const maxLogLines = 500

// GatewayService 绑定给桌面前端：端口、启停、插件、日志。
// 它直接操作进程内的 Go 内核，与走 HTTP 的 Web 管理面板相互独立。
type GatewayService struct {
	mu      sync.Mutex
	cfg     *config.Config
	gw      *app.App
	wa      *application.App
	win     *application.WebviewWindow
	port    int
	lan     bool
	running bool
	url     string
	logs    []string
}

// ---- 供前端使用的数据结构 ----

type statusInfo struct {
	Running bool   `json:"running"`
	URL     string `json:"url"`
	Port    int    `json:"port"`
	Lan     bool   `json:"lan"`
	BaseURL string `json:"baseUrl"`
	DataDir string `json:"dataDir"`
	Version string `json:"version"`
}

type pluginRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Port    int    `json:"port"`
	Dir     string `json:"dir"`
}

type actionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func newGatewayService() *GatewayService {
	return &GatewayService{}
}

// attach 完成初始化：定位数据目录、载入端口、接管日志，并自动启动网关。
func (s *GatewayService) attach(app *application.App, win *application.WebviewWindow) {
	s.wa = app
	s.win = win

	cfg, err := resolveConfig()
	if err != nil {
		s.appendLog("初始化失败：" + err.Error())
		return
	}
	s.cfg = cfg
	s.port = loadPort(cfg)
	s.lan = loadLan(cfg)
	if v := flagValue(os.Args[1:], "--port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			s.port = n
		}
	}
	// --host 显式指定监听地址时以它为准
	if v := flagValue(os.Args[1:], "--host"); v != "" {
		s.lan = isLANHost(v)
	}

	// 接管 slog：内核日志同时进界面
	slog.SetDefault(slog.New(slog.NewTextHandler(&logSink{s: s}, &slog.HandlerOptions{Level: slog.LevelInfo})))

	s.appendLog("窗口已就绪，数据目录：" + cfg.DataDir)
	s.Start(s.port, s.lan)
}

// ---- 绑定给前端的方法 ----

// Status 返回当前运行状态。
func (s *GatewayService) Status() statusInfo {
	s.mu.Lock()
	info := statusInfo{
		Running: s.running,
		URL:     s.url,
		Port:    s.port,
		Lan:     s.lan,
		Version: version.Version,
	}
	cfg := s.cfg
	s.mu.Unlock()

	if cfg != nil {
		info.DataDir = cfg.DataDir
	}
	info.BaseURL = apiBaseURL(info.Port, info.Lan)
	return info
}

// Start 启动（或端口 / 访问范围变化时重启）网关。lan 为 true 时监听所有网段。
func (s *GatewayService) Start(port int, lan bool) actionResult {
	if port <= 0 || port > 65535 {
		return actionResult{OK: false, Message: "端口不合法，请填 1-65535 之间的数字"}
	}
	host := bindHost(lan)

	s.mu.Lock()
	cfg, cur := s.cfg, s.gw
	s.mu.Unlock()
	if cfg == nil {
		return actionResult{OK: false, Message: "服务尚未就绪"}
	}

	if cur != nil && cur.Running() {
		if cur.Config().Port == port && cur.Config().Host == host {
			return actionResult{OK: true, Message: "网关已在运行：" + cur.URL()}
		}
		s.appendLog("配置已变更，正在重启网关…")
		_ = cur.Stop()
	}

	cfg.Port = port
	cfg.Host = host
	a := app.New(cfg)
	if err := a.Start(); err != nil {
		s.appendLog("启动失败：" + err.Error())
		s.setStatus(false, 0, "")
		return actionResult{OK: false, Message: err.Error()}
	}

	s.mu.Lock()
	s.gw, s.port, s.lan = a, port, lan
	s.mu.Unlock()

	s.savePort(cfg, port)
	// 访问范围只由 SetLan（界面上的显式切换）持久化，
	// 避免 --host 这类单次运行参数被永久记住。
	s.setStatus(true, port, a.URL())

	msg := "网关已启动：" + a.URL()
	if lan {
		msg += "（局域网可访问）"
	}
	s.appendLog(msg)
	return actionResult{OK: true, Message: "已启动 " + a.URL()}
}

// SetLan 切换访问范围：持久化保存；网关在运行时会重启以立即生效。
func (s *GatewayService) SetLan(lan bool) actionResult {
	s.mu.Lock()
	cfg, cur := s.cfg, s.gw
	running := s.running
	s.mu.Unlock()
	if cfg == nil {
		return actionResult{OK: false, Message: "服务尚未就绪"}
	}

	s.mu.Lock()
	s.lan = lan
	s.mu.Unlock()
	// 无论是否在运行都先落盘，保证下次启动沿用（此前运行中切换不落盘，重启后会回到默认值）
	s.saveLan(cfg, lan)

	if running && cur != nil {
		return s.Start(cur.Config().Port, lan)
	}

	msg := "访问范围已设为" + scopeLabel(lan) + "，下次启动生效"
	s.appendLog(msg)
	return actionResult{OK: true, Message: msg}
}

// scopeLabel 返回访问范围的中文名。
func scopeLabel(lan bool) string {
	if lan {
		return "局域网"
	}
	return "仅本机"
}

// Stop 停止网关。
func (s *GatewayService) Stop() actionResult {
	s.mu.Lock()
	cur := s.gw
	s.mu.Unlock()
	if cur == nil || !cur.Running() {
		return actionResult{OK: false, Message: "网关未在运行"}
	}
	if err := cur.Stop(); err != nil {
		s.appendLog("停止时出错：" + err.Error())
		return actionResult{OK: false, Message: err.Error()}
	}
	s.setStatus(false, 0, "")
	s.appendLog("网关已停止")
	return actionResult{OK: true, Message: "已停止"}
}

// Plugins 返回当前插件列表：网关在运行时用宿主内存态（含实时状态）；
// 未运行时直接扫描插件目录，保证停止状态下窗口里也能看到磁盘上已有的插件。
func (s *GatewayService) Plugins() []pluginRow {
	s.mu.Lock()
	gw, cfg := s.gw, s.cfg
	s.mu.Unlock()

	if gw != nil {
		if h := gw.Host(); h != nil {
			rows := []pluginRow{}
			for _, p := range h.List() {
				rows = append(rows, pluginRow{
					ID:      p.ID,
					Name:    p.Name,
					Version: p.Version,
					Status:  statusText(p.LastStatus),
					Port:    p.Port,
					Dir:     p.Dir,
				})
			}
			return rows
		}
	}
	return scanPluginRows(cfg)
}

// Logs 返回当前日志快照。
func (s *GatewayService) Logs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.logs))
	copy(out, s.logs)
	return out
}

// OpenPanel 用默认浏览器打开 Web 管理面板（一个独立于桌面窗口的表面）。
func (s *GatewayService) OpenPanel() {
	s.mu.Lock()
	url, running := s.url, s.running
	port := s.port
	s.mu.Unlock()
	if !running {
		url = fmt.Sprintf("http://127.0.0.1:%d", port)
		s.appendLog("网关未启动，仍尝试打开 " + url)
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// Shutdown 在应用退出阶段调用：同步停掉网关与插件子进程（幂等）。
func (s *GatewayService) Shutdown() {
	s.mu.Lock()
	gw := s.gw
	s.mu.Unlock()
	if gw != nil {
		_ = gw.Stop()
	}
}

// ---- 内部工具 ----

func (s *GatewayService) setStatus(running bool, port int, url string) {
	s.mu.Lock()
	s.running, s.url = running, url
	if port > 0 {
		s.port = port
	}
	info := statusInfo{Running: running, URL: url, Port: s.port}
	s.mu.Unlock()
	s.emit("gw:status", info)
}

func (s *GatewayService) appendLog(line string) {
	line = "[" + nowClock() + "]  " + line
	s.mu.Lock()
	s.logs = append(s.logs, line)
	if len(s.logs) > maxLogLines {
		s.logs = s.logs[len(s.logs)-maxLogLines:]
	}
	s.mu.Unlock()
	s.emit("gw:log", line)
}

// emit 向前端推送事件；未 attach 或窗口尚未就绪时静默忽略。
func (s *GatewayService) emit(name string, data any) {
	if s.wa != nil {
		s.wa.Event.Emit(name, data)
	}
}

// logSink 把 slog 输出接入界面日志区。
type logSink struct{ s *GatewayService }

func (l *logSink) Write(p []byte) (int, error) {
	if l.s != nil {
		l.s.appendLog(strings.TrimRight(string(p), "\r\n"))
	}
	return len(p), nil
}

func statusText(status string) string {
	switch status {
	case "running":
		return "运行中"
	case "starting":
		return "启动中"
	case "restarting":
		return "重启中"
	case "stopping":
		return "停止中"
	case "unhealthy":
		return "异常"
	case "failed":
		return "失败"
	case "discovered":
		return "已发现"
	case "stopped", "":
		return "已停止"
	}
	return status
}

// resolveConfig 默认把数据与插件目录放在 exe 同级，双击即可用。
func resolveConfig() (*config.Config, error) {
	cfg, err := config.Load(nil)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exeDir := filepath.Dir(exe)
	cfg.DataDir = filepath.Join(exeDir, "data")
	cfg.PluginsDir = filepath.Join(exeDir, "plugins")
	cfg.NoBrowser = true

	args := os.Args[1:]
	if v := flagValue(args, "--data-dir"); v != "" {
		cfg.DataDir = v
	}
	if v := flagValue(args, "--plugins-dir"); v != "" {
		cfg.PluginsDir = v
	}
	return cfg, nil
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return ""
}

func loadPort(cfg *config.Config) int {
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return config.DefaultPort
	}
	defer func() { _ = st.Close() }()
	if v, ok, err := st.GetSetting(portSettingKey); err == nil && ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 65535 {
			return n
		}
	}
	return config.DefaultPort
}

func (s *GatewayService) savePort(cfg *config.Config, port int) {
	s.writeSetting(cfg, portSettingKey, strconv.Itoa(port))
}

func (s *GatewayService) saveLan(cfg *config.Config, lan bool) {
	v := "0"
	if lan {
		v = "1"
	}
	s.writeSetting(cfg, lanSettingKey, v)
}

// writeSetting 持久化界面设置：优先复用运行中网关的数据库连接，
// 避免另开一个连接与网关并发写库而失败（写入失败曾导致端口/访问范围丢失）。
func (s *GatewayService) writeSetting(cfg *config.Config, key, value string) {
	if st := s.runningStore(); st != nil {
		if err := st.SetSetting(key, value); err != nil {
			s.appendLog("保存设置失败：" + err.Error())
		}
		return
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		s.appendLog("保存设置失败：" + err.Error())
		return
	}
	defer func() { _ = st.Close() }()
	if err := st.SetSetting(key, value); err != nil {
		s.appendLog("保存设置失败：" + err.Error())
	}
}

// runningStore 返回运行中网关的数据库句柄；未运行时为 nil。
func (s *GatewayService) runningStore() *store.Store {
	s.mu.Lock()
	gw := s.gw
	s.mu.Unlock()
	if gw == nil {
		return nil
	}
	return gw.Store()
}

// apiBaseURL 返回给客户端使用的 API 基础地址（含 /v1）。
// 仅本机模式用回环地址；局域网模式用本机的局域网 IPv4 地址。
func apiBaseURL(port int, lan bool) string {
	host := "127.0.0.1"
	if lan {
		if ip := netinfo.PrimaryIPv4(); ip != "" {
			host = ip
		}
	}
	return fmt.Sprintf("http://%s:%d/v1", host, port)
}

// bindHost 把「访问范围」开关映射为监听地址。
func bindHost(lan bool) string {
	if lan {
		return "0.0.0.0" // 监听所有网段
	}
	return "127.0.0.1" // 仅回环，只有本机可访问
}

// isLANHost 判断给定监听地址是否为局域网开放（非回环）。
func isLANHost(host string) bool {
	return host != "" && host != "127.0.0.1" && host != "localhost"
}

func loadLan(cfg *config.Config) bool {
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return false
	}
	defer func() { _ = st.Close() }()
	if v, ok, err := st.GetSetting(lanSettingKey); err == nil && ok {
		return v == "1"
	}
	return false
}