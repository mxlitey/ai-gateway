package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 网页登录（link 类）：回调走**网关统一入口**，插件不再监听额外端口（规范 §6.7）。
//
//	① POST /console/login/web → 从 X-Gateway-Callback-Base（或 AI_GATEWAY_CALLBACK_BASE）取基址，
//	   拼出 <基址>/web/<state> 作为回调地址，返回 {status,url,state}
//	② 面板打开 url，用户在浏览器授权，浏览器跳回网关的 /plugin-callback/<id>/web/<state>
//	③ 网关原样转发到本插件的 /console/callback/web/<state>?<授权参数>
//	④ 回调里取 refreshToken → ExchangeToken → GetUserInfo → 原子落盘 → 会话标记 ok
//	⑤ 面板轮询 GET /console/login/web?state=xxx 拿到 ok → 自动刷新账号列表
//
// 约束（规范 §6.7）：回调是免鉴权入口，state 必须校验（一次性 + TTL + 不可猜）；
// 会话 10 分钟 TTL，超时返回 expired；面板同时只跟踪一个会话，新会话替换旧会话。

const loginTTL = 10 * time.Minute

type loginSession struct {
	State   string
	AuthURL string
	Status  string
	Message string
	created time.Time

	machineID string
	deviceID  string
}

func (s *loginSession) setStatus(status, message string) {
	s.Status = status
	s.Message = message
}

type loginBroker struct {
	p       *Plugin
	mu      sync.Mutex
	cur     *loginSession
	byState map[string]*loginSession
}

func newLoginBroker(p *Plugin) *loginBroker {
	return &loginBroker{p: p, byState: map[string]*loginSession{}}
}

// callbackBase 解析本次登录应使用的回调基址，优先级见规范 §6.7：
//  1. 请求头 X-Gateway-Callback-Base（网关每次登录现算，改域名无需重启插件）
//  2. 环境变量 AI_GATEWAY_CALLBACK_BASE（进程启动时算好的兜底）
//  3. 空串 —— 表示插件被单独调试、不经网关启动，调用方需自行兜底
func callbackBase(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Gateway-Callback-Base")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("AI_GATEWAY_CALLBACK_BASE")), "/")
}

// start 发起一次登录：拼出回调地址并返回会话（含 url 与 state）。
func (b *loginBroker) start(callbackBase string) (*loginSession, error) {
	machineID := randomHex(16)
	// deviceID 必须是「16 位纯数字」，且与落盘凭证共用同一个值；
	// 否则该账号随后的每日签到会被上游拒（见 newCheckinDeviceID 的注释）。
	deviceID, err := newCheckinDeviceID()
	if err != nil {
		return nil, fmt.Errorf("生成设备号失败：%v", err)
	}
	state := randomState()
	// state 放进回调**路径**（而非查询串）：部分上游要求 redirect_uri 精确匹配，
	// 路径形态更容易登记进白名单（规范 §6.7）。
	callbackURL := callbackBase + "/web/" + state
	authURL := BuildLoginURL(b.p.eps, machineID, deviceID, callbackURL)

	s := &loginSession{
		State:     state,
		AuthURL:   authURL,
		Status:    "pending",
		Message:   "请在浏览器中完成授权",
		created:   time.Now(),
		machineID: machineID,
		deviceID:  deviceID,
	}

	// 面板同时只跟踪一个登录会话：新的替换旧的（旧 state 立即终止）。
	b.mu.Lock()
	if b.cur != nil {
		b.cur.setStatus("error", "登录会话已被新的登录取消")
	}
	b.cur = s
	b.byState[state] = s
	b.mu.Unlock()

	return s, nil
}

// status 查询会话状态（GET /console/login/web?state=xxx）。
func (b *loginBroker) status(state string) (string, string) {
	b.mu.Lock()
	s := b.byState[state]
	b.mu.Unlock()
	if s == nil {
		return "error", "登录会话不存在，请重新发起"
	}
	if s.Status == "pending" && time.Since(s.created) > loginTTL {
		s.setStatus("expired", "授权窗口已过期，请重新发起登录")
	}
	return s.Status, s.Message
}

// lookup 按 state 找回会话（回调入口用）。未知 state 返回 nil，调用方必须拒绝。
func (b *loginBroker) lookup(state string) *loginSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.byState[state]
}

// closeAll 清理全部会话（进程退出时调用）。
func (b *loginBroker) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.byState = map[string]*loginSession{}
	b.cur = nil
}

