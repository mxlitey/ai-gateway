package pricing

import (
	"testing"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-sonnet-4":   "anthropic/claude-sonnet-4",
		"OpenAI/GPT-4o":               "openai/gpt-4o",
		"claude-3.5-sonnet":           "claude-3-5-sonnet",
		"claude_3_5_sonnet":           "claude-3-5-sonnet",
		"z-ai/glm-4.6:free":           "z-ai/glm-4-6",
		"qwen/qwen3-235b-a22b@latest": "qwen/qwen3-235b-a22b",
		"  DeepSeek/DeepSeek-R1  ":    "deepseek/deepseek-r1",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestResolveFallsBackToShortNameAndDateSuffix(t *testing.T) {
	rows := []model.ModelPrice{{
		Model: "anthropic/claude-sonnet-4",
		Tiers: []model.PriceTier{{PromptPrice: 3, CompletionPrice: 15}},
	}}
	r := &Resolver{index: buildIndex(rows)}

	for _, name := range []string{
		"anthropic/claude-sonnet-4",
		"Claude-Sonnet-4",
		"claude-sonnet-4-20250514",
		"claude_sonnet_4",
	} {
		if _, ok := r.Resolve(name); !ok {
			t.Fatalf("Resolve(%q) 未命中", name)
		}
	}
	if _, ok := r.Resolve("unknown-model"); ok {
		t.Fatal("未知模型不应命中")
	}
}

func TestBuildIndexKeepsRealModelOverAlias(t *testing.T) {
	// 短名与另一条真实条目同名时，真实条目优先
	rows := []model.ModelPrice{
		{Model: "vendor/m1", Tiers: []model.PriceTier{{PromptPrice: 1}}},
		{Model: "m1", Tiers: []model.PriceTier{{PromptPrice: 9}}},
	}
	r := &Resolver{index: buildIndex(rows)}
	p, ok := r.Resolve("m1")
	if !ok || !closeEnough(p.Tiers[0].PromptPrice, 9) {
		t.Fatalf("短名覆盖了真实条目: %+v ok=%v", p, ok)
	}
}

func TestEstimateSplitsCacheRead(t *testing.T) {
	p := model.ModelPrice{
		Tiers:          []model.PriceTier{{PromptPrice: 3, CompletionPrice: 15}},
		CacheReadPrice: 0.3,
	}
	// 100 万输入，其中 40 万命中缓存；100 万输出
	q := Estimate(p, Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, CachedTokens: 400_000})
	wantInput := 0.6*3 + 0.4*0.3
	wantOutput := 15.0
	if !closeEnough(q.Input, wantInput) || !closeEnough(q.Output, wantOutput) {
		t.Fatalf("输入/输出计费异常: input=%v output=%v", q.Input, q.Output)
	}
	if !closeEnough(q.Total, wantInput+wantOutput) {
		t.Fatalf("合计异常: %v", q.Total)
	}
}

func TestEstimatePicksTierByFreshInput(t *testing.T) {
	p := model.ModelPrice{
		Tiers: []model.PriceTier{
			{UpTo: 32_000, PromptPrice: 1, CompletionPrice: 4},
			{UpTo: 128_000, PromptPrice: 2, CompletionPrice: 8},
			{UpTo: 0, PromptPrice: 4, CompletionPrice: 16},
		},
	}

	cases := []struct {
		name   string
		tokens int
		tier   int
		prompt float64
	}{
		{"第一档", 32_000, 32_000, 1},
		{"跨档边界", 32_001, 128_000, 2},
		{"兜底档", 200_000, 0, 4},
	}
	for _, c := range cases {
		q := Estimate(p, Usage{PromptTokens: c.tokens})
		if q.TierUpTo != c.tier {
			t.Fatalf("%s: 命中档位 %d，期望 %d", c.name, q.TierUpTo, c.tier)
		}
		want := float64(c.tokens) * c.prompt / 1e6
		if !closeEnough(q.Input, want) {
			t.Fatalf("%s: 输入费用 %v，期望 %v", c.name, q.Input, want)
		}
	}
}

func TestEstimatePeakFactor(t *testing.T) {
	p := model.ModelPrice{
		Tiers:       []model.PriceTier{{PromptPrice: 10, CompletionPrice: 10}},
		TimeWindows: []model.TimeWindow{{Start: "00:30", End: "08:30", Factor: 0.5}},
	}
	// 北京时间 04:00 属于错峰
	offPeak := time.Date(2026, 1, 1, 4, 0, 0, 0, priceLocation)
	q := Estimate(p, Usage{PromptTokens: 1_000_000, At: offPeak})
	if !closeEnough(q.Factor, 0.5) || !closeEnough(q.Total, 5) {
		t.Fatalf("错峰折扣未生效: factor=%v total=%v", q.Factor, q.Total)
	}
	// 北京时间 12:00 全价
	peak := time.Date(2026, 1, 1, 12, 0, 0, 0, priceLocation)
	q = Estimate(p, Usage{PromptTokens: 1_000_000, At: peak})
	if !closeEnough(q.Factor, 1) || !closeEnough(q.Total, 10) {
		t.Fatalf("峰时不应打折: factor=%v total=%v", q.Factor, q.Total)
	}
}

func TestEstimateCrossMidnightWindow(t *testing.T) {
	windows := []model.TimeWindow{{Start: "22:00", End: "06:00", Factor: 0.25}}
	for _, hour := range []int{22, 23, 0, 5} {
		at := time.Date(2026, 1, 1, hour, 0, 0, 0, priceLocation)
		if f := peakFactor(windows, at); !closeEnough(f, 0.25) {
			t.Fatalf("%d 点应命中跨午夜窗口，实际系数 %v", hour, f)
		}
	}
	for _, hour := range []int{6, 12, 21} {
		at := time.Date(2026, 1, 1, hour, 0, 0, 0, priceLocation)
		if f := peakFactor(windows, at); !closeEnough(f, 1) {
			t.Fatalf("%d 点不该命中窗口，实际系数 %v", hour, f)
		}
	}
}

func TestEstimateWeekendOnlyWindow(t *testing.T) {
	// 2026-01-03 是周六，2026-01-05 是周一
	windows := []model.TimeWindow{{Start: "00:00", End: "24:00", Factor: 0.6, Days: []int{0, 6}}}
	sat := time.Date(2026, 1, 3, 12, 0, 0, 0, priceLocation)
	if f := peakFactor(windows, sat); !closeEnough(f, 0.6) {
		t.Fatalf("周六应命中周末价，实际系数 %v", f)
	}
	mon := time.Date(2026, 1, 5, 12, 0, 0, 0, priceLocation)
	if f := peakFactor(windows, mon); !closeEnough(f, 1) {
		t.Fatalf("周一不该命中周末价，实际系数 %v", f)
	}
}

func TestEstimatePerRequest(t *testing.T) {
	p := model.ModelPrice{Unit: model.PriceUnitRequest, RequestPrice: 0.04}
	q := Estimate(p, Usage{})
	if !closeEnough(q.Total, 0.04) || !closeEnough(q.Input, 0) || !closeEnough(q.Output, 0.04) {
		t.Fatalf("按次计费异常: %+v", q)
	}
	// 即使带了 token 用量也按次计费，不受 token 影响
	q = Estimate(p, Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000})
	if !closeEnough(q.Total, 0.04) {
		t.Fatalf("按次计费不应叠加 token 费用: %+v", q)
	}
}

func closeEnough(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
