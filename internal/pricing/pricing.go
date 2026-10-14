// Package pricing 提供模型价格表：从 OpenRouter 拉取快照、归一化匹配与费用估算。
//
// 价格单位统一为「美元 / 百万 token」（USD per 1M tokens），
// 与 OpenRouter / 各家官网报价口径一致，避免逐 token 小数带来的误读。
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// DefaultEndpoint OpenRouter 模型清单接口。
const DefaultEndpoint = "https://openrouter.ai/api/v1/models"

// Endpoint 返回模型清单地址，可用环境变量 AIGW_PRICE_ENDPOINT 覆盖（便于内网镜像）。
func Endpoint() string {
	if v := strings.TrimSpace(os.Getenv("AIGW_PRICE_ENDPOINT")); v != "" {
		return v
	}
	return DefaultEndpoint
}

// SourceOpenRouter / SourceManual 价格来源。
const (
	SourceOpenRouter = "openrouter"
	SourceManual     = "manual"
	SourceNone       = "none"
)

// 价格条目在内存索引中的来源优先级：手工 > OpenRouter。
func sourceRank(source string) int {
	switch source {
	case SourceManual:
		return 2
	case SourceOpenRouter:
		return 1
	default:
		return 0
	}
}

// Resolver 价格解析器：内存索引 + 落库快照。
type Resolver struct {
	store *store.Store

	mu    sync.RWMutex
	index map[string]model.ModelPrice // 归一化模型名 -> 价格
	count int
}

// New 创建解析器（需随后调用 Reload 载入索引）。
func New(s *store.Store) *Resolver {
	return &Resolver{store: s, index: map[string]model.ModelPrice{}}
}

// Reload 从数据库重建内存索引。
func (r *Resolver) Reload() error {
	rows, err := r.store.ListModelPrices()
	if err != nil {
		return err
	}

	idx := make(map[string]model.ModelPrice, len(rows)*2)
	put := func(key string, p model.ModelPrice) {
		if key == "" {
			return
		}
		prev, ok := idx[key]
		if !ok {
			idx[key] = p
			return
		}
		// 优先级更高的来源覆盖；同来源取更便宜的一条（估算偏保守）
		if sourceRank(p.Source) > sourceRank(prev.Source) {
			idx[key] = p
			return
		}
		if sourceRank(p.Source) == sourceRank(prev.Source) && p.PromptPrice < prev.PromptPrice {
			idx[key] = p
		}
	}

	for i := range rows {
		p := rows[i]
		full := Normalize(p.Model)
		if full == "" {
			continue
		}
		put(full, p)
		// 同时用「去掉厂商前缀」的短名建索引，便于 openrouter id 与各家原生名互认
		if short := shortName(full); short != full {
			put(short, p)
		}
	}

	r.mu.Lock()
	r.index = idx
	r.count = len(rows)
	r.mu.Unlock()
	return nil
}

// Count 返回当前数据库中的价格条目数。
func (r *Resolver) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Resolve 解析模型价格，第二返回值为是否命中。
func (r *Resolver) Resolve(modelName string) (model.ModelPrice, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, key := range candidates(modelName) {
		if p, ok := r.index[key]; ok {
			return p, true
		}
	}
	return model.ModelPrice{}, false
}

// Estimate 估算一次用量的费用（美元）。
// 缓存命中的输入 token 按 cache_read_price 计价，其余输入按 prompt_price。
func Estimate(p model.ModelPrice, promptTokens, completionTokens, cachedTokens int) (total, input, output float64) {
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	fresh := promptTokens - cachedTokens

	input = float64(fresh) * p.PromptPrice / 1e6
	if p.CacheReadPrice > 0 {
		input += float64(cachedTokens) * p.CacheReadPrice / 1e6
	} else {
		input += float64(cachedTokens) * p.PromptPrice / 1e6
	}
	output = float64(completionTokens) * p.CompletionPrice / 1e6
	return input + output, input, output
}

// ---------- 名称归一化与匹配 ----------

var (
	reDateSuffix = regexp.MustCompile(`-(20\d{2}-?\d{2}-?\d{2})$`)
	reMultiDash  = regexp.MustCompile(`-{2,}`)
)

