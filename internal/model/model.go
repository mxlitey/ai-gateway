// Package model 定义领域模型与其派生行为。
package model

import "strings"

// ChannelKey 渠道下的一条上游密钥。
type ChannelKey struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
	// Label 可选展示名；插件账号同步来的密钥用它显示账号名，Key 即账号 id。
	Label string `json:"label,omitempty"`
}

// 渠道多密钥的选用策略。
const (
	// KeyStrategyRotate 轮询：按渠道依次轮换首选密钥（K1→K2→K3→K1…），均衡分摊流量。
	KeyStrategyRotate = "rotate"
	// KeyStrategyOrdered 故障转移：按配置顺序依次尝试，前一个失败才用下一个。
	KeyStrategyOrdered = "ordered"
)

// NormalizeKeyStrategy 归一化密钥选用策略，未识别值一律回退为轮询。
func NormalizeKeyStrategy(s string) string {
	if strings.TrimSpace(s) == KeyStrategyOrdered {
		return KeyStrategyOrdered
	}
	return KeyStrategyRotate
}

// Header 渠道自定义请求头。
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Channel 一个上游渠道。
type Channel struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	BaseURL       string            `json:"base_url"`
	Path          string            `json:"path"`
	Keys          []ChannelKey      `json:"keys"`
	Models        []string          `json:"models"`
	ModelMap      map[string]string `json:"model_map"`
	ModelPriority map[string]int    `json:"model_priority"`
	Headers       []Header          `json:"headers"`
	// KeyStrategy 多密钥选用策略，见 KeyStrategy* 常量；空值按轮询处理。
	KeyStrategy string `json:"key_strategy"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
	SortOrder   int64  `json:"sort_order"`
	CreatedAt   int64  `json:"created_at"`
}

// IsPluginChannel 判断渠道是否由插件注册。
func (c *Channel) IsPluginChannel() bool {
	return strings.HasPrefix(c.Source, "plugin:")
}

// EnabledKeys 返回渠道中已启用的密钥（值为纯字符串）。
func (c *Channel) EnabledKeys() []string {
	out := make([]string, 0, len(c.Keys))
	for _, k := range c.Keys {
		if k.Enabled && strings.TrimSpace(k.Key) != "" {
			out = append(out, k.Key)
		}
	}
	return out
}

// ResolveUpstreamModel 解析公开模型名对应的上游模型名。
// 优先 model_map；未命中时若 models 含同名项则同名透传。
func (c *Channel) ResolveUpstreamModel(publicModel string) (string, bool) {
	if um, ok := c.ModelMap[publicModel]; ok {
		if s := strings.TrimSpace(um); s != "" {
			return s, true
		}
	}
	for _, m := range c.Models {
		if m == publicModel {
			return publicModel, true
		}
	}
	return "", false
}

// Priority 返回「公开模型 -> 本渠道」的优先级，缺省 0。
func (c *Channel) Priority(publicModel string) int {
	if c.ModelPriority == nil {
		return 0
	}
	if p, ok := c.ModelPriority[publicModel]; ok {
		return p
	}
	return 0
}

// APIKey 客户端访问密钥。
type APIKey struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Key        string   `json:"key"`
	ChannelIDs []string `json:"channel_ids"`
	Enabled    bool     `json:"enabled"`
	CreatedAt  int64    `json:"created_at"`
}

// ErrorLog 一条上游错误记录。
type ErrorLog struct {
	ID            int64  `json:"id"`
	ChannelID     string `json:"channel_id"`
	ChannelName   string `json:"channel_name"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
	Status        int    `json:"status"`
	KeyHint       string `json:"key_hint"`
	Message       string `json:"message"`
	CreatedAt     int64  `json:"created_at"`
}

