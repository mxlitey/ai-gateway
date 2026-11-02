package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/config"
)

// 登录回调透传（见 docs/plugin-spec.md §6.7）。
//
// 上游对 redirect_uri 的形态要求各不相同（有的要求精确到 /authorize，有的固定 /auth/callback），
// 因此**路径由插件自己决定**，宿主不做任何改写。宿主只负责在「监听端口 +1」上开一个入口，
// 按插件在 plugin.json 里声明的 callback_paths 把请求原样转给插件主端口：
//
//	浏览器 ── GET http://127.0.0.1:8788/<插件自定的回调路径> ──▶ 宿主回调入口（免鉴权，只放行 GET）
//	                                                                 │ 路径 + 查询串原样透传
//	                                                                 ▼
//	                                        插件进程 http://127.0.0.1:<插件端口>/<同一路径>
//
// 端口长期占用（不按登录会话临时申请/销毁）：回调地址在插件进程里保持稳定，
// 上游白名单、授权页正则都能长期匹配。
const (
	// CallbackEnvVar 宿主注入插件进程的环境变量：回调入口基址（形如 http://127.0.0.1:8788）。
	CallbackEnvVar = "AI_GATEWAY_CALLBACK_BASE"
	// CallbackHeader 宿主在代理控制台登录请求时注入的请求头，值与 CallbackEnvVar 相同。
	// 插件优先读它，便于网关换端口后无需重启插件即可生效。
	CallbackHeader = "X-Gateway-Callback-Base"
)

// CallbackPort 返回回调入口端口：网关监听端口 +1。
func CallbackPort(gatewayPort int) int {
	if gatewayPort <= 0 || gatewayPort >= 65535 {
		return 0
	}
	return gatewayPort + 1
}

// LoginTTL 授权登录会话的有效期：由网关计时（面板轮询与回调入口都以它为准）。
//
// 从插件在 POST /console/login/{method} 返回 url + state 起算。到点后面板停止转发轮询，
// 并像用户点了「取消」一样收起登录框；迟到的回调（含粘贴回调）也会被拒绝。插件因此不必再自己
// 实现超时，但仍要校验 state——网关重启后计时登记会丢失，此时回调会照常转发（见 docs/plugin-spec.md §6.7）。
const LoginTTL = 90 * time.Second

// loginTimer 一次授权登录的计时状态（每个插件同时只跟踪一个会话，与面板行为一致）。
type loginTimer struct {
	deadline time.Time
}

// TrackLogin 开始（或重新开始）一次授权登录的计时。
func (h *Host) TrackLogin(pluginID string) {
	if pluginID == "" {
		return
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.logins == nil {
		h.logins = map[string]loginTimer{}
	}
	// 顺手清掉早已结束的登记，避免长时间运行后无限增长
	for id, t := range h.logins {
		if now.Sub(t.deadline) > 10*time.Minute {
			delete(h.logins, id)
		}
	}
	h.logins[pluginID] = loginTimer{deadline: now.Add(LoginTTL)}
}

// LoginExpired 报告该插件当前的授权登录是否已被网关判定超时。
// 没有登记（从未发起过、已结束、或网关重启过）时返回 false。
func (h *Host) LoginExpired(pluginID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.logins[pluginID]
	return ok && time.Now().After(t.deadline)
}

// FinishLogin 结束一次授权登录（成功 / 失败 / 过期），之后不再计时。
func (h *Host) FinishLogin(pluginID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.logins, pluginID)
}

