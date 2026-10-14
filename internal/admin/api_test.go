package admin

import (
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestMergeKeysKeepsRealValueOnMaskedInput(t *testing.T) {
	const real = "sk-demo-1234567890abcdef"
	existing := []model.ChannelKey{{Key: real, Enabled: true}}
	incoming := []model.ChannelKey{{Key: model.MaskKey(real), Enabled: true}}

	got := mergeKeys(existing, incoming)
	if len(got) != 1 {
		t.Fatalf("期望 1 条密钥，实际 %d", len(got))
	}
	if got[0].Key != real {
		t.Fatalf("提交脱敏值时应保留原密钥，实际 %q", got[0].Key)
	}
}

func TestMergeKeysAcceptsNewPlainValue(t *testing.T) {
	existing := []model.ChannelKey{{Key: "sk-old-1234567890abcdef", Enabled: true}}
	incoming := []model.ChannelKey{{Key: "sk-new-0987654321fedcba", Enabled: false}}

	got := mergeKeys(existing, incoming)
	if len(got) != 1 {
		t.Fatalf("期望 1 条密钥，实际 %d", len(got))
	}
	if got[0].Key != "sk-new-0987654321fedcba" {
		t.Fatalf("新密钥应被直接接受，实际 %q", got[0].Key)
	}
	if got[0].Enabled {
		t.Fatal("enabled=false 应被保留")
	}
}

func TestNormalizeModelMapFallsBackToPublicName(t *testing.T) {
	got := normalizeModelMap(map[string]string{
		"a":  "   ",
		"b":  " up-b ",
		"":   "x",
		"c2": "up-c",
	})

	if got["a"] != "a" {
		t.Fatalf("上游名为空时应回退为公开名，实际 %q", got["a"])
	}
	if got["b"] != "up-b" {
		t.Fatalf("应去除首尾空白，实际 %q", got["b"])
	}
	if got["c2"] != "up-c" {
		t.Fatalf("正常映射不应被改动，实际 %q", got["c2"])
	}
	if _, ok := got[""]; ok {
		t.Fatal("公开名为空的条目应被忽略")
	}
}

func TestHashAndVerifyPassword(t *testing.T) {
	encoded, err := HashPassword("test-password-123")
	if err != nil {
		t.Fatalf("生成摘要失败: %v", err)
	}
	if !VerifyPassword("test-password-123", encoded) {
		t.Fatal("正确密码应校验通过")
	}
	if VerifyPassword("wrong-password-456", encoded) {
		t.Fatal("错误密码不应通过")
	}
	if VerifyPassword("test-password-123", "garbage") {
		t.Fatal("非法摘要格式不应通过")
	}
}

func TestSessionToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")

	token, err := CreateToken(secret, time.Hour)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if !VerifyToken(token, secret) {
		t.Fatal("合法 Token 应校验通过")
	}
	if VerifyToken(token, []byte("fedcba9876543210fedcba9876543210")) {
		t.Fatal("错误密钥不应通过")
	}
	if VerifyToken("bad.token", secret) {
		t.Fatal("非法 Token 不应通过")
	}

	expired, err := CreateToken(secret, -time.Hour)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if VerifyToken(expired, secret) {
		t.Fatal("过期 Token 不应通过")
	}
}

func TestDescribeInvalidChatBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"正常响应", `{"choices":[{"message":{"content":"hi"}}]}`, ""},
		{"非 JSON", `not-json`, ""},
		{"内嵌错误", `{"error":{"message":"boom"}}`, "boom"},
		{"choices 缺失", `{"id":"x"}`, "choices 字段缺失"},
		{"choices 非数组", `{"choices":"nope"}`, "choices 字段异常"},
		{"choices 为空", `{"choices":[]}`, "choices 为空"},
	}
	for _, tc := range cases {
		if got := describeInvalidChatBody([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: 期望 %q，实际 %q", tc.name, tc.want, got)
		}
	}
}

func TestBeijingDayRange(t *testing.T) {
	from, to, err := beijingDayRange("2026-01-02")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if to-from != 24*60*60*1000 {
		t.Fatalf("区间应为 24 小时，实际 %d", to-from)
	}
	// 北京时间 2026-01-02 00:00 == UTC 2026-01-01 16:00
	if got := time.UnixMilli(from).UTC().Format("2006-01-02 15:04"); got != "2026-01-01 16:00" {
		t.Fatalf("起点时区换算错误，实际 %s", got)
	}
	if _, _, err := beijingDayRange("bad-date"); err == nil {
		t.Fatal("非法日期应返回错误")
	}
}