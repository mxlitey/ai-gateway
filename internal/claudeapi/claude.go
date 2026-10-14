// Package claudeapi 实现 Claude Messages API 与 OpenAI Chat Completions 的协议互转。
package claudeapi

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/id"
)

// ---------- Claude -> OpenAI ----------

// ClaudeToOpenAI 把 Claude Messages 请求转换为 OpenAI Chat Completions 请求。
func ClaudeToOpenAI(in map[string]any) map[string]any {
	out := map[string]any{}
	msgs := make([]any, 0, 8)

	// system 可以是字符串或内容块数组
	if sys, ok := in["system"]; ok && sys != nil {
		if s := blocksToText(sys); s != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": s})
		}
	}

	if raw, ok := in["messages"].([]any); ok {
		for _, m := range raw {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)
			msgs = append(msgs, convertClaudeMessage(role, mm["content"])...)
		}
	}
	out["messages"] = msgs

	for _, k := range []string{"model", "temperature", "top_p", "stream", "max_tokens"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	if v, ok := in["stop_sequences"]; ok {
		out["stop"] = v
	}
	if tools, ok := in["tools"].([]any); ok && len(tools) > 0 {
		out["tools"] = convertTools(tools)
	}
	if tc, ok := in["tool_choice"]; ok {
		out["tool_choice"] = convertToolChoice(tc)
	}
	return out
}

func convertClaudeMessage(role string, content any) []any {
	switch c := content.(type) {
	case string:
		return []any{map[string]any{"role": role, "content": c}}
	case []any:
		return convertClaudeBlocks(role, c)
	default:
		return []any{map[string]any{"role": role, "content": ""}}
	}
}

func convertClaudeBlocks(role string, blocks []any) []any {
	var texts []string
	var parts []any
	var toolCalls []any
	var toolResults []any

	for _, b := range blocks {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := bm["type"].(string)
		switch typ {
		case "text":
			t, _ := bm["text"].(string)
			texts = append(texts, t)
			parts = append(parts, map[string]any{"type": "text", "text": t})
		case "image":
			if src, ok := bm["source"].(map[string]any); ok {
				data, _ := src["data"].(string)
				mt, _ := src["media_type"].(string)
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + mt + ";base64," + data},
				})
			}
		case "tool_use":
			idv, _ := bm["id"].(string)
			name, _ := bm["name"].(string)
			args, err := json.Marshal(bm["input"])
			if err != nil {
				args = []byte("{}")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   idv,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(args),
				},
			})
		case "tool_result":
			idv, _ := bm["tool_use_id"].(string)
			toolResults = append(toolResults, map[string]any{
				"role":         "tool",
				"tool_call_id": idv,
				"content":      blocksToText(bm["content"]),
			})
		}
	}

	// tool 结果必须紧跟在触发它的 assistant 消息之后
	out := make([]any, 0, 2)
	out = append(out, toolResults...)

	if role == "assistant" {
		msg := map[string]any{"role": "assistant", "content": strings.Join(texts, "")}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return append(out, msg)
	}

	msg := map[string]any{"role": "user"}
	switch {
	case len(parts) == 0:
		msg["content"] = ""
	case len(parts) == 1:
		if p, ok := parts[0].(map[string]any); ok && p["type"] == "text" {
			msg["content"] = p["text"]
		} else {
			msg["content"] = parts
		}
	default:
		msg["content"] = parts
	}
	return append(out, msg)
}

