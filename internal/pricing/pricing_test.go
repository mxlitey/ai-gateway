package pricing

import (
	"testing"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-sonnet-4":       "anthropic/claude-sonnet-4",
		"OpenAI/GPT-4o":                   "openai/gpt-4o",
		"claude-3.5-sonnet":               "claude-3-5-sonnet",
		"claude_3_5_sonnet":               "claude-3-5-sonnet",
		"z-ai/glm-4.6:free":               "z-ai/glm-4-6",
		"qwen/qwen3-235b-a22b@latest":     "qwen/qwen3-235b-a22b",
		"  DeepSeek/DeepSeek-R1  ":        "deepseek/deepseek-r1",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestResolveFallsBackToShortNameAndDateSuffix(t *testing.T) {
	r := &Resolver{index: map[string]model.ModelPrice{}}
	row := model.ModelPrice{
		Model:           "anthropic/claude-sonnet-4",
		PromptPrice:     3,
		CompletionPrice: 15,
		Source:          SourceOpenRouter,
	}
	r.index["anthropic/claude-sonnet-4"] = row
	r.index["claude-sonnet-4"] = row

	for _, name := range []string{
		"anthropic/claude-sonnet-4",
		"Claude-Sonnet-4",
		"claude-sonnet-4-20250514",
		"claude_sonnet_4",
	} {
		p, ok := r.Resolve(name)
		if !ok {
			t.Fatalf("Resolve(%q) 未命中", name)
		}
		if p.PromptPrice != 3 {
			t.Fatalf("Resolve(%q) 命中价格异常: %+v", name, p)
		}
	}

	if _, ok := r.Resolve("unknown-model"); ok {
		t.Fatal("未知模型不应命中")
	}
}

func TestEstimateSplitsCacheRead(t *testing.T) {
	p := model.ModelPrice{PromptPrice: 3, CompletionPrice: 15, CacheReadPrice: 0.3}
	// 100 万输入，其中 40 万命中缓存；100 万输出
	total, input, output := Estimate(p, 1_000_000, 1_000_000, 400_000)
	wantInput := 0.6*3 + 0.4*0.3
	wantOutput := 15.0
	if !closeEnough(input, wantInput) || !closeEnough(output, wantOutput) {
		t.Fatalf("Estimate 输入/输出计费异常: input=%v output=%v", input, output)
	}
	if !closeEnough(total, wantInput+wantOutput) {
		t.Fatalf("Estimate 合计异常: %v", total)
	}
}

func TestParseOpenRouter(t *testing.T) {
	raw := []byte(`{"data":[
	  {"id":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4","context_length":200000,
	   "pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003"}},
	  {"id":"free/model:free","name":"Free","pricing":{"prompt":"0","completion":"0"}},
	  {"id":"openai/gpt-4o","name":"GPT-4o","pricing":{"prompt":"0.0000025","completion":"0.00001"}}
	]}`)
	prices, err := ParseOpenRouter(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 {
		t.Fatalf("应跳过全 0 价格条目，实际得到 %d 条", len(prices))
	}
	byName := map[string]model.ModelPrice{}
	for _, p := range prices {
		byName[p.Model] = p
	}
	sonnet, ok := byName["anthropic/claude-sonnet-4"]
	if !ok {
		t.Fatal("缺少 claude-sonnet-4 条目")
	}
	if !closeEnough(sonnet.PromptPrice, 3) || !closeEnough(sonnet.CompletionPrice, 15) {
		t.Fatalf("逐 token 转百万 token 换算错误: %+v", sonnet)
	}
	if !closeEnough(sonnet.CacheReadPrice, 0.3) {
		t.Fatalf("缓存价格换算错误: %+v", sonnet)
	}
	if sonnet.ContextLength != 200000 || sonnet.Source != SourceOpenRouter {
		t.Fatalf("元数据解析错误: %+v", sonnet)
	}
}

func TestSourcePriority(t *testing.T) {
	// 手工条目应覆盖 OpenRouter 的同名条目
	r := &Resolver{index: map[string]model.ModelPrice{}}
	rows := []model.ModelPrice{
		{Model: "vendor/m1", PromptPrice: 1, Source: SourceOpenRouter},
		{Model: "m1", PromptPrice: 9, Source: SourceManual},
	}
	// 模拟 Reload 的索引构建规则
	for _, p := range rows {
		full := Normalize(p.Model)
		put := func(key string) {
			prev, ok := r.index[key]
			if !ok || sourceRank(p.Source) > sourceRank(prev.Source) {
				r.index[key] = p
			}
		}
		put(full)
		if short := shortName(full); short != full {
			put(short)
		}
	}
	p, ok := r.Resolve("m1")
	if !ok || p.Source != SourceManual || p.PromptPrice != 9 {
		t.Fatalf("手工价格未覆盖 OpenRouter: %+v ok=%v", p, ok)
	}
}

func closeEnough(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}