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

// 生图渠道的工作模式。
const (
	// ImageGenSync 同步型：一次请求直接返回图片。
	ImageGenSync = "sync"
	// ImageGenAsync 异步型：先提交任务拿到 task_id，再轮询任务结果。
	ImageGenAsync = "async"
)

// response_format 的处理策略。
const (
	// ImageFormatDrop 不把 response_format 传给上游（默认，兼容不认识该字段的服务商）。
	ImageFormatDrop = "drop"
	// ImageFormatPass 原样透传客户端传入的 response_format。
	ImageFormatPass = "pass"
	// ImageFormatURL 强制要求上游返回图片地址。
	ImageFormatURL = "url"
	// ImageFormatB64 强制要求上游返回 base64。
	ImageFormatB64 = "b64_json"
)

// 参考图在上游请求体里的形态。
const (
	// ImageRefURLArray {"image_url":["u1","u2"]}，默认。
	ImageRefURLArray = "url_array"
	// ImageRefSingleURL {"image_url":"u1"}。
	ImageRefSingleURL = "single_url"
	// ImageRefURLObjectArray {"image_url":[{"url":"u1"}]}。
	ImageRefURLObjectArray = "url_object_array"
	// ImageRefImageURLObjectArray {"images":[{"image_url":"u1"}]}。
	ImageRefImageURLObjectArray = "image_url_object_array"
)

// ImageGenConfig 渠道级生图配置。
//
// 生图与对话是两套上游协议，但对外都收敛为 OpenAI 兼容的 /v1/images/generations。
// 这里把「怎么调上游」全部放进渠道配置，新增服务商时只需改配置，不必改核心代码。
type ImageGenConfig struct {
	// Mode 生图模式：空=不支持，sync=同步，async=异步任务。
	Mode string `json:"mode"`
	// Path 提交路径，默认 /images/generations（相对渠道 base_url）。
	Path string `json:"path"`
	// PollPath 异步任务轮询路径，默认 /tasks/{task_id}，{task_id} 会被替换。
	PollPath string `json:"poll_path"`
	// SubmitHeaders / PollHeaders 仅用于生图提交与轮询的附加请求头（渠道公共头仍会生效）。
	SubmitHeaders []Header `json:"submit_headers"`
	PollHeaders   []Header `json:"poll_headers"`
	// PollIntervalMS 轮询间隔（毫秒），默认 5000。
	PollIntervalMS int `json:"poll_interval_ms"`
	// TimeoutMS 异步任务整体超时（毫秒），默认 360000。
	TimeoutMS int `json:"timeout_ms"`
	// 异步任务响应字段名，默认分别对应 ModelScope 的 task_id / task_status / SUCCEED / FAILED。
	TaskIDField   string `json:"task_id_field"`
	StatusField   string `json:"status_field"`
	SuccessStatus string `json:"success_status"`
	FailedStatus  string `json:"failed_status"`
	// ImagesField 图片列表字段名（同步/异步共用），默认 output_images。
	// 支持以 . 分隔的路径与 [] 表示遍历数组，例如 data[].url、result.images[].b64_json。
	ImagesField string `json:"images_field"`
	// ErrorField 失败原因字段名，默认 error，同样支持路径。
	ErrorField string `json:"error_field"`

	// SyncTimeoutMS 同步生图等待上游响应的总超时（毫秒）。
	// 0 表示沿用 timeout_ms。仅 sync 模式生效，异步轮询仍由 timeout_ms 控制。
	SyncTimeoutMS int `json:"sync_timeout_ms"`
	// ResponseFormatMode response_format 的处理策略：drop（默认）/ pass / url / b64_json。
	ResponseFormatMode string `json:"response_format_mode"`
	// ParamMap 客户端参数名到上游参数名的映射，例如 {"size":"image_size"}。
	ParamMap map[string]string `json:"param_map"`
	// StaticParams 固定附加到上游请求体的参数；客户端已显式传入的同名字段不会被覆盖。
	StaticParams map[string]any `json:"static_params"`
	// DropParams 从上游请求体中删除的参数名。
	DropParams []string `json:"drop_params"`
	// EditPath 带参考图时的提交路径；为空表示仍用 path。
	EditPath string `json:"edit_path"`
	// ReferenceField 参考图在上游请求体里的字段名，默认 image_url。
	ReferenceField string `json:"reference_field"`
	// ReferenceShape 参考图形态，默认 url_array；另支持 single_url、url_object_array、
	// image_url_object_array。
	ReferenceShape string `json:"reference_shape"`
}

