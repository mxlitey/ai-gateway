package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// RewriteSSE 对上游 SSE 流做最小必要修补：
//
//  1. 兼容 \n\n 与 \r\n\r\n 两种事件分隔符
//  2. 仅在事件 JSON 的 id 非字符串时补一个（同一次回复复用同一个 id）
//  3. 识别流内夹带的 error 事件并通过 onError 上报（内容原样透传）
//  4. 把每个可解析的事件对象交给 onEvent（用于统计 token 用量）
//  5. 流处理结束后调用 onDone（用于落库用量）
//
// 无法识别的内容一律原样透传，避免丢包。onEvent / onDone 可为 nil。
func RewriteSSE(src io.Reader, streamID string, onError func(msg string), onEvent func(chunk map[string]any), onDone func()) io.ReadCloser {
	pr, pw := io.Pipe()

	go func() {
		defer func() { _ = pw.Close() }()
		if onDone != nil {
			defer onDone()
		}

		br := bufio.NewReaderSize(src, 32*1024)
		var buf strings.Builder

		flushEvent := func() {
			if buf.Len() == 0 {
				return
			}
			event := buf.String()
			buf.Reset()
			if _, err := pw.Write([]byte(rewriteEvent(event, streamID, onError, onEvent) + "\n\n")); err != nil {
				return
			}
		}

		for {
			line, err := br.ReadString('\n')

			if line != "" {
				// 裸 \r\n 只作为行分隔符出现（JSON 字符串内的换行必被转义），可安全归一化
				clean := strings.TrimRight(line, "\r\n")
				if clean == "" {
					flushEvent()
				} else {
					if buf.Len() > 0 {
						buf.WriteByte('\n')
					}
					buf.WriteString(clean)
				}
			}

			if err != nil {
				// 末尾未以空行结束的残留事件原样输出
				if buf.Len() > 0 {
					if _, werr := pw.Write([]byte(buf.String())); werr != nil {
						return
					}
					buf.Reset()
				}
				if err == io.EOF {
					return
				}
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()

	return pr
}

// rewriteEvent 处理单个 SSE 事件文本。
func rewriteEvent(part, streamID string, onError func(msg string), onEvent func(chunk map[string]any)) string {
	trimmed := strings.TrimSpace(part)
	if !strings.HasPrefix(trimmed, "data: ") || trimmed == "data: [DONE]" {
		return part
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed[6:]), &m); err != nil {
		return part
	}
	if m == nil {
		return part
	}

	// 上游在 2xx 的流里返回 error：业务上是失败，顺带上报
	if e, ok := m["error"].(map[string]any); ok {
		if onError != nil {
			onError(errorMessage(e))
		}
		return part
	}

	if onEvent != nil {
		onEvent(m)
	}

	// id 已是字符串则原样透传
	if _, ok := m["id"].(string); ok {
		return part
	}
	m["id"] = streamID

	out, err := json.Marshal(m)
	if err != nil {
		return part
	}
	return "data: " + string(out)
}

// ssePeekLimit 首包探测最多缓存的字节数：超过仍未见事件边界就当作正常流直接透传。
const ssePeekLimit = 1 << 20

// peekSSEFailure 在把 SSE 流交给客户端之前，读取第一条数据事件判断它是否其实是错误。
//
// 上游（尤其是插件渠道）常常先发 200 + text/event-stream，再把错误塞进
// data: {"error": ...} 事件里；此时网关若不先看一眼，就会把错误当成正常
// 流式响应透传，故障转移形同虚设。返回 prefix 需原样回放给客户端。
func peekSSEFailure(br *bufio.Reader) (prefix []byte, failure string) {
	var head bytes.Buffer
	data := make([]string, 0, 2)
	eventName := ""

	for {
		line, err := br.ReadString('\n')
		if line != "" {
			head.WriteString(line)
			clean := strings.TrimRight(line, "\r\n")
			switch {
			case clean == "":
				// 事件边界：有 data 才算一条完整事件
				if msg := sseEventFailure(eventName, data); msg != "" {
					return head.Bytes(), msg
				}
				if len(data) > 0 {
					return head.Bytes(), ""
				}
				eventName = ""
			case strings.HasPrefix(clean, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(clean, "event:"))
			case strings.HasPrefix(clean, "data:"):
				data = append(data, strings.TrimSpace(strings.TrimPrefix(clean, "data:")))
			}
			if head.Len() > ssePeekLimit {
				return head.Bytes(), ""
			}
		}

		if err != nil {
			msg := sseEventFailure(eventName, data)
			if msg == "" && len(data) == 0 {
				// 上游一个事件都没给就结束：客户端会拿到空回复，按失败处理
				msg = "上游未返回流式内容"
			}
			return head.Bytes(), msg
		}
	}
}

// sseEventFailure 判断一条事件是否是错误，返回可读原因（正常返回空串）。
func sseEventFailure(eventName string, data []string) string {
	if len(data) == 0 {
		return ""
	}
	joined := strings.TrimSpace(strings.Join(data, "\n"))
	if joined == "[DONE]" {
		return "上游未返回流式内容"
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(joined), &m); err == nil && m != nil {
		switch e := m["error"].(type) {
		case string:
			if e != "" {
				return "HTTP 200 with error: " + e
			}
		case map[string]any:
			return "HTTP 200 with error: " + errorMessage(e)
		}
		return ""
	}

	// event: error 的 data 可能不是 JSON 对象（插件会写成 JSON 字符串）
	if eventName == "error" {
		msg := joined
		var s string
		if err := json.Unmarshal([]byte(joined), &s); err == nil && s != "" {
			msg = s
		}
		return "HTTP 200 stream error: " + msg
	}
	return ""
}

// replayBody 把首包探测读到的字节拼回流的开头，Close 时关闭原始响应体。
type replayBody struct {
	io.Reader
	closer io.Closer
}

func (b *replayBody) Close() error { return b.closer.Close() }

// replayAfterPeek 组合「已读前缀 + 剩余流」。
func replayAfterPeek(prefix []byte, br *bufio.Reader, closer io.Closer) io.ReadCloser {
	return &replayBody{Reader: io.MultiReader(bytes.NewReader(prefix), br), closer: closer}
}

// errorMessage 从上游 error 对象中提取可读信息。
func errorMessage(e map[string]any) string {
	for _, k := range []string{"message", "code", "type"} {
		if v, ok := e[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return "upstream error"
}