package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 上游非 2xx 时正文会被截断到 300 字节，若同时透传原始 Content-Length，
// 客户端会读到 unexpected EOF。此处锁定修复：不复制长度与编码头。
func TestBytesResponseDropsStaleLengthAndEncoding(t *testing.T) {
	upstream := http.Header{}
	upstream.Set("Content-Type", "application/json")
	upstream.Set("Content-Length", "900")
	upstream.Set("Content-Encoding", "gzip")
	upstream.Set("X-Request-Id", "abc")

	resp := bytesResponse(http.StatusBadRequest, upstream, []byte(`{"error":"bad"}`))
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length 不应透传，got %q", got)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding 不应透传，got %q", got)
	}
	if got := resp.Header.Get("X-Request-Id"); got != "abc" {
		t.Fatalf("其余响应头应保留，got %q", got)
	}

	w := httptest.NewRecorder()
	writeResponse(w, resp)
	if w.Body.String() != `{"error":"bad"}` {
		t.Fatalf("响应体不一致: %q", w.Body.String())
	}
}

// withCORS 已用 Set 写入 ACAO，writeResponse 不能再 Add 出第二个值。
func TestWriteResponseDoesNotDuplicateCORSHeader(t *testing.T) {
	upstream := http.Header{}
	upstream.Set("Content-Type", "text/event-stream")
	upstream.Set("Access-Control-Allow-Origin", "*")
	upstream.Set("Content-Length", "1024")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     upstream,
		Body:       io.NopCloser(strings.NewReader("data: hi\n\n")),
	}

	w := httptest.NewRecorder()
	// 模拟外层 withCORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeResponse(w, resp)

	if got := w.Header().Values("Access-Control-Allow-Origin"); len(got) != 1 {
		t.Fatalf("ACAO 应只有一个值，got %v", got)
	}
	if got := w.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length 不应透传，got %q", got)
	}
}

// 超过上限的响应体应报错，而不是返回被静默截断的数据。
func TestReadAllLimited(t *testing.T) {
	if _, err := readAllLimited(strings.NewReader("0123456789"), 10); err != nil {
		t.Fatalf("恰好等于上限时应成功: %v", err)
	}
	if _, err := readAllLimited(strings.NewReader("01234567890"), 10); err == nil {
		t.Fatal("超过上限时应返回错误")
	}
}
