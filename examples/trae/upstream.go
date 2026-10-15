package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// clientUA 返回上游请求的 User-Agent。
//
// 取值依据 AgentHub rules.cjs headers.json.trae.userAgent = "TraeClient/TTNet"：
// 旧实现 "Trae/<IdeVersion>" 是 IDE 客户端形态，会被风控识别为非官方客户端。
func (p *Plugin) clientUA() string {
	if ua := strings.TrimSpace(p.eps.UserAgent); ua != "" {
		return ua
	}
	return "Trae/" + p.eps.IdeVersion
}

// soloHeaders 设置 llm_utils_chat / get_detail_param 所需的 SOLO 专属头。
//
// 指纹逐字段对齐 AgentHub proxy/adapters.cjs 的 headers()：「缺任何一项都可能被上游
// 风控识别为非官方客户端」。referer 需由调用方按**实际请求 URL** 设置（同源伪装）。
func (p *Plugin) soloHeaders(req *http.Request, a *Account, stream bool) {
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("User-Agent", p.clientUA())
	at := a.AccessToken
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+at)
	req.Header.Set("X-Cloudide-Token", at)
	req.Header.Set("X-Ide-Token", at)
	if a.UID != "" {
		req.Header.Set("X-Uid", a.UID)
	}
	req.Header.Set("X-App-Id", p.eps.AppID)
	req.Header.Set("X-App-Version", "default")
	req.Header.Set("App-Version", p.eps.IdeVersion)
	req.Header.Set("X-Ide-Version", p.eps.IdeVersion)
	req.Header.Set("X-Ide-Version-Code", p.eps.IdeVersionCode)
	req.Header.Set("X-App-Version-Code", p.eps.IdeVersionCode)
	req.Header.Set("X-Ide-Version-Type", "stable")
	req.Header.Set("X-Device-Type", "windows")
	req.Header.Set("X-Device-Cpu", "Intel")
	req.Header.Set("X-OS-Version", p.eps.OSVersion)
	req.Header.Set("X-Device-Brand", p.eps.DeviceBrand)
	req.Header.Set("Request-Traffic-Type", "prod")
	req.Header.Set("Package-Type", "stable_cn")
	req.Header.Set("X-Lgw-Req-Sdk-Type", "3")
	req.Header.Set("X-Lscbd-Aid", "787976")
	req.Header.Set("X-Lscbd-Platform", "windows")
	req.Header.Set("X-Ss-Dp", "787976")
	if a.MachineID != "" {
		req.Header.Set("X-Machine-Id", a.MachineID)
	}
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
	p.setTraceHeaders(req)
}

// setTraceHeaders 生成 AgentHub 同款的四个链路追踪头。
//
// 格式取自其 headers()：x-tt-trace-id = "00-<32hex>-<32hex>-01"，
// x-flow-traceparent = "04-<前32hex>-<16hex>-01"，x-custom-trace-id 取前 16 位。
func (p *Plugin) setTraceHeaders(req *http.Request) {
	tid := "00-" + randomHex(16) + "-" + randomHex(16) + "-01"
	req.Header.Set("X-Tt-Trace-Id", tid)
	req.Header.Set("X-Flow-Traceparent", "04-"+tid[3:35]+"-"+randomHex(16)+"-01")
	req.Header.Set("X-Custom-Trace-Id", tid[3:19])
	req.Header.Set("X-Request-Id", "req_"+randomHex(16))
}

// ugHeaders 设置签到 / 积分（api.trae.cn）所需的头。
//
// 依据 AgentHub proxy/adapters.cjs：ug 域请求除凭证外还必须带 origin / referer /
// x-app-type（缺了会被风控识别成非官方客户端），且两种凭证形态并存 ——
// **查签到状态用 Bearer，签到领取与积分查询用 Cloud-IDE-JWT**。
func (p *Plugin) ugHeaders(req *http.Request, a *Account, bearer bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.clientUA())
	if bearer {
		req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	} else {
		req.Header.Set("Authorization", "Cloud-IDE-JWT "+a.AccessToken)
	}
	req.Header.Set("X-User-Region", "CN")
	req.Header.Set("X-App-Type", "trae")
	req.Header.Set("Origin", p.eps.ConsoleHost)
	req.Header.Set("Referer", p.eps.ConsoleHost+"/")
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
}

