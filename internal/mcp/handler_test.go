package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/config"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// TestInstructionsFor 验证 initialize 的说明书区分「聚合入口」与「单服务器直连」。
func TestInstructionsFor(t *testing.T) {
	aggregate := instructionsFor("")
	if !strings.Contains(aggregate, "tools/list") || !strings.Contains(aggregate, "structuredContent.error") {
		t.Fatalf("聚合入口说明书不完整: %s", aggregate)
	}

	single := instructionsFor("image-generation")
	if !strings.Contains(single, "image-generation") || !strings.Contains(single, "tools/call") {
		t.Fatalf("直连说明书不完整: %s", single)
	}

	// 存储说明只在直连入口追加（且由 upstream 是否带 instructions 决定），聚合入口不追加
	if strings.Contains(aggregate, "/v1/storage/tickets") {
		t.Fatalf("聚合入口不应追加存储说明: %s", aggregate)
	}
}

const testUpstreamInstructions = "UPSTREAM-INSTRUCTIONS: 先 list_image_models 再 generate_image"

// fakeUpstream 模拟一台上游 MCP：initialize 返回自定义 serverInfo 与 instructions。
func fakeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "initialize" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"protocolVersion": defaultVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "image-generation", "version": "2.0.0"},
				"instructions":    testUpstreamInstructions,
			},
		})
	}))
}

// newTestHandler 构造一个指向 fakeUpstream 的聚合入口。
func newTestHandler(t *testing.T, upstreamURL string) *Handler {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	key := &model.APIKey{
		ID: "key-1", Name: "test", Key: "sk-test",
		ChannelScope: model.ScopeAll, ChannelIDs: []string{},
		MCPScope: model.ScopeAll, MCPServerIDs: []string{},
		Enabled: true, CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.CreateAPIKey(key); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	srv := &model.MCPServer{
		ID: "srv-1", Name: "生图", Alias: "image-generation", URL: upstreamURL,
		Keys:    []model.MCPKey{{Key: "sk-up", Enabled: true}},
		Enabled: true,
	}
	if err := s.CreateMCPServer(srv); err != nil {
		t.Fatalf("create mcp server: %v", err)
	}
	return NewHandler(s, NewService(s, &config.Config{}))
}

// initializeResult 解析网关 initialize 的响应。
type initializeResponse struct {
	Result struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	} `json:"result"`
}

func callInitialize(t *testing.T, h *Handler, path string) initializeResponse {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + defaultVersion + `"}}`
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer sk-test")
	w := httptest.NewRecorder()
	h.Handle(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var out initializeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v, body = %s", err, w.Body.String())
	}
	return out
}

// TestHandleInitializePassthrough 单服务器直连时沿用上游的 serverInfo 与 instructions。
func TestHandleInitializePassthrough(t *testing.T) {
	up := fakeUpstream(t)
	defer up.Close()
	h := newTestHandler(t, up.URL)

	out := callInitialize(t, h, "/mcp/image-generation")
	if out.Result.ServerInfo.Name != "image-generation" || out.Result.ServerInfo.Version != "2.0.0" {
		t.Fatalf("serverInfo = %+v, 应沿用上游身份", out.Result.ServerInfo)
	}
	if !strings.Contains(out.Result.Instructions, testUpstreamInstructions) {
		t.Fatalf("直连未透传上游 instructions: %s", out.Result.Instructions)
	}
}

// TestHandleInitializeAggregate 聚合入口按服务器分段拼接上游 instructions。
func TestHandleInitializeAggregate(t *testing.T) {
	up := fakeUpstream(t)
	defer up.Close()
	h := newTestHandler(t, up.URL)

	out := callInitialize(t, h, "/mcp")
	if out.Result.ServerInfo.Name != "ai-gateway-mcp" {
		t.Fatalf("聚合入口 serverInfo = %+v", out.Result.ServerInfo)
	}
	if !strings.Contains(out.Result.Instructions, "## 生图") ||
		!strings.Contains(out.Result.Instructions, testUpstreamInstructions) {
		t.Fatalf("聚合未拼接上游 instructions: %s", out.Result.Instructions)
	}
	if !strings.Contains(out.Result.Instructions, "tools/list") {
		t.Fatalf("聚合说明书缺少调用指引: %s", out.Result.Instructions)
	}
	// 聚合入口只拼接上游说明，不追加网关存储说明
	if strings.Contains(out.Result.Instructions, "/v1/storage/tickets") {
		t.Fatalf("聚合入口不应追加存储说明: %s", out.Result.Instructions)
	}
}
