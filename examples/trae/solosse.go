package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SOLO 自定义 SSE → OpenAI 的转换（解析逻辑参考 trae2api/internal/upstream/solosse.go）。
//
// 上游事件序列（实测）：
//
//	event:metadata      data:{"model":"","session_id":...}
//	event:timing_cost   data:{"name":"llm_raw_chat_v2",...}
//	event:notify_usage  data:{"billing_mode":"credits","cn_credits_remain_info":{"ide_credits":N,"work_credits":M}}
//	event:output        data:{"response":"<增量>","reasoning_content":"<思考增量>","tool_calls":<null|object|array>}   ← ×N
//	event:token_usage   data:{"prompt_tokens":..,"completion_tokens":..,"total_tokens":..}
//	event:done          data:{"finish_reason":"stop"}
//	event:error         data:{"code":4008,"message":"..."}
//
// 上游只接受流式（非流式会被拒），所以插件一律以 stream=true 向上游请求：
// 客户端要流式就边收边发，要非流式就在本地把 SSE 聚合成一个完整 chat.completion。

// soloEvent 是归一化后的单条 SOLO 事件。
type soloEvent struct {
	Event        string
	Response     string
	Reasoning    string
	ToolCalls    json.RawMessage
	Usage        map[string]any
	Notify       map[string]any
	FinishReason string
	ErrorCode    int64
	ErrorMessage string
}

// parseSOLOEvent 解析一条事件。
func parseSOLOEvent(eventName, dataLine string) *soloEvent {
	ev := &soloEvent{Event: strings.TrimSpace(eventName)}
	if strings.TrimSpace(dataLine) == "" {
		return ev
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(dataLine), &raw); err != nil {
		return ev
	}
	// 事件名归一化（AgentHub 实证）：上游把思考增量发在 event:thought（与 output 同构）、
	// 收尾发在 event:turn_completion（等价于 done）。归一之后，流式与聚合两条路径都只需认识
	// output / done。放在解析之后，与 AgentHub 一致地忽略「有 event 无 data」的空事件。
	switch ev.Event {
	case "thought":
		ev.Event = "output"
	case "turn_completion":
		ev.Event = "done"
	}
	switch ev.Event {
	case "output":
		if v, ok := raw["response"].(string); ok {
			ev.Response = v
		}
		if v, ok := raw["reasoning_content"].(string); ok {
			ev.Reasoning = v
		}
		if tc, ok := raw["tool_calls"]; ok {
			ev.ToolCalls, _ = json.Marshal(tc)
		}
	case "token_usage":
		ev.Usage = raw
	case "notify_usage":
		ev.Notify = raw
	case "done":
		if v, ok := raw["finish_reason"].(string); ok {
			ev.FinishReason = v
		}
		if ev.FinishReason == "" {
			if v, ok := raw["finishReason"].(string); ok {
				ev.FinishReason = v
			}
		}
	case "error":
		ev.ErrorCode = numberField(raw, "code")
		if v, ok := raw["message"].(string); ok {
			ev.ErrorMessage = v
		}
	}
	return ev
}

// sseScan 维护跨行的 event/data 累积。
type sseScan struct {
	event string
	data  strings.Builder
}

func (s *sseScan) reset() {
	s.event = ""
	s.data.Reset()
}

// feed 处理一行，返回事件边界处解析出的事件（无则 nil）。
func (s *sseScan) feed(line string) *soloEvent {
	switch {
	case line == "":
		if s.event == "" {
			s.reset()
			return nil
		}
		ev := parseSOLOEvent(s.event, s.data.String())
		s.reset()
		return ev
	case strings.HasPrefix(line, "event:"):
		s.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
	case strings.HasPrefix(line, "data:"):
		s.data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	case strings.HasPrefix(line, ":"):
		// 注释行忽略
	}
	return nil
}

// ---------- 非流式：聚合 ----------

