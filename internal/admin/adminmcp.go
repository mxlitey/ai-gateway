package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/adminmcp"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// SetAdminMCP 注入管理 MCP 注册表（由 app 层在构造后调用）。
func (a *API) SetAdminMCP(srv *adminmcp.Server) {
	a.adminMCP = srv
	// 重置令牌需要落库，这里把生成逻辑注入给注册表，避免 adminmcp 反向依赖 store 细节
	srv.SetTokenIssuer(a.resetAdminMCPToken)
}

// adminMCPState 返回管理 MCP 的当前状态与可复制的客户端配置。
//
// 令牌明文不落库，因此这里只能返回「是否已生成」；
// 明文仅在开关打开或重置令牌的响应中出现一次。
func (a *API) adminMCPState(w http.ResponseWriter, r *http.Request) {
	if a.adminMCP == nil {
		jsonErr(w, http.StatusServiceUnavailable, "管理 MCP 未初始化")
		return
	}
	cfg := a.adminMCP.Config()

	// 前端需要知道所有可见工具的清单，便于说明「开启后 AI 能做什么」
	names := make([]string, 0, 16)
	for _, t := range a.adminMCP.ListTools() {
		if n, ok := t["name"].(string); ok {
			names = append(names, n)
		}
	}

	jsonOK(w, map[string]any{
		"enabled":    cfg.Enabled(),
		"writable":   cfg.Writable(),
		"has_token":  cfg.HasToken(),
		"endpoint":   "/admin/mcp",
		"access_url": a.adminMCPAccessURL(r),
		"tool_count": len(names),
		"tools":      names,
	})
}

// adminMCPAccessURL 返回客户端应填写的基础地址：按本次管理请求的 Host 推导，
// 这样反代 / 隧道下复制出的配置直接就是外部域名。
func (a *API) adminMCPAccessURL(r *http.Request) string {
	if base := requestBase(r); base != "" {
		return base + "/admin/mcp"
	}
	if cfg := a.cfg; cfg != nil {
		return fmt.Sprintf("http://127.0.0.1:%d/admin/mcp", cfg.Port)
	}
	return "/admin/mcp"
}

// requestBase 按请求的 Host 与转发头推导面板自身的访问基址；无法推导时返回空串。
func requestBase(r *http.Request) string {
	if r == nil {
		return ""
	}
	proto := "http"
	if v := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); v == "https" || v == "http" {
		proto = v
	} else if r.TLS != nil {
		proto = "https"
	}
	host := firstHeaderValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t/\\?#@") {
		return ""
	}
	return proto + "://" + host
}

// firstHeaderValue 取逗号分隔转发头的第一项并转小写。
func firstHeaderValue(v string) string {
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// saveAdminMCP 保存开关与写权限；首次启用时自动生成令牌并返回明文。
//
// 立即生效：更新的是 adminmcp.Config 的内存状态，路由始终挂载，
// 由 Config.Enabled() 决定是否响应。
func (a *API) saveAdminMCP(w http.ResponseWriter, r *http.Request) {
	if a.adminMCP == nil {
		jsonErr(w, http.StatusServiceUnavailable, "管理 MCP 未初始化")
		return
	}
	var body struct {
		Enabled  bool `json:"enabled"`
		Writable bool `json:"writable"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	cfg := a.adminMCP.Config()
	needToken := body.Enabled && !cfg.HasToken()

	// 开关先落库，令牌随后生成；两步都成功才算保存成功
	if err := a.store.SetSetting(store.SettingAdminMCPEnabled, boolText(body.Enabled)); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.store.SetSetting(store.SettingAdminMCPWritable, boolText(body.Writable)); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg.Update(body.Enabled, body.Writable)

	resp := map[string]any{"success": true, "enabled": body.Enabled, "writable": body.Writable}

	if needToken {
		token, err := a.resetAdminMCPToken()
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["token"] = token
		resp["config_json"] = a.buildMCPConfigJSON(r, token)
		resp["notice"] = "令牌明文只显示这一次，请立即复制保存"
	}
	jsonOK(w, resp)
}

// resetAdminMCPToken 生成新令牌，落库其摘要并返回明文。
// 旧令牌立即失效（摘要被覆盖）。
func (a *API) resetAdminMCPToken() (string, error) {
	if a.adminMCP == nil {
		return "", fmt.Errorf("管理 MCP 未初始化")
	}
	token := adminmcp.TokenPrefix + id.Hex(32)
	hash := adminmcp.HashToken(token)
	if err := a.store.SetSetting(store.SettingAdminMCPTokenHash, hash); err != nil {
		return "", err
	}
	a.adminMCP.Config().SetTokenHash(hash)
	return token, nil
}

// resetAdminMCPTokenAPI 面板「重置令牌」按钮的入口。
func (a *API) resetAdminMCPTokenAPI(w http.ResponseWriter, r *http.Request) {
	if a.adminMCP == nil {
		jsonErr(w, http.StatusServiceUnavailable, "管理 MCP 未初始化")
		return
	}
	token, err := a.resetAdminMCPToken()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"success":     true,
		"token":       token,
		"config_json": a.buildMCPConfigJSON(r, token),
		"notice":      "旧令牌已失效；新令牌明文只显示这一次",
	})
}

// buildMCPConfigJSON 生成通用的 mcpServers 配置片段，带鉴权头。
func (a *API) buildMCPConfigJSON(r *http.Request, token string) string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"ai-gateway-admin": map[string]any{
				"type": "http",
				"url":  a.adminMCPAccessURL(r),
				"headers": map[string]string{
					"Authorization": "Bearer " + token,
				},
			},
		},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}

// boolText 把布尔值转为落库形式 "1"/"0"。
func boolText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
