package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/model"
)

// sessionHeader Streamable HTTP 的会话标识头。
const sessionHeader = "Mcp-Session-Id"

// maxResponseBytes 单次上游响应体的读取上限，防止异常上游撑爆内存。
const maxResponseBytes = 16 << 20

// client 对一个 MCP 上游的调用封装：无状态，每次调用自带会话 id。
//
// 会话（Mcp-Session-Id）缓存按「服务器 + 密钥」隔离：不同密钥通常对应不同账号，
// 上游可能为它们签发不同会话，混用会串号。
type client struct {
	http *http.Client

	mu       sync.Mutex
	sessions map[string]string // serverID \x00 keyHint -> session id

	// initMu/initialized 记录已完成 initialize 协商的（服务器, 密钥）组合；
	// initing 用于让并发请求只发起一次握手。
	initMu      sync.Mutex
	initialized map[string]bool
	initing     map[string]*initCall
}

func newClient() *client {
	return &client{
		http: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				// 工具调用可能很慢，整体超时由每个请求的 context 控制
				ResponseHeaderTimeout: 120 * time.Second,
				DisableCompression:    true,
				MaxIdleConnsPerHost:   8,
			},
		},
		sessions:    map[string]string{},
		initialized: map[string]bool{},
		initing:     map[string]*initCall{},
	}
}

// sessionKey 生成会话缓存的键。
func sessionKey(serverID, key string) string {
	return serverID + "\x00" + model.KeyHint(key)
}

// dropSession 清除某密钥的会话（上游返回 404 表示会话已失效时调用）。
// 会话失效同时也意味着需要重新握手。
func (c *client) dropSession(serverID, key string) {
	k := sessionKey(serverID, key)
	c.mu.Lock()
	delete(c.sessions, k)
	c.mu.Unlock()

	c.initMu.Lock()
	delete(c.initialized, k)
	c.initMu.Unlock()
}

// sessionOf 返回某密钥已缓存的会话 id（没有则为空串）。
func (c *client) sessionOf(serverID, key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[sessionKey(serverID, key)]
}

// buildRequest 组装一条 JSON-RPC 请求（固定 id=1，网关与上游是一对一请求，无需多路复用）。
func buildRequest(method string, params any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
}

// rpcCall 向上游发一条 JSON-RPC 请求并解析响应。
//
// key 参与 Authorization 与自定义请求头占位符；r 为客户端原始请求（可为 nil），
// 用于解析渠道级请求头里的 {{header}} 占位符。
//
// 首次调用会自动完成 initialize 握手（按服务器 + 密钥隔离），之后复用会话。
func (c *client) rpcCall(ctx context.Context, srv *model.MCPServer, key string, r *http.Request, method string, params any) (json.RawMessage, error) {
	if method != "initialize" {
		if err := c.ensureInitialized(ctx, srv, key, r); err != nil {
			return nil, err
		}
	}
	return c.post(ctx, srv, key, r, method, params)
}

// initCall 一次进行中的握手：done 关闭后可读取 err。
type initCall struct {
	done chan struct{}
	err  error
}

// ensureInitialized 保证（服务器, 密钥）已完成 initialize；并发请求只有一个真正握手。
func (c *client) ensureInitialized(ctx context.Context, srv *model.MCPServer, key string, r *http.Request) error {
	k := sessionKey(srv.ID, key)

	c.initMu.Lock()
	if c.initialized[k] {
		c.initMu.Unlock()
		return nil
	}
	if call, ok := c.initing[k]; ok {
		c.initMu.Unlock()
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return &upstreamError{Message: "等待上游握手超时"}
		}
	}
	call := &initCall{done: make(chan struct{})}
	c.initing[k] = call
	c.initMu.Unlock()

	finish := func(err error) error {
		c.initMu.Lock()
		delete(c.initing, k)
		if err == nil {
			c.initialized[k] = true
		}
		call.err = err
		c.initMu.Unlock()
		close(call.done)
		return err
	}

	// 1) initialize：协商协议版本，上游可能在此返回 Mcp-Session-Id
	raw, err := c.post(ctx, srv, key, r, "initialize", map[string]any{
		"protocolVersion": defaultVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "ai-gateway",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return finish(err)
	}
	var initRes initializeResult
	_ = json.Unmarshal(raw, &initRes)

	// 2) notifications/initialized：规范要求的握手收尾通知，上游不应返回响应
	_ = c.notify(ctx, srv, key, r, "notifications/initialized", map[string]any{})

	return finish(nil)
}

