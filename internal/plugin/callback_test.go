package plugin

import (
	"testing"
	"time"
)

// TestLoginExpiry 网关计时的登记、超时与结束。
func TestLoginExpiry(t *testing.T) {
	h := &Host{}
	if h.LoginExpired("trae") {
		t.Fatal("未登记过的插件不应判定超时")
	}

	h.TrackLogin("trae")
	if h.LoginExpired("trae") {
		t.Fatal("刚登记的登录不应判定超时")
	}

	// 把截止时间拨到过去，模拟 90 秒已到
	h.mu.Lock()
	h.logins["trae"] = loginTimer{deadline: time.Now().Add(-time.Second)}
	h.mu.Unlock()
	if !h.LoginExpired("trae") {
		t.Fatal("超过有效期后应判定超时")
	}

	h.FinishLogin("trae")
	if h.LoginExpired("trae") {
		t.Fatal("结束后的登录不应再判定超时")
	}

	// 重新发起会重置计时
	h.TrackLogin("trae")
	if h.LoginExpired("trae") {
		t.Fatal("重新发起后应重新计时")
	}
}

// TestCallbackBaseUsesPluginPort 回调基址就是插件自己的回环端口，不再占用网关端口 +1。
func TestCallbackBaseUsesPluginPort(t *testing.T) {
	h := &Host{items: map[string]*instance{
		"trae": {manifest: &Manifest{ID: "trae"}, status: StatusRunning, port: 52341},
	}}
	if got := h.CallbackBase("trae"); got != "http://127.0.0.1:52341" {
		t.Fatalf("CallbackBase(trae) = %q", got)
	}
	// 插件不存在或未分配端口时为空，插件据此决定是否退回自身端口
	if got := h.CallbackBase("missing"); got != "" {
		t.Fatalf("未知插件应返回空，实际 %q", got)
	}
	if got := callbackBaseFor(0); got != "" {
		t.Fatalf("端口未知时应返回空，实际 %q", got)
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

// TestNormalizeCallbackURL 浏览器地址栏会隐藏协议，粘贴出来的链接常没有 http://。
func TestNormalizeCallbackURL(t *testing.T) {
	cases := []struct {
		in       string
		host     string
		path     string
		rawQuery string
		wantErr  bool
	}{
		{
			in:       "127.0.0.1:52341/authorize?code=abc&state=xyz",
			host:     "127.0.0.1:52341",
			path:     "/authorize",
			rawQuery: "code=abc&state=xyz",
		},
		{
			in:       "http://127.0.0.1:52341/authorize?code=abc",
			host:     "127.0.0.1:52341",
			path:     "/authorize",
			rawQuery: "code=abc",
		},
		{
			in:       "https://localhost:8788/auth/callback?code=1",
			host:     "localhost:8788",
			path:     "/auth/callback",
			rawQuery: "code=1",
		},
		{
			in:       "//127.0.0.1:52341/authorize",
			host:     "127.0.0.1:52341",
			path:     "/authorize",
		},
		{
			in:       "  127.0.0.1:52341/authorize  ",
			host:     "127.0.0.1:52341",
			path:     "/authorize",
		},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
	}
	for _, c := range cases {
		u, err := normalizeCallbackURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("normalizeCallbackURL(%q) 应报错，实际得到 %v", c.in, u)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeCallbackURL(%q) 报错: %v", c.in, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("normalizeCallbackURL(%q) scheme = %q", c.in, u.Scheme)
		}
		if u.Host != c.host || u.Path != c.path || u.RawQuery != c.rawQuery {
			t.Fatalf("normalizeCallbackURL(%q) = host %q path %q query %q，期望 %q %q %q",
				c.in, u.Host, u.Path, u.RawQuery, c.host, c.path, c.rawQuery)
		}
	}
}
