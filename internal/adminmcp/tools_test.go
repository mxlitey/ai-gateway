package adminmcp

import (
	"path/filepath"
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// newTestServer 构造一个带临时库的管理 MCP，返回注册表与库。
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	pricer := pricing.New(st)
	srv := NewServer(NewConfig(true, "read-hash", "write-hash"))
	RegisterReadTools(srv, ReadDeps{Store: st, Pricer: pricer})
	RegisterWriteTools(srv, WriteDeps{Store: st, Pricer: pricer})
	return srv, st
}

// hasTool 判断工具是否出现在给定权限的清单里。
func hasTool(srv *Server, name string, writable bool) bool {
	for _, t := range srv.ListTools(writable) {
		if t["name"] == name {
			return true
		}
	}
	return false
}

// TestModelMetaToolsRegistered 元数据与价格的写工具只对读写令牌可见。
func TestModelMetaToolsRegistered(t *testing.T) {
	srv, _ := newTestServer(t)

	readOnly := []string{"get_model_meta", "get_price"}
	for _, name := range readOnly {
		if !hasTool(srv, name, false) {
			t.Fatalf("只读令牌应能看到 %s", name)
		}
	}

	writes := []string{"set_model_meta", "delete_model_meta", "set_price", "delete_price"}
	for _, name := range writes {
		if hasTool(srv, name, false) {
			t.Fatalf("只读令牌不应看到写工具 %s", name)
		}
		if !hasTool(srv, name, true) {
			t.Fatalf("读写令牌应能看到 %s", name)
		}
	}
}

// TestSetModelMetaTool set_model_meta 保存后 get_model_meta 能读回，且做规整。
func TestSetModelMetaTool(t *testing.T) {
	srv, st := newTestServer(t)

	res := srv.CallTool("set_model_meta", map[string]any{
		"model":          "img-a",
		"description":    "  通用文生图  ",
		"reference":      true,
		"chinese_text":   true,
		"max_ref_count":  float64(5),
		"min_width_px":   float64(512),
		"min_height_px":  float64(512),
		"max_width_px":   float64(4096),
		"max_height_px":  float64(4096),
		"size_multiple":  float64(16),
		"output_formats": []any{"PNG", "gif"},
	}, true)
	if res["isError"] == true {
		t.Fatalf("保存失败: %v", res)
	}

	got, err := st.GetModelMeta("img-a")
	if err != nil || got == nil {
		t.Fatalf("应能读回: %v", err)
	}
	if got.Description != "通用文生图" {
		t.Fatalf("描述应去掉首尾空白，实际 %q", got.Description)
	}
	if !got.Reference || !got.ChineseText || got.MaxRefCount != 5 {
		t.Fatalf("能力与约束不符: %+v", got)
	}
	if got.MinWidthPx != 512 || got.MaxWidthPx != 4096 ||
		got.MinHeightPx != 512 || got.MaxHeightPx != 4096 {
		t.Fatalf("分辨率区间不符: %+v", got)
	}
	if got.SizeMultiple != 16 {
		t.Fatalf("尺寸倍数不符: %+v", got)
	}
	if len(got.OutputFormats) != 1 || got.OutputFormats[0] != "png" {
		t.Fatalf("格式应只留 png，实际 %v", got.OutputFormats)
	}
}

// TestSetModelMetaRequiresModel 缺少 model 直接报错。
func TestSetModelMetaRequiresModel(t *testing.T) {
	srv, _ := newTestServer(t)
	res := srv.CallTool("set_model_meta", map[string]any{}, true)
	if res["isError"] != true {
		t.Fatal("缺少 model 应返回错误")
	}
}

// TestSetModelMetaWriteNeedsWritable 只读令牌调用写工具被拒。
func TestSetModelMetaWriteNeedsWritable(t *testing.T) {
	srv, _ := newTestServer(t)
	res := srv.CallTool("set_model_meta", map[string]any{"model": "x"}, false)
	if res["isError"] != true {
		t.Fatal("只读令牌调用写工具应报错")
	}
}

// TestGetModelMetaSplitsOrphans 没有渠道声明的条目归入 orphans。
func TestGetModelMetaSplitsOrphans(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.CallTool("set_model_meta", map[string]any{"model": "no-channel"}, true)

	res := srv.CallTool("get_model_meta", map[string]any{}, false)
	out, ok := res["content"].([]map[string]any)
	if !ok || len(out) == 0 {
		t.Fatalf("结果结构异常: %v", res)
	}
	text := out[0]["text"].(string)
	if !contains(text, "no-channel") || !contains(text, "orphans") {
		t.Fatalf("无渠道的模型应出现在 orphans 里: %s", text)
	}
}

