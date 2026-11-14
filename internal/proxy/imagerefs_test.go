package proxy

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/imagestore"
)

func newRefStore(t *testing.T, maxBytes int64) *imagestore.Store {
	t.Helper()
	s, err := imagestore.OpenWith(t.TempDir(), imagestore.UploadURLPrefix, time.Hour, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRewriteImageRefsConvertsOwnUpload(t *testing.T) {
	uploads := newRefStore(t, 8<<20)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, 64)...)
	saved, err := uploads.SaveReader(bytes.NewReader(png), 8<<20, true)
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{uploads: uploads}
	body := map[string]any{"image_url": "http://127.0.0.1:8787" + uploads.URL(saved.Name)}
	if err := h.rewriteImageRefs(body); err != nil {
		t.Fatal(err)
	}
	got, _ := body["image_url"].(string)
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected data URI, got %q", got)
	}
}

func TestRewriteImageRefsKeepsPublicHost(t *testing.T) {
	uploads := newRefStore(t, 8<<20)
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, 64)...)
	saved, err := uploads.SaveReader(bytes.NewReader(png), 8<<20, true)
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{uploads: uploads, imageHostPublic: true}
	ref := "https://gw.example.com" + uploads.URL(saved.Name)
	body := map[string]any{"image_url": []any{ref}}
	if err := h.rewriteImageRefs(body); err != nil {
		t.Fatal(err)
	}
	if got := body["image_url"].([]any)[0]; got != ref {
		t.Fatalf("public host ref should stay unchanged, got %v", got)
	}
}

func TestRewriteImageRefsRejectsOversized(t *testing.T) {
	uploads := newRefStore(t, 8<<20)
	big := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x01}, referenceMaxBytes+1)...)
	saved, err := uploads.SaveReader(bytes.NewReader(big), 8<<20, true)
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{uploads: uploads}
	body := map[string]any{"image_url": "http://127.0.0.1:8787" + uploads.URL(saved.Name)}
	err = h.rewriteImageRefs(body)
	if !errors.Is(err, errReferenceTooLarge) {
		t.Fatalf("expected errReferenceTooLarge, got %v", err)
	}
	if status, code := imageRefErrorStatus(err); status != 413 || code != "reference_exceeds_base64_limit" {
		t.Fatalf("unexpected mapping: %d %s", status, code)
	}
}

func TestRewriteImageRefsRejectsMissing(t *testing.T) {
	uploads := newRefStore(t, 8<<20)
	h := &Handler{uploads: uploads}
	body := map[string]any{
		"image_url": "http://127.0.0.1:8787" + imagestore.UploadURLPrefix + strings.Repeat("a", 32) + ".png",
	}
	err := h.rewriteImageRefs(body)
	if !errors.Is(err, errUploadedRefMissing) {
		t.Fatalf("expected errUploadedRefMissing, got %v", err)
	}
}