// Normalized 补齐生图配置的默认值并归一化模式。
func (g ImageGenConfig) Normalized() ImageGenConfig {
	mode := strings.ToLower(strings.TrimSpace(g.Mode))
	switch mode {
	case ImageGenSync, ImageGenAsync:
		g.Mode = mode
	default:
		g.Mode = ""
	}

	g.Path = normalizePath(g.Path, "/images/generations")
	g.PollPath = normalizePath(g.PollPath, "/tasks/{task_id}")
	if g.PollIntervalMS <= 0 {
		g.PollIntervalMS = 5000
	}
	if g.PollIntervalMS < 100 {
		g.PollIntervalMS = 100
	}
	if g.TimeoutMS <= 0 {
		// 异步生图常需 3-5 分钟（ModelScope Qwen 实测可达 244 秒），默认给 6 分钟
		g.TimeoutMS = 360000
	}
	if g.TimeoutMS < 1000 {
		g.TimeoutMS = 1000
	}
	g.TaskIDField = defaultField(g.TaskIDField, "task_id")
	g.StatusField = defaultField(g.StatusField, "task_status")
	g.SuccessStatus = defaultField(g.SuccessStatus, "SUCCEED")
	g.FailedStatus = defaultField(g.FailedStatus, "FAILED")
	g.ImagesField = defaultField(g.ImagesField, "output_images")
	g.ErrorField = defaultField(g.ErrorField, "error")

	g.EditPath = normalizePath(g.EditPath, "")
	// SyncTimeoutMS 留 0 表示沿用 timeout_ms，避免把默认值写回配置、让面板无法区分“未设置”。
	if g.SyncTimeoutMS > 0 && g.SyncTimeoutMS < 1000 {
		g.SyncTimeoutMS = 1000
	}
	if g.SyncTimeoutMS > 1800000 {
		g.SyncTimeoutMS = 1800000
	}
	switch strings.ToLower(strings.TrimSpace(g.ResponseFormatMode)) {
	case ImageFormatPass:
		g.ResponseFormatMode = ImageFormatPass
	case ImageFormatURL:
		g.ResponseFormatMode = ImageFormatURL
	case ImageFormatB64:
		g.ResponseFormatMode = ImageFormatB64
	default:
		g.ResponseFormatMode = ImageFormatDrop
	}
	if g.ParamMap == nil {
		g.ParamMap = map[string]string{}
	}
	if g.StaticParams == nil {
		g.StaticParams = map[string]any{}
	}
	if g.DropParams == nil {
		g.DropParams = []string{}
	}
	g.ReferenceField = defaultField(g.ReferenceField, "image_url")
	switch strings.ToLower(strings.TrimSpace(g.ReferenceShape)) {
	case ImageRefSingleURL:
		g.ReferenceShape = ImageRefSingleURL
	case ImageRefURLObjectArray:
		g.ReferenceShape = ImageRefURLObjectArray
	case ImageRefImageURLObjectArray:
		g.ReferenceShape = ImageRefImageURLObjectArray
	default:
		g.ReferenceShape = ImageRefURLArray
	}
	if g.SubmitHeaders == nil {
		g.SubmitHeaders = []Header{}
	}
	if g.PollHeaders == nil {
		g.PollHeaders = []Header{}
	}
	return g
}

// SupportsImageGen 渠道是否配置为生图渠道（sync 或 async）。
func (c *Channel) SupportsImageGen() bool {
	return c != nil && c.ImageGen.Normalized().Mode != ""
}

// ImageConfig 返回补齐默认值后的生图配置。
func (c *Channel) ImageConfig() ImageGenConfig {
	if c == nil {
		return ImageGenConfig{}.Normalized()
	}
	return c.ImageGen.Normalized()
}

// IsImageModel 判断公开模型是否在本渠道上被标记为生图模型。
func (c *Channel) IsImageModel(publicModel string) bool {
	if c == nil {
		return false
	}
	for _, m := range c.ImageModels {
		if strings.TrimSpace(m) == publicModel {
			return true
		}
	}
	return false
}

