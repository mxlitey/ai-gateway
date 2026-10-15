// Package channel 提供渠道级能力：自定义请求头占位符与上游路径推导。
package channel

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/model"
)

var (
	headerNameRe  = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+\\-.^_`|~]+$")
	placeholderRe = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
)

// passthroughBlocked 透传时需要排除的请求头：连接类、网关接管、客户端认证凭据。
var passthroughBlocked = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"proxy-connection":  true,
	"transfer-encoding": true,
	"te":                true,
	"trailer":           true,
	"upgrade":           true,
	"host":              true,
	"content-length":    true,
	"content-encoding":  true,
	"accept-encoding":   true,
	"authorization":     true,
	"x-api-key":         true,
	"x-plugin-account":  true,
	"cookie":            true,
}

// NormalizeHeaders 规范化渠道自定义请求头：兼容数组与对象输入，过滤非法名/值并去重。
func NormalizeHeaders(raw []model.Header) []model.Header {
	out := make([]model.Header, 0, len(raw))
	seen := map[string]bool{}
	for _, h := range raw {
		name := strings.TrimSpace(h.Name)
		value := h.Value
		if name == "" || !headerNameRe.MatchString(name) {
			continue
		}
		if strings.ContainsAny(value, "\r\n") {
			continue
		}
		lower := strings.ToLower(name)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, model.Header{Name: name, Value: value})
	}
	return out
}

// ResolveHeaderValue 解析请求头值中的占位符。
//
//	{{name}}        取客户端同名请求头（大小写不敏感）
//	{{a | b | c}}   从左到右取第一个命中的候选
//	{{[常量]}}      字面量，恒命中
//	{{uuid}}        随机 UUID
//	{{timestamp}}   当前毫秒时间戳
func ResolveHeaderValue(value string, r *http.Request) string {
	return placeholderRe.ReplaceAllStringFunc(value, func(m string) string {
		raw := m[2 : len(m)-2]
		if strings.TrimSpace(raw) == "" {
			return m
		}
		for _, cand := range strings.Split(raw, "|") {
			key := strings.TrimSpace(cand)
			if key == "" {
				continue
			}
			// 方括号包裹 = 字面常量，恒命中
			if len(key) >= 2 && strings.HasPrefix(key, "[") && strings.HasSuffix(key, "]") {
				return key[1 : len(key)-1]
			}
			if v := resolveCandidate(key, r); v != "" {
				return v
			}
		}
		return ""
	})
}

func resolveCandidate(key string, r *http.Request) string {
	if r != nil && r.Header != nil {
		if v := r.Header.Get(key); v != "" {
			return v
		}
	}
	switch strings.ToLower(key) {
	case "uuid":
		return id.UUID()
	case "timestamp":
		return itoa(nowMillis())
	}
	return ""
}

// ApplyUpstreamKey 把渠道密钥写入上游请求头。
// 插件渠道的「密钥」是账号 id，用专用头 X-Plugin-Account 传递选号；其余渠道用 Authorization。
func ApplyUpstreamKey(h http.Header, ch *model.Channel, key string) {
	if ch != nil && ch.IsPluginChannel() {
		h.Set("X-Plugin-Account", key)
		return
	}
	h.Set("Authorization", "Bearer "+key)
}

// ApplyChannelHeaders 把渠道自定义请求头写入 headers（同名覆盖内置头）。
func ApplyChannelHeaders(h http.Header, ch *model.Channel, r *http.Request) {
	if ch == nil {
		return
	}
	for _, hd := range NormalizeHeaders(ch.Headers) {
		h.Set(hd.Name, ResolveHeaderValue(hd.Value, r))
	}
}

// ApplyPassthroughHeaders 透传客户端请求头（排除连接类与认证凭据）。
func ApplyPassthroughHeaders(h http.Header, r *http.Request) {
	if r == nil || r.Header == nil {
		return
	}
	for name, values := range r.Header {
		if passthroughBlocked[strings.ToLower(name)] {
			continue
		}
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				continue
			}
			h.Add(name, v)
		}
	}
}