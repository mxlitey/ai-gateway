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
	// DisabledModels 在本渠道上被单独停用的公开模型名；命中的模型不参与路由。
	DisabledModels []string `json:"disabled_models"`
	Headers        []Header `json:"headers"`
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

// IsModelDisabled 判断某个公开模型是否在本渠道上被单独停用。
func (c *Channel) IsModelDisabled(publicModel string) bool {
	for _, m := range c.DisabledModels {
		if m == publicModel {
			return true
		}
	}
	return false
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
	ID                int64   `json:"id"`
	CreatedAt         int64   `json:"created_at"`
	APIKeyID          string  `json:"api_key_id"`
	APIKeyName        string  `json:"api_key_name"`
	ChannelID         string  `json:"channel_id"`
	ChannelName       string  `json:"channel_name"`
	Endpoint          string  `json:"endpoint"`
	Stream            bool    `json:"stream"`
	Model             string  `json:"model"`
	UpstreamModel     string  `json:"upstream_model"`
	UpstreamKeyHint   string  `json:"upstream_key_hint"`
	Status            int     `json:"status"`
	Success           bool    `json:"success"`
	Attempts          int     `json:"attempts"`
	DurationMS        int64   `json:"duration_ms"`
	PromptTokens      int     `json:"prompt_tokens"`
	CompletionTokens  int     `json:"completion_tokens"`
	TotalTokens       int     `json:"total_tokens"`
	CachedTokens      int     `json:"cached_tokens"`
	ReasoningTokens   int     `json:"reasoning_tokens"`
	CostPromptUSD     float64 `json:"cost_prompt_usd"`
	CostCompletionUSD float64 `json:"cost_completion_usd"`
	CostUSD           float64 `json:"cost_usd"`
	PriceModel        string  `json:"price_model"`
	PriceSource       string  `json:"price_source"`
	Error             string  `json:"error"`
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

// MCPKey 一个 MCP 上游服务器下的密钥。
//
// 与渠道密钥的区别：MCP 服务器通常直接接受一个 Token，因此这里额外支持
// 「每日调用次数上限」，用于给不同密钥分配不同的用量额度
// （-1 表示不限，0 表示不允许调用，正数为上限）。
type MCPKey struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	// Enabled 是否参与选号。
	Enabled bool `json:"enabled"`
	// DailyLimit 该密钥每日计费调用次数上限：-1 不限，0 不允许调用，正数为上限。
	DailyLimit int `json:"daily_limit"`
}

// MCPServer 一个聚合的上游 MCP 服务器（Streamable HTTP）。
type MCPServer struct {
	ID string `json:"id"`
	// Name 展示名，同时也是默认工具命名空间前缀。
	Name string `json:"name"`
	// Alias 工具命名空间前缀（对外工具名为 alias__tool）；留空时由 Name 推导。
	Alias string `json:"alias"`
	// URL Streamable HTTP 端点（MCP 客户端视角的服务地址）。
	URL     string   `json:"url"`
	Headers []Header `json:"headers"`
	Keys    []MCPKey `json:"keys"`
	// KeyStrategy 多密钥选用策略，见 KeyStrategy* 常量。
	KeyStrategy string `json:"key_strategy"`
	// DailyLimit 整台服务器每日计费调用上限：-1 不限，0 不允许调用，正数为上限。
	DailyLimit int `json:"daily_limit"`
	// ToolLimits 工具级每日计费调用上限（上游工具名 -> 上限），用于不同工具分配不同配额；
	// -1 或未列出的工具表示不限，0 表示该工具不允许调用。
	ToolLimits map[string]int `json:"tool_limits"`
	// BillableMode 计费口径：success 表示成功调用即计费；marker 表示响应含 BillableMarker 才计费。
	// 后者用于上游「判定无需联网就直接作答、不产生计费」的场景（如百度 AI 搜索）。
	BillableMode string `json:"billable_mode"`
	// BillableMarker 计费标记：marker 模式下响应文本包含它才算一次计费调用。
	BillableMarker string `json:"billable_marker"`
	// TimeoutMS 单次工具调用超时（毫秒）。
	TimeoutMS int    `json:"timeout_ms"`
	Enabled   bool   `json:"enabled"`
	Source    string `json:"source"`
	SortOrder int64  `json:"sort_order"`
	CreatedAt int64  `json:"created_at"`

	// 以下为运行时派生字段，不落库。
	ToolCount       int    `json:"tool_count"`
	LastStatus      string `json:"last_status"`
	LastError       string `json:"last_error"`
	DailyCalls      int64  `json:"daily_calls"`
	DailyLimitTotal int64  `json:"daily_limit_total"`
	// KeyDailyCalls 各密钥当天的计费调用次数（key_hint -> 次数），面板展示用。
	KeyDailyCalls map[string]int64 `json:"key_daily_calls"`
	// ToolDailyCalls 各工具当天的计费调用次数（上游工具名 -> 次数），面板展示用。
	ToolDailyCalls map[string]int64 `json:"tool_daily_calls"`
}

