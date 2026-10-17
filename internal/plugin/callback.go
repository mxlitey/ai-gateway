package plugin

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/netinfo"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// 统一登录回调入口（见 docs/plugin-spec.md §6.6）：
//
//	浏览器 ── GET /plugin-callback/<插件id>/<method>/<state> ──▶ 网关（免鉴权，只放行 GET）
//	                                                              │ 只映射到插件 /console/callback/*
//	                                                              ▼
//	                                插件进程 http://127.0.0.1:<port>/console/callback/<method>/<state>
//
// 插件不再自行监听额外端口：把 AI_GATEWAY_CALLBACK_BASE 拼上 /<method>/<state> 就是回调地址。
const (
	// CallbackPathPrefix 网关侧回调入口前缀。
	CallbackPathPrefix = "/plugin-callback"
	// CallbackEnvVar 宿主注入插件进程的环境变量：该插件的回调基址（不含 method/state）。
	CallbackEnvVar = "AI_GATEWAY_CALLBACK_BASE"
	// CallbackHeader 宿主在代理控制台登录请求时注入的请求头：本次请求推导出的回调基址。
	// 插件应优先读它——改隧道域名 / 换访问入口后无需重启插件即可生效。
	CallbackHeader = "X-Gateway-Callback-Base"
	// callbackPluginPath 回调在插件进程内映射到的路径前缀。
	callbackPluginPath = "/console/callback"
)

// CallbackBase 返回该插件的回调基址（形如 https://gw.example.com/plugin-callback/trae）。
//
// 取值优先级：
//  1. 面板配置的「外部访问地址」（反代 / 隧道场景的权威来源）；
//  2. 按监听地址推导：回环 → 127.0.0.1；0.0.0.0 / :: → 本机局域网 IPv4。
//
// 这是注入给插件的环境变量值，进程启动后不再变化；每次登录请求还会带上
// CallbackHeader 覆盖它（见 CallbackBaseForRequest）。
func (h *Host) CallbackBase(inst *instance) string {
	if base := h.externalBase(); base != "" {
		return base + CallbackPathPrefix + "/" + inst.manifest.ID
	}
	return localBase(h.cfg) + CallbackPathPrefix + "/" + inst.manifest.ID
}

// CallbackBaseForRequest 返回本次登录请求应使用的回调基址：配置值优先，其次按请求头推导。
//
// 只采信「直连方为回环」的 X-Forwarded-*（同机反代天然是回环）；公网直连方伪造
// X-Forwarded-Host 会被忽略，避免把授权码引到攻击者域名。
func (h *Host) CallbackBaseForRequest(r *http.Request, inst *instance) string {
	if base := h.externalBase(); base != "" {
		return base + CallbackPathPrefix + "/" + inst.manifest.ID
	}
	if base := forwardedBase(r); base != "" {
		return base + CallbackPathPrefix + "/" + inst.manifest.ID
	}
	return localBase(h.cfg) + CallbackPathPrefix + "/" + inst.manifest.ID
}

// CallbackTarget 返回插件进程内回调接口的基地址（形如 http://127.0.0.1:20800/console/callback）。
// 调用方须自行保证 inst 不会被并发修改（例如持有 h.mu）。
func (h *Host) CallbackTarget(inst *instance) (string, error) {
	if !inst.manifest.HasConsole() {
		return "", fmt.Errorf("插件未声明控制台: %s", inst.manifest.ID)
	}
	if inst.status != StatusRunning || inst.port == 0 {
		return "", fmt.Errorf("插件当前状态为 %s", inst.status)
	}
	return "http://127.0.0.1:" + strconv.Itoa(inst.port) + callbackPluginPath, nil
}

// callbackTargetOf 加锁读取插件状态并返回其回调接口基地址。
func (h *Host) callbackTargetOf(pluginID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	inst, ok := h.items[pluginID]
	if !ok {
		return "", fmt.Errorf("插件不存在: %s", pluginID)
	}
	return h.CallbackTarget(inst)
}

