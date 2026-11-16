package admin

import (
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
}

// adminMCPState 返回管理 MCP 的当前状态与两档令牌的情况。
//
// 令牌明文不落库，因此这里只能返回「是否已生成」；
// 明文仅在开关打开或重置令牌的响应中出现一次。
func (a *API) adminMCPState(w http.ResponseWriter, r *http.Request) {
	if a.adminMCP == nil {
		jsonErr(w, http.StatusServiceUnavailable, "管理 MCP 未初始化")
		return
	}
	cfg := a.adminMCP.Config()

	// 前端需要知道两个档位各自的工具数量，便于说明「这枚令牌能做什么」
	names := toolNames(a.adminMCP.ListTools(true))
	readNames := toolNames(a.adminMCP.ListTools(false))

	jsonOK(w, map[string]any{
		"enabled":             cfg.Enabled(),
		"endpoint":            "/admin/mcp",
		"access_url":          a.adminMCPAccessURL(r),
		"tool_count":          len(names),
		"readonly_tool_count": len(readNames),
		"has_readonly_token":  cfg.HasReadonlyToken(),
		"has_writable_token":  cfg.HasWritableToken(),
		"tools":               names,
	})
}

// toolNames 提取工具定义里的名称，供面板展示。
func toolNames(tools []map[string]any) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if n, ok := t["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
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

// saveAdminMCP 保存开关；首次启用时自动补齐两档令牌并返回明文。
//
// 立即生效：更新的是 adminmcp.Config 的内存状态，路由始终挂载，
// 由 Config.Enabled() 决定是否响应。
func (a *API) saveAdminMCP(w http.ResponseWriter, r *http.Request) {
	if a.adminMCP == nil {
		jsonErr(w, http.StatusServiceUnavailable, "管理 MCP 未初始化")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body) {
		return
	}

	// 开关先落库，令牌随后生成；两步都成功才算保存成功
	if err := a.store.SetSetting(store.SettingAdminMCPEnabled, boolText(body.Enabled)); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.adminMCP.Config().Update(body.Enabled)

	resp := map[string]any{"success": true, "enabled": body.Enabled}

	if body.Enabled {
		readToken, writeToken, err := a.issueAdminMCPTokens(false)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if readToken != "" || writeToken != "" {
			resp["readonly_token"] = readToken
			resp["writable_token"] = writeToken
			resp["notice"] = "令牌明文只显示这一次，请立即复制保存"
		}
	}
	jsonOK(w, resp)
}

// issueAdminMCPTokens 补齐两档令牌：regenerate 为 false 时只生成缺失的那一档。
// 返回本次新生成的明文（未生成的为空串）；对应档位的旧令牌在覆盖后立即失效。
func (a *API) issueAdminMCPTokens(regenerate bool) (readToken, writeToken string, err error) {
	if a.adminMCP == nil {
		return "", "", fmt.Errorf("管理 MCP 未初始化")
	}
	cfg := a.adminMCP.Config()

	if regenerate || !cfg.HasReadonlyToken() {
		plain := adminmcp.TokenPrefix + id.Hex(32)
		hash := adminmcp.HashToken(plain)
		if err := a.store.SetSetting(store.SettingAdminMCPReadOnlyTokenHash, hash); err != nil {
			return "", "", err
		}
		cfg.SetReadonlyTokenHash(hash)
		readToken = plain
	}
	if regenerate || !cfg.HasWritableToken() {
		plain := adminmcp.TokenPrefix + id.Hex(32)
		hash := adminmcp.HashToken(plain)
		if err := a.store.SetSetting(store.SettingAdminMCPTokenHash, hash); err != nil {
			return "", "", err
		}
		cfg.SetWritableTokenHash(hash)
		writeToken = plain
	}
	// 旧版「允许写操作」开关已由令牌档位取代
	_ = a.store.DeleteSetting(store.SettingAdminMCPWritable)
	return readToken, writeToken, nil
}

// resetAdminMCPToken 重新生成两档令牌，返回明文；旧令牌立即失效。
func (a *API) resetAdminMCPToken() (readToken, writeToken string, err error) {
	return a.issueAdminMCPTokens(true)
}

// resetAdminMCPTokenAPI 面板「重置令牌」按钮的入口。
func (a *API) resetAdminMCPTokenAPI(w http.ResponseWriter, r *http.Request) {
	if a.adminMCP == nil {
		jsonErr(w, http.StatusServiceUnavailable, "管理 MCP 未初始化")
		return
	}
	readToken, writeToken, err := a.resetAdminMCPToken()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"success":        true,
		"readonly_token": readToken,
		"writable_token": writeToken,
		"notice":         "旧令牌已失效；新令牌明文只显示这一次",
	})
}

// boolText 把布尔值转为落库形式 "1"/"0"。
func boolText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
