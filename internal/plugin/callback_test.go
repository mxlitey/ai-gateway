package plugin

import (
	"testing"
)

// TestCallbackPort 回调端口固定为监听端口 +1，越界时返回 0。
func TestCallbackPort(t *testing.T) {
	if got := CallbackPort(8787); got != 8788 {
		t.Fatalf("8787 的回调端口应为 8788，实际 %d", got)
	}
	if got := CallbackPort(0); got != 0 {
		t.Fatalf("端口未知时应返回 0，实际 %d", got)
	}
	if got := CallbackPort(65535); got != 0 {
		t.Fatalf("65535 没有可用的 +1 端口，应返回 0，实际 %d", got)
	}
}

// TestMatchCallbackPath 前缀匹配：只有完全相等或落在子路径下才算命中。
func TestMatchCallbackPath(t *testing.T) {
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{"/authorize", "/authorize", true},
		{"/auth/callback", "/auth/callback", true},
		{"/oauth/abc123", "/oauth", true},
		{"/authorize/extra", "/authorize", true},
		{"/authorize2", "/authorize", false},
		{"/oauth2/cb", "/oauth", false},
		{"/v1/chat/completions", "/oauth", false},
	}
	for _, c := range cases {
		if got := matchCallbackPath(c.path, c.pattern); got != c.want {
			t.Fatalf("matchCallbackPath(%q, %q) = %v，期望 %v", c.path, c.pattern, got, c.want)
		}
	}
}

// TestCallbackInstancePicksLongestPrefix 多个插件都匹配时取最长前缀。
func TestCallbackInstancePicksLongestPrefix(t *testing.T) {
	short := &instance{
		manifest: &Manifest{ID: "alpha", CallbackPaths: []string{"/oauth"}},
		status:   StatusRunning,
		port:     20901,
	}
	long := &instance{
		manifest: &Manifest{ID: "beta", CallbackPaths: []string{"/oauth/extra"}},
		status:   StatusRunning,
		port:     20902,
	}
	h := &Host{items: map[string]*instance{"alpha": short, "beta": long}}

	inst, ok := h.callbackInstance("/oauth/extra/callback")
	if !ok || inst.manifest.ID != "beta" {
		t.Fatalf("应命中更长前缀的插件 beta，实际 %+v", inst)
	}
	inst, ok = h.callbackInstance("/oauth/abc")
	if !ok || inst.manifest.ID != "alpha" {
		t.Fatalf("应命中 alpha，实际 %+v", inst)
	}
	if _, ok := h.callbackInstance("/unknown"); ok {
		t.Fatal("没有插件认领的路径应返回 false")
	}
}

// TestCallbackInstanceSkipsStopped 未运行的插件不参与回调路由。
func TestCallbackInstanceSkipsStopped(t *testing.T) {
	stopped := &instance{
		manifest: &Manifest{ID: "alpha", CallbackPaths: []string{"/oauth"}},
		status:   StatusStopped,
		port:     20901,
	}
	h := &Host{items: map[string]*instance{"alpha": stopped}}
	if _, ok := h.callbackInstance("/oauth/abc"); ok {
		t.Fatal("停用的插件不应接管回调")
	}
}

// TestCallbackConflictLocked 重叠的 callback_paths 会被拒绝。
func TestCallbackConflictLocked(t *testing.T) {
	running := &instance{
		manifest: &Manifest{ID: "alpha", CallbackPaths: []string{"/oauth"}},
		status:   StatusRunning,
		port:     20901,
	}
	h := &Host{items: map[string]*instance{"alpha": running}}

	overlap := &instance{manifest: &Manifest{ID: "beta", CallbackPaths: []string{"/oauth/cb"}}}
	if msg := h.callbackConflictLocked(overlap); msg == "" {
		t.Fatal("包含关系的回调路径应被判定为冲突")
	}

	distinct := &instance{manifest: &Manifest{ID: "gamma", CallbackPaths: []string{"/auth/callback"}}}
	if msg := h.callbackConflictLocked(distinct); msg != "" {
		t.Fatalf("互不重叠的路径不应冲突，实际: %s", msg)
	}
}

// TestPathClean 拒绝 .. 与反斜杠逃逸。
func TestPathClean(t *testing.T) {
	cases := map[string]string{
		"/oauth/../v1/chat": "/v1/chat",
		"/oauth/./cb":       "/oauth/cb",
		`/oauth\cb`:         "/",
		"":                  "/",
	}
	for in, want := range cases {
		if got := pathClean(in); got != want {
			t.Fatalf("pathClean(%q) = %q，期望 %q", in, got, want)
		}
	}
}
