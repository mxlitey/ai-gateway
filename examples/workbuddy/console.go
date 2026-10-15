package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 控制台接口：按 docs/plugin-spec.md §6 的约定实现，界面由网关面板统一渲染。
//
//	GET    /console/accounts                      账号列表（含可用登录方式）
//	POST   /console/accounts                      手工新增账号（粘贴 token）
//	DELETE /console/accounts/{id}                 删除账号
//	POST   /console/accounts/{id}/checkin         签到
//	POST   /console/accounts/{id}/quota           刷新额度
//	POST   /console/login/{method}                发起登录（method=desktop | oauth-cn | oauth-intl）
//	GET    /console/login/oauth-{cn,intl}?state=x 轮询授权结果

type accountsResponse struct {
	Accounts     []accountView `json:"accounts"`
	Notice       string        `json:"notice,omitempty"`
	LoginMethods []loginMethod `json:"login_methods,omitempty"`
}

// accountView 是账号对外的安全视图：只暴露面板需要的字段，
// 不含 access_token / refresh_token / device_token 等凭据（见 docs/plugin-spec.md §8）。
type accountView struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Site           string  `json:"site"`
	Enabled        bool    `json:"enabled"`
	Plan           string  `json:"plan"`
	Credits        float64 `json:"credits"`
	CheckedInToday bool    `json:"checked_in_today"`
	StreakDays     int     `json:"streak_days"`
	ExpiresAt      int64   `json:"expires_at"`
	LastError      string  `json:"last_error"`
}

func accountViewOf(a Account) accountView {
	return accountView{
		ID:             a.ID,
		Name:           a.Name,
		Site:           a.Site,
		Enabled:        a.Enabled,
		Plan:           a.Plan,
		Credits:        a.Credits,
		CheckedInToday: a.CheckedInToday,
		StreakDays:     a.StreakDays,
		ExpiresAt:      a.TokenExpiresAt,
		LastError:      a.LastError,
	}
}

func accountViewList(list []Account) []accountView {
	out := make([]accountView, 0, len(list))
	for _, a := range list {
		out = append(out, accountViewOf(a))
	}
	return out
}

func (p *Plugin) loginMethods() []loginMethod {
	methods := []loginMethod{{
		ID:    "desktop",
		Label: "从本机桌面端导入",
		Kind:  "action",
		Hint:  "读取本机 WorkBuddy 的登录态，需要桌面端正在运行",
	}}
	return append(methods, p.oauthMethods()...)
}

func (p *Plugin) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	accounts := accountViewList(p.store.list())
	notice := ""
	if len(accounts) == 0 {
		notice = "还没有账号：可以点「从本机桌面端导入」，或用 OAuth 授权登录"
	}
	writeOK(w, accountsResponse{
		Accounts:     accounts,
		Notice:       notice,
		LoginMethods: p.loginMethods(),
	})
}

