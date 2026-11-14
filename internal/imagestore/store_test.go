package imagestore

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00}, 64)...)

func TestSaveAndServe(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	name, err := s.Save(pngBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}\.png$`).MatchString(name) {
		t.Fatalf("unexpected name: %s", name)
	}
	stored, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, pngBytes) {
		t.Fatal("stored content mismatch")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, URL(name), nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content-type = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Fatal("served content mismatch")
	}
}

func TestServeRejectsUnknownNames(t *testing.T) {
	s, err := Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		URLPrefix + "not-a-file.png",
		URLPrefix + "../../etc/passwd",
		URLPrefix + "0123456789abcdef0123456789abcde0/../../x.png",
		URLPrefix,
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Save(pngBytes, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Save(pngBytes, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, old), stale, stale); err != nil {
		t.Fatal(err)
	}

	s.Cleanup(time.Now())

	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Fatalf("过期文件未清理: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, fresh)); err != nil {
		t.Fatalf("新文件被误删: %v", err)
	}
}

func TestSaveRejectsEmpty(t *testing.T) {
	s, err := Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(nil, ""); err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestSaveReaderStreamsAndSniffs(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWith(dir, UploadURLPrefix, time.Hour, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{0xAB}, 900_000)...)
	saved, err := s.SaveReader(bytes.NewReader(payload), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Bytes != int64(len(payload)) {
		t.Fatalf("bytes = %d, want %d", saved.Bytes, len(payload))
	}
	if !strings.HasSuffix(saved.Name, ".png") || saved.ContentType != "image/png" {
		t.Fatalf("name=%s content-type=%s", saved.Name, saved.ContentType)
	}
	if s.URL(saved.Name) != UploadURLPrefix+saved.Name {
		t.Fatalf("url = %s", s.URL(saved.Name))
	}
	got, err := os.ReadFile(filepath.Join(dir, saved.Name))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("stored content mismatch: %v", err)
	}
}

func TestSaveReaderRejectsTooLarge(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWith(dir, UploadURLPrefix, time.Hour, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReader(bytes.NewReader(append(append([]byte{}, pngBytes...), bytes.Repeat([]byte{1}, 2048)...)), 0, true); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("超限文件未清理: %d", len(entries))
	}
}

func TestSaveReaderRejectsNonImage(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWith(dir, UploadURLPrefix, time.Hour, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReader(strings.NewReader("<html>hi</html>"), 0, true); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("非法类型未清理: %d", len(entries))
	}
}

func TestDelete(t *testing.T) {
	s, err := Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	name, err := s.Save(pngBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(name); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, URL(name), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if err := s.Delete(name); !os.IsNotExist(err) {
		t.Fatalf("重复删除应返回 NotExist，实际 %v", err)
	}
	if err := s.Delete("../etc/passwd"); !os.IsNotExist(err) {
		t.Fatalf("非法文件名应返回 NotExist，实际 %v", err)
	}
}

func TestCacheControlFollowsRetention(t *testing.T) {
	s, err := Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	name, err := s.Save(pngBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, URL(name), nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=1800" {
		t.Fatalf("cache-control = %q", got)
	}
}