// aggregateSOLO 读完整 SOLO SSE，聚合成一个 OpenAI chat.completion。
// 返回上游额度快照（notify_usage 的 ide_credits，可能为 nil）与流内错误。
func aggregateSOLO(r io.Reader, model string) (map[string]any, *float64, *upstreamError, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var (
		content      strings.Builder
		reasoning    strings.Builder
		finishReason = "stop"
		usage        map[string]any
		credits      *float64
		streamErr    *upstreamError
		toolCalls    = map[int]map[string]any{}
		toolOrder    []int
	)
	sc := &sseScan{}
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, credits, nil, err
		}
		if ev := sc.feed(strings.TrimRight(line, "\r\n")); ev != nil {
			switch ev.Event {
			case "output":
				content.WriteString(ev.Response)
				reasoning.WriteString(ev.Reasoning)
				mergeToolCallJSON(toolCalls, &toolOrder, ev.ToolCalls)
			case "token_usage":
				usage = ev.Usage
			case "notify_usage":
				if v, src := extractIdeCredits(ev.Notify); src != "" {
					f := v
					credits = &f
				}
			case "done":
				if ev.FinishReason != "" {
					finishReason = ev.FinishReason
				}
			case "error":
				streamErr = codeToUpstreamError(ev.ErrorCode, ev.ErrorMessage)
			}
		}
		if err == io.EOF {
			break
		}
	}
	if streamErr != nil {
		return nil, credits, streamErr, nil
	}

	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolOrder) > 0 {
		sortInts(toolOrder)
		calls := make([]map[string]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			calls = append(calls, toolCalls[idx])
		}
		message["tool_calls"] = calls
	}
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		resp["usage"] = usage
	}
	return resp, credits, nil, nil
}

// codeToUpstreamError 把流内 event:error 的业务码映射为切号语义错误。
func codeToUpstreamError(code int64, msg string) *upstreamError {
	if e := classifyCode(code); e != nil {
		return e
	}
	m := truncateRunes(msg, 80)
	if m == "" {
		m = fmt.Sprintf("上游返回错误 code=%d", code)
	}
	return &upstreamError{Status: http.StatusBadGateway, Msg: m}
}

// ---------- 流式：SOLO SSE → OpenAI SSE ----------

