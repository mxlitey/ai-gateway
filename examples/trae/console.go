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
//	GET    /console/accounts                      账号列表（含 notice 与 login_methods）
//	POST   /console/accounts                      手工导入（面板不调用，供脚本使用）
//	DELETE /console/accounts/{id}                 删除账号
//	POST   /console/accounts/{id}/checkin         签到
//	POST   /console/accounts/{id}/quota           刷新额度
//	POST   /console/login/web                     发起网页登录
//	GET    /console/login/web?state=xxx           轮询登录结果

// accountView 是账号对外的安全视图：只暴露面板需要的字段，
// 绝不含 access_token / refresh_token / machineId 等凭据（规范 §6.4 / §8）。
type accountView struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Site           string  `json:"site"`
	Enabled        bool    `json:"enabled"`
	Plan           string  `json:"plan"`
	Credits        float64 `json:"credits"`
	CheckedInToday bool    `json:"checked_in_today"`
	StreakDays     int     `json:"streak_days,omitempty"` // 上游未提供连签天数时省略（面板显示 —）
	ExpiresAt      int64   `json:"expires_at"`            // 毫秒
	LastError      string  `json:"last_error"`
}

type accountsResponse struct {
	Accounts     []accountView `json:"accounts"`
	Notice       string        `json:"notice,omitempty"`
	LoginMethods []loginMethod `json:"login_methods,omitempty"`
}

// loginMethod 是登录按钮声明（规范 §6.6：不再输出 kind 字段，面板只看返回体形状）。
type loginMethod struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint,omitempty"`
}

// accountViewOf 做字段映射：
//   - id / name 取 uid / 昵称（空则用 uid）
//   - site 固定 "cn"（本插件只支持国内站）
//   - plan 固定 "免费计划"（SOLO 免费通道）
//   - credits 取 ide_credits 快照（允许小数）
//   - expires_at 上游是秒，规范要毫秒 → ×1000
//   - last_error 必须脱敏
func accountViewOf(a Account) accountView {
	expires := int64(0)
	if a.ExpiresAt > 0 {
		expires = a.ExpiresAt * 1000
	}
	return accountView{
		ID:             a.UID,
		Name:           a.name(),
		Site:           "cn",
		Enabled:        a.usable(),
		Plan:           "免费计划",
		Credits:        a.Credits,
		CheckedInToday: a.CheckedInToday,
		StreakDays:     a.StreakDays,
		ExpiresAt:      expires,
		LastError:      sanitizeError(a.LastError),
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
	return []loginMethod{{
		ID:    "web",
		Label: "登录 TRAE 账号",
		Hint:  "在浏览器完成授权，回调自动捕获",
	}}
}

func (p *Plugin) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	accounts := accountViewList(p.store.list())
	notice := ""
	if len(accounts) == 0 {
		notice = "还没有账号：点「登录 TRAE 账号」，或调用 POST /console/accounts 手工导入"
	}
	writeOK(w, accountsResponse{
		Accounts:     accounts,
		Notice:       notice,
		LoginMethods: p.loginMethods(),
	})
}

