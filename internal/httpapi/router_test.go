package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/admin"
	"github.com/czg95/ai-gateway-v2/internal/imagestore"
	"github.com/czg95/ai-gateway-v2/internal/proxy"
)

// TestImageFilesRoute 验证生图结果路由优先于 /v1 代理，且不需要密钥。
func TestImageFilesRoute(t *testing.T) {
	images, err := imagestore.Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	name, err := images.Save([]byte("\x89PNG\r\n\x1a\npayload"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}\.png$`).MatchString(name) {
		t.Fatalf("unexpected name: %s", name)
	}

	// Proxy / Admin 为 nil：路由只做方法值注册，不会被调用
	h := NewRouter(Deps{Proxy: (*proxy.Handler)(nil), Admin: (*admin.API)(nil), Images: images})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, imagestore.URL(name), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type = %q", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, imagestore.URLPrefix+"../secret.png", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("路径穿越不应返回图片，实际 %d", rec.Code)
	}
}
