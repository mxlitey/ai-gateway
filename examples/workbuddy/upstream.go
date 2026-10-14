package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Endpoints 是上游接口模板，全部可在 config.json 里覆盖：
// 上游一改，只改配置，不动代码。
type Endpoints struct {
	IntlChatOrigin    string `json:"intl_chat_origin"`
	IntlBillingOrigin string `json:"intl_billing_origin"`
	CNChatOrigin      string `json:"cn_chat_origin"`
	CNBillingOrigin   string `json:"cn_billing_origin"`

	AuthState    string `json:"auth_state"`
	AuthToken    string `json:"auth_token"`
	LoginAccount string `json:"login_account"`
	TokenRefresh string `json:"token_refresh"`

	Checkin         string `json:"checkin"`
	CheckinStatus   string `json:"checkin_status"`
	ResourceSummary string `json:"resource_summary"`
	ResourceFree    string `json:"resource_free"`
	ResourcePaid    string `json:"resource_paid"`
	EnterpriseUsage string `json:"enterprise_usage"`

	ChatCompletions string `json:"chat_completions"`
	Models          string `json:"models"`
}

func defaultEndpoints() Endpoints {
	return Endpoints{
		// 国内版：对话走 copilot.tencent.com，账单走 www.codebuddy.cn
		CNChatOrigin:      "https://copilot.tencent.com",
		CNBillingOrigin:   "https://www.codebuddy.cn",
		IntlChatOrigin:    "https://www.workbuddy.ai",
		IntlBillingOrigin: "https://www.workbuddy.ai",

		AuthState:    "/v2/plugin/auth/state",
		AuthToken:    "/v2/plugin/auth/token",
		LoginAccount: "/v2/plugin/login/account",
		TokenRefresh: "/v2/plugin/auth/token/refresh",

		Checkin:         "/v2/billing/meter/daily-checkin",
		CheckinStatus:   "/v2/billing/meter/checkin-activity-status",
		ResourceSummary: "/billing/meter/get-user-resource-summary",
		ResourceFree:    "/billing/meter/get-user-resource-free-packages",
		ResourcePaid:    "/billing/meter/get-user-resource-paid-packages",
		EnterpriseUsage: "/billing/meter/get-enterprise-user-usage",

		ChatCompletions: "/v2/chat/completions",
		// 可用模型清单在产品配置里：data.agents[].models（实测 /v2/models 不存在）
		Models: "/v3/config",
	}
}

// OAuth 登录相关的固定语义值
const (
	oauthPendingCode = 11217 // 上游表示「还在等浏览器授权」
)

// realmConfig 是某个站点区域的出口配置。
type realmConfig struct {
	ChatOrigin    string
	BillingOrigin string
	Domain        string
	ChatUA        string
	BillingUA     string
	Intl          bool
	HasCheckin    bool
}

func (p *Plugin) realmCfg(site string) realmConfig {
	if site == "intl" {
		return realmConfig{
			ChatOrigin:    strings.TrimRight(p.eps.IntlChatOrigin, "/"),
			BillingOrigin: strings.TrimRight(p.eps.IntlBillingOrigin, "/"),
			Domain:        "www.workbuddy.ai",
			ChatUA:        "WorkBuddy/5.5.2 WorkBuddy AI/5.5.2 CLI/5.5.2",
			BillingUA:     "WorkBuddy/5.5.2",
			Intl:          true,
			HasCheckin:    false,
		}
	}
	return realmConfig{
		ChatOrigin:    strings.TrimRight(p.eps.CNChatOrigin, "/"),
		BillingOrigin: strings.TrimRight(p.eps.CNBillingOrigin, "/"),
		Domain:        "copilot.tencent.com",
		ChatUA:        "WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1",
		BillingUA:     "WorkBuddy/5.5.6",
		HasCheckin:    true,
	}
}

// apiEnvelope 是上游统一响应外壳：code=0 表示成功。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// ---------- 请求头 ----------