// handleAccountAdd 手工新增账号（粘贴 token）：POST /console/accounts
//
// body（全部可选，除 token 外）：{"name","site","token","uid"}
func (p *Plugin) handleAccountAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Site  string `json:"site"`
		Token string `json:"token"`
		UID   string `json:"uid"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	token := strings.TrimSpace(body.Token)
	if token == "" {
		writeErr(w, http.StatusBadRequest, "请提供 token")
		return
	}
	uid := strings.TrimSpace(body.UID)
	if uid == "" {
		uid = jwtUID(token)
	}
	if uid == "" {
		writeErr(w, http.StatusBadRequest, "无法从 token 解析用户 id，请一并提供 uid")
		return
	}
	site := strings.TrimSpace(body.Site)
	if site != "cn" && site != "intl" {
		site = firstNonEmpty(realmFromToken(token, ""), "cn")
	}

	acc := Account{
		ID:             uid,
		UID:            uid,
		Name:           firstNonEmpty(strings.TrimSpace(body.Name), shortUID(uid)),
		Site:           site,
		Domain:         p.realmCfg(site).Domain,
		AccessToken:    token,
		TokenExpiresAt: jwtExpMillis(token),
		Source:         "manual",
		AddedAt:        time.Now().UnixMilli(),
	}
	saved, created := p.store.upsert(acc)
	go p.refreshAccount(&saved)

	verb := "已更新"
	if created {
		verb = "已新增"
	}
	writeOK(w, map[string]any{
		"message": fmt.Sprintf("%s账号 %s", verb, saved.Name),
		"account": accountViewOf(saved),
	})
}

func (p *Plugin) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !p.store.remove(id) {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	writeOK(w, map[string]any{"success": true})
}

func (p *Plugin) handleCheckin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acc, ok := p.store.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := p.ensureToken(&acc); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}

	res, msg, err := p.doCheckin(&acc)
	if err != nil {
		p.store.update(id, func(a *Account) { a.LastError = err.Error() })
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	updated, _ := p.store.update(id, func(a *Account) {
		a.CheckedInToday = res.CheckedInToday
		a.LastError = ""
		if res.StreakDays > 0 {
			a.StreakDays = res.StreakDays
		}
		if res.TodayCredit > 0 {
			a.TodayCredit = res.TodayCredit
		}
	})
	go p.refreshAccount(&updated)
	writeOK(w, map[string]any{"message": msg, "account": accountViewOf(updated)})
}

func (p *Plugin) handleQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acc, ok := p.store.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if err := p.ensureToken(&acc); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}

	updated, _, msg, err := p.fetchQuota(&acc)
	if err != nil {
		p.store.update(id, func(a *Account) { a.LastError = err.Error() })
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	saved, _ := p.store.update(id, func(a *Account) {
		a.Credits = updated.Credits
		a.CreditsAt = updated.CreditsAt
		a.Plan = updated.Plan
		a.LastError = ""
	})
	writeOK(w, map[string]any{"message": msg, "account": accountViewOf(saved)})
}

// handleLoginStart 发起登录：action 类（desktop）当场完成，link 类（oauth-*）返回授权链接。
//
// 面板只按方式 id 调用，所以「国内站 / 国际站」用两个方法 id 表达；
// 请求体里若显式带了 realm，则以请求体为准。
func (p *Plugin) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")

	if method == "desktop" {
		p.loginFromDesktop(w)
		return
	}

	realm, ok := oauthRealmByMethod[method]
	if !ok {
		writeErr(w, http.StatusBadRequest, "未知的登录方式："+method)
		return
	}
	if p.oauth == nil {
		writeErr(w, http.StatusNotImplemented, "本插件未启用 OAuth 登录")
		return
	}
	var body struct {
		Realm string `json:"realm"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if v := strings.TrimSpace(body.Realm); v != "" {
		realm = v
	}

	s, err := p.oauth.start(realm)
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

// loginFromDesktop 从本机桌面端读取登录态导入账号（action 类登录）。
func (p *Plugin) loginFromDesktop(w http.ResponseWriter) {
	accounts, notes, err := ImportFromDesktop()
	if err != nil {
		msg := err.Error()
		if len(notes) > 0 {
			msg = strings.Join(notes, "；")
		}
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	created := 0
	for _, a := range accounts {
		if _, isNew := p.store.upsert(a); isNew {
			created++
		}
		acc := a
		go p.refreshAccount(&acc)
	}
	msg := fmt.Sprintf("已导入 %d 个账号", len(accounts))
	if created != len(accounts) {
		msg = fmt.Sprintf("已更新 %d 个账号（其中 %d 个新增）", len(accounts), created)
	}
	writeOK(w, map[string]any{"status": "ok", "message": msg})
}

// refreshAccount 顺带刷新额度与签到状态；失败只记在账号上，不打扰用户操作。
func (p *Plugin) refreshAccount(acc *Account) {
	updated, _, _, err := p.fetchQuota(acc)
	if err != nil {
		p.store.update(acc.ID, func(a *Account) { a.LastError = err.Error() })
		return
	}
	checkedIn, streak, todayCredit := updated.CheckedInToday, updated.StreakDays, updated.TodayCredit
	if st, err := p.fetchCheckinStatus(acc); err == nil {
		checkedIn, streak = st.CheckedInToday, st.StreakDays
		if st.TodayCredit > 0 {
			todayCredit = st.TodayCredit
		}
	}
	p.store.update(acc.ID, func(a *Account) {
		a.Credits = updated.Credits
		a.CreditsAt = updated.CreditsAt
		a.Plan = updated.Plan
		a.CheckedInToday = checkedIn
		a.StreakDays = streak
		a.TodayCredit = todayCredit
		a.LastError = ""
	})
}

func writeOK(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}