// TestDeleteModelMetaTool 删除后查不到。
func TestDeleteModelMetaTool(t *testing.T) {
	srv, st := newTestServer(t)
	srv.CallTool("set_model_meta", map[string]any{"model": "temp"}, true)
	if got, _ := st.GetModelMeta("temp"); got == nil {
		t.Fatal("保存失败")
	}
	res := srv.CallTool("delete_model_meta", map[string]any{"model": "temp"}, true)
	if res["isError"] == true {
		t.Fatalf("删除失败: %v", res)
	}
	if got, _ := st.GetModelMeta("temp"); got != nil {
		t.Fatalf("删除后应查不到，实际 %+v", got)
	}
}

// TestSetPriceTool 按次计费的价格能保存，非法单位被拒。
func TestSetPriceTool(t *testing.T) {
	srv, st := newTestServer(t)

	res := srv.CallTool("set_price", map[string]any{
		"model":         "img-a",
		"unit":          "per_request",
		"request_price": float64(0.2),
	}, true)
	if res["isError"] == true {
		t.Fatalf("保存失败: %v", res)
	}
	prices, err := st.ListModelPrices()
	if err != nil || len(prices) != 1 {
		t.Fatalf("应有 1 条价格: %v %v", prices, err)
	}
	if prices[0].Model != "img-a" || prices[0].RequestPrice != 0.2 {
		t.Fatalf("价格内容不符: %+v", prices[0])
	}

	bad := srv.CallTool("set_price", map[string]any{"model": "x", "unit": "per_minute"}, true)
	if bad["isError"] != true {
		t.Fatal("非法 unit 应被拒绝")
	}
}

// TestSetPriceRequiresFallbackTier 按 token 计费缺少兜底档时报错。
func TestSetPriceRequiresFallbackTier(t *testing.T) {
	srv, _ := newTestServer(t)
	res := srv.CallTool("set_price", map[string]any{
		"model": "chat-a",
		"tiers": []any{
			map[string]any{"up_to": float64(1000), "prompt_price": float64(1), "completion_price": float64(2)},
		},
	}, true)
	if res["isError"] != true {
		t.Fatal("缺少兜底档应报错")
	}
}

// TestDeletePriceTool 删除后价格表为空。
func TestDeletePriceTool(t *testing.T) {
	srv, st := newTestServer(t)
	srv.CallTool("set_price", map[string]any{"model": "chat-a", "unit": "per_request", "request_price": float64(1)}, true)
	res := srv.CallTool("delete_price", map[string]any{"model": "chat-a"}, true)
	if res["isError"] == true {
		t.Fatalf("删除失败: %v", res)
	}
	if n, _ := st.CountModelPrices(); n != 0 {
		t.Fatalf("应已清空，实际剩 %d 条", n)
	}
}

// TestIssueAPIKeyHidesPlainByDefault 默认不回显明文，显式 return_key=true 才返回。
func TestIssueAPIKeyHidesPlainByDefault(t *testing.T) {
	srv, st := newTestServer(t)

	res := srv.CallTool("issue_api_key", map[string]any{"name": "默认"}, true)
	if res["isError"] == true {
		t.Fatalf("签发失败: %v", res)
	}
	text := res["content"].([]map[string]any)[0]["text"].(string)
	keys, err := st.ListAPIKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("应有 1 枚密钥: %v %v", keys, err)
	}
	if contains(text, keys[0].Key) {
		t.Fatalf("默认响应不应包含明文密钥: %s", text)
	}
	if !contains(text, "hint") {
		t.Fatalf("默认响应应给出脱敏值: %s", text)
	}

	res = srv.CallTool("issue_api_key", map[string]any{"name": "显式", "return_key": true}, true)
	if res["isError"] == true {
		t.Fatalf("签发失败: %v", res)
	}
	text = res["content"].([]map[string]any)[0]["text"].(string)
	keys, _ = st.ListAPIKeys()
	if len(keys) != 2 {
		t.Fatalf("应有 2 枚密钥，实际 %d", len(keys))
	}
	var second string
	for _, k := range keys {
		if k.Name == "显式" {
			second = k.Key
		}
	}
	if second == "" || !contains(text, second) {
		t.Fatalf("return_key=true 时应返回明文: %s", text)
	}
}

