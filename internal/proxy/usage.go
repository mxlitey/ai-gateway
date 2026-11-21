package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/routing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Recorder 收集一次「网关 → 上游」请求的结果：渠道、用量、耗时与估算费用。
//
// 每次实际上游尝试使用一个 Recorder；故障转移后的下一次尝试使用新的 Recorder。
// Commit 幂等，可以在响应体读完后与请求收尾处各调用一次。
type Recorder struct {
	store  *store.Store
	pricer *pricing.Resolver

	mu    sync.Mutex
	row   model.RequestLog
	done  bool
	start time.Time
}

// newRecorder 创建记录器。
func newRecorder(s *store.Store, pricer *pricing.Resolver, k *model.APIKey, endpoint string, stream bool, modelName string) *Recorder {
	r := &Recorder{store: s, pricer: pricer, start: time.Now()}
	r.row.CreatedAt = r.start.UnixMilli()
	r.row.RequestID = id.UUID()
	r.row.AttemptNo = 1
	r.row.Endpoint = endpoint
	r.row.Stream = stream
	r.row.Model = modelName
	r.row.PriceSource = pricing.SourceNone
	if k != nil {
		r.row.APIKeyID = k.ID
		r.row.APIKeyName = k.Name
	}
	return r
}

// Attempt 记录又尝试了一个上游目标。
func (r *Recorder) Attempt() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.row.Attempts++
	r.mu.Unlock()
}

// Served 记录最终提供服务的目标。
func (r *Recorder) Served(t routing.Target) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.row.ChannelID = t.Channel.ID
	r.row.ChannelName = t.Channel.Name
	r.row.UpstreamModel = t.UpstreamModel
	r.row.UpstreamKeyHint = model.KeyHint(t.Key)
}

// nextAttempt 结算当前上游尝试，并创建下一次尝试的记录器。
// 无论当前尝试是否已结算都会返回新记录器：故障转移的每一次尝试都要单独落一条。
func (r *Recorder) nextAttempt(t routing.Target) *Recorder {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.row.Status == 0 {
		r.row.Status = http.StatusBadGateway
	}
	if r.row.Error == "" {
		r.row.Error = "upstream attempt failed"
	}
	// 快照要沿用的字段，避免在锁外读取
	endpoint, stream, modelName := r.row.Endpoint, r.row.Stream, r.row.Model
	apiKeyID, apiKeyName := r.row.APIKeyID, r.row.APIKeyName
	requestID, attemptNo := r.row.RequestID, r.row.AttemptNo
	r.mu.Unlock()
	// Commit 内部有 done 幂等保护，已结算过的不会重复写
	r.Commit()

	n := newRecorder(r.store, r.pricer, nil, endpoint, stream, modelName)
	n.row.CreatedAt = time.Now().UnixMilli()
	// 同一次客户端请求的故障转移共用 request_id，序号递增，便于在明细里还原调用链
	n.row.RequestID = requestID
	n.row.AttemptNo = attemptNo + 1
	n.row.APIKeyID = apiKeyID
	n.row.APIKeyName = apiKeyName
	n.Served(t)
	return n
}

// MarkNonStream 标记本次上游实际返回的不是流式响应。
//
// 客户端可能请求了 stream:true，但上游一次性返回全文（常见于插件渠道降级）。
// 这种调用没有「首字」概念，记录按非流式展示，也不参与渠道卡片的 TTFT 与输出速度统计。
func (r *Recorder) MarkNonStream() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.row.Stream = false
	r.mu.Unlock()
}

// Finish 记录终态；已写入的非空字段不会被覆盖。
func (r *Recorder) Finish(status int, message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if status > 0 {
		r.row.Status = status
	}
	if strings.TrimSpace(message) != "" && r.row.Error == "" {
		r.row.Error = truncate(message, 2000)
	}
}

// ObserveJSON 从非流式响应体中提取用量。
func (r *Recorder) ObserveJSON(raw []byte) {
	if r == nil || len(raw) == 0 {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return
	}
	r.ObserveEvent(data)
}

// ObserveEvent 从流式响应的单个事件对象中提取用量。
//
// 同时承担首字耗时的打点：流式回复里第一个带文本（或推理）增量的 chunk
// 到达时记一次时间差，之后不再覆盖。只有首字才有意义，
// 后续 chunk 的到达时间受生成长度影响，不能作为响应速度指标。
func (r *Recorder) ObserveEvent(chunk map[string]any) {
	if r == nil || chunk == nil {
		return
	}
	// 流里夹带的 error 事件按失败结算：HTTP 状态仍是 200，但这次调用对客户端是失败的。
	// Claude 转发路径把上游错误转成 error 事件后仍会把它交给 onUsage，因此在这里统一处理。
	if e, ok := chunk["error"].(map[string]any); ok {
		r.Finish(0, "HTTP 200 stream error: "+errorMessage(e))
	}
	r.mu.Lock()
	// 非流式响应是一次性返回全文，没有「首字」概念，保持 0
	if r.row.Stream && r.row.FirstTokenMS == 0 && carriesContent(chunk) {
		r.row.FirstTokenMS = time.Since(r.start).Milliseconds()
	}
	r.mu.Unlock()

	usage, _ := chunk["usage"].(map[string]any)
	if usage == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	applyUsage(&r.row, usage)
}