// blocksToText 把「字符串或内容块数组」统一转成纯文本。
func blocksToText(v any) string {
	switch c := v.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, b := range c {
			switch bb := b.(type) {
			case string:
				sb.WriteString(bb)
			case map[string]any:
				if t, ok := bb["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	default:
		return ""
	}
}

func convertTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tm["name"].(string)
		if name == "" {
			continue
		}
		fn := map[string]any{"name": name}
		if desc, ok := tm["description"]; ok {
			fn["description"] = desc
		}
		if schema, ok := tm["input_schema"]; ok {
			fn["parameters"] = schema
		} else {
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func convertToolChoice(v any) any {
	tm, ok := v.(map[string]any)
	if !ok {
		return "auto"
	}
	switch typ, _ := tm["type"].(string); typ {
	case "any":
		return "required"
	case "tool":
		name, _ := tm["name"].(string)
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	default:
		return "auto"
	}
}

// ---------- OpenAI -> Claude ----------

// OpenAIToClaude 把非流式 OpenAI Chat Completions 响应转换为 Claude Messages 响应。
func OpenAIToClaude(resp map[string]any, model string) map[string]any {
	respID, _ := resp["id"].(string)
	if respID == "" {
		respID = "msg_" + id.Hex(12)
	}

	content := []any{}
	stopReason := "end_turn"
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}

	if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
		if ch, ok := choices[0].(map[string]any); ok {
			if msg, ok := ch["message"].(map[string]any); ok {
				if t, ok := msg["content"].(string); ok && t != "" {
					content = append(content, map[string]any{"type": "text", "text": t})
				}
				if tcs, ok := msg["tool_calls"].([]any); ok {
					for _, tc := range tcs {
						tcm, _ := tc.(map[string]any)
						fn, _ := tcm["function"].(map[string]any)
						name, _ := fn["name"].(string)
						argsStr, _ := fn["arguments"].(string)
						var input any = map[string]any{}
						if argsStr != "" {
							_ = json.Unmarshal([]byte(argsStr), &input)
						}
						content = append(content, map[string]any{
							"type":  "tool_use",
							"id":    tcm["id"],
							"name":  name,
							"input": input,
						})
					}
				}
			}
			if fr, ok := ch["finish_reason"].(string); ok {
				stopReason = mapFinishReason(fr)
			}
		}
	}

	if u, ok := resp["usage"].(map[string]any); ok {
		if v, ok := u["prompt_tokens"]; ok {
			usage["input_tokens"] = v
		}
		if v, ok := u["completion_tokens"]; ok {
			usage["output_tokens"] = v
		}
	}

	return map[string]any{
		"id":            respID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}
}

func mapFinishReason(fr string) string {
	switch fr {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default: // stop, content_filter, "" ...
		return "end_turn"
	}
}

// ---------- 流式：OpenAI SSE -> Claude SSE ----------

// StreamOpenAIToClaude 把 OpenAI 的 SSE 流实时转换为 Claude Messages 的 SSE 事件流。
//
// 说明：当前覆盖文本增量、结束原因与用量；工具调用的增量事件尚未映射（详见 TODO）。
// onUsage 会收到每个可解析的上游 chunk（用于统计 token 用量），onDone 在流处理结束时调用；两者可为 nil。
func StreamOpenAIToClaude(src io.Reader, model string, onUsage func(chunk map[string]any), onDone func()) io.ReadCloser {
	pr, pw := io.Pipe()

	go func() {
		defer func() { _ = pw.Close() }()
		if onDone != nil {
			defer onDone()
		}

		br := bufio.NewReaderSize(src, 32*1024)
		msgID := "msg_" + id.Hex(12)

		started := false
		blockOpen := false
		stopReason := "end_turn"
		outputTokens := int64(0)

		ensureStart := func() error {
			if started {
				return nil
			}
			started = true
			if err := writeEvent(pw, "message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":            msgID,
					"type":          "message",
					"role":          "assistant",
					"model":         model,
					"content":       []any{},
					"stop_reason":   nil,
					"stop_sequence": nil,
					"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
				},
			}); err != nil {
				return err
			}
			if err := writeEvent(pw, "content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         0,
				"content_block": map[string]any{"type": "text", "text": ""},
			}); err != nil {
				return err
			}
			blockOpen = true
			return nil
		}

		finish := func() error {
			if err := ensureStart(); err != nil {
				return err
			}
			if blockOpen {
				if err := writeEvent(pw, "content_block_stop", map[string]any{
					"type": "content_block_stop", "index": 0,
				}); err != nil {
					return err
				}
				blockOpen = false
			}
			if err := writeEvent(pw, "message_delta", map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": outputTokens},
			}); err != nil {
				return err
			}
			return writeEvent(pw, "message_stop", map[string]any{"type": "message_stop"})
		}

		for {
			line, err := br.ReadString('\n')
			if line != "" {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "data: ") {
					payload := trimmed[6:]
					if payload == "[DONE]" {
						_ = finish()
						return
					}
					var chunk map[string]any
					if json.Unmarshal([]byte(payload), &chunk) == nil && chunk != nil {
						if onUsage != nil {
							onUsage(chunk)
						}
						if e, ok := chunk["error"].(map[string]any); ok {
							_ = writeEvent(pw, "error", map[string]any{
								"type":  "error",
								"error": map[string]any{"type": "api_error", "message": errorText(e)},
							})
						}
						if idv, ok := chunk["id"].(string); ok && idv != "" {
							msgID = idv
						}
						if u, ok := chunk["usage"].(map[string]any); ok {
							if v, ok := u["completion_tokens"].(float64); ok {
								outputTokens = int64(v)
							}
						}
						if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
							if ch, ok := choices[0].(map[string]any); ok {
								if delta, ok := ch["delta"].(map[string]any); ok {
									if t, ok := delta["content"].(string); ok && t != "" {
										if err := ensureStart(); err != nil {
											return
										}
										if err := writeEvent(pw, "content_block_delta", map[string]any{
											"type":  "content_block_delta",
											"index": 0,
											"delta": map[string]any{"type": "text_delta", "text": t},
										}); err != nil {
											return
										}
									}
								}
								if fr, ok := ch["finish_reason"].(string); ok && fr != "" {
									stopReason = mapFinishReason(fr)
								}
							}
						}
					}
				}
			}

			if err != nil {
				_ = finish()
				if err == io.EOF {
					return
				}
				return
			}
		}
	}()

	return pr
}

func writeEvent(pw *io.PipeWriter, name string, data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = pw.Write([]byte("event: " + name + "\ndata: " + string(b) + "\n\n"))
	return err
}

func errorText(e map[string]any) string {
	for _, k := range []string{"message", "code", "type"} {
		if s, ok := e[k].(string); ok && s != "" {
			return s
		}
	}
	return "upstream error"
}