// CallbackBaseForPlugin 返回该插件本次请求应使用的回调基址；插件不存在时返回空串。
// 供管理接口在代理登录请求时注入 CallbackHeader 使用。
func (h *Host) CallbackBaseForPlugin(pluginID string, r *http.Request) string {
	h.mu.Lock()
	inst, ok := h.items[pluginID]
	h.mu.Unlock()
	if !ok {
		return ""
	}
	return h.CallbackBaseForRequest(r, inst)
}

// externalBase 读取面板配置的「外部访问地址」，已去掉结尾斜杠；未配置时返回空串。
func (h *Host) externalBase() string {
	if h.store == nil {
		return ""
	}
	v, ok, err := h.store.GetSetting(store.SettingExternalBaseURL)
	if err != nil || !ok {
		return ""
	}
	return normalizeBase(v)
}

// localBase 按监听地址推导本机可用的访问基址。
func localBase(cfg *config.Config) string {
	host := "127.0.0.1"
	if cfg != nil {
		switch {
		case netinfo.IsLoopbackHost(cfg.Host):
			host = "127.0.0.1"
		case cfg.Host == "0.0.0.0" || cfg.Host == "::" || cfg.Host == "":
			if ip := netinfo.PrimaryIPv4(); ip != "" {
				host = ip
			}
		default:
			host = cfg.Host
		}
	}
	port := config.DefaultPort
	if cfg != nil && cfg.Port > 0 {
		port = cfg.Port
	}
	return "http://" + host + ":" + strconv.Itoa(port)
}

// forwardedBase 从反向代理转发头推导基址；无法可靠推导时返回空串。
func forwardedBase(r *http.Request) string {
	if !loopbackRemote(r) {
		return ""
	}
	proto := firstForwarded(r.Header.Get("X-Forwarded-Proto"))
	if proto != "http" && proto != "https" {
		proto = "http"
	}
	host := firstForwarded(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	host = sanitizeHost(host)
	if host == "" {
		return ""
	}
	return proto + "://" + host
}

// loopbackRemote 判断请求的直连方是否为回环地址。
func loopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// firstForwarded 取逗号分隔转发列表的第一项。
func firstForwarded(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// sanitizeHost 校验转发头里的主机名，剔除非法的端口与路径成分。
func sanitizeHost(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.ContainsAny(v, " \t/\\?#@") {
		return ""
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		// 没有端口也合法；但含冒号却不合法说明是坏值
		if strings.Contains(v, ":") {
			return ""
		}
	}
	return v
}

// normalizeBase 规整配置的外部访问地址：去掉结尾斜杠并校验协议。
func normalizeBase(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return ""
	}
	return strings.TrimRight(v, "/")
}

// ---------- 免鉴权回调路由 ----------

// ServeCallback 处理 /plugin-callback/<插件id>/<method>/<state> 请求。
//
// 该路由**免鉴权**（浏览器重定向带不了管理员凭据），因此边界必须收紧：
// 只放行 GET、只映射到插件 /console/callback/*，绝不成为通往插件其它端点的跳板。
func (h *Host) ServeCallback(w http.ResponseWriter, r *http.Request, pluginID, sub string) {
	if r.Method != http.MethodGet {
		callbackErrorPage(w, http.StatusMethodNotAllowed, "回调只接受 GET 请求")
		return
	}
	if pluginID == "" {
		callbackErrorPage(w, http.StatusNotFound, "缺少插件 id")
		return
	}
	// 回调路径只允许出现在 /console/callback 之下，杜绝用 .. 或反斜杠逃逸成任意端点
	if strings.Contains(sub, "..") || strings.Contains(sub, "\\") {
		callbackErrorPage(w, http.StatusNotFound, "非法的回调路径")
		return
	}

	base, err := h.callbackTargetOf(pluginID)
	if err != nil {
		// 插件不存在 / 没运行 / 没声明控制台：给用户可读的页面，而不是 JSON
		callbackErrorPage(w, http.StatusConflict, "插件当前不可用，请回到网关控制台重试")
		return
	}

	target := base
	if sub != "" {
		target += "/" + sub
	}
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		callbackErrorPage(w, http.StatusBadGateway, "构造回调请求失败")
		return
	}
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	// 带上原始查询串，插件据此解析授权码；透传 X-Forwarded-* 供插件生成跳转地址
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