// billingHeaders 是账单/签到这类接口用的轻量身份头。
// 这些端点只认「官网行为」的标头组合：Origin/Referer/X-CodeBuddy-Request，
// 外加按 uid 稳定派生的设备指纹，缺一样就可能被风控挡掉。
func billingHeaders(acc *Account, cfg realmConfig) map[string]string {
	h := map[string]string{
		"Content-Type":        "application/json",
		"Accept":              "application/json, text/plain, */*",
		"X-Requested-With":    "XMLHttpRequest",
		"User-Agent":          cfg.BillingUA,
		"Origin":              cfg.BillingOrigin,
		"Referer":             cfg.BillingOrigin + "/",
		"Authorization":       "Bearer " + acc.AccessToken,
		"X-User-Id":           acc.UID,
		"X-Domain":            firstNonEmpty(acc.Domain, cfg.Domain),
		"X-CodeBuddy-Request": "1",
		"X-Request-ID":        requestID(acc.UID),
		"X-Machine-ID":        deriveID(acc.UID, "machine"),
		"X-Session-ID":        deriveID(acc.UID, "session"),
	}
	if cfg.Intl {
		h["Accept-Language"] = "en-US"
	} else {
		h["Accept-Language"] = "zh-CN"
		h["X-Product"] = "SaaS"
	}
	if acc.EnterpriseID != "" {
		h["X-Enterprise-Id"] = acc.EnterpriseID
		h["X-Tenant-Id"] = acc.EnterpriseID
	} else {
		h["X-No-Enterprise-Id"] = "1"
	}
	if acc.DeviceToken != "" {
		h["X-Device-Token"] = acc.DeviceToken
	}
	return h
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---------- 底层请求 ----------

func (p *Plugin) doJSON(method, url string, body any, headers map[string]string) (*apiEnvelope, int, error) {
	var payload io.Reader
	if body == nil {
		payload = bytes.NewReader([]byte("{}"))
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		payload = bytes.NewReader(raw)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求上游失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取上游响应失败: %w", err)
	}

	env := &apiEnvelope{}
	if err := json.Unmarshal(raw, env); err != nil {
		snippet := string(raw)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, resp.StatusCode, fmt.Errorf("登录态无效或已过期（401）")
		}
		return nil, resp.StatusCode, fmt.Errorf("上游返回了非 JSON 响应（HTTP %d）：%s", resp.StatusCode, snippet)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return env, resp.StatusCode, fmt.Errorf("登录态无效或已过期（401）")
	}
	return env, resp.StatusCode, nil
}

// callBilling 用账单身份头调一个接口；origin 为空时用该区域的账单域。
func (p *Plugin) callBilling(acc *Account, origin, path string, body any) (*apiEnvelope, error) {
	cfg := p.realmCfg(acc.Site)
	if origin == "" {
		origin = cfg.BillingOrigin
	}
	env, _, err := p.doJSON(http.MethodPost, origin+path, body, billingHeaders(acc, cfg))
	return env, err
}

// ---------- 签到 ----------

type checkinResult struct {
	CheckedInToday bool
	StreakDays     int
	TodayCredit    float64
	DailyCredit    float64
}

// fetchCheckinStatus 查签到状态（只读）。注意它挂在对话域上，但用的是账单身份头。
func (p *Plugin) fetchCheckinStatus(acc *Account) (checkinResult, error) {
	cfg := p.realmCfg(acc.Site)
	if !cfg.HasCheckin {
		return checkinResult{}, nil
	}
	env, err := p.callBilling(acc, cfg.ChatOrigin, p.eps.CheckinStatus, nil)
	if err != nil {
		return checkinResult{}, err
	}
	data := decodeData(env.Data)

	res := checkinResult{}
	if v, ok := data["today_checked_in"].(bool); ok {
		res.CheckedInToday = v
	}
	res.StreakDays = int(toFloat(data["streak_days"]))
	res.TodayCredit = toFloat(data["today_credit"])
	res.DailyCredit = toFloat(data["daily_credit"])
	return res, nil
}

