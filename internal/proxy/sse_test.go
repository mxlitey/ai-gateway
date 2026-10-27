package proxy

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestPeekSSEFailureDetectsPluginErrorEvent(t *testing.T) {
	raw := "data: {\"error\":{\"message\":\"上游错误：no_trace\",\"code\":400}}\n\ndata: [DONE]\n\n"
	prefix, failure := peekSSEFailure(bufio.NewReader(strings.NewReader(raw)))
	if !strings.Contains(failure, "no_trace") {
		t.Fatalf("expected failure to contain upstream message, got %q", failure)
	}
	if !strings.HasPrefix(raw, string(prefix)) {
		t.Fatalf("prefix must be a head of the original stream")
	}
}

func TestPeekSSEFailureDetectsEventErrorName(t *testing.T) {
	raw := "event: error\ndata: \"上游错误：boom\"\n\n"
	if _, failure := peekSSEFailure(bufio.NewReader(strings.NewReader(raw))); !strings.Contains(failure, "boom") {
		t.Fatalf("expected failure to contain boom, got %q", failure)
	}
}

func TestPeekSSEFailurePassesNormalStreamAndReplays(t *testing.T) {
	raw := ": keep-alive\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	br := bufio.NewReader(strings.NewReader(raw))
	prefix, failure := peekSSEFailure(br)
	if failure != "" {
		t.Fatalf("expected no failure, got %q", failure)
	}

	body := replayAfterPeek(prefix, br, io.NopCloser(strings.NewReader("")))
	replayed, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(replayed) != raw {
		t.Fatalf("replayed stream mismatch:\nwant %q\n got %q", raw, string(replayed))
	}
}

func TestPeekSSEFailureTreatsEmptyStreamAsFailure(t *testing.T) {
	for _, raw := range []string{"", "data: [DONE]\n\n"} {
		if _, failure := peekSSEFailure(bufio.NewReader(strings.NewReader(raw))); failure == "" {
			t.Fatalf("expected failure for empty stream %q", raw)
		}
	}
}
