package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/channel"
	"github.com/czg95/ai-gateway-v2/internal/model"
)

// ImageGenResult 一次生图调用的最终结果（已转换成 OpenAI 兼容响应体）。
type ImageGenResult struct {
	// StatusCode 对外返回的状态码；2xx 表示成功。
	StatusCode int
	// Body OpenAI 兼容的 images 响应体（成功时）。
	Body []byte
	// UpstreamStatus 上游最后一次响应的状态码（0 表示网络错误），用于错误日志与冷却。
	UpstreamStatus int
	// Error 失败原因（成功时为空）。
	Error string
}

// GenerateImage 按渠道配置调用上游生图接口，并统一转换为 OpenAI 兼容响应。
//
// 支持两种模式：
//   - sync：一次 POST 直接返回图片；
//   - async：POST 提交任务拿到 task_id，再按配置轮询任务直到成功/失败/超时。
//
// clientReq 仅用于解析渠道请求头占位符，可为 nil。
func GenerateImage(ctx context.Context, client *http.Client, ch *model.Channel, key string, input map[string]any, publicModel string, clientReq *http.Request) ImageGenResult {
	cfg := ch.ImageConfig()
	if cfg.Mode == "" {
		return ImageGenResult{StatusCode: http.StatusBadRequest, Error: "渠道未配置生图能力"}
	}
	upstreamModel, ok := ch.ResolveUpstreamModel(publicModel)
	if !ok {
		return ImageGenResult{StatusCode: http.StatusBadRequest, Error: "模型 " + publicModel + " 未在该渠道配置"}
	}

	responseFormat, _ := input["response_format"].(string)
	payload := cloneImagePayload(input)
	payload["model"] = upstreamModel
	// response_format 是网关输出偏好，不传给上游（异步服务商通常不认识该字段）
	delete(payload, "response_format")

	submitURL := channel.BaseURL(ch) + cfg.Path
	slog.Debug("[image] -> submit", "channel", ch.Name, "url", submitURL, "mode", cfg.Mode)

	raw, status, err := doImageRequest(ctx, client, http.MethodPost, submitURL, payload, ch, key, cfg.SubmitHeaders, clientReq)
	if err != nil {
		return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: 0, Error: "请求上游失败: " + err.Error()}
	}
	if status < 200 || status >= 300 {
		return ImageGenResult{
			StatusCode:     http.StatusBadGateway,
			UpstreamStatus: status,
			Error:          fmt.Sprintf("生图上游拒绝 (HTTP %d): %s", status, truncateImageText(raw, 800)),
		}
	}

	var submit map[string]any
	_ = json.Unmarshal(raw, &submit)

	if cfg.Mode == model.ImageGenSync {
		body, err := normalizeImageResponse(ctx, client, submit, cfg, responseFormat)
		if err != nil {
			return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: status, Error: err.Error()}
		}
		return ImageGenResult{StatusCode: http.StatusOK, Body: body}
	}

	// 兼容「提交即返回图片」的服务商：有图片就直接转换，不再轮询
	if images := extractImages(submit, cfg.ImagesField); len(images) > 0 {
		body, err := normalizeImageResponse(ctx, client, submit, cfg, responseFormat)
		if err != nil {
			return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: status, Error: err.Error()}
		}
		return ImageGenResult{StatusCode: http.StatusOK, Body: body}
	}

	taskID := stringField(submit, cfg.TaskIDField)
	if taskID == "" {
		return ImageGenResult{
			StatusCode:     http.StatusBadGateway,
			UpstreamStatus: status,
			Error:          "上游未返回任务 ID（字段 " + cfg.TaskIDField + "）: " + truncateImageText(raw, 400),
		}
	}
	return pollImageTask(ctx, client, ch, key, cfg, taskID, responseFormat, clientReq)
}

// pollImageTask 轮询异步生图任务，直到成功、失败或超时。
func pollImageTask(ctx context.Context, client *http.Client, ch *model.Channel, key string, cfg model.ImageGenConfig, taskID, responseFormat string, clientReq *http.Request) ImageGenResult {
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()

	interval := time.Duration(cfg.PollIntervalMS) * time.Millisecond
	pollURL := channel.BaseURL(ch) + strings.ReplaceAll(cfg.PollPath, "{task_id}", taskID)
	deadline := time.Duration(cfg.TimeoutMS) * time.Millisecond

	for {
		raw, status, err := doImageRequest(pollCtx, client, http.MethodGet, pollURL, nil, ch, key, cfg.PollHeaders, clientReq)
		if err != nil {
			if pollCtx.Err() == context.DeadlineExceeded {
				return ImageGenResult{
					StatusCode:     http.StatusGatewayTimeout,
					UpstreamStatus: 0,
					Error:          fmt.Sprintf("生图任务 %s 轮询超时（%s）", taskID, deadline),
				}
			}
			return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: 0, Error: "轮询生图任务失败: " + err.Error()}
		}
		if status < 200 || status >= 300 {
			return ImageGenResult{
				StatusCode:     http.StatusBadGateway,
				UpstreamStatus: status,
				Error:          fmt.Sprintf("轮询生图任务被拒绝 (HTTP %d): %s", status, truncateImageText(raw, 800)),
			}
		}

		var task map[string]any
		if err := json.Unmarshal(raw, &task); err != nil {
			return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: status, Error: "解析生图任务响应失败: " + err.Error()}
		}

		taskStatus := strings.ToUpper(stringField(task, cfg.StatusField))
		switch {
		case taskStatus == strings.ToUpper(cfg.SuccessStatus):
			body, err := normalizeImageResponse(pollCtx, client, task, cfg, responseFormat)
			if err != nil {
				return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: status, Error: err.Error()}
			}
			return ImageGenResult{StatusCode: http.StatusOK, Body: body}

		case taskStatus == strings.ToUpper(cfg.FailedStatus):
			return ImageGenResult{
				StatusCode:     http.StatusBadGateway,
				UpstreamStatus: status,
				Error:          "生图任务失败 (" + taskStatus + "): " + imageErrorText(task, cfg.ErrorField),
			}

		case taskStatus == "":
			return ImageGenResult{
				StatusCode:     http.StatusBadGateway,
				UpstreamStatus: status,
				Error:          "生图任务响应缺少状态字段 " + cfg.StatusField + ": " + truncateImageText(raw, 400),
			}
		}

		select {
		case <-pollCtx.Done():
			return ImageGenResult{
				StatusCode:     http.StatusGatewayTimeout,
				UpstreamStatus: status,
				Error:          fmt.Sprintf("生图任务 %s 轮询超时（%s）", taskID, deadline),
			}
		case <-time.After(interval):
		}
	}
}

