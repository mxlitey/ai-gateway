package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// newCancelTestStore 建一个带单密钥渠道的最小环境。
func newCancelTestStore(t *testing.T, ch *model.Channel) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// 用量日志是异步落库的，关库前留一点时间，避免误报写入失败
	t.Cleanup(func() {
		time.Sleep(300 * time.Millisecond)
		_ = s.Close()
	})
	if err := s.CreateAPIKey(&model.APIKey{
		ID: "key-1", Name: "test", Key: "sk-test",
		ChannelScope: model.ScopeAll, Enabled: true, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	if err := s.CreateChannel(ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return s
}

// blockingUpstream 收到请求后挂起，直到测试结束释放，用于模拟「上游迟迟不返回」。
func blockingUpstream(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var once bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !once {
			once = true
			close(started)
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
	}))
	// LIFO：先释放上游 handler，再关服务器，避免 Close 卡在活跃连接上
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv, started
}

// TestHandleImagesClientCancelDoesNotCool 验证同步生图被调用方取消时不熔断密钥。
func TestHandleImagesClientCancelDoesNotCool(t *testing.T) {
	upstream, started := blockingUpstream(t)

	s := newCancelTestStore(t, &model.Channel{
		ID: "ch-1", Name: "sync", BaseURL: upstream.URL,
		Keys:        []model.ChannelKey{{Key: "sk-up", Enabled: true}},
		Models:      []string{"img"},
		ImageModels: []string{"img"},
		ImageGen:    model.ImageGenConfig{Mode: model.ImageGenSync, Path: "/images/generations"},
		Enabled:     true,
	})
	h := New(s, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"img","prompt":"x"}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	go func() {
		<-started
		cancel()
	}()

	w := httptest.NewRecorder()
	h.Handle(w, req)

	if h.Cooler().Cooling("ch-1", "sk-up") {
		t.Fatal("调用方断开不应熔断上游密钥")
	}
	if w.Code == http.StatusBadGateway {
		t.Fatalf("取消不应记为 502，实际 %d %s", w.Code, w.Body.String())
	}
}

// TestForwardClientCancelDoesNotCool 验证对话链路取消后返回 499 且不熔断密钥。
func TestForwardClientCancelDoesNotCool(t *testing.T) {
	upstream, started := blockingUpstream(t)

	s := newCancelTestStore(t, &model.Channel{
		ID: "ch-1", Name: "chat", BaseURL: upstream.URL,
		Keys:    []model.ChannelKey{{Key: "sk-up", Enabled: true}},
		Models:  []string{"m"},
		Enabled: true,
	})
	h := New(s, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	go func() {
		<-started
		cancel()
	}()

	w := httptest.NewRecorder()
	h.Handle(w, req)

	if h.Cooler().Cooling("ch-1", "sk-up") {
		t.Fatal("调用方断开不应熔断上游密钥")
	}
	if w.Code != 499 {
		t.Fatalf("取消应返回 499，实际 %d %s", w.Code, w.Body.String())
	}
}

// TestForwardCanceledBeforeStart 验证已取消的请求直接返回，不再请求上游。
func TestForwardCanceledBeforeStart(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	s := newCancelTestStore(t, &model.Channel{
		ID: "ch-1", Name: "chat", BaseURL: srv.URL,
		Keys:    []model.ChannelKey{{Key: "sk-up", Enabled: true}},
		Models:  []string{"m"},
		Enabled: true,
	})
	h := New(s, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.Handle(w, req)

	if hits != 0 {
		t.Fatalf("已取消的请求不应打到上游，实际命中 %d 次", hits)
	}
	if h.Cooler().Cooling("ch-1", "sk-up") {
		t.Fatal("已取消的请求不应熔断上游密钥")
	}
}