// doCheckin 执行签到；已签到按成功语义返回。
func (p *Plugin) doCheckin(acc *Account) (checkinResult, string, error) {
	cfg := p.realmCfg(acc.Site)
	if !cfg.HasCheckin {
		return checkinResult{}, "", fmt.Errorf("该站点没有签到功能")
	}
	env, err := p.callBilling(acc, "", p.eps.Checkin, nil)
	if err != nil {
		return checkinResult{}, "", err
	}
	switch env.Code {
	case 0:
		return checkinResult{CheckedInToday: true}, "签到成功", nil
	case 10001:
		return checkinResult{CheckedInToday: true}, "今天已经签到过了", nil
	default:
		msg := env.Msg
		if msg == "" {
			msg = fmt.Sprintf("上游返回 code=%d", env.Code)
		}
		return checkinResult{}, "", fmt.Errorf("签到失败：%s", msg)
	}
}

// ---------- 额度 ----------

// quotaPackage 是一个资源包（套餐/加油包）的可读视图。
type quotaPackage struct {
	Name    string  `json:"name"`
	Code    string  `json:"package_code"`
	Product string  `json:"product_name"`
	Unit    string  `json:"unit"`
	Remain  float64 `json:"remain"`
	Used    float64 `json:"used"`
	Size    float64 `json:"size"`
	Expire  string  `json:"expire_time"`
}

// fetchQuota 查额度：先取套餐概览拿到 PackageCode，再分别拉免费包与付费包。
func (p *Plugin) fetchQuota(acc *Account) (Account, []quotaPackage, string, error) {
	env, err := p.callBilling(acc, "", p.eps.ResourceSummary, nil)
	if err != nil {
		return *acc, nil, "", err
	}
	if env.Code != 0 {
		return *acc, nil, "", fmt.Errorf("查询额度失败：%s", firstNonEmpty(env.Msg, fmt.Sprintf("code=%d", env.Code)))
	}

	summary := decodeData(env.Data)
	var codes []string
	if pkgs, ok := summary["Packages"].([]any); ok {
		for _, item := range pkgs {
			if m, ok := item.(map[string]any); ok {
				if code := asString(m["PackageCode"]); code != "" {
					codes = append(codes, code)
				}
			}
		}
	}

	updated := *acc
	var packages []quotaPackage
	if len(codes) > 0 {
		freeBody := map[string]any{
			"PackageCodes": codes,
			"PageNumber":   1,
			"PageSize":     200,
			"Status":       []int{0},
		}
		if env, err := p.callBilling(acc, "", p.eps.ResourceFree, freeBody); err == nil && env.Code == 0 {
			packages = append(packages, parsePackages(env.Data)...)
		}

		paidBody := map[string]any{
			"PackageCodes":  codes,
			"PageNumber":    1,
			"PageSize":      200,
			"Status":        []int{0, 3},
			"NeedRenewInfo": true,
		}
		if env, err := p.callBilling(acc, "", p.eps.ResourcePaid, paidBody); err == nil && env.Code == 0 {
			packages = append(packages, parsePackages(env.Data)...)
		}
	}

	// 剩余额度 = 各资源包剩余之和；套餐名取第一个有名字的包
	total := 0.0
	for _, pkg := range packages {
		total += pkg.Remain
		if updated.Plan == "" && pkg.Name != "" {
			updated.Plan = pkg.Name
		}
	}
	if len(packages) > 0 {
		updated.Credits = total
	}
	updated.CreditsAt = time.Now().UnixMilli()

	msg := fmt.Sprintf("额度已刷新：%s", formatCredits(updated.Credits))
	if updated.Plan != "" {
		msg = fmt.Sprintf("额度已刷新：%s（%s）", formatCredits(updated.Credits), updated.Plan)
	}
	return updated, packages, msg, nil
}

