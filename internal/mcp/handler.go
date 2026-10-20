package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// maxRequestBody 单次请求体上限。
const maxRequestBody = 8 << 20

// Handler /mcp 入口。
type Handler struct {
	store *store.Store
	svc   *Service
}

// NewHandler 创建 MCP 入口处理器。
func NewHandler(store *store.Store, svc *Service) *Handler {
	return &Handler{store: store, svc: svc}
}

// Service 暴露聚合服务，供管理接口复用。
func (h *Handler) Service() *Service { return h.svc }

// Handle 处理 /mcp 与 /mcp/{alias}。
//
// alias 非空时只聚合该服务器（单服务器直通），用于客户端必须看到原始工具名的场景。
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// 本网关不提供 GET 的 SSE 推送流，按规范返回 405
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, errMsg := h.verify(r); errMsg != "" {
		writeRPCError(w, nil, CodeInvalidRequest, "Unauthorized: "+errMsg)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	_ = r.Body.Close()
	if err != nil {
		writeRPCError(w, nil, CodeParseError, "读取请求体失败")
		return
	}

	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		writeRPCError(w, nil, CodeParseError, "请求体不是合法的 JSON-RPC")
		return
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		writeRPCError(w, req.ID, CodeInvalidRequest, "不是合法的 JSON-RPC 2.0 请求")
		return
	}

	alias := strings.TrimPrefix(r.URL.Path, "/mcp")
	alias = strings.TrimPrefix(alias, "/")

	// 通知没有 id，不需要响应（但仍要处理 initialized 等）
	if !req.hasID() {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req, alias)
	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		h.handleToolsList(w, r, req, alias)
	case "tools/call":
		h.handleToolsCall(w, r, req, alias)
	default:
		// 单服务器模式：未知方法（resources / prompts 等）原样转发
		if alias != "" {
			h.handlePassthrough(w, r, req, alias)
			return
		}
		writeRPCError(w, req.ID, CodeMethodNotFound, "网关未实现该方法: "+req.Method)
	}
}

// verify 校验客户端密钥；返回密钥对象与错误信息。
func (h *Handler) verify(r *http.Request) (*model.APIKey, string) {
	key := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		key = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if key == "" {
		key = r.Header.Get("x-api-key")
	}
	if key == "" {
		return nil, "缺少 API 密钥（Authorization 或 x-api-key）"
	}
	k, err := h.store.FindEnabledAPIKeyByValue(key)
	if err != nil || k == nil {
		return nil, "API 密钥无效或已停用"
	}
	return k, ""
}

// handleInitialize 与客户端协商版本，并声明网关能力。
func (h *Handler) handleInitialize(w http.ResponseWriter, req request, alias string) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &params)

	version := negotiateVersion(params.ProtocolVersion)

	// 只在实际有可用服务器时声明 tools 能力，避免客户端对着空网关反复调用
	caps := map[string]any{}
	if servers, err := h.targets(alias); err == nil && len(servers) > 0 {
		caps["tools"] = map[string]any{"listChanged": false}
	}

	writeRPCResult(w, req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    caps,
		"serverInfo": map[string]any{
			"name":    "ai-gateway-mcp",
			"version": "1.0.0",
		},
	})
}

// negotiateVersion 取交集：客户端声明的版本受支持就用它，否则回退到最新版本。
func negotiateVersion(client string) string {
	if client == "" {
		return defaultVersion
	}
	for _, v := range supportedVersions {
		if v == client {
			return v
		}
	}
	return defaultVersion
}

