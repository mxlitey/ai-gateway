package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/mcp"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// SetMCP 注入 MCP 聚合服务（供服务器探测、工具刷新与用量统计复用）。
func (a *API) SetMCP(svc *mcp.Service) { a.mcp = svc }

// mcpInput 新增 / 编辑 MCP 服务器的入参。
type mcpInput struct {
	Name        string         `json:"name"`
	Alias       string         `json:"alias"`
	URL         string         `json:"url"`
	Headers     []model.Header `json:"headers"`
	Keys        []model.MCPKey `json:"keys"`
	KeyStrategy string         `json:"key_strategy"`
	DailyLimit  int            `json:"daily_limit"`
	ToolLimits  map[string]int `json:"tool_limits"`
	// BillableMode / BillableMarker 决定哪些调用计入次数，用于与上游控制台对齐。
	BillableMode   string `json:"billable_mode"`
	BillableMarker string `json:"billable_marker"`
	TimeoutMS      int    `json:"timeout_ms"`
	Enabled        *bool  `json:"enabled"`
}

// mcpSubroutes 分发 /admin/api/mcp/* 下的请求。
func (a *API) mcpSubroutes(w http.ResponseWriter, r *http.Request, path, method string) {
	switch {
	case path == "/mcp/servers" && method == http.MethodGet:
		a.listMCPServers(w)
	case path == "/mcp/servers" && method == http.MethodPost:
		a.createMCPServer(w, r)
	case path == "/mcp/tools" && method == http.MethodGet:
		a.listMCPTools(w, r)
	case path == "/mcp/tools/call" && method == http.MethodPost:
		a.callMCPTool(w, r)
	case path == "/mcp/usage/summary" && method == http.MethodGet:
		a.mcpUsageSummary(w, r)
	case path == "/mcp/calls" && method == http.MethodGet:
		a.mcpCallLogs(w, r)
	case path == "/mcp/calls" && method == http.MethodDelete:
		a.cleanupMCPCalls(w, r)
	case strings.HasPrefix(path, "/mcp/servers/"):
		a.mcpServerSubroutes(w, r, strings.TrimPrefix(path, "/mcp/servers/"), method)
	default:
		jsonErr(w, http.StatusNotFound, "Not found")
	}
}

