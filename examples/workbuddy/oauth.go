package main

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// OAuth 免客户端登录：全程不需要桌面端在场。
//
//	1. POST {chat_origin}/v2/plugin/auth/state?platform=CLI   → {state, authUrl}
//	2. 用户在本机浏览器打开 authUrl，用自己的方式登录授权
//	3. GET  {chat_origin}/v2/plugin/auth/token?state=xxx      → code=11217 表示还在等；code=0 拿到 token
//	4. GET  {chat_origin}/v2/plugin/login/account?state=xxx   → 昵称（可选）
const oauthSessionTTL = 10 * time.Minute

// loginMethod 由宿主面板渲染：kind=link 时面板会把返回的链接展示给用户点开。
type loginMethod struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // action：面板直接调用；link：面板展示链接并轮询
	Hint  string `json:"hint,omitempty"`
}

type oauthSession struct {
	State   string `json:"state"`
	AuthURL string `json:"url"`
	Realm   string `json:"realm"`

	Status  string `json:"status"` // pending | ok | error | expired
	Message string `json:"message,omitempty"`

	created time.Time
}

type oauthManager struct {
	p        *Plugin
	mu       sync.Mutex
	sessions map[string]*oauthSession
}

func newOAuthManager(p *Plugin, _ string) *oauthManager {
	return &oauthManager{p: p, sessions: map[string]*oauthSession{}}
}

// oauthRealmByMethod 把面板上的登录方式 id 映射到站点区域（空串表示按已有账号自动判断）。
// 面板只会按 id 调用，所以「国内站 / 国际站」用两个方法 id 来表达。
var oauthRealmByMethod = map[string]string{
	"oauth-cn":   "cn",
	"oauth-intl": "intl",
}

// oauthMethods 由 Plugin 暴露给控制台：声明本插件支持 OAuth 登录。
func (p *Plugin) oauthMethods() []loginMethod {
	if p.oauth == nil {
		return nil
	}
	return []loginMethod{
		{
			ID:    "oauth-cn",
			Label: "OAuth 授权登录（国内站）",
			Kind:  "link",
			Hint:  "在本机浏览器打开授权链接登录国内站（codebuddy.cn），完成后账号自动入库",
		},
		{
			ID:    "oauth-intl",
			Label: "OAuth 授权登录（国际站）",
			Kind:  "link",
			Hint:  "在本机浏览器打开授权链接登录国际站（workbuddy.ai），完成后账号自动入库",
		},
	}
}

func (o *oauthManager) registerRoutes(mux *http.ServeMux) {
	for method := range oauthRealmByMethod {
		mux.HandleFunc("GET /console/login/"+method, o.handleStatus)
	}
}

// defaultRealm 优先跟随已有账号的区域，否则按国内站处理。
func (o *oauthManager) defaultRealm() string {
	for _, a := range o.p.store.list() {
		if a.Site == "intl" || a.Site == "cn" {
			return a.Site
		}
	}
	return "cn"
}

func (o *oauthManager) baseHeaders(cfg realmConfig) map[string]string {
	return map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "application/json, text/plain, */*",
		"X-Requested-With": "XMLHttpRequest",
		"User-Agent":       cfg.BillingUA,
		"Origin":           cfg.BillingOrigin,
		"Referer":          cfg.BillingOrigin + "/",
	}
}

// start 发起一次授权，返回 state 与 authUrl。
func (o *oauthManager) start(realm string) (*oauthSession, error) {
	if realm != "cn" && realm != "intl" {
		realm = o.defaultRealm()
	}
	p := o.p
	cfg := p.realmCfg(realm)

	const platform = "CLI"
	target := cfg.ChatOrigin + p.eps.AuthState + "?platform=" + url.QueryEscape(platform)
	env, _, err := p.doJSON(http.MethodPost, target, nil, o.baseHeaders(cfg))
	if err != nil {
		return nil, err
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("发起授权失败：%s", firstNonEmpty(env.Msg, fmt.Sprintf("code=%d", env.Code)))
	}

	data := decodeData(env.Data)
	state := asString(data["state"])
	authURL := asString(data["authUrl"])
	if state == "" || authURL == "" {
		return nil, fmt.Errorf("授权响应里缺少 state/authUrl")
	}

	s := &oauthSession{
		State:   state,
		AuthURL: authURL,
		Realm:   realm,
		Status:  "pending",
		Message: "请在浏览器中完成授权",
		created: time.Now(),
	}
	o.mu.Lock()
	o.sessions[state] = s
	o.mu.Unlock()
	return s, nil
}