// IsManual 判断服务器是否由用户手工添加（非插件注册）。
func (s *MCPServer) IsManual() bool { return s.Source == "" || s.Source == "manual" }

// EnabledKeys 返回已启用的密钥值。
func (s *MCPServer) EnabledKeys() []string {
	out := make([]string, 0, len(s.Keys))
	for _, k := range s.Keys {
		if k.Enabled && strings.TrimSpace(k.Key) != "" {
			out = append(out, k.Key)
		}
	}
	return out
}

// MCPTool 一条缓存的工具定义。
type MCPTool struct {
	ServerID    string `json:"server_id"`
	Name        string `json:"name"`
	ExposedName string `json:"exposed_name"`
	// Raw 上游返回的原始工具对象（已包含 name / description / inputSchema 等字段）。
	Raw       string `json:"-"`
	UpdatedAt int64  `json:"updated_at"`
}

// MCP 计费口径：决定一次成功调用是否计入配额与「调用次数」。
const (
	// MCPBillableSuccess 成功调用即计费（默认）。
	MCPBillableSuccess = "success"
	// MCPBillableMarker 仅当响应文本包含标记串时计费。
	MCPBillableMarker = "marker"
)

// NormalizeBillableMode 归一化计费口径，未识别值回退为「成功即计费」。
func NormalizeBillableMode(s string) string {
	if strings.TrimSpace(s) == MCPBillableMarker {
		return MCPBillableMarker
	}
	return MCPBillableSuccess
}

// MCPCallLog 一次工具调用的结果。
type MCPCallLog struct {
	ID         int64  `json:"id"`
	CreatedAt  int64  `json:"created_at"`
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name"`
	ServerURL  string `json:"server_url"`
	Tool       string `json:"tool"`
	KeyHint    string `json:"key_hint"`
	Success    bool   `json:"success"`
	// Billable 本次调用是否计入计费次数（marker 模式下未触发上游计费时为 false）。
	Billable   bool   `json:"billable"`
	HTTPStatus int    `json:"http_status"`
	Attempts   int    `json:"attempts"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error"`
}

// MCPUsageBucket 按服务器 / 工具分组的调用统计。
type MCPUsageBucket struct {
	Key      string `json:"key"`
	Calls    int64  `json:"calls"`
	Success  int64  `json:"success"`
	Failures int64  `json:"failures"`
}

// MCPKeyUsage 单个密钥的调用统计。
//
// 密钥以脱敏值（KeyHint）为标识，同一台服务器内唯一；Label / DailyLimit /
// Enabled 来自当前配置，用于把统计结果直接展示成「这把我们用了多少」。
type MCPKeyUsage struct {
	ServerID   string `json:"server_id"`
	ServerName string `json:"server_name"`
	KeyHint    string `json:"key_hint"`
	Label      string `json:"label"`
	Calls      int64  `json:"calls"`
	Success    int64  `json:"success"`
	Failures   int64  `json:"failures"`
	DailyLimit int    `json:"daily_limit"`
	Enabled    bool   `json:"enabled"`
	// Unlimited 该密钥没有单独的每日上限（是否受服务器级上限约束由前端另行提示）。
	Unlimited bool `json:"unlimited"`
}

// MCPUsageSummary 时间区间内的 MCP 调用汇总。
type MCPUsageSummary struct {
	From     int64 `json:"from"`
	To       int64 `json:"to"`
	Calls    int64 `json:"calls"`
	Success  int64 `json:"success"`
	Failures int64 `json:"failures"`
	// Billable 计入上游计费的调用次数（marker 口径下可能小于 Success）。
	Billable int64            `json:"billable"`
	ByServer []MCPUsageBucket `json:"by_server"`
	ByTool   []MCPUsageBucket `json:"by_tool"`
	ByKey    []MCPKeyUsage    `json:"by_key"`
	ByDay    []MCPUsageBucket `json:"by_day"`
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
