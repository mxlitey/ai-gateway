package imagestore

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