// doImageRequest 发起一次生图相关请求并读取响应体。
func doImageRequest(ctx context.Context, client *http.Client, method, url string, payload map[string]any, ch *model.Channel, key string, extraHeaders []model.Header, clientReq *http.Request) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, 0, err
	}
	headers := http.Header{}
	channel.ApplyPassthroughHeaders(headers, clientReq)
	headers.Set("Content-Type", "application/json")
	channel.ApplyUpstreamKey(headers, ch, key)
	channel.ApplyChannelHeaders(headers, ch, clientReq)
	channel.ApplyHeaderList(headers, extraHeaders, clientReq)
	req.Header = headers

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// normalizeImageResponse 把上游响应转换成 OpenAI images 响应。
func normalizeImageResponse(ctx context.Context, client *http.Client, upstream map[string]any, cfg model.ImageGenConfig, responseFormat string) ([]byte, error) {
	images := extractImages(upstream, cfg.ImagesField)
	if len(images) == 0 {
		return nil, fmt.Errorf("上游未返回图片（字段 %s）", cfg.ImagesField)
	}

	wantBase64 := strings.EqualFold(strings.TrimSpace(responseFormat), "b64_json")
	data := make([]map[string]any, 0, len(images))
	for _, img := range images {
		url, _ := img["url"].(string)
		b64, _ := img["b64_json"].(string)
		switch {
		case wantBase64 && url != "":
			encoded, err := fetchImageBase64(ctx, client, url)
			if err != nil {
				return nil, fmt.Errorf("下载图片失败: %w", err)
			}
			data = append(data, map[string]any{"b64_json": encoded})
		case b64 != "" && url == "":
			data = append(data, map[string]any{"b64_json": b64})
		case url != "":
			data = append(data, map[string]any{"url": url})
		case b64 != "":
			data = append(data, map[string]any{"b64_json": b64})
		}
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("上游返回的图片地址为空")
	}

	out := map[string]any{"created": time.Now().Unix(), "data": data}
	return json.Marshal(out)
}

// extractImages 从上游对象中提取图片列表，兼容字符串数组与对象数组。
func extractImages(obj map[string]any, field string) []map[string]any {
	if obj == nil {
		return nil
	}
	list, _ := lookupField(obj, field).([]any)
	if len(list) == 0 {
		// 兼容同步型服务商返回标准 OpenAI data 字段的场景
		list, _ = obj["data"].([]any)
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		switch v := item.(type) {
		case string:
			if v != "" {
				out = append(out, map[string]any{"url": v})
			}
		case map[string]any:
			entry := map[string]any{}
			if u, _ := v["url"].(string); u != "" {
				entry["url"] = u
			}
			if b, _ := v["b64_json"].(string); b != "" {
				entry["b64_json"] = b
			}
			if len(entry) > 0 {
				out = append(out, entry)
			}
		}
	}
	return out
}

// lookupField 按字段名取顶层值。
func lookupField(obj map[string]any, field string) any {
	if field == "" {
		return nil
	}
	return obj[field]
}

// stringField 读取字符串字段。
func stringField(obj map[string]any, field string) string {
	v, _ := lookupField(obj, field).(string)
	return strings.TrimSpace(v)
}

// imageErrorText 从上游响应中提取失败原因。
func imageErrorText(obj map[string]any, field string) string {
	if field != "" {
		if msg := errorMessageValue(obj[field]); msg != "" {
			return msg
		}
	}
	for _, key := range []string{"error", "errors"} {
		if msg := errorMessageValue(obj[key]); msg != "" {
			return msg
		}
	}
	if msg, _ := obj["message"].(string); strings.TrimSpace(msg) != "" {
		return msg
	}
	return "上游未提供失败原因"
}

func errorMessageValue(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		if msg, _ := x["message"].(string); strings.TrimSpace(msg) != "" {
			return msg
		}
		if msg, _ := x["msg"].(string); strings.TrimSpace(msg) != "" {
			return msg
		}
	}
	return ""
}

// fetchImageBase64 下载图片并编码为 base64（响应体不包含 data URI 前缀）。
func fetchImageBase64(ctx context.Context, client *http.Client, url string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	const maxImageSize = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxImageSize {
		return "", fmt.Errorf("图片超过 %d MB", maxImageSize>>20)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// cloneImagePayload 复制客户端请求体，避免修改后影响故障转移的其它目标。
func cloneImagePayload(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func truncateImageText(raw []byte, n int) string {
	s := strings.TrimSpace(string(raw))
	if len(s) <= n {
		return s
	}
	return s[:n]
}
