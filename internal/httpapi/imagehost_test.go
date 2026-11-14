package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/imagestore"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, 64)...)

func newTestImageHost(t *testing.T, maxBytes int64) (*ImageHost, *imagestore.Store, *imagestore.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	err = db.CreateAPIKey(&model.APIKey{
		ID: "k1", Name: "test", Key: "sk-test",
		ChannelScope: "all", ChannelIDs: []string{},
		MCPScope: "all", MCPServerIDs: []string{},
		Enabled: true, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	images, err := imagestore.Open(filepath.Join(dir, "images"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := imagestore.OpenWith(filepath.Join(dir, "uploads"), imagestore.UploadURLPrefix, time.Hour, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return NewImageHost(uploads, images, db, "https://gw.example.com", true), images, uploads
}

func TestImageHostCapabilities(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	h.public = false

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/images/host", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Capabilities(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Enabled    bool   `json:"enabled"`
		Public     bool   `json:"public"`
		PublicBase string `json:"public_base"`
		MaxMB      int64  `json:"max_mb"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Enabled || out.Public {
		t.Fatalf("unexpected enabled/public: %+v", out)
	}
	if out.PublicBase != "https://gw.example.com" || out.MaxMB != 1 || out.TTLSeconds != 3600 {
		t.Fatalf("unexpected capabilities: %+v", out)
	}
}

func TestImageHostUploadRequiresKey(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/uploads", bytes.NewReader(testPNG))
	req.Header.Set("Content-Type", "image/png")
	h.Upload(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestImageHostUploadAndDelete(t *testing.T) {
	h, _, uploads := newTestImageHost(t, 1<<20)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/uploads", bytes.NewReader(testPNG))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Upload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID          string `json:"id"`
		URL         string `json:"url"`
		Bytes       int64  `json:"bytes"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://gw.example.com"+imagestore.UploadURLPrefix+out.ID {
		t.Fatalf("url = %q", out.URL)
	}
	if out.Bytes != int64(len(testPNG)) || out.ContentType != "image/png" {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	uploads.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, out.URL[len("https://gw.example.com"):], nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("serve status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, imagestore.UploadURLPrefix+out.ID, nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(uploads.Dir(), out.ID)); !os.IsNotExist(err) {
		t.Fatalf("文件未删除: %v", err)
	}
}

func TestImageHostRejectsNonImage(t *testing.T) {
	h, _, _ := newTestImageHost(t, 1<<20)

	for _, tc := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"声明为 html", "text/html", "<html></html>"},
		{"伪装成 png", "image/png", "<html></html>"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/images/uploads", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		req.Header.Set("Authorization", "Bearer sk-test")
		h.Upload(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("%s: status = %d, body = %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestImageHostRejectsTooLarge(t *testing.T) {
	h, _, _ := newTestImageHost(t, 128)

	body := append(append([]byte{}, testPNG...), bytes.Repeat([]byte{0x02}, 256)...)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/uploads", bytes.NewReader(body))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Upload(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestImageHostDeletesGeneratedImage(t *testing.T) {
	h, images, _ := newTestImageHost(t, 1<<20)
	name, err := images.Save(testPNG, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, imagestore.URLPrefix+name, nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(images.Dir(), name)); !os.IsNotExist(err) {
		t.Fatalf("文件未删除: %v", err)
	}
}

// TestUploadRoutePrecedesProxy 验证上传接口注册在 /v1/ 代理之前，不会被代理吃掉。
func TestUploadRoutePrecedesProxy(t *testing.T) {
	h, images, uploads := newTestImageHost(t, 1<<20)
	router := NewRouter(Deps{Images: images, Uploads: uploads, ImageHost: h})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/uploads", bytes.NewReader(testPNG))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("Authorization", "Bearer sk-test")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