func normalizePath(p, def string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return def
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

func defaultField(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
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
	// ImageModels 本渠道上被标记为生图的公开模型名，只能走 /v1/images/generations。
	ImageModels []string `json:"image_models"`
	// ImageGen 渠道级生图配置（同步/异步）。
	ImageGen ImageGenConfig `json:"image_generation"`
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
	// 只登记在生图模型列表里的模型同样按同名透传
	for _, m := range c.ImageModels {
		if strings.TrimSpace(m) == publicModel {
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

// 密钥权限范围取值：all=全部可用，selected=仅勾选项，none=完全不授权。
// 用显式范围而不是「空列表代表全部」，才能表达「一个都不给」。
const (
	ScopeAll      = "all"
	ScopeSelected = "selected"
	ScopeNone     = "none"
)

// APIKey 客户端访问密钥。
type APIKey struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Key          string   `json:"key"`
	ChannelScope string   `json:"channel_scope"`
	ChannelIDs   []string `json:"channel_ids"`
	MCPScope     string   `json:"mcp_scope"`
	MCPServerIDs []string `json:"mcp_server_ids"`
	Enabled      bool     `json:"enabled"`
	CreatedAt    int64    `json:"created_at"`
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

// RequestLog 一次「网关 → 上游」请求的最终结果（含用量与估算费用）。
// 每次故障转移都实际请求了一次上游，因此分别落一条记录；网关级拒绝不记录。
type RequestLog struct {
	ID               int64   `json:"id"`
	CreatedAt        int64   `json:"created_at"`
	APIKeyID         string  `json:"api_key_id"`
	APIKeyName       string  `json:"api_key_name"`
	ChannelID        string  `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	Endpoint         string  `json:"endpoint"`
	Stream           bool    `json:"stream"`
	Model            string  `json:"model"`
	UpstreamModel    string  `json:"upstream_model"`
	UpstreamKeyHint  string  `json:"upstream_key_hint"`
	Status           int     `json:"status"`
	Success          bool    `json:"success"`
	Attempts         int     `json:"attempts"`
	DurationMS       int64   `json:"duration_ms"`
	FirstTokenMS     int64   `json:"first_token_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	CostPrompt       float64 `json:"cost_prompt"`
	CostCompletion   float64 `json:"cost_completion"`
	Cost             float64 `json:"cost"`
	PriceModel       string  `json:"price_model"`
	PriceSource      string  `json:"price_source"`
	Error            string  `json:"error"`
}

// 价格单位。
const (
	PriceUnitToken   = "per_token"   // 按 token 计费（对话、嵌入等）
	PriceUnitRequest = "per_request" // 按请求次数计费（生图等按次收费的端点）
)

// PriceTier 输入档位：输入 token 不超过 UpTo 时适用这一档单价。
//
// UpTo 为 0 表示该档没有上限，必须作为兜底档排在最后。
type PriceTier struct {
	UpTo            int     `json:"up_to"`
	PromptPrice     float64 `json:"prompt_price"`
	CompletionPrice float64 `json:"completion_price"`
}

// TimeWindow 时段系数：北京时间落在 [Start, End) 内的请求乘以 Factor。
//
// Factor 小于 1 表示错峰折扣（如 0.5 五折），大于 1 表示高峰加价；
// Start 大于 End 时跨午夜（如 22:00-06:00）。
// Days 限定适用的星期，0=周日 … 6=周六；为空表示每天都适用。
type TimeWindow struct {
	Start  string  `json:"start"`
	End    string  `json:"end"`
	Factor float64 `json:"factor"`
	Days   []int   `json:"days,omitempty"`
}

// ModelPrice 手工维护的价格条目，按公开模型名匹配。
//
// 只用于与官方价格做对比，因此不区分上游模型、渠道或密钥——
// 同一个公开模型无论落到哪个渠道，都按这一份价格估算。
type ModelPrice struct {
	Model string `json:"model"`
	// Unit 计费单位：per_token（默认）或 per_request。
	Unit string `json:"unit"`
	// Tiers 输入分档单价；按 token 计费时至少一档，最后一档 UpTo 必须为 0。
	Tiers []PriceTier `json:"tiers"`
	// CacheReadPrice 缓存命中输入的单价（每百万 token），不分档；0 表示按输入价计。
	// 币种由面板的「货币符号」决定，这里只存纯数字。
	CacheReadPrice float64 `json:"cache_read_price"`
	// RequestPrice 每次请求的单价，仅 unit=per_request 时有意义。
	RequestPrice float64 `json:"request_price"`
	// TimeWindows 时段系数，空表示全天同价。
	TimeWindows []TimeWindow `json:"time_windows"`
	Note        string       `json:"note"`
	UpdatedAt   int64        `json:"updated_at"`
}

// ModelMeta 生图模型的元数据声明（面板手工维护，按公开模型名存一份）。
//
// 上游的 /v1/models 只有 id / object / created / owned_by，拿不到任何能力信息，
// 而主动探测一次就是一次真实计费出图，因此这些约束只能人工声明。
// 字段全部可选：零值表示「未声明」，对外输出时按「不限制 / 不声明」处理。
type ModelMeta struct {
	// Model 公开模型名，与客户端请求里的 model 严格一致（不做大小写归一化）。
	Model string `json:"model"`
	// Description 一句话说明，会随模型列表一起给到 AI 客户端。
	Description string `json:"description"`

	// ---- 能力开关 ----
	// Reference 支持参考图（图生图 / 改图）。
	Reference bool `json:"reference"`
	// NegativePrompt 支持反向提示词（negative_prompt）。
	NegativePrompt bool `json:"negative_prompt"`
	// Seed 支持随机种子（seed），可用于复现同一张图。
	Seed bool `json:"seed"`
	// ChinesePrompt 能理解中文提示词。
	ChinesePrompt bool `json:"chinese_prompt"`
	// ChineseText 能把中文文字正确渲染进画面（如海报标题）。
	ChineseText bool `json:"chinese_text"`

	// ---- 参考图约束 ----
	// MaxRefCount 参考图最多几张，0 表示不限。
	MaxRefCount int `json:"max_ref_count"`
	// MaxRefSidePx 参考图长边像素上限，0 表示不限。
	MaxRefSidePx int `json:"max_ref_side_px"`
	// MaxRefMB 参考图单张体积上限（MB），0 表示不限。
	MaxRefMB int `json:"max_ref_mb"`
	// RefFormats 允许的参考图格式（png / jpeg / webp），空表示不限制。
	RefFormats []string `json:"ref_formats"`

	// ---- 出图张数 ----
	// MaxN 不带参考图时 n 的上限，0 表示不限。
	MaxN int `json:"max_n"`
	// MaxNRef 带参考图时 n 的上限，0 表示沿用 MaxN。
	MaxNRef int `json:"max_n_ref"`
	// DefaultN 客户端未传 n 时使用的默认张数，0 表示由上游决定。
	DefaultN int `json:"default_n"`

	// ---- 尺寸 ----
	// Sizes 支持的尺寸枚举，形如 1024x1024；空表示不声明。
	Sizes []string `json:"sizes"`
	// CustomSize 允许在 Sizes 之外自定义尺寸。
	CustomSize bool `json:"custom_size"`
	// IgnoreSizeWithRef 带参考图时上游忽略 size 参数。
	IgnoreSizeWithRef bool `json:"ignore_size_with_ref"`

	// UpdatedAt 最后保存时间（Unix 毫秒）。
	UpdatedAt int64 `json:"updated_at"`
	// HasChannel 当前是否仍有渠道承载该模型名；由管理接口计算，不入库。
	HasChannel bool `json:"has_channel"`
}

// UsageBucket 用量汇总的一个分组。
// Requests 包含失败请求（失败的那次调用也算一次），SuccessRequests 是其中成功的部分。
type UsageBucket struct {
	Key              string  `json:"key"`
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"success_requests"`
	FailedRequests   int64   `json:"failed_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	Cost             float64 `json:"cost"`
	// 以下两项仅在按渠道分组时填充，用于渠道卡片的响应能力对比
	TTFTMS       int64   `json:"ttft_ms"`
	TokensPerSec float64 `json:"tokens_per_sec"`
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
	Cost             float64       `json:"cost"`
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
	// ToolLimits 工具级每日计费调用上限（上游工具名 -> 上限），用于不同工具分配不同配额。
	// 语义：-1 完全不限，且不校验密钥剩余次数（上游对该工具不计费）；
	// 未列出或 0 表示该工具不单独限量，但仍校验上游密钥剩余次数（同服务器的工具可能共享额度）；
	// 正数为该工具在每条密钥上的每日上限。
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

// LocalSourcePrefix 本地 MCP 服务器的 Source 前缀，后接 mcp 目录下的子目录名。
const LocalSourcePrefix = "local:"

// IsManual 判断服务器是否由用户手工添加（非插件注册）。
func (s *MCPServer) IsManual() bool { return s.Source == "" || s.Source == "manual" }

// IsLocal 判断服务器是否由 mcp 目录自动登记的本地 Python 服务器。
func (s *MCPServer) IsLocal() bool { return strings.HasPrefix(s.Source, LocalSourcePrefix) }

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