// streamSOLO 把 SOLO SSE 边收边转成 OpenAI chat.completion.chunk 写给 w。
//
// 返回 wroteAny 表示是否已经写出过任何 chunk：
//   - 若流在写出任何 chunk 之前就遇到上游错误，调用方仍可改用非 2xx 状态码（此时尚未写响应头）。
//   - 若已经写出过 chunk，则错误只能作为 SSE 内的事件告知客户端。
func streamSOLO(w http.ResponseWriter, r io.Reader, model string) (wroteAny bool, credits *float64, streamErr *upstreamError, err error) {
	flusher, _ := w.(http.Flusher)
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	var pendingUsage map[string]any
	sawDone := false

	writeChunk := func(delta map[string]any, finish string) error {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{choice},
		}
		if pendingUsage != nil {
			chunk["usage"] = pendingUsage
			pendingUsage = nil
		}
		raw, _ := json.Marshal(chunk)
		if _, werr := io.WriteString(w, "data: "+string(raw)+"\n\n"); werr != nil {
			return werr
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	writeDONE := func() error {
		if _, werr := io.WriteString(w, "data: [DONE]\n\n"); werr != nil {
			return werr
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	br := bufio.NewReaderSize(r, 64*1024)
	sc := &sseScan{}
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			return wroteAny, credits, nil, rerr
		}
		if ev := sc.feed(strings.TrimRight(line, "\r\n")); ev != nil {
			switch ev.Event {
			case "output":
				delta := map[string]any{}
				if ev.Response != "" {
					delta["content"] = ev.Response
				}
				if ev.Reasoning != "" {
					delta["reasoning_content"] = ev.Reasoning
				}
				if len(ev.ToolCalls) > 0 && string(ev.ToolCalls) != "null" {
					if tc := normalizeStreamToolCalls(ev.ToolCalls); tc != nil {
						delta["tool_calls"] = tc
					}
				}
				if len(delta) == 0 {
					break
				}
				if werr := writeChunk(delta, ""); werr != nil {
					return wroteAny, credits, nil, werr
				}
				wroteAny = true
			case "token_usage":
				pendingUsage = ev.Usage
			case "notify_usage":
				if v, src := extractIdeCredits(ev.Notify); src != "" {
					f := v
					credits = &f
				}
			case "done":
				if werr := writeChunk(map[string]any{}, ev.FinishReason); werr != nil {
					return wroteAny, credits, nil, werr
				}
				wroteAny = true
				if werr := writeDONE(); werr != nil {
					return wroteAny, credits, nil, werr
				}
				sawDone = true
			case "error":
				se := codeToUpstreamError(ev.ErrorCode, ev.ErrorMessage)
				if !wroteAny {
					// 还没写出任何内容：交给调用方用非 2xx + {'error':...} 回复。
					return false, credits, se, nil
				}
				msg := fmt.Sprintf("上游错误：%s", se.Msg)
				if _, werr := io.WriteString(w, "event: error\ndata: "+jsonEscape(msg)+"\n\n"); werr != nil {
					return wroteAny, credits, nil, werr
				}
				if werr := writeDONE(); werr != nil {
					return wroteAny, credits, nil, werr
				}
				sawDone = true
			}
		}
		if rerr == io.EOF {
			break
		}
	}
	if !sawDone {
		// 上游中断（无 done）：兜底补一个 [DONE]，保证客户端能正常收尾。
		if werr := writeDONE(); werr != nil {
			return wroteAny, credits, nil, werr
		}
	}
	return wroteAny, credits, nil, nil
}

// normalizeStreamToolCalls 把上游的 function_call 字段规范成 OpenAI 的 function。
func normalizeStreamToolCalls(raw json.RawMessage) []map[string]any {
	var tc []map[string]any
	if json.Unmarshal(raw, &tc) != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(tc))
	for _, call := range tc {
		if call == nil {
			continue
		}
		if fc, ok := call["function_call"].(map[string]any); ok {
			call["function"] = fc
			delete(call, "function_call")
		}
		if fn, ok := call["function"].(map[string]any); ok {
			delete(fn, "namespace")
			delete(fn, "partial_arguments")
		}
		out = append(out, call)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------- tool_calls 合并（非流式聚合用） ----------

func mergeToolCallJSON(toolCalls map[int]map[string]any, toolOrder *[]int, raw json.RawMessage) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		var one map[string]any
		if json.Unmarshal(raw, &one) != nil {
			return
		}
		arr = []map[string]any{one}
	}
	for _, call := range arr {
		if call == nil {
			continue
		}
		idx := 0
		if v, ok := call["index"].(float64); ok {
			idx = int(v)
		}
		merged, seen := toolCalls[idx]
		if !seen {
			merged = map[string]any{"index": idx}
			toolCalls[idx] = merged
			*toolOrder = append(*toolOrder, idx)
		}
		mergeToolCallDelta(merged, call)
	}
}

// mergeToolCallDelta 把流式 tool_call 片段合并进累计对象：
// id/type/function.name 直覆盖，function.arguments 拼接（上游用 function_call 字段名）。
func mergeToolCallDelta(merged, delta map[string]any) {
	if v, ok := delta["id"].(string); ok && v != "" {
		merged["id"] = v
	}
	if v, ok := delta["type"].(string); ok && v != "" {
		merged["type"] = v
	}
	df, _ := delta["function"].(map[string]any)
	if df == nil {
		df, _ = delta["function_call"].(map[string]any)
	}
	if df == nil {
		return
	}
	delete(df, "namespace")
	delete(df, "partial_arguments")
	mf, _ := merged["function"].(map[string]any)
	if mf == nil {
		mf = map[string]any{}
		merged["function"] = mf
	}
	if v, ok := df["name"].(string); ok && v != "" {
		mf["name"] = v
	}
	if v, ok := df["arguments"].(string); ok && v != "" {
		if prev, _ := mf["arguments"].(string); prev != "" {
			mf["arguments"] = prev + v
		} else {
			mf["arguments"] = v
		}
	}
}

func sortInts(a []int) {
	for i := 0; i < len(a)-1; i++ {
		for j := i + 1; j < len(a); j++ {
			if a[j] < a[i] {
				a[i], a[j] = a[j], a[i]
			}
		}
	}
}

func jsonEscape(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}