// RequestLog 一次 /v1 调用的最终结果（含用量与估算费用）。
// 同一次请求的多目标故障转移只落一条记录，attempts 表示尝试过的目标数。
type RequestLog struct {
	ID              int64   `json:"id"`
	CreatedAt       int64   `json:"created_at"`
	APIKeyID        string  `json:"api_key_id"`
	APIKeyName      string  `json:"api_key_name"`
	ChannelID       string  `json:"channel_id"`
	ChannelName     string  `json:"channel_name"`
	Endpoint        string  `json:"endpoint"`
	Stream          bool    `json:"stream"`
	Model           string  `json:"model"`
	UpstreamModel   string  `json:"upstream_model"`
	UpstreamKeyHint string  `json:"upstream_key_hint"`
	Status          int     `json:"status"`
	Success         bool    `json:"success"`
	Attempts        int     `json:"attempts"`
	DurationMS      int64   `json:"duration_ms"`
	PromptTokens    int     `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens     int     `json:"total_tokens"`
	CachedTokens    int     `json:"cached_tokens"`
	ReasoningTokens int     `json:"reasoning_tokens"`
	CostPromptUSD   float64 `json:"cost_prompt_usd"`
	CostCompletionUSD float64 `json:"cost_completion_usd"`
	CostUSD         float64 `json:"cost_usd"`
	PriceModel      string  `json:"price_model"`
	PriceSource     string  `json:"price_source"`
	Error           string  `json:"error"`
}

// ModelPrice 模型价格条目，价格为「美元 / 百万 token」。
type ModelPrice struct {
	Model           string  `json:"model"`
	DisplayName     string  `json:"display_name"`
	PromptPrice     float64 `json:"prompt_price"`
	CompletionPrice float64 `json:"completion_price"`
	CacheReadPrice  float64 `json:"cache_read_price"`
	ContextLength   int     `json:"context_length"`
	Source          string  `json:"source"`
	UpdatedAt       int64   `json:"updated_at"`
}

// UsageBucket 用量汇总的一个分组。
type UsageBucket struct {
	Key              string  `json:"key"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// UsageSummary 时间区间内的用量与费用汇总。
type UsageSummary struct {
	From             int64         `json:"from"`
	To               int64         `json:"to"`
	Requests         int64         `json:"requests"`
	SuccessRequests  int64         `json:"success_requests"`
	FailedRequests   int64         `json:"failed_requests"`
	UnpricedRequests int64         `json:"unpriced_requests"`
	PromptTokens     int64         `json:"prompt_tokens"`
	CompletionTokens int64         `json:"completion_tokens"`
	TotalTokens      int64         `json:"total_tokens"`
	CachedTokens     int64         `json:"cached_tokens"`
	CostUSD          float64       `json:"cost_usd"`
	ByModel          []UsageBucket `json:"by_model"`
	ByChannel        []UsageBucket `json:"by_channel"`
	ByAPIKey         []UsageBucket `json:"by_api_key"`
	// ByKey 按上游密钥提示（脱敏值）分组，用于填充「上游密钥」筛选下拉框
	ByKey []UsageBucket `json:"by_key"`
	ByDay []UsageBucket `json:"by_day"`
}

// Plugin 插件实例。
type Plugin struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Dir         string `json:"dir"`
	Port        int    `json:"port"`
	Enabled     bool   `json:"enabled"`
	Autostart   bool   `json:"autostart"`
	LastStatus  string `json:"last_status"`
	LastError   string `json:"last_error"`
	LastSeenAt  int64  `json:"last_seen_at"`
	InstalledAt int64  `json:"installed_at"`

	// 控制台（plugin.json 的 console / capabilities 段）
	Console         bool     `json:"console"`
	ConsoleTitle    string   `json:"console_title"`
	ConsoleBasePath string   `json:"console_base_path"`
	Capabilities    []string `json:"capabilities"`
}

// KeyHint 对密钥做脱敏，形如 abcd123...wxyz。
func KeyHint(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:7] + "..." + key[len(key)-4:]
}

// MaskKey 读取接口使用的脱敏形式；未超长时整体保留。
func MaskKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:7] + "..." + key[len(key)-4:]
}