// handleAccountAdd 手工导入（面板不调用）：
//
//	body: {"name":"可选","site":"cn","token":"<回调链接 或 凭证 JSON 字符串>"}
//
// 以 uid 为准 upsert：已存在则更新凭证、不存在则新增，两种情况都返回成功。
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
		writeErr(w, http.StatusBadRequest, "请提供 token（TRAE 登录回调链接或凭证 JSON）")
		return
	}
	acc, created, err := p.importCredential(token, strings.TrimSpace(body.Name), strings.TrimSpace(body.UID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, truncateRunes(err.Error(), 90))
		return
	}
	verb := "已更新"
	if created {
		verb = "已新增"
	}
	writeOK(w, map[string]any{
		"message": fmt.Sprintf("%s账号 %s", verb, acc.name()),
		"account": accountViewOf(acc),
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

// handleCheckin 签到：今日已签按成功语义返回（幂等友好）。
func (p *Plugin) handleCheckin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := p.store.get(id); !ok {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	updated, msg, err := p.performCheckin(id)
	if err != nil {
		writeUpstreamErr(w, err)
		return
	}
	writeOK(w, map[string]any{"message": msg, "account": accountViewOf(updated)})
}

// performCheckin 对一个账号执行签到（幂等：已签则只回报状态）并刷新额度快照。
func (p *Plugin) performCheckin(id string) (Account, string, error) {
	acc, ok := p.store.get(id)
	if !ok {
		return Account{}, "", &upstreamError{Status: http.StatusNotFound, Msg: "账号不存在"}
	}
	accCopy := acc
	if err := p.ensureToken(&accCopy); err != nil {
		p.noteUpstreamError(&accCopy, asUpstreamError(err))
		return Account{}, "", err
	}

	checkedIn, enable, doc, err := p.checkinStatus(&accCopy)
	if err != nil {
		p.noteUpstreamError(&accCopy, asUpstreamError(err))
		return Account{}, "", err
	}

	msg := "今天已经签到过了"
	if !checkedIn {
		if !enable {
			return Account{}, "", &upstreamError{Status: http.StatusBadGateway, Msg: "该账号当前不可签到（上游未开放签到）"}
		}
		if err := p.checkinClaim(&accCopy); err != nil {
			p.noteUpstreamError(&accCopy, asUpstreamError(err))
			return Account{}, "", err
		}
		msg = "签到成功"
	}

	credits, src := extractIdeCredits(doc)
	updated, _ := p.store.updateState(id, func(st *accountState) {
		st.CheckedInToday = true
		st.LastError = ""
		if src != "" {
			st.Credits = credits
			st.CreditsAt = nowMillis()
		}
	})
	return updated, msg, nil
}

// handleQuota 刷新额度：只在被调用时向上游查询并更新快照（不做定时轮询）。
func (p *Plugin) handleQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	acc, ok := p.store.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	accCopy := acc
	if err := p.ensureToken(&accCopy); err != nil {
		writeUpstreamErr(w, err)
		return
	}
	if _, _, _, err := p.checkinStatus(&accCopy); err != nil {
		p.noteUpstreamError(&accCopy, asUpstreamError(err))
		writeUpstreamErr(w, err)
		return
	}
	updated, note := p.refreshQuotaSnapshot(id)
	writeOK(w, map[string]any{"message": "额度已刷新" + note, "account": accountViewOf(updated)})
}

// refreshQuotaSnapshot 向上游查一次签到状态接口，把额度与签到信息写进快照。
//
// credits 取上游下发的 ide_credits（SOLO 真正可用的额度）。依据 trae2api docs/RESEARCH.md §3：
// 权威来源是对话流里的 event:notify_usage 的 cn_credits_remain_info.ide_credits，
// 该事件在转发对话时会被顺带捕获（见 chat.go noteCredits）。
//
// TODO(上游额度端点)：参考实现的额度查询接口是 ide_user_ent_usage（聚合值，含 work 包，
// 不代表 SOLO 可用额度），本插件刻意不使用它；而除了对话流之外，参考项目没有其它
// 「单取 ide_credits」的端点。因此这里对签到状态接口的响应做一次深度搜索：找到 ide_credits 就用它，
// 否则退回通用 credits 字段。若将来上游提供专门的 ide_credits 查询端点，改 config.json + 此处即可。
func (p *Plugin) refreshQuotaSnapshot(id string) (Account, string) {
	acc, ok := p.store.get(id)
	if !ok {
		return Account{}, "账号不存在"
	}
	checkedIn, _, doc, err := p.checkinStatus(&acc)
	if err != nil {
		p.noteUpstreamError(&acc, asUpstreamError(err))
		cur, _ := p.store.get(id)
		return cur, ""
	}
	credits, src := extractIdeCredits(doc)
	updated, _ := p.store.updateState(id, func(st *accountState) {
		st.CheckedInToday = checkedIn
		st.LastError = ""
		if src != "" {
			st.Credits = credits
			st.CreditsAt = nowMillis()
		}
	})
	if src == "" {
		return updated, "（上游未提供额度字段）"
	}
	return updated, ""
}

// writeUpstreamErr 把 error 映射为合适的 HTTP 状态 + {"error":...}。
func writeUpstreamErr(w http.ResponseWriter, err error) {
	ue := asUpstreamError(err)
	writeErr(w, ue.Status, ue.Msg)
}

// asUpstreamError 把任意错误规整为 *upstreamError（非本类型时按 502 处理）。
func asUpstreamError(err error) *upstreamError {
	if err == nil {
		return &upstreamError{Status: http.StatusBadGateway, Msg: "上游错误"}
	}
	if ue, ok := err.(*upstreamError); ok {
		return ue
	}
	return &upstreamError{Status: http.StatusBadGateway, Msg: truncateRunes(err.Error(), 80)}
}

// ---------- 响应工具 ----------

func writeOK(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	if status < 400 {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// 错误文案压到 240 字节内：OpenAI 入口只透传响应体前 300 字节（规范 §5.3）。
	_ = json.NewEncoder(w).Encode(map[string]string{"error": clampBytes(msg, 240)})
}

func nowMillis() int64 { return time.Now().UnixMilli() }