// mcpServerSubroutes 分发单台服务器的子操作。
func (a *API) mcpServerSubroutes(w http.ResponseWriter, r *http.Request, rest, method string) {
	switch {
	case strings.HasSuffix(rest, "/toggle") && method == http.MethodPatch:
		a.toggleMCPServer(w, strings.TrimSuffix(rest, "/toggle"))
		return
	case strings.HasSuffix(rest, "/refresh") && method == http.MethodPost:
		a.refreshMCPServer(w, r, strings.TrimSuffix(rest, "/refresh"))
		return
	case strings.HasSuffix(rest, "/test") && method == http.MethodPost:
		a.testMCPServer(w, r, strings.TrimSuffix(rest, "/test"))
		return
	}
	if rest == "" || strings.Contains(rest, "/") {
		jsonErr(w, http.StatusNotFound, "Not found")
		return
	}

	switch method {
	case http.MethodPut:
		a.updateMCPServer(w, r, rest)
	case http.MethodDelete:
		if err := a.store.DeleteMCPServer(rest); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if a.mcp != nil {
			a.mcp.RebuildRoutes()
			a.mcp.ForgetTools(rest)
		}
		jsonOK(w, map[string]any{"success": true})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// listMCPServers 返回服务器列表，附带工具数、今日调用数与运行时状态。
func (a *API) listMCPServers(w http.ResponseWriter) {
	servers, err := a.store.ListMCPServers()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := a.store.CountMCPToolsByServer()
	if err != nil {
		counts = map[string]int{}
	}

	out := make([]model.MCPServer, 0, len(servers))
	for i := range servers {
		s := servers[i]
		s.ToolCount = counts[s.ID]
		if a.mcp != nil {
			s.LastStatus, s.LastError = a.mcp.State(s.ID)
			s.DailyCalls = a.mcp.DailyCalls(s.ID)
			s.KeyDailyCalls = a.mcp.DailyCallsByKey(s.ID)
			s.ToolDailyCalls = a.mcp.DailyCallsByTool(s.ID)
		}
		total := int64(0)
		for _, k := range s.Keys {
			if k.DailyLimit > 0 {
				total += int64(k.DailyLimit)
			}
		}
		s.DailyLimitTotal = total
		out = append(out, s)
	}
	jsonOK(w, out)
}

func (a *API) createMCPServer(w http.ResponseWriter, r *http.Request) {
	var in mcpInput
	if !decodeBody(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	url := strings.TrimSpace(in.URL)
	if name == "" || url == "" {
		jsonErr(w, http.StatusBadRequest, "name and url are required")
		return
	}
	alias, err := a.resolveAlias(in.Alias, name, "")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	order, err := a.store.NextMCPServerSortOrder()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	billableMode, billableMarker := sanitizeBillable(in.BillableMode, in.BillableMarker)
	srv := &model.MCPServer{
		ID:             id.UUID(),
		Name:           name,
		Alias:          alias,
		URL:            url,
		Headers:        channel.NormalizeHeaders(in.Headers),
		Keys:           sanitizeMCPKeys(in.Keys),
		KeyStrategy:    model.NormalizeKeyStrategy(in.KeyStrategy),
		DailyLimit:     clampLimit(in.DailyLimit),
		ToolLimits:     sanitizeToolLimits(in.ToolLimits),
		BillableMode:   billableMode,
		BillableMarker: billableMarker,
		TimeoutMS:      normalizeTimeout(in.TimeoutMS),
		Enabled:        in.Enabled == nil || *in.Enabled,
		Source:         "manual",
		SortOrder:      order,
		CreatedAt:      time.Now().UnixMilli(),
	}
	if err := a.store.CreateMCPServer(srv); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 新建后异步拉一次工具，失败不影响创建结果（面板会显示错误）
	a.refreshMCPAsync(srv.ID)
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, srv)
}

func (a *API) updateMCPServer(w http.ResponseWriter, r *http.Request, serverID string) {
	existing, err := a.store.GetMCPServer(serverID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		jsonErr(w, http.StatusNotFound, "MCP server not found")
		return
	}

	var in mcpInput
	if !decodeBody(w, r, &in) {
		return
	}

	next := *existing
	if s := strings.TrimSpace(in.Name); s != "" {
		next.Name = s
	}
	alias, err := a.resolveAlias(in.Alias, next.Name, existing.ID)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	next.Alias = alias
	if s := strings.TrimSpace(in.URL); s != "" {
		next.URL = s
	}
	if in.Headers != nil {
		next.Headers = channel.NormalizeHeaders(in.Headers)
	}
	if in.Keys != nil {
		next.Keys = mergeMCPKeys(existing.Keys, in.Keys)
	}
	if s := strings.TrimSpace(in.KeyStrategy); s != "" {
		next.KeyStrategy = model.NormalizeKeyStrategy(s)
	}
	next.DailyLimit = clampLimit(in.DailyLimit)
	next.ToolLimits = sanitizeToolLimits(in.ToolLimits)
	next.BillableMode, next.BillableMarker = sanitizeBillable(in.BillableMode, in.BillableMarker)
	next.TimeoutMS = normalizeTimeout(in.TimeoutMS)
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}

	if err := a.store.UpdateMCPServer(&next); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.refreshMCPAsync(next.ID)
	jsonOK(w, next)
}

func (a *API) toggleMCPServer(w http.ResponseWriter, serverID string) {
	srv, err := a.store.GetMCPServer(serverID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if srv == nil {
		jsonErr(w, http.StatusNotFound, "MCP server not found")
		return
	}
	srv.Enabled = !srv.Enabled
	if err := a.store.UpdateMCPServer(srv); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if srv.Enabled {
		a.refreshMCPAsync(srv.ID)
	}
	jsonOK(w, srv)
}

// refreshMCPServer 重新拉取该服务器的工具列表（面板「刷新工具」按钮）。
func (a *API) refreshMCPServer(w http.ResponseWriter, r *http.Request, serverID string) {
	if a.mcp == nil {
		jsonErr(w, http.StatusServiceUnavailable, "MCP 服务未启用")
		return
	}
	srv, err := a.store.GetMCPServer(serverID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if srv == nil {
		jsonErr(w, http.StatusNotFound, "MCP server not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := a.mcp.RefreshTools(ctx, srv); err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	tools, _ := a.store.ListMCPTools()
	count := 0
	for _, t := range tools {
		if t.ServerID == serverID {
			count++
		}
	}
	jsonOK(w, map[string]any{"success": true, "tools": count})
}

// testMCPServer 探测服务器是否可用。
func (a *API) testMCPServer(w http.ResponseWriter, r *http.Request, serverID string) {
	if a.mcp == nil {
		jsonErr(w, http.StatusServiceUnavailable, "MCP 服务未启用")
		return
	}
	srv, err := a.store.GetMCPServer(serverID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if srv == nil {
		jsonErr(w, http.StatusNotFound, "MCP server not found")
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := a.mcp.Probe(ctx, srv); err != nil {
		jsonOK(w, map[string]any{
			"success":     false,
			"error":       err.Error(),
			"duration_ms": time.Since(start).Milliseconds(),
		})
		return
	}
	jsonOK(w, map[string]any{"success": true, "duration_ms": time.Since(start).Milliseconds()})
}

// refreshMCPAsync 异步刷新工具，避免拖慢保存接口。
func (a *API) refreshMCPAsync(serverID string) {
	if a.mcp == nil {
		return
	}
	srv, err := a.store.GetMCPServer(serverID)
	if err != nil || srv == nil || !srv.Enabled {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = a.mcp.RefreshTools(ctx, srv)
	}()
}

// listMCPTools 返回工具缓存；可按 server_id 过滤。
func (a *API) listMCPTools(w http.ResponseWriter, r *http.Request) {
	tools, err := a.store.ListMCPTools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	serverID := strings.TrimSpace(r.URL.Query().Get("server_id"))

	type item struct {
		ServerID    string          `json:"server_id"`
		ExposedName string          `json:"exposed_name"`
		Tool        json.RawMessage `json:"tool"`
		UpdatedAt   int64           `json:"updated_at"`
	}
	out := make([]item, 0, len(tools))
	for _, t := range tools {
		if serverID != "" && t.ServerID != serverID {
			continue
		}
		raw := json.RawMessage(t.Raw)
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		out = append(out, item{
			ServerID:    t.ServerID,
			ExposedName: t.ExposedName,
			Tool:        raw,
			UpdatedAt:   t.UpdatedAt,
		})
	}
	jsonOK(w, out)
}

// callMCPTool 面板上的「试调用」：按对外工具名直接走一次真实调用。
func (a *API) callMCPTool(w http.ResponseWriter, r *http.Request) {
	if a.mcp == nil {
		jsonErr(w, http.StatusServiceUnavailable, "MCP 服务未启用")
		return
	}
	var body struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	args := body.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var argMap map[string]any
	if err := json.Unmarshal(args, &argMap); err != nil {
		jsonErr(w, http.StatusBadRequest, "arguments 必须是 JSON 对象")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	result, err := a.mcp.CallExposed(ctx, name, argMap)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	jsonOK(w, result)
}

// mcpUsageSummary 调用次数统计（按服务器 / 工具 / 日期）。
func (a *API) mcpUsageSummary(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRangeParams(r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sum, err := a.store.MCPUsageSummary(from, to)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, sum)
}

// mcpCallLogs 调用明细。
func (a *API) mcpCallLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, err := parseRangeParams(r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.MCPCallLogFilter{
		FromMs:   from,
		ToMs:     to,
		ServerID: q.Get("server_id"),
		Tool:     q.Get("tool"),
		OnlyFail: q.Get("only_fail") == "1",
		Limit:    limit,
		Offset:   offset,
	}

	items, err := a.store.ListMCPCallLogs(f)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := a.store.CountMCPCallLogs(f)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"items": items,
		"total": total,
		"from":  from,
		"to":    to,
	})
}

// cleanupMCPCalls 清理调用日志：days 表示保留最近多少天，0 表示全部清理。
func (a *API) cleanupMCPCalls(w http.ResponseWriter, r *http.Request) {
	days, err := parseDaysParam(r.URL.Query().Get("days"), manualCleanupDays)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	before := time.Now().AddDate(0, 0, -days).UnixMilli()
	if days == 0 {
		before = time.Now().Add(time.Minute).UnixMilli()
	}
	n, err := a.store.CleanupMCPCallLogsBefore(before)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonOK(w, map[string]any{"success": true, "removed": n, "days": days})
}

// ---------- 工具函数 ----------

// resolveAlias 校验并返回工具命名空间前缀；留空时由 name 推导。
func (a *API) resolveAlias(input, name, selfID string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		raw = name
	}
	alias := mcp.NormalizeAlias(raw)
	if alias == "" {
		return "", errInvalidAlias
	}
	servers, err := a.store.ListMCPServers()
	if err != nil {
		return "", err
	}
	for _, s := range servers {
		if s.ID == selfID {
			continue
		}
		if strings.EqualFold(s.Alias, alias) {
			return "", errAliasConflict
		}
	}
	return alias, nil
}

var (
	errInvalidAlias  = errors.New("解析不出合法的工具命名空间前缀，请改用字母、数字、- 或 _")
	errAliasConflict = errors.New("工具命名空间前缀已被其他服务器占用")
)

// sanitizeMCPKeys 清理新增时的密钥列表。
func sanitizeMCPKeys(keys []model.MCPKey) []model.MCPKey {
	out := make([]model.MCPKey, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(k.Key)
		if v == "" {
			continue
		}
		out = append(out, model.MCPKey{
			Key:        v,
			Label:      strings.TrimSpace(k.Label),
			Enabled:    k.Enabled,
			DailyLimit: clampLimit(k.DailyLimit),
		})
	}
	return out
}

// mergeMCPKeys 合并编辑提交的密钥：等于脱敏值时保留原值。
func mergeMCPKeys(existing, incoming []model.MCPKey) []model.MCPKey {
	byMask := map[string]string{}
	for _, k := range existing {
		byMask[model.MaskKey(k.Key)] = k.Key
	}
	out := make([]model.MCPKey, 0, len(incoming))
	for _, k := range incoming {
		v := strings.TrimSpace(k.Key)
		if strings.Contains(v, "...") {
			if real, ok := byMask[v]; ok {
				v = real
			}
		}
		if v == "" {
			continue
		}
		out = append(out, model.MCPKey{
			Key:        v,
			Label:      strings.TrimSpace(k.Label),
			Enabled:    k.Enabled,
			DailyLimit: clampLimit(k.DailyLimit),
		})
	}
	return out
}

// clampLimit 把每日调用上限收敛到合理区间：-1 表示不限，0 表示不允许调用。
func clampLimit(n int) int {
	if n < 0 {
		return -1
	}
	if n > 1000000 {
		return 1000000
	}
	return n
}

// sanitizeToolLimits 清理工具级每日上限：丢弃空工具名；-1（不限）与「未配置」等价，不落库。
func sanitizeToolLimits(in map[string]int) map[string]int {
	out := map[string]int{}
	for tool, n := range in {
		name := strings.TrimSpace(tool)
		if name == "" {
			continue
		}
		if v := clampLimit(n); v >= 0 {
			out[name] = v
		}
	}
	return out
}

// sanitizeBillable 归一化计费口径；marker 模式缺少标记时退化为「成功即计费」，
// 避免配了个永远不计费的服务器。
func sanitizeBillable(mode, marker string) (string, string) {
	m := model.NormalizeBillableMode(mode)
	s := strings.TrimSpace(marker)
	if m == model.MCPBillableMarker && s == "" {
		return model.MCPBillableSuccess, ""
	}
	return m, s
}

// normalizeTimeout 单次调用超时：默认 120s，上限 30 分钟。
func normalizeTimeout(ms int) int {
	if ms <= 0 {
		return 120000
	}
	if ms < 5000 {
		return 5000
	}
	if ms > 1800000 {
		return 1800000
	}
	return ms
}

// parseRangeParams 解析时间区间：from/to（毫秒）优先，其次 date，最后 days。
func parseRangeParams(r *http.Request) (int64, int64, error) {
	q := r.URL.Query()
	if fromS, toS := q.Get("from"), q.Get("to"); fromS != "" && toS != "" {
		from, err1 := strconv.ParseInt(fromS, 10, 64)
		to, err2 := strconv.ParseInt(toS, 10, 64)
		if err1 != nil || err2 != nil || to <= from {
			return 0, 0, errors.New("from / to 非法")
		}
		return from, to, nil
	}
	if d := q.Get("date"); d != "" {
		day, err := time.ParseInLocation("2006-01-02", d, time.Local)
		if err != nil {
			return 0, 0, errors.New("date 需为 YYYY-MM-DD")
		}
		return day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli(), nil
	}
	days := 7
	if s := q.Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || n > 365 {
			return 0, 0, errors.New("days 需为 1-365")
		}
		days = n
	}
	now := time.Now()
	end := now.UnixMilli()
	start := now.AddDate(0, 0, -days+1)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, start.Location())
	return start.UnixMilli(), end, nil
}