// carriesContent 判断该 chunk 是否携带了内容增量。
//
// 兼容两种形状：OpenAI 的 choices[].delta.content / reasoning_content，
// 以及 Claude 原生事件的 delta.text。空串与纯 role 的首包不算。
func carriesContent(chunk map[string]any) bool {
	nonEmpty := func(v any) bool {
		s, _ := v.(string)
		return s != ""
	}
	if choices, ok := chunk["choices"].([]any); ok {
		for _, c := range choices {
			ch, _ := c.(map[string]any)
			if ch == nil {
				continue
			}
			if delta, ok := ch["delta"].(map[string]any); ok {
				if nonEmpty(delta["content"]) || nonEmpty(delta["reasoning_content"]) {
					return true
				}
			}
		}
	}
	if delta, ok := chunk["delta"].(map[string]any); ok && nonEmpty(delta["text"]) {
		return true
	}
	return false
}

// Discard 放弃本次记录（异步作业在提交阶段只返回作业号，真正完成时再补写用量）。
func (r *Recorder) Discard() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()
}

// Commit 计算费用并落库；重复调用直接返回。
func (r *Recorder) Commit() {
	if r == nil || r.store == nil {
		return
	}

	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	row := r.row
	r.mu.Unlock()

	row.DurationMS = time.Since(r.start).Milliseconds()
	RecordUsage(r.store, r.pricer, row)
}

// RecordUsage 补齐派生字段、估算费用并异步写入一条用量记录。
//
// 调用明细记录的是「网关 → 上游」的真实请求：每次故障转移都实际请求了一次上游，
// 因此各自落一条；同一次客户端请求可以产生多条记录。成功与失败都记录，
// 失败记录 Token 为 0、费用为 0，用于计算上游成功率与失败数。
//
// 网关级拒绝（鉴权失败、请求体非法、没有可用渠道等）没有请求上游，
// 由 error_logs 记录，不进入调用明细。除 /v1 转发外，诊断（test-upstream）
// 等直接调用上游的流程也复用本函数。
//
// 计费只按公开模型名匹配价格表（不看上游模型与渠道），与厂商官方价对比才有意义。
func RecordUsage(s *store.Store, pricer *pricing.Resolver, row model.RequestLog) {
	if s == nil || row.Attempts == 0 {
		return
	}
	if row.TotalTokens == 0 {
		row.TotalTokens = row.PromptTokens + row.CompletionTokens
	}
	row.Success = row.Status >= 200 && row.Status < 300 && row.Error == ""

	if pricer != nil {
		if price, ok := pricer.Resolve(row.Model); ok {
			row.PriceModel = price.Model
			row.PriceSource = pricing.SourceManual
			// 失败请求没有实际产出，只记录「是否配了价」，不计算费用
			// （按次计费的模型否则会给失败也记一笔钱）
			if row.Success {
				q := pricing.Estimate(price, pricing.Usage{
					PromptTokens:     row.PromptTokens,
					CompletionTokens: row.CompletionTokens,
					CachedTokens:     row.CachedTokens,
					At:               time.UnixMilli(row.CreatedAt),
				})
				row.Cost, row.CostPrompt, row.CostCompletion = q.Total, q.Input, q.Output
			}
		}
	}

	out := row
	go func() {
		if err := s.InsertRequestLog(&out); err != nil {
			slog.Error("[usage] 写入用量日志失败", "err", err)
		}
	}()
}

// ExtractUsage 从响应体（OpenAI / Claude 形状）中提取 token 用量写入 row。
func ExtractUsage(row *model.RequestLog, raw []byte) {
	if row == nil || len(raw) == 0 {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return
	}
	if usage, ok := data["usage"].(map[string]any); ok {
		applyUsage(row, usage)
	}
}

// observeBody 在响应体读完/关闭时提交用量记录。
type observeBody struct {
	io.ReadCloser
	rec *Recorder
}

func (b *observeBody) Close() error {
	err := b.ReadCloser.Close()
	b.rec.Commit()
	return err
}

// applyUsage 从 OpenAI 形状的 usage 对象中提取 token 计数。
func applyUsage(row *model.RequestLog, u map[string]any) {
	if v := intOf(u["prompt_tokens"]); v > 0 {
		row.PromptTokens = v
	}
	if v := intOf(u["completion_tokens"]); v > 0 {
		row.CompletionTokens = v
	}
	if v := intOf(u["total_tokens"]); v > 0 {
		row.TotalTokens = v
	}
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if v := intOf(d["cached_tokens"]); v > 0 {
			row.CachedTokens = v
		}
	}
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		if v := intOf(d["reasoning_tokens"]); v > 0 {
			row.ReasoningTokens = v
		}
	}
	// 兼容 Claude 原生形状的用量字段
	if row.PromptTokens == 0 {
		if v := intOf(u["input_tokens"]); v > 0 {
			row.PromptTokens = v
		}
	}
	if row.CompletionTokens == 0 {
		if v := intOf(u["output_tokens"]); v > 0 {
			row.CompletionTokens = v
		}
	}
	if row.CachedTokens == 0 {
		if v := intOf(u["cache_read_input_tokens"]); v > 0 {
			row.CachedTokens = v
		}
	}
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// endpointOf 把 /v1 路径归类为端点名，便于统计。
func endpointOf(path string) string {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return "chat"
	case strings.HasSuffix(path, "/messages"):
		return "messages"
	case strings.HasSuffix(path, "/responses"):
		return "responses"
	case strings.HasSuffix(path, "/embeddings"):
		return "embeddings"
	case strings.HasSuffix(path, "/images/generations"):
		return "images"
	case strings.HasSuffix(path, "/models"):
		return "models"
	default:
		return "other"
	}
}