// oauthHeaders 设置 ExchangeToken / GetUserInfo 所需的头（无签名，仅 UA）。
func (p *Plugin) oauthHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.clientUA())
}

// ---------- 错误分类：状态码即切号信号（规范 §5.2） ----------

// upstreamError 是映射到网关切号语义后的错误。
type upstreamError struct {
	Status  int    // 回给网关的 HTTP 状态码
	Msg     string // 中文文案（≤300 字节）
	Disable bool   // 是否把该账号硬禁用（登录态失效）
	Code    int64  // 上游业务码（0 = 无）；用于区分「可重试的容量类错误」
}

func (e *upstreamError) Error() string { return e.Msg }

// classifyCode 按上游业务码映射（trae2api 实测码表）。
func classifyCode(code int64) *upstreamError {
	switch code {
	case 1001:
		return &upstreamError{Status: http.StatusUnauthorized, Msg: "账号登录态失效，请重新登录", Disable: true, Code: 1001}
	case 1005:
		// 积分不足属账号级权益问题：用 403（规范 §5.2「无权/未开通」→ 切号），
		// 让网关换一个还有额度的账号继续，而不是把失败直接丢给客户端。
		return &upstreamError{Status: http.StatusForbidden, Msg: "该账号未开通此权益", Disable: false, Code: 1005}
	case 4008:
		return &upstreamError{Status: http.StatusTooManyRequests, Msg: "免费额度已用尽，等待重置或签到", Disable: false, Code: 4008}
	case 4011:
		return &upstreamError{Status: http.StatusTooManyRequests, Msg: "触发上游限流，请稍后重试", Disable: false, Code: 4011}
	case 9074:
		// 签到高峰的容量限制：属可重试的临时错误（见 checkinClaimWithRetry）。
		return &upstreamError{Status: http.StatusBadGateway, Msg: "上游繁忙（签到人数过多），请稍后重试", Disable: false, Code: 9074}
	case 4001:
		// 参数无效：模型不存在 / 版本不匹配 —— 换谁都一样，不切号（规范 §5.2）。
		return &upstreamError{Status: http.StatusBadRequest, Msg: "模型不可用，请更换模型", Disable: false, Code: 4001}
	}
	return nil
}

// classifyUpstream 先看上游业务码，再看 HTTP 状态码。
// 本插件只支持国内站单站点，模型缺失对谁都一样，因此**从不使用 404**（设计稿 §7）。
func classifyUpstream(status int, body string) *upstreamError {
	if code := extractCode(body); code != 0 {
		if e := classifyCode(code); e != nil {
			return e
		}
	}
	switch {
	case status == http.StatusUnauthorized:
		return &upstreamError{Status: http.StatusUnauthorized, Msg: "账号登录态失效，请重新登录", Disable: true}
	case status == http.StatusForbidden:
		return &upstreamError{Status: http.StatusForbidden, Msg: "该账号无权使用（权益不足或地区限制）", Disable: false}
	case status == http.StatusTooManyRequests:
		return &upstreamError{Status: http.StatusTooManyRequests, Msg: "触发上游限流，请稍后重试", Disable: false}
	case status >= 500:
		return &upstreamError{Status: http.StatusBadGateway, Msg: "上游服务异常，请稍后重试", Disable: false}
	case status >= 400:
		// 其余 4xx 归为请求本身的问题 —— 不切号。
		return &upstreamError{Status: http.StatusBadRequest, Msg: "上游拒绝了该请求（参数无效）", Disable: false}
	}
	return &upstreamError{Status: http.StatusBadGateway, Msg: "上游返回了未知错误", Disable: false}
}

// extractCode 从上游响应体里提取业务码（形如 {"code":1001} 或 {"Result":{"code":...}}）。
func extractCode(body string) int64 {
	body = strings.TrimSpace(body)
	if body == "" {
		return 0
	}
	var obj map[string]any
	if json.Unmarshal([]byte(body), &obj) == nil {
		if v := numberField(obj, "code"); v != 0 {
			return v
		}
		if r, ok := obj["Result"].(map[string]any); ok {
			if v := numberField(r, "code"); v != 0 {
				return v
			}
		}
		if d, ok := obj["data"].(map[string]any); ok {
			if v := numberField(d, "code"); v != 0 {
				return v
			}
		}
	}
	// 兜底：直接扫已知码（这些是具体四位数，误判概率极低）。
	for _, c := range []int64{1001, 1005, 4001, 4008, 4011, 9074} {
		if strings.Contains(body, `"code":`+strconv.FormatInt(c, 10)) {
			return c
		}
	}
	return 0
}

