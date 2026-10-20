// Package mcp 实现 MCP（Model Context Protocol）上游聚合。
//
// 网关对外是一个 MCP 服务器（POST /mcp），对内是一个 MCP 客户端：
// 把多台 Streamable HTTP 上游的工具合并成一份列表，并把 tools/call 路由到正确的服务器。
package mcp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 错误码。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// 网关自定义：上游全部不可用。
	CodeNoUpstream = -32001
	// 网关自定义：调用次数配额已用尽。
	CodeQuotaExceeded = -32002
)

// 对外声明支持的协议版本，按新到旧排列。
var supportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// defaultVersion 是客户端未声明或声明了未知版本时回退的版本。
const defaultVersion = "2025-06-18"

// request 一条 JSON-RPC 请求或通知（通知没有 id）。
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// hasID 判断是否为请求（需要回响应）而非通知。
func (r request) hasID() bool {
	return len(r.ID) > 0 && string(r.ID) != "null"
}

// rpcError JSON-RPC 错误对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// response JSON-RPC 响应。
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// envelope 用于从上游响应中解析出 id 与 result/error。
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// initializeResult 上游 initialize 的响应结果（只取网关用得到的字段）。
type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// toolDef 上游 tools/list 返回的单个工具；保留 Raw 以便原样透传其余字段。
type toolDef struct {
	Name string `json:"name"`
	Raw  json.RawMessage
}

// upstreamError 上游调用失败：区分「换个密钥可能就好了」与「换谁来都一样」。
type upstreamError struct {
	Status    int    // HTTP 状态码，0 表示连接失败
	Code      int    // JSON-RPC 错误码，0 表示没有
	Message   string
	Retryable bool
}

func (e *upstreamError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
	}
	return e.Message
}
