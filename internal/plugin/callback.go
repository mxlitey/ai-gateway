package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 登录回调（见 docs/plugin-spec.md §6.7）。
//
// 上游对 redirect_uri 的形态要求各不相同（TRAE 要求精确 `^http://127\.0\.0\.1:\d+/authorize$`，
// LobsterAI 固定 /auth/callback），因此**路径由插件自己决定**，宿主不做任何改写。
//
// 回调地址直接指向插件自己的回环端口：
//
//	浏览器 ── GET http://127.0.0.1:<插件端口>/<插件自定的回调路径> ──▶ 插件进程
//
// 插件本来就在该端口上实现这些路径，宿主不需要再开一个转发入口，因此也不必占用
// 「监听端口 +1」这样的固定端口。固定端口曾经的价值只有两点，都不成立：
//   - Docker 直达：要求宿主机端口号与容器内端口号恰好相等，很脆弱；
//   - 上游白名单：现有上游只校验回环主机与路径，端口是任意数字。
//
// 远程 / Docker 部署时浏览器打不开回环地址属预期，用户在地址栏复制整条回调链接，
// 粘贴回面板即可（见 SubmitCallback）——该流程同样不经过任何回调监听器。
const (
	// CallbackEnvVar 宿主注入插件进程的环境变量：回调基址（形如 http://127.0.0.1:52341）。
	CallbackEnvVar = "AI_GATEWAY_CALLBACK_BASE"
	// CallbackHeader 宿主在代理控制台登录请求时注入的请求头，值与 CallbackEnvVar 相同。
	CallbackHeader = "X-Gateway-Callback-Base"
)

// callbackBaseFor 把插件回环端口拼成回调基址。
//
// 主机名固定为 127.0.0.1：多数上游对 redirect_uri 做回环硬校验，
// 换成局域网 IP / 域名会被直接拒绝。
func callbackBaseFor(port int) string {
	if port <= 0 {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// CallbackBase 返回某个插件的登录回调基址（就是插件自己的回环端口）。
// 插件未运行或尚未分配端口时返回空串。
func (h *Host) CallbackBase(pluginID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	inst, ok := h.items[pluginID]
	if !ok {
		return ""
	}
	return callbackBaseFor(inst.port)
}

// LoginTTL 授权登录会话的有效期：由网关计时（面板轮询与粘贴回调都以它为准）。
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

// matchCallbackPath 判断请求路径是否落在插件声明的回调路径下。
func matchCallbackPath(path, pattern string) bool {
	if path == pattern {
		return true
	}
	return strings.HasPrefix(path, pattern+"/")
}

// SubmitCallback 用用户粘贴的回调链接完成登录。
//
// 适用场景：网关在 Docker / 远端运行时，浏览器停在宿主机或用户自己的电脑上，
// 访问不到插件进程的回环端口（页面打不开）。此时用户在浏览器地址栏复制整条回调链接，
// 由网关按路径找到对应插件并转交。
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

	target := callbackBaseFor(port) + p
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
