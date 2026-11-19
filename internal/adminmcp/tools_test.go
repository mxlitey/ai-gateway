package adminmcp

import (
	"path/filepath"
	"testing"

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
