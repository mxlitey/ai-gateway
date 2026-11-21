package proxy

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestApplyChatParamsRewritesBody(t *testing.T) {
	body := map[string]any{
		"model":       "gpt-4o",
		"max_tokens":  float64(256),
		"thinking":    map[string]any{"type": "enabled"},
		"temperature": 0.5,
	}
	ch := &model.Channel{
		ParamMap:     map[string]string{"max_tokens": "max_completion_tokens"},
		DropParams:   []string{"thinking"},
		StaticParams: map[string]any{"temperature": 0.9, "top_p": 0.8},
	}

	out := applyChatParams(body, ch)
	if _, ok := out["max_tokens"]; ok {
		t.Fatalf("改名后不应保留旧字段: %+v", out)
	}
	if out["max_completion_tokens"] != float64(256) {
		t.Fatalf("改名未生效: %+v", out)
	}
	if _, ok := out["thinking"]; ok {
		t.Fatalf("剥离未生效: %+v", out)
	}
	if out["temperature"] != 0.5 {
		t.Fatalf("参数改写不应覆盖客户端已传的值: %+v", out)
	}
	if out["top_p"] != 0.8 {
		t.Fatalf("参数改写未补齐缺失字段: %+v", out)
	}
	// 原始请求体不能被改写，否则下一个渠道会带着上一个渠道的参数
	if _, ok := body["thinking"]; !ok {
		t.Fatalf("原始 body 被修改: %+v", body)
	}
}

func TestApplyChatParamsNoopWithoutConfig(t *testing.T) {
	body := map[string]any{"model": "gpt-4o"}
	if got := applyChatParams(body, &model.Channel{}); got["model"] != "gpt-4o" {
		t.Fatalf("未配置时应原样返回: %+v", got)
	}
}
