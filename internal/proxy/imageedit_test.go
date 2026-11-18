package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// pngBytes 构造一个可被 DetectContentType 识别为 PNG 的最小内容。
func pngBytes(extra int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, extra)...)
}

func multipartRequest(t *testing.T, files map[string][][]byte, fields map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, list := range files {
		for _, data := range list {
			fw, err := mw.CreateFormFile(name, name+".png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(8 << 20); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	return req
}

func TestEditReferenceImagesConvertsToDataURI(t *testing.T) {
	req := multipartRequest(t,
		map[string][][]byte{"image": {pngBytes(32)}},
		map[string]string{"prompt": "make it blue"})
	defer func() { _ = req.MultipartForm.RemoveAll() }()

	refs, err := editReferenceImages(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %d", len(refs))
	}
	if !strings.HasPrefix(refs[0], "data:image/png;base64,") {
		t.Fatalf("expected PNG data URI, got %q", refs[0])
	}
}

func TestEditReferenceImagesSupportsMultiple(t *testing.T) {
	req := multipartRequest(t,
		map[string][][]byte{"image[]": {pngBytes(16), pngBytes(24)}},
		map[string]string{"prompt": "merge them"})
	defer func() { _ = req.MultipartForm.RemoveAll() }()

	refs, err := editReferenceImages(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %d", len(refs))
	}
}

func TestEditReferenceImagesRejectsMissingImage(t *testing.T) {
	req := multipartRequest(t, nil, map[string]string{"prompt": "no image"})
	defer func() { _ = req.MultipartForm.RemoveAll() }()

	_, err := editReferenceImages(req)
	if !errors.Is(err, errInvalidReference) {
		t.Fatalf("expected errInvalidReference, got %v", err)
	}
	if status, code := imageRefErrorStatus(err); status != http.StatusBadRequest || code != "invalid_reference" {
		t.Fatalf("unexpected mapping: %d %s", status, code)
	}
}

func TestEditReferenceImagesRejectsUnsupportedType(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	req := multipartRequest(t,
		map[string][][]byte{"image": {svg}},
		map[string]string{"prompt": "svg"})
	defer func() { _ = req.MultipartForm.RemoveAll() }()

	_, err := editReferenceImages(req)
	if !errors.Is(err, errInvalidReference) {
		t.Fatalf("expected errInvalidReference, got %v", err)
	}
}

func TestEditReferenceImagesRejectsOversized(t *testing.T) {
	req := multipartRequest(t,
		map[string][][]byte{"image": {pngBytes(referenceMaxBytes)}},
		map[string]string{"prompt": "too big"})
	defer func() { _ = req.MultipartForm.RemoveAll() }()

	_, err := editReferenceImages(req)
	if !errors.Is(err, errReferenceTooLarge) {
		t.Fatalf("expected errReferenceTooLarge, got %v", err)
	}
	if status, code := imageRefErrorStatus(err); status != http.StatusRequestEntityTooLarge ||
		code != "reference_exceeds_base64_limit" {
		t.Fatalf("unexpected mapping: %d %s", status, code)
	}
}

// TestHandleImageEditEndToEnd 验证标准 multipart 请求被转换成渠道配置的上游参考图字段。
func TestHandleImageEditEndToEnd(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/edits" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		_, _ = w.Write([]byte(`{"data":[{"url":"https://cdn.example.com/e.png"}]}`))
	}))
	defer upstream.Close()

	s, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if err := s.CreateAPIKey(&model.APIKey{
		ID: "key-1", Name: "test", Key: "sk-test",
		ChannelScope: model.ScopeAll, Enabled: true, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	if err := s.CreateChannel(&model.Channel{
		ID: "ch-1", Name: "sync", BaseURL: upstream.URL,
		Keys:        []model.ChannelKey{{Key: "sk-up", Enabled: true}},
		Models:      []string{"img"},
		ImageModels: []string{"img"},
		ImageGen: model.ImageGenConfig{
			Mode:               model.ImageGenSync,
			Path:               "/images/generations",
			EditPath:           "/images/edits",
			ReferenceField:     "images",
			ReferenceShape:     model.ImageRefImageURLObjectArray,
			ResponseFormatMode: model.ImageFormatURL,
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	body, contentType := rawMultipartBody(t,
		map[string][][]byte{"image": {pngBytes(32)}},
		map[string]string{"model": "img", "prompt": "make it blue"})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	New(s, nil).HandleImageEdit(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	list, _ := upstreamBody["images"].([]any)
	if len(list) != 1 {
		t.Fatalf("upstream images = %v", upstreamBody["images"])
	}
	first, _ := list[0].(map[string]any)
	ref, _ := first["image_url"].(string)
	if !strings.HasPrefix(ref, "data:image/png;base64,") {
		t.Fatalf("upstream image_url = %q", ref)
	}
	if _, ok := upstreamBody["image_url"]; ok {
		t.Fatalf("image_url 应被移出上游请求体: %v", upstreamBody)
	}

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0]["url"] != "https://cdn.example.com/e.png" {
		t.Fatalf("response data = %s", w.Body.String())
	}
}

// TestHandleImageEditRejectsAsync 验证编辑接口不承接异步作业。
func TestHandleImageEditRejectsAsync(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	_ = s.CreateAPIKey(&model.APIKey{
		ID: "key-1", Name: "test", Key: "sk-test",
		ChannelScope: model.ScopeAll, Enabled: true, CreatedAt: time.Now().UnixMilli(),
	})

	body, contentType := rawMultipartBody(t,
		map[string][][]byte{"image": {pngBytes(32)}},
		map[string]string{"model": "img", "prompt": "x"})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", body)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-AIGW-Async", "true")
	w := httptest.NewRecorder()

	New(s, nil).HandleImageEdit(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "不支持异步作业") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

// rawMultipartBody 构造未解析的 multipart 请求体，交给 handler 自行解析。
func rawMultipartBody(t *testing.T, files map[string][][]byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, list := range files {
		for _, data := range list {
			fw, err := mw.CreateFormFile(name, name+".png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}