// handleStatus 轮询一次授权结果；成功后把账号写进账号池。
func (o *oauthManager) handleStatus(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		writeErr(w, http.StatusBadRequest, "缺少 state")
		return
	}
	o.mu.Lock()
	s := o.sessions[state]
	o.mu.Unlock()
	if s == nil {
		writeErr(w, http.StatusNotFound, "授权会话不存在，请重新发起")
		return
	}
	if time.Since(s.created) > oauthSessionTTL {
		s.Status = "expired"
		s.Message = "授权窗口已过期，请重新发起"
	}
	if s.Status == "pending" {
		if err := o.poll(s); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	writeOK(w, map[string]any{"status": s.Status, "message": s.Message, "url": s.AuthURL})
}

func (o *oauthManager) poll(s *oauthSession) error {
	p := o.p
	cfg := p.realmCfg(s.Realm)
	target := cfg.ChatOrigin + p.eps.AuthToken + "?state=" + url.QueryEscape(s.State)

	env, _, err := p.doJSON(http.MethodGet, target, nil, o.baseHeaders(cfg))
	if err != nil {
		// 轮询失败不算错误，下一次再试
		s.Message = "等待授权中…"
		return nil
	}
	switch {
	case env.Code == oauthPendingCode:
		s.Message = firstNonEmpty(env.Msg, "等待浏览器中完成授权")
		return nil
	case env.Code != 0:
		s.Status = "error"
		s.Message = firstNonEmpty(env.Msg, fmt.Sprintf("授权失败 code=%d", env.Code))
		return nil
	}

	data := decodeData(env.Data)
	token := asString(data["accessToken"])
	if token == "" {
		s.Message = "等待上游下发 token…"
		return nil
	}
	uid := jwtUID(token)
	if uid == "" {
		s.Status = "error"
		s.Message = "无法从 token 解析用户 id，请重试"
		return nil
	}

	site := realmFromToken(token, asString(data["domain"]))
	if site == "" {
		site = s.Realm
	}
	expires := normalizeEpochMillis(data["expiresAt"])
	if expires == 0 {
		expires = jwtExpMillis(token)
	}

	acc := Account{
		ID:             uid,
		UID:            uid,
		Name:           firstNonEmpty(o.fetchNickname(cfg, s.State, token), shortUID(uid)),
		Site:           site,
		Domain:         firstNonEmpty(asString(data["domain"]), cfg.Domain),
		AccessToken:    token,
		RefreshToken:   asString(data["refreshToken"]),
		TokenExpiresAt: expires,
		Source:         "oauth",
		AddedAt:        time.Now().UnixMilli(),
	}
	saved, created := p.store.upsert(acc)

	verb := "已更新"
	if created {
		verb = "已新增"
	}
	s.Status = "ok"
	s.Message = fmt.Sprintf("授权成功，账号 %s %s", verb, saved.Name)

	// 入库后顺手刷一次额度（OAuth 账号可以自己续期，所以这里也顺带验证连通性）
	toRefresh := saved
	go p.refreshAccount(&toRefresh)
	return nil
}

func (o *oauthManager) fetchNickname(cfg realmConfig, state, token string) string {
	p := o.p
	h := o.baseHeaders(cfg)
	h["Authorization"] = "Bearer " + token
	target := cfg.ChatOrigin + p.eps.LoginAccount + "?state=" + url.QueryEscape(state)
	env, _, err := p.doJSON(http.MethodGet, target, nil, h)
	if err != nil {
		return ""
	}
	return asString(decodeData(env.Data)["nickname"])
}