// handleCallback 处理网关转发来的浏览器回调（GET /console/callback/web/<state>?<授权参数>）。
//
// 免鉴权入口：先按 state 找回会话，找不到或已过期一律丢弃、不产生任何副作用（规范 §6.7）。
func (b *loginBroker) handleCallback(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.PathValue("state"))
	s := b.lookup(state)
	if s == nil {
		writeLoginPage(w, false, "登录会话不存在或已过期，请回到网关控制台重试")
		return
	}
	if time.Since(s.created) > loginTTL {
		s.setStatus("expired", "授权窗口已过期，请重新发起登录")
		writeLoginPage(w, false, "授权窗口已过期，请回到网关控制台重新发起")
		return
	}

	if !strings.Contains(r.URL.RawQuery, "refreshToken") && !strings.Contains(r.URL.RawQuery, "userJwt") {
		// 可能是浏览器预取 /favicon.ico 之类，直接忽略。
		writeLoginPage(w, false, "等待授权回调…")
		return
	}

	info, err := ParseCallback(r.URL.String())
	if err != nil {
		s.setStatus("error", "回调解析失败："+truncateRunes(err.Error(), 60))
		writeLoginPage(w, false, "回调解析失败，请回到网关控制台重试")
		return
	}

	acc := Account{
		AccessToken: info.AccessToken, RefreshToken: info.RefreshToken,
		UID: info.UID, Nickname: info.Nickname, EnterpriseID: info.EnterpriseID,
		ExpiresAt: info.ExpiresAt, Domain: "trae.cn", ApiHost: b.p.eps.OAuthHost,
		MachineID: s.machineID, DeviceID: s.deviceID, Enabled: true,
	}
	if strings.TrimSpace(acc.RefreshToken) != "" {
		if err := b.p.refreshToken(&acc); err != nil {
			s.setStatus("error", "换取 token 失败："+asUpstreamError(err).Msg)
			writeLoginPage(w, false, "登录失败，请回到网关控制台重试")
			return
		}
	}
	if uid, nick, ent, err := b.p.getUserInfo(&acc); err == nil {
		if strings.TrimSpace(uid) != "" {
			acc.UID = uid
		}
		if strings.TrimSpace(nick) != "" {
			acc.Nickname = nick
		}
		if strings.TrimSpace(ent) != "" {
			acc.EnterpriseID = ent
		}
	}
	if strings.TrimSpace(acc.UID) == "" || strings.TrimSpace(acc.AccessToken) == "" {
		s.setStatus("error", "未能从回调确定账号信息，请重试或改用手工导入")
		writeLoginPage(w, false, "登录失败，请回到网关控制台重试")
		return
	}
	if err := ensureCheckinDeviceID(&acc); err != nil {
		s.setStatus("error", "生成 deviceId 失败："+err.Error())
		writeLoginPage(w, false, "登录失败，请回到网关控制台重试")
		return
	}

	saved, created := b.p.store.upsert(acc.toAuth())
	verb := "已更新"
	if created {
		verb = "已新增"
	}
	s.setStatus("ok", fmt.Sprintf("登录成功，账号 %s %s", verb, saved.name()))
	writeLoginPage(w, true, "登录成功，可关闭此窗口")
}

// writeLoginPage 返回友好的 HTML 提示（不返回 JSON）。
func writeLoginPage(w http.ResponseWriter, ok bool, message string) {
	title := "登录失败"
	color := "#d93025"
	if ok {
		title = "登录成功"
		color = "#1a7f37"
	}
	body := strings.NewReplacer("{{TITLE}}", title, "{{COLOR}}", color, "{{MSG}}", message).
		Replace(loginPageHTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

const loginPageHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{TITLE}}</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#0f1115;color:#e6e6e6;
display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#181b21;padding:40px 56px;border-radius:14px;text-align:center;box-shadow:0 8px 30px rgba(0,0,0,.4)}
h1{color:{{COLOR}};font-size:22px;margin:0 0 12px}p{color:#9aa4b2;margin:0;font-size:14px}</style>
</head><body><div class="card"><h1>{{TITLE}}</h1><p>{{MSG}}</p></div></body></html>`

// ---------- 控制台登录端点 ----------

// handleLoginStart 发起登录：POST /console/login/web → {"status","message","url","state"}。
// 响应体里**同时有 url 与 state**，面板据此判定为 link 类并轮询。
func (p *Plugin) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")
	if method != "web" {
		writeErr(w, http.StatusBadRequest, "未知的登录方式："+method)
		return
	}
	base := callbackBase(r)
	if base == "" {
		// 插件被单独调试（未经网关启动）：没有统一回调入口可用，如实告知而不是静默降级。
		writeErr(w, http.StatusBadGateway, "缺少回调基址：请经网关启动插件，或手工导入凭证")
		return
	}
	s, err := p.login.start(base)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, map[string]any{
		"status":  s.Status,
		"message": s.Message,
		"url":     s.AuthURL,
		"state":   s.State,
	})
}

// handleLoginStatus 轮询登录结果：GET /console/login/web?state=xxx → {"status","message"}。
func (p *Plugin) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")
	if method != "web" {
		writeErr(w, http.StatusBadRequest, "未知的登录方式："+method)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		writeErr(w, http.StatusBadRequest, "缺少 state")
		return
	}
	status, message := p.login.status(state)
	writeOK(w, map[string]any{"status": status, "message": message})
}
