package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 控制台接口：按 docs/plugin-spec.md §6 的约定实现，界面由网关面板统一渲染。
//
//	GET    /console/accounts                      账号列表（含可用登录方式）
//	POST   /console/accounts                      添加账号（source=desktop 从本机桌面端导入）
//	DELETE /console/accounts/{id}                 删除账号
//	POST   /console/accounts/{id}/checkin         签到
//	POST   /console/accounts/{id}/quota           刷新额度
//	POST   /console/login/{method}                发起登录（OAuth 返回授权链接）
//	GET    /console/login/oauth?state=xxx         轮询授权结果

type accountsResponse struct {
	Accounts     []Account     `json:"accounts"`
	Notice       string        `json:"notice,omitempty"`
	LoginMethods []loginMethod `json:"login_methods,omitempty"`
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
	accounts := p.store.list()
	if accounts == nil {
		accounts = []Account{}
	}
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

func (p *Plugin) handleAccountAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	switch body.Source {
	case "", "desktop":
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
		writeOK(w, map[string]any{"message": msg})
	default:
		writeErr(w, http.StatusBadRequest, "不支持的导入方式："+body.Source)
	}
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
	writeOK(w, map[string]any{"message": msg, "account": updated})
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
	writeOK(w, map[string]any{"message": msg, "account": saved})
}

// handleLoginStart 发起登录：link 类（OAuth）返回授权链接，面板展示给用户点开。
func (p *Plugin) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")
	var body struct {
		Realm string `json:"realm"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)

	switch method {
	case "oauth":
		if p.oauth == nil {
			writeErr(w, http.StatusNotImplemented, "本插件未启用 OAuth 登录")
			return
		}
		s, err := p.oauth.start(body.Realm)
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
	default:
		writeErr(w, http.StatusBadRequest, "未知的登录方式："+method)
	}
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