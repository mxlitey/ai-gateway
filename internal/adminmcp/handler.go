package adminmcp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// maxRequestBody 单次请求体上限。
const maxRequestBody = 1 << 20

// Handler 处理 POST /admin/mcp。
//
// 与 /mcp 聚合入口完全独立：不复用密钥校验、不读取 MCP 服务器表、
// 不参与工具路由，只调用本进程注册的管理工具。
type Handler struct {
	srv *Server
}

// NewHandler 创建管理 MCP 入口。
func NewHandler(srv *Server) *Handler { return &Handler{srv: srv} }

// Handle 处理一条 JSON-RPC 请求。
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 未启用时直接 404，避免暴露该端点的存在
	if !h.srv.cfg.Enabled() {
		http.NotFound(w, r)
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

	// 通知无 id，不需要响应
	if !req.hasID() {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if !h.srv.cfg.Authorize(r.Header.Get("Authorization")) {
		// JSON-RPC 错误统一走 200，避免客户端把它当成传输层故障
		writeRPCError(w, req.ID, CodeInvalidRequest, "Unauthorized: 管理令牌无效或服务未启用")
		return
	}

	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req)
	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": h.srv.ListTools()})
	case "tools/call":
		h.handleToolsCall(w, req)
	default:
		writeRPCError(w, req.ID, CodeMethodNotFound, "管理网关未实现该方法: "+req.Method)
	}
}

// handleInitialize 协商协议版本并声明能力。
func (h *Handler) handleInitialize(w http.ResponseWriter, req request) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &params)

	writeRPCResult(w, req.ID, map[string]any{
		"protocolVersion": negotiateVersion(params.ProtocolVersion),
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo": map[string]any{
			"name":    "ai-gateway-admin",
			"version": "1.0.0",
		},
	})
}

// handleToolsCall 校验参数并执行工具。
func (h *Handler) handleToolsCall(w http.ResponseWriter, req request) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPCError(w, req.ID, CodeInvalidParams, "参数不合法")
		return
	}
	if strings.TrimSpace(params.Name) == "" {
		writeRPCError(w, req.ID, CodeInvalidParams, "缺少工具名")
		return
	}
	writeRPCResult(w, req.ID, h.srv.CallTool(params.Name, params.Arguments))
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
	// JSON-RPC 错误仍走 200，由 error 字段表达
	w.WriteHeader(http.StatusOK)
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
