package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// TestStorageTicketUnauthorized 验证未携带有效密钥时拒绝签发预签名 URL。
func TestStorageTicketUnauthorized(t *testing.T) {
	h := NewStorage(nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/tickets", nil)
	h.Ticket(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无密钥应返回 401，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestStorageTicketForbidden 验证密钥的对象存储权限被禁用时拒绝签发。
func TestStorageTicketForbidden(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	key := &model.APIKey{
		ID: "k-none", Name: "chat-only", Key: "sk-test",
		ChannelScope: model.ScopeAll, MCPScope: model.ScopeAll,
		StorageScope: model.ScopeNone,
		Enabled:      true, CreatedAt: time.Now().UnixMilli(),
	}
	if err := st.CreateAPIKey(key); err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}

	h := NewStorage(nil, st)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/tickets", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Ticket(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("对象存储权限禁用应返回 403，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "storage_forbidden") {
		t.Fatalf("错误码应为 storage_forbidden，实际: %s", rec.Body.String())
	}
}