// CallbackBase 返回注入给插件的回调基址（形如 http://127.0.0.1:8788）。
//
// 主机名固定为 127.0.0.1：多数上游（如 TRAE）对 redirect_uri 做 `^http://127\.0\.0\.1:\d+/...$`
// 硬校验，换成局域网 IP / 域名会被直接拒绝。局域网或 Docker 下浏览器打不开该地址属预期，
// 用户把地址栏里的完整链接粘回面板即可（见 SubmitCallback）。
func (h *Host) CallbackBase() string {
	if h.cfg == nil {
		return ""
	}
	port := CallbackPort(h.cfg.Port)
	if port == 0 {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// StartCallbackServer 在「监听端口 +1」上启动回调透传入口。
//
// 端口被占用时返回错误；调用方只需记录告警——回调不可用不影响网关主功能，
// 用户仍可在面板粘贴回调链接完成登录（见 SubmitCallback）。
func (h *Host) StartCallbackServer() error {
	if h.cfg == nil {
		return fmt.Errorf("运行配置不可用")
	}
	port := CallbackPort(h.cfg.Port)
	if port == 0 {
		return fmt.Errorf("回调端口非法（监听端口需在 1-65534）: %d", h.cfg.Port)
	}
	addr := net.JoinHostPort(callbackBindHost(h.cfg), strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听回调入口 %s 失败: %w", addr, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(h.ServeCallback), ReadHeaderTimeout: 10 * time.Second}

	h.mu.Lock()
	if h.shutdown {
		h.mu.Unlock()
		_ = ln.Close()
		return fmt.Errorf("宿主已关闭")
	}
	h.cbSrv, h.cbLn = srv, ln
	h.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			h.logger().Warn("回调入口异常退出", "addr", addr, "err", err)
		}
	}()
	h.logger().Info("插件登录回调入口已就绪", "url", h.CallbackBase())
	return nil
}

// callbackBindHost 返回回调入口的绑定地址：网关监听 0.0.0.0 / :: 时同样对外监听。
func callbackBindHost(cfg *config.Config) string {
	if cfg == nil {
		return "127.0.0.1"
	}
	switch cfg.Host {
	case "", "0.0.0.0", "::":
		return "0.0.0.0"
	default:
		return cfg.Host
	}
}

// StopCallbackServer 关闭回调入口（幂等）。
func (h *Host) StopCallbackServer() {
	h.mu.Lock()
	srv := h.cbSrv
	h.cbSrv, h.cbLn = nil, nil
	h.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// ServeCallback 处理回调入口上的请求：按路径找到认领它的插件，再原样转发。
//
// 该入口**免鉴权**（浏览器重定向带不了管理员凭据），因此边界必须收紧：
// 只放行 GET、只转发到插件自身端口上的同一路径，绝不成为通往插件其它端点的跳板。
func (h *Host) ServeCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		callbackErrorPage(w, http.StatusMethodNotAllowed, "回调只接受 GET 请求")
		return
	}
	inst, ok := h.callbackInstance(r.URL.Path)
	if !ok {
		callbackErrorPage(w, http.StatusNotFound,
			"没有插件认领这个回调路径，请回到网关控制台重新发起登录")
		return
	}
	// 网关计时已过：迟到的授权回调不再转发（见 LoginTTL）
	if h.LoginExpired(inst.manifest.ID) {
		callbackErrorPage(w, http.StatusGone,
			"授权链接已超过 90 秒有效期，请回到网关面板重新发起登录")
		return
	}

	target := "http://127.0.0.1:" + strconv.Itoa(inst.port) + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		callbackErrorPage(w, http.StatusBadGateway, "构造回调请求失败")
		return
	}
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	for _, name := range []string{"X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Prefix"} {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		callbackErrorPage(w, http.StatusBadGateway, "插件未响应，请确认插件正在运行")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// callbackInstance 按路径找到认领该回调的插件（最长前缀优先）。
func (h *Host) callbackInstance(path string) (*instance, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var best *instance
	bestLen := -1
	bestPort := 0
	for _, inst := range h.items {
		if inst.status != StatusRunning || inst.port == 0 {
			continue
		}
		for _, ap := range inst.manifest.CallbackPaths {
			ap = strings.TrimRight(ap, "/")
			if ap == "" || !matchCallbackPath(path, ap) {
				continue
			}
			// 前缀更长者优先；等长时取插件端口小者，保证结果稳定
			if len(ap) > bestLen || (len(ap) == bestLen && inst.port < bestPort) {
				best, bestLen, bestPort = inst, len(ap), inst.port
			}
		}
	}
	return best, best != nil
}

// matchCallbackPath 判断请求路径是否落在插件声明的回调路径下。
func matchCallbackPath(path, pattern string) bool {
	if path == pattern {
		return true
	}
	return strings.HasPrefix(path, pattern+"/")
}

// callbackConflictLocked 检查待启动插件的回调路径是否与其它运行中插件冲突。
// 调用方须持有 h.mu。
func (h *Host) callbackConflictLocked(inst *instance) string {
	if len(inst.manifest.CallbackPaths) == 0 {
		return ""
	}
	for id, other := range h.items {
		if id == inst.manifest.ID || other.status != StatusRunning {
			continue
		}
		for _, mine := range inst.manifest.CallbackPaths {
			mine = strings.TrimRight(strings.TrimSpace(mine), "/")
			if mine == "" {
				continue
			}
			for _, theirs := range other.manifest.CallbackPaths {
				theirs = strings.TrimRight(strings.TrimSpace(theirs), "/")
				if theirs == "" {
					continue
				}
				if matchCallbackPath(mine, theirs) || matchCallbackPath(theirs, mine) {
					return fmt.Sprintf("回调路径 %s 已被插件 %s 占用（callback_paths 不能重叠）", mine, id)
				}
			}
		}
	}
	return ""
}

// SubmitCallback 用用户粘贴的回调链接完成登录。
//
// 适用场景：Docker 部署时回调端口只存在于容器内，宿主机浏览器访问不到（页面打不开）。
// 此时用户在浏览器地址栏复制整条回调链接，由网关按路径找到对应插件并转交。
//
// 安全边界：只接受回环地址、只允许插件在 plugin.json 的 callback_paths 里声明的路径。
func (h *Host) SubmitCallback(pluginID, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("链接格式不正确，请粘贴浏览器地址栏中的完整回调地址")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("回调链接只支持 http/https")
	}
	host := strings.ToLower(u.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("只接受本机回环地址的回调链接")
	}

	h.mu.Lock()
	inst, ok := h.items[pluginID]
	var port int
	var status string
	var allowedPaths []string
	if ok {
		port, status = inst.port, inst.status
		allowedPaths = append([]string(nil), inst.manifest.CallbackPaths...)
	}
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("插件不存在: %s", pluginID)
	}
	if len(allowedPaths) == 0 {
		return fmt.Errorf("该插件没有声明回调路径（callback_paths）")
	}
	if status != StatusRunning || port == 0 {
		return fmt.Errorf("插件当前状态为 %s，请先启动插件", status)
	}
	if h.LoginExpired(pluginID) {
		return fmt.Errorf("授权链接已超过 90 秒有效期，请回到网关面板重新发起登录")
	}

	p := u.EscapedPath()
	p = pathClean(p)
	allowed := false
	for _, ap := range allowedPaths {
		if matchCallbackPath(p, strings.TrimRight(strings.TrimSpace(ap), "/")) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("回调链接路径不在插件允许范围内")
	}

	target := "http://127.0.0.1:" + strconv.Itoa(port) + p
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("构造回调请求失败: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("插件未响应，请确认插件正在运行")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("插件未接受该回调（HTTP %d），请确认链接是否完整", resp.StatusCode)
	}
	return nil
}

// pathClean 规整回调路径，拒绝 .. 与反斜杠逃逸。
func pathClean(p string) string {
	if p == "" {
		return "/"
	}
	if strings.Contains(p, `\`) {
		return "/"
	}
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" || s == "." {
			continue
		}
		if s == ".." {
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		out = append(out, s)
	}
	return "/" + strings.Join(out, "/")
}

// callbackErrorPage 返回统一风格的纯文本错误页（不回显任何内部信息）。
func callbackErrorPage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, callbackPageHTML(message))
}

func callbackPageHTML(message string) string {
	return `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>登录回调</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#0f1115;color:#e6e6e6;
display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#181b21;padding:40px 56px;border-radius:14px;text-align:center;box-shadow:0 8px 30px rgba(0,0,0,.4)}
h1{color:#d93025;font-size:22px;margin:0 0 12px}p{color:#9aa4b2;margin:0;font-size:14px}</style>
</head><body><div class="card"><h1>登录回调失败</h1><p>` + message + `</p></div></body></html>`
}
