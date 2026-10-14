package proxy

import (
	"bufio"
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