// TestDeleteAPIKeyTool 只读令牌看不到删除工具；读写令牌签发后可删除，删除后查不到。
func TestDeleteAPIKeyTool(t *testing.T) {
	srv, st := newTestServer(t)

	for _, name := range []string{"list_api_keys", "delete_api_key"} {
		if name == "list_api_keys" && !hasTool(srv, name, false) {
			t.Fatalf("只读令牌应能看到 %s", name)
		}
		if name == "delete_api_key" && hasTool(srv, name, false) {
			t.Fatalf("只读令牌不应看到写工具 %s", name)
		}
		if !hasTool(srv, name, true) {
			t.Fatalf("读写令牌应能看到 %s", name)
		}
	}

	created := srv.CallTool("issue_api_key", map[string]any{"name": "临时密钥"}, true)
	if created["isError"] == true {
		t.Fatalf("签发失败: %v", created)
	}
	keys, err := st.ListAPIKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("应有 1 枚密钥: %v %v", keys, err)
	}
	keyID := keys[0].ID

	if res := srv.CallTool("delete_api_key", map[string]any{"key_id": keyID}, false); res["isError"] != true {
		t.Fatal("只读令牌调用删除应报错")
	}
	if res := srv.CallTool("delete_api_key", map[string]any{"key_id": "no-such-id"}, true); res["isError"] != true {
		t.Fatal("删除不存在的密钥应报错")
	}

	if res := srv.CallTool("delete_api_key", map[string]any{"key_id": keyID}, true); res["isError"] == true {
		t.Fatalf("删除失败: %v", res)
	}
	if got, _ := st.GetAPIKey(keyID); got != nil {
		t.Fatalf("删除后应查不到，实际 %+v", got)
	}
}

// TestGetAPIKeyTool 默认脱敏，reveal_key=true 才返回明文；不存在时报错。
func TestGetAPIKeyTool(t *testing.T) {
	srv, st := newTestServer(t)

	srv.CallTool("issue_api_key", map[string]any{"name": "查询用", "return_key": true}, true)
	keys, err := st.ListAPIKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("应有 1 枚密钥: %v %v", keys, err)
	}
	plain := keys[0].Key

	res := srv.CallTool("get_api_key", map[string]any{"key_id": keys[0].ID}, false)
	if res["isError"] == true {
		t.Fatalf("查询失败: %v", res)
	}
	text := res["content"].([]map[string]any)[0]["text"].(string)
	if contains(text, plain) {
		t.Fatalf("默认不应返回明文: %s", text)
	}
	if !contains(text, "hint") {
		t.Fatalf("默认应给出脱敏值: %s", text)
	}

	res = srv.CallTool("get_api_key", map[string]any{"key_id": keys[0].ID, "reveal_key": true}, false)
	text = res["content"].([]map[string]any)[0]["text"].(string)
	if !contains(text, plain) {
		t.Fatalf("reveal_key=true 时应返回明文: %s", text)
	}

	if res := srv.CallTool("get_api_key", map[string]any{"key_id": "no-such-id"}, false); res["isError"] != true {
		t.Fatal("查询不存在的密钥应报错")
	}
}

// TestListAndGetKeysRevealSwitch 列表与单查都支持脱敏/明文切换。
func TestListAndGetKeysRevealSwitch(t *testing.T) {
	srv, st := newTestServer(t)
	srv.CallTool("issue_api_key", map[string]any{"name": "列表用", "return_key": true}, true)
	keys, _ := st.ListAPIKeys()
	if len(keys) != 1 {
		t.Fatalf("应有 1 枚密钥，实际 %d", len(keys))
	}
	plain := keys[0].Key

	if text := toolText(srv.CallTool("list_api_keys", map[string]any{}, false)); contains(text, plain) {
		t.Fatalf("list_api_keys 默认不应返回明文: %s", text)
	}
	if text := toolText(srv.CallTool("list_api_keys", map[string]any{"reveal_keys": true}, false)); !contains(text, plain) {
		t.Fatalf("reveal_keys=true 时应返回明文: %s", text)
	}

	// 上游渠道密钥同样支持切换
	ch := &model.Channel{
		ID: "ch-1", Name: "测试渠道", BaseURL: "https://example.com/v1",
		Keys: []model.ChannelKey{{Key: "sk-upstream-secret", Enabled: true}},
		Models: []string{"m"}, Enabled: true,
	}
	if err := st.CreateChannel(ch); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	if text := toolText(srv.CallTool("list_channels", map[string]any{}, false)); contains(text, "sk-upstream-secret") {
		t.Fatalf("list_channels 默认不应返回明文: %s", text)
	}
	if text := toolText(srv.CallTool("list_channels", map[string]any{"reveal_keys": true}, false)); !contains(text, "sk-upstream-secret") {
		t.Fatalf("reveal_keys=true 时应返回明文: %s", text)
	}
}

// toolText 取出工具结果的文本内容。
func toolText(res map[string]any) string {
	items, ok := res["content"].([]map[string]any)
	if !ok || len(items) == 0 {
		return ""
	}
	s, _ := items[0]["text"].(string)
	return s
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
