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
	"strconv"
	"strings"
	"time"
)

// 上游端点的请求头全集合照抄自 trae2api/internal/upstream/headers.go（实测必须）。
// clientUA 固定为 "Trae/<IdeVersion>"。
func (p *Plugin) clientUA() string { return "Trae/" + p.eps.IdeVersion }

// soloHeaders 设置 llm_utils_chat / get_detail_param 所需的 SOLO 专属头。
func (p *Plugin) soloHeaders(req *http.Request, a *Account, stream bool) {
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
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
	req.Header.Set("X-Ide-Version", p.eps.IdeVersion)
	req.Header.Set("X-Ide-Version-Code", p.eps.IdeVersionCode)
	req.Header.Set("X-App-Version-Code", p.eps.IdeVersionCode)
	req.Header.Set("X-Ide-Version-Type", "stable")
	req.Header.Set("X-Device-Type", "macos")
	req.Header.Set("X-OS-Version", p.eps.OSVersion)
	req.Header.Set("X-Device-Brand", p.eps.DeviceBrand)
	req.Header.Set("Request-Traffic-Type", "prod")
	if a.MachineID != "" {
		req.Header.Set("X-Machine-Id", a.MachineID)
	}
	if a.DeviceID != "" {
		req.Header.Set("X-Device-Id", a.DeviceID)
	}
}

// ugHeaders 设置签到 / 积分（api.trae.cn）所需的头。
func (p *Plugin) ugHeaders(req *http.Request, a *Account) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.clientUA())
	req.Header.Set("Authorization", "Cloud-IDE-JWT "+a.AccessToken)
	req.Header.Set("X-User-Region", "CN")
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
}

func (e *upstreamError) Error() string { return e.Msg }

// classifyCode 按上游业务码映射（trae2api 实测码表）。
func classifyCode(code int64) *upstreamError {
	switch code {
	case 1001:
		return &upstreamError{Status: http.StatusUnauthorized, Msg: "账号登录态失效，请重新登录", Disable: true}
	case 1005:
		return &upstreamError{Status: http.StatusForbidden, Msg: "该账号未开通此权益", Disable: false}
	case 4008:
		return &upstreamError{Status: http.StatusTooManyRequests, Msg: "免费额度已用尽，等待重置或签到", Disable: false}
	case 4011:
		return &upstreamError{Status: http.StatusTooManyRequests, Msg: "触发上游限流，请稍后重试", Disable: false}
	case 4001:
		// 参数无效：模型不存在 / 版本不匹配 —— 换谁都一样，不切号（规范 §5.2）。
		return &upstreamError{Status: http.StatusBadRequest, Msg: "模型不可用，请更换模型", Disable: false}
	case 9074:
		return &upstreamError{Status: http.StatusBadGateway, Msg: "上游繁忙（签到人数过多），请稍后重试", Disable: false}
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
	body := map[string]any{"ReqSource": "IDE", "IDEVersion": p.eps.IdeVersion}
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

// checkinStatus 查询签到状态 + 额度快照。
// 返回的 raw 供 extractIdeCredits 进一步提取 SOLO 真正可用的 ide_credits。
func (p *Plugin) checkinStatus(a *Account) (checkedIn bool, enable bool, raw map[string]any, err error) {
	req, err := http.NewRequest(http.MethodPost, p.eps.UgHost+p.eps.EpCheckinStatus, bytes.NewReader([]byte("{}")))
	if err != nil {
		return false, false, nil, err
	}
	p.ugHeaders(req, a)
	data, err := p.doJSON(req)
	if err != nil {
		return false, false, nil, err
	}
	var resp struct {
		CheckedIn bool `json:"checked_in"`
		Enable    bool `json:"enable"`
	}
	_ = json.Unmarshal(data, &resp)
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	return resp.CheckedIn, resp.Enable, doc, nil
}

// checkinClaim 执行签到。
func (p *Plugin) checkinClaim(a *Account) error {
	req, err := http.NewRequest(http.MethodPost, p.eps.UgHost+p.eps.EpCheckinClaim, bytes.NewReader([]byte(`{"req_source":2}`)))
	if err != nil {
		return err
	}
	p.ugHeaders(req, a)
	data, err := p.doJSON(req)
	if err != nil {
		return err
	}
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &resp)
	if resp.Code != 0 {
		if e := classifyCode(int64(resp.Code)); e != nil {
			return e
		}
		msg := strings.TrimSpace(resp.Message)
		if msg == "" {
			msg = "签到失败"
		}
		return &upstreamError{Status: http.StatusBadGateway, Msg: truncateRunes(msg, 100)}
	}
	return nil
}

// ---------- 额度：SOLO 真正看 notify_usage 的 ide_credits ----------

// extractIdeCredits 从上游响应里提取 SOLO 通道可用的 ide_credits 余额。
//
// 依据 trae2api docs/RESEARCH.md §3：对话流里的 event:notify_usage 返回
//   "cn_credits_remain_info": {"ide_credits": N, "work_credits": M}
// SOLO 通道真正可用的是 ide_credits；ide_user_ent_usage 的聚合值含 work 包，不能代表 SOLO 额度，
// 因此这里优先取 ide_credits，取不到才退回 credits（此时调用方会给出说明）。
//
// 兼容多种形状：顶层 notify_usage / cn_credits_remain_info，任意深度内的 ide_credits，以及顶层 credits。
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
	if v, ok := deepFindNumber(doc, "credits"); ok {
		return v, "credits"
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
		"&x_app_version=" + url.QueryEscape(eps.IdeVersion) +
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