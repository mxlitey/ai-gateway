package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/claudeapi"
	"github.com/czg95/ai-gateway-v2/internal/routing"
)

// handleClaude 处理 Claude Messages API 请求：转 OpenAI 上游，再把结果转回 Claude 形状。
func (h *Handler) handleClaude(w http.ResponseWriter, r *http.Request, claudeBody map[string]any, allowed []string, rec *Recorder) {
	modelName, _ := claudeBody["model"].(string)
	isStream, _ := claudeBody["stream"].(bool)

	openaiBody := claudeapi.ClaudeToOpenAI(claudeBody)

	// 参考 one-api：流式请求注入 stream_options 以获取 token 用量
	if isStream {
		so, _ := openaiBody["stream_options"].(map[string]any)
		if so == nil {
			so = map[string]any{}
		}
		so["include_usage"] = true
		openaiBody["stream_options"] = so
	}

	targets, err := h.balancer.SelectTarget(modelName, allowed)
	if err != nil {
		slog.Error("选择目标失败", "err", err)
		h.logGatewayError(modelName, http.StatusInternalServerError, "选择渠道失败: "+err.Error())
		rec.Finish(http.StatusInternalServerError, "select target failed: "+err.Error())
		claudeError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if len(targets) == 0 {
		h.logGatewayError(modelName, http.StatusServiceUnavailable, "没有可用渠道（渠道被停用、密钥全在冷却中或该模型未映射）")
		rec.Finish(http.StatusServiceUnavailable, "No available channel for model: "+modelName)
		claudeError(w, http.StatusServiceUnavailable, "No available channel for model: "+modelName)
		return
	}

	resp := h.forward(r.Context(), r, fwdSpec{
		targets:     targets,
		publicModel: modelName,
		body:        openaiBody,
		rawQuery:    r.URL.RawQuery,
		rec:         rec,
		buildPath:   func(t routing.Target) string { return channel.ChatPath(t.Channel) },
		applyModel: func(t routing.Target) {
			if t.UpstreamModel != "" {
				openaiBody["model"] = t.UpstreamModel
			}
		},
		isStream:           isStream,
		acceptStreamHeader: true,
		handleUpstream: func(up *http.Response, t routing.Target) (*http.Response, bool, string) {
			return h.claudeUpstreamResponse(up, t, modelName, isStream, rec)
		},
		allFailed: func(lastErr, last429 string) *http.Response {
			rec.Finish(http.StatusBadGateway, lastErr)
			return claudeErrorResponse(http.StatusBadGateway,
				"All targets failed. Last error: "+lastErr+rateDetail(last429))
		},
	})

	if resp == nil {
		rec.Finish(http.StatusBadGateway, "all targets failed")
		rec.Commit()
		writeResponse(w, resp)
		return
	}
	rec.Finish(resp.StatusCode, "")
	resp.Body = &observeBody{ReadCloser: resp.Body, rec: rec}
	writeResponse(w, resp)
}

func (h *Handler) claudeUpstreamResponse(up *http.Response, t routing.Target, modelName string, isStream bool, rec *Recorder) (*http.Response, bool, string) {
	ct := up.Header.Get("Content-Type")

	if up.StatusCode < 200 || up.StatusCode >= 300 {
		raw := readBodyLimited(up, 4096)
		h.logError(t, modelName, up.StatusCode, raw)
		rec.Finish(up.StatusCode, raw)
		return claudeErrorResponse(up.StatusCode, "Upstream error: "+raw), false, ""
	}

	upstreamIsSSE := isStream && strings.Contains(ct, "text/event-stream")
	if upstreamIsSSE {
		// 同 OpenAI 路径：先探测首包，避免 200+SSE 里的错误绕过故障转移
		br := bufio.NewReaderSize(up.Body, 32*1024)
		prefix, failure := peekSSEFailure(br)
		if failure != "" {
			h.logError(t, modelName, http.StatusOK, "HTTP 200 stream error: "+failure)
			return nil, true, failure
		}

		headers := http.Header{}
		headers.Set("Content-Type", "text/event-stream")
		headers.Set("Cache-Control", "no-cache")
		headers.Set("Connection", "keep-alive")
		headers.Set("X-Accel-Buffering", "no")
		headers.Set("Access-Control-Allow-Origin", "*")

		stream := claudeapi.StreamOpenAIToClaude(replayAfterPeek(prefix, br, up.Body), modelName, rec.ObserveEvent, rec.Commit)
		return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: stream}, false, ""
	}

	if isStream {
		slog.Warn("[proxy][claude] 上游对 stream:true 返回了非 SSE 响应，回退到非流式校验", "content_type", ct)
	}

	raw, err := io.ReadAll(up.Body)
	_ = up.Body.Close()
	if err != nil {
		rec.Finish(http.StatusBadGateway, "read upstream body failed: "+err.Error())
		return claudeErrorResponse(http.StatusBadGateway, "read upstream body failed: "+err.Error()), false, ""
	}

	// 与真实转发同一标准：200 也可能是内容为错误的假成功
	if msg := h.validateUpstreamResult(raw, t, modelName); msg != "" {
		return nil, true, msg
	}

	rec.ObserveJSON(raw)

	var openaiResp map[string]any
	if err := json.Unmarshal(raw, &openaiResp); err != nil {
		rec.Finish(http.StatusBadGateway, "invalid upstream response")
		return claudeErrorResponse(http.StatusBadGateway, "invalid upstream response"), false, ""
	}

	return jsonResponse(http.StatusOK, claudeapi.OpenAIToClaude(openaiResp, modelName)), false, ""
}

func claudeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": message},
	})
}

func claudeErrorResponse(status int, message string) *http.Response {
	return jsonResponse(status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": message},
	})
}