func numberField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

// ---------- 基础请求 ----------

// doJSON 发请求并返回 2xx 响应体；非 2xx 时返回归类后的 *upstreamError。
func (p *Plugin) doJSON(req *http.Request) ([]byte, error) {
	resp, err := p.api.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求上游失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, classifyUpstream(resp.StatusCode, string(raw))
	}
	return raw, nil
}

// refreshToken 通过 ExchangeToken 强制刷新 access token（refreshToken 会轮换）。
// 成功后更新 a 的字段；调用方负责 saveAuth 落盘。
func (p *Plugin) refreshToken(a *Account) error {
	if strings.TrimSpace(a.RefreshToken) == "" {
		return &upstreamError{Status: http.StatusUnauthorized, Msg: "账号缺少 refreshToken，请重新登录", Disable: true}
	}
	host := strings.TrimSpace(a.ApiHost)
	if host == "" {
		host = p.eps.OAuthHost
	}
	body := map[string]any{
		"ClientID":     p.eps.ClientID,
		"RefreshToken": a.RefreshToken,
		"ClientSecret": "-",
		"UserID":       "",
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, host+p.eps.EpExchange, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	p.oauthHeaders(req)
	data, err := p.doJSON(req)
	if err != nil {
		return err
	}
	var resp struct {
		Result struct {
			Token               string `json:"Token"`
			TokenExpireAt       int64  `json:"TokenExpireAt"`
			TokenExpireDuration int64  `json:"TokenExpireDuration"`
			RefreshToken        string `json:"RefreshToken"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("解析 ExchangeToken 响应失败")
	}
	if strings.TrimSpace(resp.Result.Token) == "" {
		return &upstreamError{Status: http.StatusUnauthorized, Msg: "账号登录态已失效，请重新登录", Disable: true}
	}
	a.AccessToken = resp.Result.Token
	if strings.TrimSpace(resp.Result.RefreshToken) != "" {
		a.RefreshToken = resp.Result.RefreshToken
	}
	if resp.Result.TokenExpireAt > 0 {
		a.ExpiresAt = normalizeExpireSeconds(resp.Result.TokenExpireAt)
	} else if resp.Result.TokenExpireDuration > 0 {
		a.ExpiresAt = time.Now().Add(time.Duration(resp.Result.TokenExpireDuration) * time.Second).Unix()
	}
	return nil
}

// normalizeExpireSeconds 把上游的 TokenExpireAt（毫秒）归一化为 Unix 秒。
func normalizeExpireSeconds(v int64) int64 {
	if v > 1e12 {
		return v / 1000
	}
	return v
}

// ensureToken 保证账号有可用的 accessToken：快过期（含已过期）时用 refreshToken 续期并落盘。
func (p *Plugin) ensureToken(a *Account) error {
	if a.tokenAlive() {
		return nil
	}
	if strings.TrimSpace(a.RefreshToken) == "" {
		return &upstreamError{Status: http.StatusUnauthorized, Msg: "账号登录态已过期，请重新登录", Disable: true}
	}
	if err := p.refreshToken(a); err != nil {
		return err
	}
	_ = p.store.saveAuth(a.toAuth())
	return nil
}

// getUserInfo 查询账号资料（登录后补 uid / 昵称 / 企业 id）。
func (p *Plugin) getUserInfo(a *Account) (uid, nickname, enterpriseID string, err error) {
	host := strings.TrimSpace(a.ApiHost)
	if host == "" {
		host = p.eps.OAuthHost
	}
	// GetUserInfo 与 ExchangeToken 同在授权域，用授权版本号（AgentHub：authAppVersion）。
	body := map[string]any{"ReqSource": "IDE", "IDEVersion": p.eps.AuthAppVersion}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, host+p.eps.EpUserInfo, bytes.NewReader(raw))
	if err != nil {
		return "", "", "", err
	}
	p.oauthHeaders(req)
	req.Header.Set("X-Cloudide-Token", a.AccessToken)
	data, err := p.doJSON(req)
	if err != nil {
		return "", "", "", err
	}
	var resp struct {
		Result struct {
			UserID       string `json:"UserID"`
			ScreenName   string `json:"ScreenName"`
			EnterpriseID string `json:"EnterpriseID"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", "", "", fmt.Errorf("解析 GetUserInfo 响应失败")
	}
	return resp.Result.UserID, resp.Result.ScreenName, resp.Result.EnterpriseID, nil
}

// modelInfo 是从模型表里抽出的最小信息。
type modelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// fetchModels 拉 SOLO 模型表（get_detail_param）。
func (p *Plugin) fetchModels(a *Account) ([]modelInfo, error) {
	body := map[string]any{
		"function":            p.eps.Function,
		"config_names":        nil,
		"need_prompt":         false,
		"current_config_info": nil,
		"poly_prompt":         true,
		"mode_type":           nil,
		"agent_type":          nil,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, p.eps.AgentHost+p.eps.EpModels, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	p.soloHeaders(req, a, false)
	req.Header.Set("Referer", p.eps.AgentHost+p.eps.EpModels)
	data, err := p.doJSON(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ConfigInfoList []struct {
			ConfigName    string `json:"config_name"`
			DisplayConfig struct {
				DisplayName string `json:"display_name"`
			} `json:"display_config"`
		} `json:"config_info_list"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("解析模型表失败")
	}
	out := make([]modelInfo, 0, len(resp.ConfigInfoList))
	for _, cfg := range resp.ConfigInfoList {
		if strings.TrimSpace(cfg.ConfigName) == "" {
			continue
		}
		out = append(out, modelInfo{ID: cfg.ConfigName, Name: cfg.DisplayConfig.DisplayName})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("上游模型表为空")
	}
	return out, nil
}

// checkinState 是签到状态接口的解析结果。
//
// 字段依据 AgentHub proxy/adapters.cjs 的 checkinStatus：上游除 checked_in / enable 外
// 还返回 consecutive_days（连签天数）、credits_earned_today、checkin_date。
type checkinState struct {
	CheckedIn          bool
	Enable             bool
	Unavailable        bool // 「签到服务未对该账号开放」（非登录态失效）
	StreakDays         int
	CreditsEarnedToday float64
}

// checkinClaimResult 是签到领取的结果语义。
type checkinClaimResult struct {
	Already     bool // 上游反馈「今日已签到」→ 幂等成功
	Unavailable bool // 「签到服务未对该账号开放」
	Message     string
}

// 上游在「今天已签到」时也用成功码返回，文案形如「今日已签到」；
// 按规范 §6.5「幂等友好」的要求，这种情况必须当成功语义，不能当硬错误。
var checkedInRe = regexp.MustCompile(`(?i)已签到|已经签到|already`)

// isCode 判断错误是否为指定上游业务码。
func isCode(err error, code int64) bool {
	ue, ok := err.(*upstreamError)
	return ok && ue.Code == code
}

// checkinStatus 查询签到状态。
//
// 依据 AgentHub proxy/adapters.cjs 的注释「GET+did（cockpit 现行）为主，
// POST+Cloud-IDE-JWT 兜底」：主路径是 GET + ?did=<deviceId> + Bearer，
// 旧口径（POST 空体 + Cloud-IDE-JWT）仅作兜底。
//
// ⚠️ 签到语境下的 code 1001 表示「签到服务未对该账号开放（scope 限制）」，
// **不是**登录态失效 —— 若按 classifyCode(1001) 处理会把好账号硬禁用（AgentHub 实测坑）。
func (p *Plugin) checkinStatus(a *Account) (checkinState, error) {
	// 主：GET + ?did=<deviceId> + Bearer（cockpit 现行口径）
	if a.DeviceID != "" {
		u := p.eps.UgHost + p.eps.EpCheckinStatus + "?did=" + url.QueryEscape(a.DeviceID)
		if req, err := http.NewRequest(http.MethodGet, u, nil); err == nil {
			p.ugHeaders(req, a, true)
			st, perr := p.parseCheckinStatus(req)
			// ⚠️ 只有「拿到真实状态」才直接采信。若这一形态被判成 1001，必须继续走 POST 兜底：
			// GET+Bearer 不被上游接受时它同样回 1001，单看它一个就会把能签到的账号误判成「未开放」。
			if perr == nil && !st.Unavailable {
				return st, nil
			}
		}
	}
	// 兜底：POST 空体 + Cloud-IDE-JWT（旧口径，实测可用）
	req, err := http.NewRequest(http.MethodPost, p.eps.UgHost+p.eps.EpCheckinStatus, bytes.NewReader([]byte("{}")))
	if err != nil {
		return checkinState{}, err
	}
	p.ugHeaders(req, a, false)
	return p.parseCheckinStatus(req)
}

func (p *Plugin) parseCheckinStatus(req *http.Request) (checkinState, error) {
	var st checkinState
	data, err := p.doJSON(req)
	if err != nil {
		if isCode(err, 1001) {
			st.Unavailable = true
			return st, nil
		}
		return st, err
	}
	var resp struct {
		Code               int64   `json:"code"`
		CheckedIn          bool    `json:"checked_in"`
		CheckedInCamel     bool    `json:"checkedIn"`
		Enable             bool    `json:"enable"`
		ConsecutiveDays    int     `json:"consecutive_days"`
		CreditsEarnedToday float64 `json:"credits_earned_today"`
	}
	_ = json.Unmarshal(data, &resp)
	// 上游既用 code 0 也用 code 200 表示成功（AgentHub 同判据）。
	if resp.Code != 0 && resp.Code != 200 {
		if resp.Code == 1001 {
			st.Unavailable = true
			return st, nil
		}
		if e := classifyCode(resp.Code); e != nil {
			return st, e
		}
		return st, &upstreamError{Status: http.StatusBadGateway, Msg: "签到状态查询失败", Code: resp.Code}
	}
	st.CheckedIn = resp.CheckedIn || resp.CheckedInCamel
	st.Enable = resp.Enable
	st.StreakDays = resp.ConsecutiveDays
	st.CreditsEarnedToday = resp.CreditsEarnedToday
	return st, nil
}

// checkinClaim 执行签到。
//
// 请求体是空对象 `{}`（AgentHub 现行口径）—— 旧口径的 `{"req_source":2}` 已不被现行
// 上游接受；头用 Cloud-IDE-JWT 并带 origin / referer / x-app-type。
func (p *Plugin) checkinClaim(a *Account) (checkinClaimResult, error) {
	var out checkinClaimResult
	req, err := http.NewRequest(http.MethodPost, p.eps.UgHost+p.eps.EpCheckinClaim, bytes.NewReader([]byte("{}")))
	if err != nil {
		return out, err
	}
	p.ugHeaders(req, a, false)
	data, err := p.doJSON(req)
	if err != nil {
		if isCode(err, 1001) {
			out.Unavailable = true
			return out, nil
		}
		return out, err
	}
	var resp struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	_ = json.Unmarshal(data, &resp)
	out.Message = strings.TrimSpace(resp.Message)
	if out.Message == "" {
		out.Message = strings.TrimSpace(resp.Msg)
	}
	switch {
	case resp.Code == 1001:
		out.Unavailable = true
		return out, nil
	case resp.Code == 0 || resp.Code == 200:
		out.Already = checkedInRe.MatchString(out.Message)
		return out, nil
	}
	if e := classifyCode(resp.Code); e != nil {
		return out, e
	}
	msg := out.Message
	if msg == "" {
		msg = "签到失败"
	}
	return out, &upstreamError{Status: http.StatusBadGateway, Msg: truncateRunes(msg, 100), Code: resp.Code}
}

// checkinClaimWithRetry 执行签到；遇到上游容量类错误（9074）时退避重试。
func (p *Plugin) checkinClaimWithRetry(a *Account) (checkinClaimResult, error) {
	const attempts = 4
	var out checkinClaimResult
	var last error
	for i := 0; i < attempts; i++ {
		if out, last = p.checkinClaim(a); last == nil {
			return out, nil
		}
		if !isRetryableUpstream(last) || i == attempts-1 {
			break
		}
		time.Sleep(time.Duration(1<<uint(i)) * time.Second) // 1s / 2s / 4s
	}
	if ue, ok := last.(*upstreamError); ok && isRetryableUpstream(last) {
		return out, &upstreamError{
			Status: ue.Status,
			Msg:    "上游暂时拒绝签到（业务码 " + strconv.FormatInt(ue.Code, 10) + "），已自动重试 " + strconv.Itoa(attempts) + " 次仍未成功，请稍后再试",
			Code:   ue.Code,
		}
	}
	return out, last
}

// isRetryableUpstream 判断是否属于「重试可能成功」的上游错误（当前只有签到容量限制 9074）。
func isRetryableUpstream(err error) bool {
	ue, ok := err.(*upstreamError)
	return ok && ue.Code == 9074
}

// ---------- 额度 ----------
//
// 面板「积分」列取 ide_user_ent_usage 的口径：**Σ 未过期权益包的剩余积分**（允许小数）。
//
// 口径依据 AgentHub proxy/adapters.cjs 的 traeCredits + pool.deriveAccountCredits。
// 该值覆盖账号下的全部权益包（含 work 包），并不等于 SOLO 通道单独可用的 ide_credits；
// SOLO 专属口径只出现在对话流的 event:notify_usage.cn_credits_remain_info.ide_credits 里，
// 但那是「对话那一刻」的瞬时值，不适合当作面板余额，因此不作为积分来源。

// entUsageRemain 查账号额度明细，返回未过期权益包的剩余积分之和。
//
// 逐包规则（AgentHub 实测口径；旧实现「Σ全部包 limit − Σ全部包 amount」是错的）：
//   - 隐藏包（is_hide）/ 已取消（status=3）/ 兑换码包（product_type=3）跳过；
//   - credits_limit 缺失或为 0 → 该包不属于该账号，跳过；
//   - credits_limit < 0 是「不限量」哨兵，不参与求和 → 整体返回 -1；
//   - 剩余 = max(credits_limit − credits_amount, 0)，剔除已过期的包后求和。
func (p *Plugin) entUsageRemain(a *Account) (float64, error) {
	req, err := http.NewRequest(http.MethodPost, p.eps.UgHost+p.eps.EpEntUsage, bytes.NewReader([]byte("{}")))
	if err != nil {
		return 0, err
	}
	p.ugHeaders(req, a, false)
	data, err := p.doJSON(req)
	if err != nil {
		// pay/ug 域对部分账号（scope 限制）整体拒绝并返回 code 1001：
		// 这是「积分服务不开放」，不是凭证失效，不能按登录态失效处理。
		if isCode(err, 1001) {
			return 0, &upstreamError{Status: http.StatusForbidden, Msg: "该账号未开放积分服务（上游 scope 限制）", Code: 1001}
		}
		return 0, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, &upstreamError{Status: http.StatusBadGateway, Msg: "额度查询响应无法解析"}
	}
	packs, _ := digValue(doc, "user_entitlement_pack_list")
	list, _ := packs.([]any)
	if len(list) == 0 {
		return 0, &upstreamError{Status: http.StatusBadGateway, Msg: "上游未返回额度信息"}
	}
	now := time.Now().Unix()
	var sum float64
	unlimited, valid := false, 0
	for _, item := range list {
		pk, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if digBool(pk, "is_hide") {
			continue
		}
		if n, ok := digNumber(pk, "status"); ok && int(n) == 3 {
			continue
		}
		if n, ok := digNumber(pk, "product_type"); ok && int(n) == 3 {
			continue
		}
		limit, ok := digNumber(pk, "credits_limit")
		if !ok || limit == 0 {
			continue
		}
		// 到期时间可能以秒或毫秒给出；取不到视为长期有效。
		expired := false
		for _, k := range []string{"end_time", "expire_time", "expire_at", "deadline", "valid_until"} {
			if ms, ok := digNumber(pk, k); ok && ms > 0 {
				if ms < 1e12 { // 秒 → 毫秒
					ms *= 1000
				}
				expired = ms/1000 <= float64(now)
				break
			}
		}
		if expired {
			continue
		}
		valid++
		if limit < 0 {
			unlimited = true
			continue
		}
		used, _ := digNumber(pk, "credits_amount")
		if rem := limit - used; rem > 0 {
			sum += rem
		}
	}
	if valid == 0 {
		return 0, &upstreamError{Status: http.StatusBadGateway, Msg: "上游未返回有效额度包"}
	}
	if unlimited {
		return -1, nil
	}
	return sum, nil
}

// digValue 在任意深度按（不区分大小写的）键名查找值。
//
// 与 AgentHub util.dig 同款做法：credits_limit / credits_amount 等字段的实际位置
// 并不固定在 quota. 下，逐层查找可兼容上游结构变动。
func digValue(v any, key string) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if strings.EqualFold(k, key) {
				return val, true
			}
		}
		for _, val := range t {
			if got, ok := digValue(val, key); ok {
				return got, true
			}
		}
	case []any:
		for _, val := range t {
			if got, ok := digValue(val, key); ok {
				return got, true
			}
		}
	}
	return nil, false
}

// digNumber 取任意深度的数值字段（JSON 数字统一解为 float64）。
func digNumber(v any, key string) (float64, bool) {
	got, ok := digValue(v, key)
	if !ok {
		return 0, false
	}
	switch n := got.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// digBool 取任意深度的布尔字段（缺失按 false）。
func digBool(v any, key string) bool {
	got, ok := digValue(v, key)
	if !ok {
		return false
	}
	b, _ := got.(bool)
	return b
}

// extractIdeCredits 从对话流的 notify_usage 里提取 SOLO 通道专属的 ide_credits。
//
// 依据 trae2api docs/RESEARCH.md §3：notify_usage 返回
//   "cn_credits_remain_info": {"ide_credits": N, "work_credits": M}
// ⚠️ 刻意**不**兜底到通用 credits 字段：签到状态接口的顶层 credits 是签到相关的数字
// （不是余额），历史上正是这个兜底让面板把积分显示成 100。
func extractIdeCredits(doc map[string]any) (float64, string) {
	if doc == nil {
		return 0, ""
	}
	if v, ok := deepFindNumber(doc, "ide_credits"); ok {
		return v, "ide_credits"
	}
	if v, ok := deepFindNumber(doc, "ideCredits"); ok {
		return v, "ide_credits"
	}
	return 0, ""
}

// deepFindNumber 在嵌套 JSON 里按 key 名找第一个数值（遍历顺序固定，结果可复现）。
func deepFindNumber(m map[string]any, key string) (float64, bool) {
	if len(m) == 0 {
		return 0, false
	}
	if v, ok := m[key]; ok {
		if f, ok := toFloatOK(v); ok {
			return f, true
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		switch t := m[k].(type) {
		case map[string]any:
			if f, ok := deepFindNumber(t, key); ok {
				return f, true
			}
		case []any:
			for _, item := range t {
				if mm, ok := item.(map[string]any); ok {
					if f, ok := deepFindNumber(mm, key); ok {
						return f, true
					}
				}
			}
		}
	}
	return 0, false
}

func toFloatOK(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

func sortStrings(a []string) {
	for i := 0; i < len(a)-1; i++ {
		for j := i + 1; j < len(a); j++ {
			if a[j] < a[i] {
				a[i], a[j] = a[j], a[i]
			}
		}
	}
}

// ---------- 登录 URL 与回调解析（复刻 trae2api/internal/server/callback.go） ----------

// BuildLoginURL 构造 TRAE 登录 URL；machineID / deviceID 必须与落盘凭证共用同一对（hex32）。
func BuildLoginURL(eps Endpoints, machineID, deviceID, callbackURL string) string {
	v := url.Values{}
	v.Set("login_version", "1")
	v.Set("auth_from", "solo")
	v.Set("login_channel", "native_ide")
	v.Set("plugin_version", "2.3.62834")
	v.Set("auth_type", "local")
	v.Set("client_id", eps.ClientID)
	v.Set("redirect", "0")
	return eps.ConsoleHost + "/authorization?" + v.Encode() +
		"&login_trace_id=" + url.QueryEscape(machineTraceID(machineID, deviceID)) +
		"&auth_callback_url=" + url.QueryEscape(callbackURL) +
		"&machine_id=" + url.QueryEscape(machineID) +
		"&device_id=" + url.QueryEscape(deviceID) +
		"&x_device_id=" + url.QueryEscape(deviceID) +
		"&x_machine_id=" + url.QueryEscape(machineID) +
		"&x_device_brand=PC" +
		"&x_device_type=PC" +
		"&x_os_version=1.0" +
		"&x_app_version=" + url.QueryEscape(eps.AuthAppVersion) +
		"&x_app_type=stable"
}

// machineTraceID 取 (machineID+deviceID) 的后 16 个 hex 字符作为 login_trace_id。
func machineTraceID(machineID, deviceID string) string {
	h := machineID + deviceID
	const n = 16
	if len(h) >= n {
		return h[len(h)-n:]
	}
	return strings.Repeat("0", n-len(h)) + h
}

// CallbackInfo 是回调链接的解析结果（脱敏前，仅内部使用）。
type CallbackInfo struct {
	RefreshToken string
	AccessToken  string
	UID          string
	Nickname     string
	EnterpriseID string
	ExpiresAt    int64 // Unix 秒（仅 userJwt 兜底路径会填）
}

// ParseCallback 解析 TRAE 登录回调链接，提取凭证字段。
func ParseCallback(rawURL string) (*CallbackInfo, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("回调链接为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("回调链接无法解析")
	}
	q := u.Query()
	info := &CallbackInfo{RefreshToken: q.Get("refreshToken")}
	userInfo := parseJSONParam(q.Get("userInfo"))
	info.UID = getString(userInfo, "UserID")
	info.Nickname = fixMojibake(getString(userInfo, "ScreenName"), info.UID)
	info.EnterpriseID = getString(userInfo, "TenantID")

	userJwt := parseJSONParam(q.Get("userJwt"))
	jwtToken := getString(userJwt, "Token")
	if info.RefreshToken == "" {
		info.RefreshToken = getString(userJwt, "RefreshToken")
	}
	if info.RefreshToken == "" {
		info.AccessToken = jwtToken
		if info.AccessToken == "" {
			return nil, fmt.Errorf("回调链接缺少 refreshToken，且 userJwt 也没有 Token")
		}
		if exp := getInt64(userJwt, "TokenExpireAt"); exp > 0 {
			info.ExpiresAt = normalizeExpireSeconds(exp)
		}
	}
	return info, nil
}

// parseJSONParam 解回调里 URL 编码的 JSON 参数（parse_qs 已解一层，这里再容错解一层）。
func parseJSONParam(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	candidates := []string{raw}
	if uq, err := url.QueryUnescape(raw); err == nil && uq != raw {
		candidates = append(candidates, uq)
	}
	for _, c := range candidates {
		var obj map[string]any
		if json.Unmarshal([]byte(c), &obj) == nil && obj != nil {
			return obj
		}
	}
	return nil
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return fmt.Sprintf("%v", v)
}

func getInt64(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch x := m[key].(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

// fixMojibake 修复回调 userInfo 中文被双重编码导致的昵称乱码（复刻 login.sh 的处理）。
func fixMojibake(s, uid string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !hasCJK(s) && strings.ContainsFunc(s, func(r rune) bool { return r > 0x7f }) {
		// 无 CJK 但有非 ASCII（典型乱码）→ 回退为「用户 + uid 末 4 位」。
		suffix := uid
		if len(suffix) > 4 {
			suffix = suffix[len(suffix)-4:]
		}
		return "用户" + suffix
	}
	return s
}

func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

// ---------- 设备 / 机器 id ----------

// randomHex 生成 n 字节的随机 hex 串（2n 个 hex 字符）。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// randomUUID 生成标准 8-4-4-4-12 形态的 v4 UUID（SOLO 会话 / 项目标识用）。
func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return randomHex(32)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// firstPositive 返回第一个为正数的值（非正数 / 非数值忽略）。
func firstPositive(vals ...any) float64 {
	for _, v := range vals {
		if n := toFloat(v); n > 0 {
			return n
		}
	}
	return 0
}

// toFloat 把 JSON 值转为 float64（非数值返回 0）。
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

// randomState 生成登录会话用的随机 state（URL 安全、不承载凭据语义）。
func randomState() string {
	return randomHex(16)
}

// newCheckinDeviceID 生成 16 位纯数字签到 deviceId（首字符非 0）。
// 规则来自 trae2api/internal/auth/deviceid.go：每个账号必须互异，否则会被上游按"该设备已签到"拦截。
func newCheckinDeviceID() (string, error) {
	digits := make([]byte, 16)
	first, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		return "", err
	}
	digits[0] = byte('1' + first.Int64())
	for i := 1; i < 16; i++ {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		digits[i] = byte('0' + d.Int64())
	}
	return string(digits), nil
}

// ensureCheckinDeviceID 仅在 DeviceID 为空时补一个 16 位数字 deviceId（已有值一律保留）。
func ensureCheckinDeviceID(a *Account) error {
	if strings.TrimSpace(a.DeviceID) != "" {
		return nil
	}
	id, err := newCheckinDeviceID()
	if err != nil {
		return err
	}
	a.DeviceID = id
	return nil
}

// truncateRunes 按字符数截断，避免把多字节中文截成半个字符。
func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}