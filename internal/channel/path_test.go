package channel

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestChatPathFallsBackWhenBaseHasEndpoint(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{"普通前缀补默认路径", "https://api.example.com/v1", "", "/chat/completions"},
		{"主机已写全端点", "https://api.example.com/v1/chat/completions", "", ""},
		{"主机已写 messages", "https://api.anthropic.com/v1/messages", "", ""},
		{"主机带尾斜杠", "https://api.example.com/v1/messages/", "", ""},
		{"显式路径优先", "https://api.example.com/v1", "/openai/chat/completions", "/openai/chat/completions"},
		{"显式路径补前导斜杠", "https://api.example.com/v1", "custom/chat", "/custom/chat"},
	}
	for _, c := range cases {
		ch := &model.Channel{BaseURL: c.base, Path: c.path}
		if got := ChatPath(ch); got != c.want {
			t.Errorf("%s: ChatPath() = %q，期望 %q", c.name, got, c.want)
		}
	}
}

func TestModelsURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{"默认前缀", "https://api.example.com/v1", "", "https://api.example.com/v1/models"},
		{"主机已写全端点", "https://api.example.com/v1/chat/completions", "", "https://api.example.com/v1/models"},
		{"主机已写 messages", "https://api.anthropic.com/v1/messages", "", "https://api.anthropic.com/v1/models"},
		{"显式路径", "https://api.example.com", "/v2/chat/completions", "https://api.example.com/v2/models"},
	}
	for _, c := range cases {
		ch := &model.Channel{BaseURL: c.base, Path: c.path}
		if got := ModelsURL(ch); got != c.want {
			t.Errorf("%s: ModelsURL() = %q，期望 %q", c.name, got, c.want)
		}
	}
}
