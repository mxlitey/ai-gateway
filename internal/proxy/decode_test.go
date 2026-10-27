package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeJSONObject(t *testing.T) {
	body := map[string]any{"model": "deepseek-flash", "messages": []any{}}
	plain, _ := json.Marshal(body)

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	cases := []struct {
		name    string
		headers map[string]string
		body    []byte
		wantOK  bool
	}{
		{name: "普通请求体", body: plain, wantOK: true},
		{name: "gzip 请求体", headers: map[string]string{"Content-Encoding": "gzip"}, body: gz.Bytes(), wantOK: true},
		{name: "不支持的编码", headers: map[string]string{"Content-Encoding": "br"}, body: plain},
		{name: "非法 JSON", body: []byte("{oops")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(c.body))
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			out, err := decodeJSONObject(req)
			if c.wantOK {
				if err != nil {
					t.Fatalf("期望解析成功，实际报错: %v", err)
				}
				if out["model"] != "deepseek-flash" {
					t.Fatalf("model 解析错误: %v", out["model"])
				}
				return
			}
			if err == nil {
				t.Fatalf("期望解析失败，实际成功")
			}
		})
	}
}

func TestDecodeJSONObjectTooLarge(t *testing.T) {
	big := strings.Repeat("x", maxRequestBody+1024)
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(big))
	if _, err := decodeJSONObject(req); err == nil || !strings.Contains(err.Error(), "过大") {
		t.Fatalf("期望提示请求体过大，实际: %v", err)
	}
}