func parsePackages(raw json.RawMessage) []quotaPackage {
	data := decodeData(raw)
	accounts, _ := data["Accounts"].([]any)
	out := make([]quotaPackage, 0, len(accounts))
	for _, item := range accounts {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		size := pickFloat(m, "CycleCapacitySizePrecise", "CycleCapacitySize", "CapacitySizePrecise", "CapacitySize")
		remain := pickFloat(m, "CycleCapacityRemainPrecise", "CycleCapacityRemain", "CapacityRemainPrecise", "CapacityRemain")
		used := pickFloat(m, "CycleCapacityUsedPrecise", "CycleCapacityUsed", "CapacityUsedPrecise", "CapacityUsed")
		// 上游有时只给其中两个字段，按总量补齐第三个
		if size > 0 && used <= 0 && remain <= size {
			used = max(0, size-remain)
		} else if size > 0 && remain <= 0 && used < size {
			remain = max(0, size-used)
		}
		out = append(out, quotaPackage{
			Name:    firstNonEmpty(asString(m["PackageName"]), "资源包"),
			Code:    asString(m["PackageCode"]),
			Product: asString(m["ProductName"]),
			Unit:    firstNonEmpty(asString(m["CapacityUnit"]), "credits"),
			Remain:  remain,
			Used:    used,
			Size:    size,
			Expire:  firstNonEmpty(asString(m["CycleEndTime"]), asString(m["ExpiredTime"])),
		})
	}
	return out
}

func pickFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			if f := toFloat(v); f != 0 {
				return f
			}
		}
	}
	return 0
}

// ---------- 续期 ----------

// ensureToken 保证账号有可用的 accessToken。
//
// 关键约束：从桌面端导入的账号**不主动刷新**——refreshToken 是独占的，
// 我们一刷，桌面端就被踢下线；OAuth 登录的账号自己刷。
func (p *Plugin) ensureToken(acc *Account) error {
	if acc.tokenAlive() {
		return nil
	}
	if acc.Source == "desktop" {
		return fmt.Errorf("登录态已过期：请打开一次桌面端让它自动刷新，然后重新导入")
	}
	if acc.RefreshToken == "" {
		return fmt.Errorf("登录态已过期且没有 refreshToken，请重新授权登录")
	}
	return p.refreshToken(acc)
}

func (p *Plugin) refreshToken(acc *Account) error {
	cfg := p.realmCfg(acc.Site)
	h := billingHeaders(acc, cfg)
	h["X-Refresh-Token"] = acc.RefreshToken
	h["X-Auth-Refresh-Source"] = "plugin"

	env, _, err := p.doJSON(http.MethodPost, cfg.ChatOrigin+p.eps.TokenRefresh, nil, h)
	if err != nil {
		return err
	}
	if env.Code != 0 {
		return fmt.Errorf("续期失败：%s", firstNonEmpty(env.Msg, fmt.Sprintf("code=%d", env.Code)))
	}

	data := decodeData(env.Data)
	access := asString(data["accessToken"])
	if access == "" {
		return fmt.Errorf("续期响应里没有 accessToken")
	}
	now := time.Now().UnixMilli()
	acc.AccessToken = access
	if v := asString(data["refreshToken"]); v != "" {
		acc.RefreshToken = v
	}
	if v := toFloat(data["expiresIn"]); v > 0 {
		acc.TokenExpiresAt = now + int64(v)*1000
	}
	if v := toFloat(data["refreshExpiresIn"]); v > 0 {
		acc.RefreshExpiresAt = now + int64(v)*1000
	}
	_, _ = p.store.update(acc.ID, func(a *Account) {
		a.AccessToken = acc.AccessToken
		a.RefreshToken = acc.RefreshToken
		a.TokenExpiresAt = acc.TokenExpiresAt
		a.RefreshExpiresAt = acc.RefreshExpiresAt
	})
	return nil
}

// ---------- 工具 ----------

func decodeData(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%g", &f); err == nil {
			return f
		}
	}
	return 0
}

func keyMatches(key string, names ...string) bool {
	norm := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	for _, n := range names {
		if norm == strings.ToLower(n) {
			return true
		}
	}
	return false
}

// deepFind 按 key 名在嵌套 JSON 里找第一个匹配值；遍历顺序固定，结果可复现。
func deepFind(m map[string]any, pred func(key string, v any) bool) (any, bool) {
	if len(m) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if pred(k, m[k]) {
			return m[k], true
		}
	}
	for _, k := range keys {
		switch t := m[k].(type) {
		case map[string]any:
			if got, ok := deepFind(t, pred); ok {
				return got, true
			}
		case []any:
			for _, item := range t {
				if mm, ok := item.(map[string]any); ok {
					if got, ok := deepFind(mm, pred); ok {
						return got, true
					}
				}
			}
		}
	}
	return nil, false
}

func formatCredits(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}