// post 发送一条需要响应的 JSON-RPC 请求。
func (c *client) post(ctx context.Context, srv *model.MCPServer, key string, r *http.Request, method string, params any) (json.RawMessage, error) {
	payload, err := json.Marshal(buildRequest(method, params))
	if err != nil {
		return nil, &upstreamError{Message: "编码请求失败: " + err.Error()}
	}
	resp, err := c.do(ctx, srv, key, r, payload)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var body io.Reader = io.LimitReader(resp.Body, maxResponseBytes)
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body = bufio.NewReader(body)
	}
	if resp.StatusCode == http.StatusNotFound && c.sessionOf(srv.ID, key) != "" {
		// 会话已失效：清掉缓存，让上层换密钥或重新握手
		c.dropSession(srv.ID, key)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(body, 4096))
		return nil, httpError(resp.StatusCode, string(raw))
	}
	return readRPCResult(body)
}

// notify 发送一条 JSON-RPC 通知（无 id，不解析响应）。
func (c *client) notify(ctx context.Context, srv *model.MCPServer, key string, r *http.Request, method string, params any) error {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, srv, key, r, payload)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// do 组装并发送 HTTP 请求，同时维护会话 id。
func (c *client) do(ctx context.Context, srv *model.MCPServer, key string, r *http.Request, payload []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(srv.URL), bytes.NewReader(payload))
	if err != nil {
		return nil, &upstreamError{Message: "构造请求失败: " + err.Error()}
	}

	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	// 两个都接受：上游可以回一次性 JSON，也可以回 SSE 流
	headers.Set("Accept", "application/json, text/event-stream")
	if key != "" {
		// 与渠道一致：密钥写在 Authorization，自定义头可覆盖
		headers.Set("Authorization", "Bearer "+key)
	}
	channel.ApplyChannelHeaders(headers, mcpChannel(srv), r)
	if sess := c.sessionOf(srv.ID, key); sess != "" {
		headers.Set(sessionHeader, sess)
	}
	req.Header = headers

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &upstreamError{Message: "连接上游失败: " + err.Error(), Retryable: true}
	}
	if sess := resp.Header.Get(sessionHeader); sess != "" {
		c.mu.Lock()
		c.sessions[sessionKey(srv.ID, key)] = sess
		c.mu.Unlock()
	}
	return resp, nil
}

// readRPCResult 从 JSON 或 SSE 响应中取出 result 字段。
//
// Streamable HTTP 允许上游用 SSE 返回：每行 `data: {...}`，其中可能有
// 通知（无 id）与最终响应（有 id），这里取第一条带 result/error 的消息。
func readRPCResult(body io.Reader) (json.RawMessage, error) {
	if br, ok := body.(*bufio.Reader); ok {
		for {
			line, err := br.ReadString('\n')
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if raw == "" || raw == "[DONE]" {
					if err != nil {
						break
					}
					continue
				}
				result, rpcErr, ok := decodeEnvelope([]byte(raw))
				if ok {
					if rpcErr != nil {
						return nil, &upstreamError{Code: rpcErr.Code, Message: rpcErr.Message, Retryable: false}
					}
					return result, nil
				}
			}
			if err != nil {
				break
			}
		}
		return nil, &upstreamError{Message: "上游 SSE 流中没有 JSON-RPC 响应"}
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, &upstreamError{Message: "读取上游响应失败: " + err.Error(), Retryable: true}
	}
	result, rpcErr, ok := decodeEnvelope(raw)
	if !ok {
		return nil, &upstreamError{Message: "上游响应不是合法的 JSON-RPC: " + truncate(string(raw), 300)}
	}
	if rpcErr != nil {
		return nil, &upstreamError{Code: rpcErr.Code, Message: rpcErr.Message}
	}
	return result, nil
}

// decodeEnvelope 解析一条响应：成功返回 result，失败返回 rpc 错误。
func decodeEnvelope(raw []byte) (json.RawMessage, *rpcError, bool) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, false
	}
	if env.Error != nil {
		return nil, env.Error, true
	}
	if env.Result == nil {
		return nil, nil, false
	}
	return env.Result, nil, true
}

// httpError 把上游 HTTP 错误映射成「是否值得换个密钥重试」。
//
// 与 docs/plugin-spec.md §5.2 的口径一致：
// 401/403/404/429/5xx 换下一个目标；400/405/422 等属于请求本身的问题，不重试。
func httpError(status int, body string) *upstreamError {
	e := &upstreamError{
		Status:  status,
		Message: fmt.Sprintf("上游返回 HTTP %d: %s", status, truncate(strings.TrimSpace(body), 300)),
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusTooManyRequests:
		e.Retryable = true
	default:
		// 会话失效也表现为 404，由调用方额外清一次会话
		e.Retryable = status >= 500
	}
	return e
}

// mcpChannel 把 MCP 服务器适配成渠道，仅用于复用请求头占位符解析。
func mcpChannel(srv *model.MCPServer) *model.Channel {
	return &model.Channel{Headers: srv.Headers}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
