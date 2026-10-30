// Package adminmcp 提供管理 MCP 的协议层与工具注册表。
//
// 网关把自身的管理能力暴露为一个独立的 MCP 服务器（POST /admin/mcp），
// 与 internal/mcp 的「聚合上游」语义完全分开：这里的工具全部由本进程直接实现，
// 不经过任何上游，也不出现在 /mcp 的工具列表里。
//
// 鉴权独立于业务密钥：使用管理令牌（明文只在生成时展示一次，库里只存 SHA-256 摘要），
// 不接受 sk- 客户端密钥，也不接受面板会话 Token。
package adminmcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
)

// JSON-RPC 2.0 错误码。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// 对外声明支持的协议版本，按新到旧排列。
var supportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const defaultVersion = "2025-06-18"

// TokenPrefix 管理令牌前缀，与业务密钥 sk- 明确区分，避免误用。
const TokenPrefix = "amcp_"

// request 一条 JSON-RPC 请求或通知。
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r request) hasID() bool {
	return len(r.ID) > 0 && string(r.ID) != "null"
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// HashToken 计算管理令牌的摘要（十六进制 SHA-256）。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyToken 以常量时间比较令牌与已存摘要。
func VerifyToken(token, storedHash string) bool {
	if token == "" || storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(storedHash)) == 1
}

// Config 管理 MCP 的运行期状态：开关、写权限、令牌摘要，随面板保存即时切换。
//
// 令牌摘要只存摘要不存明文，因此「查看令牌」只在生成的那一刻可用；
// 之后只能重置（旧令牌立即失效）。
type Config struct {
	mu        sync.RWMutex
	enabled   bool
	writable  bool
	tokenHash string
}

// NewConfig 用库中读到的值构造运行期状态。
func NewConfig(enabled, writable bool, tokenHash string) *Config {
	return &Config{enabled: enabled, writable: writable, tokenHash: strings.TrimSpace(tokenHash)}
}

// Enabled 返回管理 MCP 是否启用。
func (c *Config) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// Writable 返回是否允许写操作（未启用时一律为 false）。
func (c *Config) Writable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.writable && c.enabled
}

// HasToken 判断是否已生成过令牌。
func (c *Config) HasToken() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tokenHash != ""
}

// Update 更新开关与写权限。
func (c *Config) Update(enabled, writable bool) {
	c.mu.Lock()
	c.enabled = enabled
	c.writable = writable
	c.mu.Unlock()
}

// SetTokenHash 更新令牌摘要。
func (c *Config) SetTokenHash(hash string) {
	c.mu.Lock()
	c.tokenHash = strings.TrimSpace(hash)
	c.mu.Unlock()
}

// Authorize 校验请求头里的管理令牌：必须启用、已生成令牌、且摘要匹配。
func (c *Config) Authorize(authorization string) bool {
	if !c.Enabled() {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	c.mu.RLock()
	hash := c.tokenHash
	c.mu.RUnlock()
	return VerifyToken(token, hash)
}

// ToolHandler 工具实现：返回结构化结果（由调用方序列化）或错误。
type ToolHandler func(args map[string]any) (any, error)

// tool 一个已注册的管理工具。
type tool struct {
	name        string
	description string
	schema      map[string]any
	write       bool
	handler     ToolHandler
}

// Server 管理 MCP 的工具注册表与执行入口。
type Server struct {
	cfg        *Config
	tools      map[string]*tool
	order      []string
	tokenIssue func() (string, error)
}

// NewServer 创建注册表。cfg 为运行期状态，所有工具都按它的开关过滤。
func NewServer(cfg *Config) *Server {
	return &Server{cfg: cfg, tools: map[string]*tool{}}
}

// Config 返回运行期状态，供 HTTP 层做鉴权与状态查询。
func (s *Server) Config() *Config { return s.cfg }

// SetTokenIssuer 注册「重置令牌」的生成逻辑（由 admin 包注入，负责落库）。
func (s *Server) SetTokenIssuer(fn func() (string, error)) { s.tokenIssue = fn }

// Tools 注册一个工具。write 为 true 时，仅在「允许写操作」开启后可见可调用。
func (s *Server) Tools(name, description string, schema map[string]any, write bool, h ToolHandler) {
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	s.tools[name] = &tool{name: name, description: description, schema: schema, write: write, handler: h}
	s.order = append(s.order, name)
}

// ListTools 返回当前可见的工具定义（写操作按写权限过滤）。
func (s *Server) ListTools() []map[string]any {
	writable := s.cfg.Writable()
	out := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		if t.write && !writable {
			continue
		}
		out = append(out, map[string]any{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.schema,
		})
	}
	return out
}

// CallTool 执行工具，返回 MCP 的 result 结构（成功或 isError）。
func (s *Server) CallTool(name string, args map[string]any) map[string]any {
	t, ok := s.tools[name]
	if !ok {
		return ErrorResult("未知工具: " + name)
	}
	if t.write && !s.cfg.Writable() {
		return ErrorResult("写操作未开启：请在设置页同时启用「管理 MCP」与「允许写操作」")
	}
	if args == nil {
		args = map[string]any{}
	}
	v, err := t.handler(args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return JSONResult(v)
}

// TextResult 包装纯文本结果。
func TextResult(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
}

// ErrorResult 包装工具级失败（isError=true，仍走 JSON-RPC 成功响应）。
func ErrorResult(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

// JSONResult 把结构化结果序列化为缩进 JSON。
func JSONResult(v any) map[string]any {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ErrorResult("编码结果失败: " + err.Error())
	}
	return TextResult(string(raw))
}

func negotiateVersion(client string) string {
	for _, v := range supportedVersions {
		if v == client {
			return v
		}
	}
	return defaultVersion
}