// handleToolsList 合并所有启用服务器的工具列表。
func (h *Handler) handleToolsList(w http.ResponseWriter, r *http.Request, req request, alias string) {
	servers, err := h.targets(alias)
	if err != nil {
		writeRPCError(w, req.ID, CodeInternalError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	tools := make([]map[string]any, 0, 16)
	seen := map[string]bool{}
	for i := range servers {
		srv := &servers[i]
		defs, err := h.svc.cachedTools(ctx, srv, r)
		if err != nil {
			// 单台不可达不影响整体：跳过，面板上能看到它的错误
			slog.Warn("[mcp] 拉取工具列表失败，跳过该服务器", "server", srv.Name, "err", err)
			continue
		}
		for _, d := range defs {
			name := d.Name
			if alias == "" {
				name = exposedName(serverAlias(srv), d.Name)
			}
			if seen[name] {
				continue // 同一服务器内同名工具或截断撞名，保留第一个
			}
			seen[name] = true

			var item map[string]any
			if err := json.Unmarshal(d.Raw, &item); err != nil {
				continue
			}
			item["name"] = name
			tools = append(tools, item)
		}
	}

	writeRPCResult(w, req.ID, map[string]any{"tools": tools})
}

// handleToolsCall 把对外工具名解析回（服务器, 上游工具名）并转发。
func (h *Handler) handleToolsCall(w http.ResponseWriter, r *http.Request, req request, alias string) {
	var params map[string]any
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, CodeInvalidParams, "参数不合法")
		return
	}
	name, _ := params["name"].(string)
	if strings.TrimSpace(name) == "" {
		writeRPCError(w, req.ID, CodeInvalidParams, "缺少工具名")
		return
	}

	var srv *model.MCPServer
	toolName := name
	if alias == "" {
		route, ok := h.svc.registry.lookup(name)
		if !ok {
			writeRPCError(w, req.ID, CodeInvalidParams,
				fmt.Sprintf("未知工具 %q；请先调用 tools/list 获取可用工具", name))
			return
		}
		s, err := h.store.GetMCPServer(route.ServerID)
		if err != nil || s == nil || !s.Enabled {
			writeRPCError(w, req.ID, CodeNoUpstream, "该工具所属的服务器已不可用")
			return
		}
		srv = s
		toolName = route.Tool
	} else {
		srv = h.findByAlias(alias)
		if srv == nil {
			writeRPCError(w, req.ID, CodeNoUpstream, "服务器不存在: "+alias)
			return
		}
	}

	// 用上游工具名替换对外名，其余参数（arguments / _meta）原样透传
	params["name"] = toolName

	result, err := h.svc.callTool(r.Context(), srv, r, params)
	if err != nil {
		code := codeOf(err)
		var ue *upstreamError
		if errors.As(err, &ue) && ue.Message == errQuotaExhausted {
			code = CodeQuotaExceeded
		}
		writeRPCError(w, req.ID, code, err.Error())
		return
	}
	writeRPCResult(w, req.ID, result)
}

// handlePassthrough 单服务器模式下把任意方法原样转发给上游。
func (h *Handler) handlePassthrough(w http.ResponseWriter, r *http.Request, req request, alias string) {
	srv := h.findByAlias(alias)
	if srv == nil {
		writeRPCError(w, req.ID, CodeNoUpstream, "服务器不存在: "+alias)
		return
	}
	var params any
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeRPCError(w, req.ID, CodeInvalidParams, "参数不合法")
			return
		}
	}

	result, err := h.svc.passthrough(r.Context(), srv, r, req.Method, params)
	if err != nil {
		writeRPCError(w, req.ID, codeOf(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rawResponse(req.ID, result))
}

// targets 返回本次请求涉及的服务器：单服务器模式只返回它，聚合模式返回全部启用的。
func (h *Handler) targets(alias string) ([]model.MCPServer, error) {
	if alias != "" {
		srv := h.findByAlias(alias)
		if srv == nil {
			return nil, fmt.Errorf("服务器不存在: %s", alias)
		}
		return []model.MCPServer{*srv}, nil
	}
	all, err := h.store.ListMCPServers()
	if err != nil {
		return nil, err
	}
	out := make([]model.MCPServer, 0, len(all))
	for i := range all {
		if !all[i].Enabled {
			continue
		}
		out = append(out, all[i])
	}
	return out, nil
}

// findByAlias 按别名（缺省为 id）查找服务器；只返回已启用的服务器。
func (h *Handler) findByAlias(alias string) *model.MCPServer {
	target := strings.ToLower(strings.TrimSpace(alias))
	servers, err := h.store.ListMCPServers()
	if err != nil {
		return nil
	}
	for i := range servers {
		s := &servers[i]
		if !s.Enabled {
			continue
		}
		if strings.ToLower(serverAlias(s)) == target || strings.EqualFold(s.ID, alias) {
			return s
		}
	}
	return nil
}

// ---------- JSON-RPC 输出 ----------

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	out, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"编码响应失败"}}`))
		return
	}
	_, _ = w.Write(out)
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // JSON-RPC 错误仍走 200，由 error 字段表达
	out, err := json.Marshal(response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	})
	if err != nil {
		return
	}
	_, _ = w.Write(out)
}

// rawResponse 把上游的 result 包成带客户端 id 的响应。
func rawResponse(id json.RawMessage, result json.RawMessage) []byte {
	out, err := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: json.RawMessage(result)})
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"编码响应失败"}}`)
	}
	return out
}

func codeOf(err error) int {
	var ue *upstreamError
	if errors.As(err, &ue) {
		if ue.Code != 0 {
			return ue.Code
		}
		return CodeNoUpstream
	}
	return CodeInternalError
}