// Normalize 把模型名归一化为匹配键：小写、统一分隔符、去掉 :: 变体与日期后缀。
func Normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if i := strings.LastIndex(s, ":"); i >= 0 { // 去掉 openrouter 的 :free / :nitro 变体
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 { // 去掉 @版本 后缀
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = reMultiDash.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// shortName 去掉 openrouter 的厂商前缀（anthropic/claude-x -> claude-x）。
func shortName(normalized string) string {
	if i := strings.LastIndex(normalized, "/"); i >= 0 {
		return normalized[i+1:]
	}
	return normalized
}

// candidates 生成按优先级排列的候选匹配键。
func candidates(modelName string) []string {
	n := Normalize(modelName)
	if n == "" {
		return nil
	}
	out := []string{n}
	if short := shortName(n); short != n {
		out = append(out, short)
	}
	if t := reDateSuffix.ReplaceAllString(n, ""); t != n && t != "" {
		out = append(out, t)
		if short := shortName(t); short != t && short != "" {
			out = append(out, short)
		}
	}
	if t := strings.TrimSuffix(n, "-latest"); t != n {
		out = append(out, t)
	}
	return out
}

// ---------- OpenRouter 拉取 ----------

// Fetch 拉取 OpenRouter 模型清单并转换为价格条目（USD / 百万 token）。
func Fetch(ctx context.Context, client *http.Client, endpoint string) ([]model.ModelPrice, error) {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 OpenRouter 失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("OpenRouter 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return ParseOpenRouter(raw)
}

// ParseOpenRouter 解析 OpenRouter /api/v1/models 响应。
func ParseOpenRouter(raw []byte) ([]model.ModelPrice, error) {
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("解析 OpenRouter 响应失败: %w", err)
	}

	now := time.Now().UnixMilli()
	out := make([]model.ModelPrice, 0, len(payload.Data))
	seen := map[string]bool{}

	for _, item := range payload.Data {
		id, _ := item["id"].(string)
		key := Normalize(id)
		if key == "" || seen[key] {
			continue
		}

		pricing, _ := item["pricing"].(map[string]any)
		prompt := perMillion(pricing["prompt"])
		completion := perMillion(pricing["completion"])
		cacheRead := perMillion(pricing["input_cache_read"])
		if cacheRead == 0 {
			cacheRead = perMillion(pricing["cached_prompt"])
		}
		// 全 0 视为免费/无定价条目，跳过以免污染价格表
		if prompt == 0 && completion == 0 {
			continue
		}

		name, _ := item["name"].(string)
		ctxLen := int(toFloat(item["context_length"]))
		if ctxLen == 0 {
			if tp, ok := item["top_provider"].(map[string]any); ok {
				ctxLen = int(toFloat(tp["context_length"]))
			}
		}

		seen[key] = true
		out = append(out, model.ModelPrice{
			Model:           key,
			DisplayName:     strings.TrimSpace(name),
			PromptPrice:     prompt,
			CompletionPrice: completion,
			CacheReadPrice:  cacheRead,
			ContextLength:   ctxLen,
			Source:          SourceOpenRouter,
			UpdatedAt:       now,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// Refresh 拉取最新价格并整体替换 openrouter 来源的条目，随后重建索引。
func (r *Resolver) Refresh(ctx context.Context, client *http.Client, endpoint string) (int, error) {
	prices, err := Fetch(ctx, client, endpoint)
	if err != nil {
		return 0, err
	}
	n, err := r.store.ReplacePricesOfSource(SourceOpenRouter, prices)
	if err != nil {
		return 0, err
	}
	if err := r.Reload(); err != nil {
		return n, err
	}
	_ = r.store.MarkDone(store.SettingPriceRefreshAt)
	return n, nil
}

// RefreshIfDue 后台惰性刷新：距上次成功刷新超过 interval 时才拉取。
func (r *Resolver) RefreshIfDue(ctx context.Context, client *http.Client, endpoint string, interval time.Duration) {
	if r.store == nil || !r.store.DueSince(store.SettingPriceRefreshAt, interval) {
		return
	}
	n, err := r.Refresh(ctx, client, endpoint)
	if err != nil {
		slog.Warn("[pricing] 价格快照刷新失败（将沿用现有价格表）", "err", err)
		return
	}
	slog.Info("[pricing] 价格快照已更新", "models", n)
}

// perMillion 把 OpenRouter 的「USD / token」转为「USD / 百万 token」。
func perMillion(v any) float64 {
	return toFloat(v) * 1e6
}

// toFloat 兼容字符串与数字两种 JSON 表示。
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}