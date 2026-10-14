package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/routing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// Recorder 收集一次 /v1 调用的结果：命中渠道、用量、耗时与估算费用。
//
// 同一次请求内的多次故障转移只产出一条记录；所有方法可并发调用，
// Commit 幂等，可以在响应体读完后与请求收尾处各调用一次。
type Recorder struct {
	store  *store.Store
	pricer *pricing.Resolver

	mu   sync.Mutex
	row  model.RequestLog
	done bool
	start time.Time
}

// newRecorder 创建记录器。
func newRecorder(s *store.Store, pricer *pricing.Resolver, k *model.APIKey, endpoint string, stream bool, modelName string) *Recorder {
	r := &Recorder{store: s, pricer: pricer, start: time.Now()}
	r.row.CreatedAt = r.start.UnixMilli()
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
func (r *Recorder) ObserveEvent(chunk map[string]any) {
	if r == nil || chunk == nil {
		return
	}
	usage, _ := chunk["usage"].(map[string]any)
	if usage == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	applyUsage(&r.row, usage)
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
// 除 /v1 转发外，诊断（test-upstream）等直接调用上游、消耗真实额度的流程也复用它，
// 以保证任何一次真实模型请求都会体现在用量里。
func RecordUsage(s *store.Store, pricer *pricing.Resolver, row model.RequestLog) {
	if s == nil {
		return
	}
	if row.TotalTokens == 0 {
		row.TotalTokens = row.PromptTokens + row.CompletionTokens
	}
	row.Success = row.Status >= 200 && row.Status < 300 && row.Error == ""
	// 用量只统计成功的请求；失败的上游异常由 error_logs 单独记录（见「错误日志」页面）
	if !row.Success {
		return
	}

	if pricer != nil && (row.PromptTokens > 0 || row.CompletionTokens > 0) {
		price, ok := pricer.Resolve(row.Model)
		if !ok && row.UpstreamModel != "" {
			price, ok = pricer.Resolve(row.UpstreamModel)
		}
		if ok {
			row.PriceModel = price.Model
			row.PriceSource = price.Source
			row.CostUSD, row.CostPromptUSD, row.CostCompletionUSD =
				pricing.Estimate(price, row.PromptTokens, row.CompletionTokens, row.CachedTokens)
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
	case strings.HasSuffix(path, "/models"):
		return "models"
	default:
		return "other"
	}
}