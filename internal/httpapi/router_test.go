package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
