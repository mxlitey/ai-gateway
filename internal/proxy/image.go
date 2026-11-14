package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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

	// 带参考图且配置了编辑路径时改走编辑接口，参考图按渠道配置的形态重塑。
	refs := referenceURLs(input)
	submitPath := cfg.Path
	useEdit := len(refs) > 0 && cfg.EditPath != ""
	if useEdit {
		submitPath = cfg.EditPath
	}
	payload := buildImagePayload(input, cfg, upstreamModel, responseFormat, refs, useEdit)

	// 同步生图可能要等上游数分钟：用渠道级超时兜底，而不是共享 Transport 的响应头超时。
	syncTimeout := cfg.SyncTimeoutMS
	if syncTimeout <= 0 {
		syncTimeout = cfg.TimeoutMS
	}
	if syncTimeout <= 0 {
		// 未经 Normalized 的配置兜底为 120 秒，与历史行为一致
		syncTimeout = 120000
	}
	submitCtx, cancel := context.WithTimeout(ctx, time.Duration(syncTimeout)*time.Millisecond)
	defer cancel()

	submitURL := channel.BaseURL(ch) + submitPath
	slog.Debug("[image] -> submit", "channel", ch.Name, "url", submitURL, "mode", cfg.Mode, "edit", useEdit)

	raw, status, err := doImageRequest(submitCtx, client, http.MethodPost, submitURL, payload, ch, key, cfg.SubmitHeaders, clientReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ImageGenResult{
				StatusCode: http.StatusGatewayTimeout,
				Error:      fmt.Sprintf("生图上游响应超时（%s）", time.Duration(syncTimeout)*time.Millisecond),
			}
		}
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
func extractImages(obj map[string]any, path string) []map[string]any {
	if obj == nil {
		return nil
	}
	list := imageListFrom(lookupPath(obj, path))
	if len(list) == 0 {
		// 兼容同步型服务商返回标准 OpenAI data 字段的场景
		list = imageListFrom(obj["data"])
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

// imageListFrom 把路径取值结果归一化成列表。
func imageListFrom(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any, string:
		return []any{x}
	}
	return nil
}

// lookupPath 按路径读取嵌套字段，支持 a.b、a.0.b 与 a[].b（[] 表示遍历数组逐个取值）。
func lookupPath(obj map[string]any, path string) any {
	path = strings.TrimSpace(path)
	if path == "" || obj == nil {
		return nil
	}
	return walkPath(obj, strings.Split(path, "."))
}

func walkPath(cur any, segs []string) any {
	for len(segs) > 0 {
		seg := strings.TrimSpace(segs[0])
		segs = segs[1:]
		if seg == "" {
			continue
		}
		seg = strings.TrimSuffix(seg, "[]")
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg]
		case []any:
			if idx, err := strconv.Atoi(seg); err == nil {
				if idx < 0 || idx >= len(node) {
					return nil
				}
				cur = node[idx]
				continue
			}
			next := make([]any, 0, len(node))
			for _, item := range node {
				if m, ok := item.(map[string]any); ok {
					next = append(next, lookupPath(m, seg))
				}
			}
			cur = next
		default:
			return nil
		}
	}
	return cur
}

// stringField 按路径读取字符串值。
func stringField(obj map[string]any, path string) string {
	v, _ := lookupPath(obj, path).(string)
	return strings.TrimSpace(v)
}

// imageErrorText 从上游响应中提取失败原因。
func imageErrorText(obj map[string]any, field string) string {
	if field != "" {
		if msg := errorMessageValue(lookupPath(obj, field)); msg != "" {
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

// buildImagePayload 按渠道配置把客户端请求转换成上游请求体。
//
// 转换顺序：编辑分流 -> 参数改名 -> 删除参数 -> 固定参数 -> response_format 策略。
// 所有能力都由渠道配置驱动，不针对具体服务商写死。
func buildImagePayload(input map[string]any, cfg model.ImageGenConfig, upstreamModel, responseFormat string, refs []string, useEdit bool) map[string]any {
	payload := cloneImagePayload(input)
	payload["model"] = upstreamModel
	delete(payload, "response_format")

	if useEdit {
		field := strings.TrimSpace(cfg.ReferenceField)
		if field == "" {
			field = "image_url"
		}
		delete(payload, "image_url")
		payload[field] = shapeReference(refs, cfg.ReferenceShape)
	}

	for from, to := range cfg.ParamMap {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" || to == "" || from == to {
			continue
		}
		v, ok := payload[from]
		if !ok {
			continue
		}
		payload[to] = v
		delete(payload, from)
	}

	for _, name := range cfg.DropParams {
		if name = strings.TrimSpace(name); name != "" {
			delete(payload, name)
		}
	}

	for k, v := range cfg.StaticParams {
		if _, exists := payload[k]; !exists {
			payload[k] = v
		}
	}

	switch cfg.ResponseFormatMode {
	case model.ImageFormatPass:
		if strings.TrimSpace(responseFormat) != "" {
			payload["response_format"] = responseFormat
		}
	case model.ImageFormatURL:
		payload["response_format"] = "url"
	case model.ImageFormatB64:
		payload["response_format"] = "b64_json"
	}
	return payload
}

// referenceURLs 读取客户端传入的参考图地址（字符串或字符串数组）。
func referenceURLs(input map[string]any) []string {
	v, ok := input["image_url"]
	if !ok {
		return nil
	}
	var out []string
	switch x := v.(type) {
	case string:
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	case []string:
		for _, s := range x {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// shapeReference 按渠道配置把参考图地址转换成上游期望的形态。
func shapeReference(refs []string, shape string) any {
	switch shape {
	case model.ImageRefSingleURL:
		if len(refs) > 0 {
			return refs[0]
		}
		return ""
	case model.ImageRefURLObjectArray:
		out := make([]map[string]any, 0, len(refs))
		for _, u := range refs {
			out = append(out, map[string]any{"url": u})
		}
		return out
	case model.ImageRefImageURLObjectArray:
		out := make([]map[string]any, 0, len(refs))
		for _, u := range refs {
			out = append(out, map[string]any{"image_url": u})
		}
		return out
	default:
		return refs
	}
}

func truncateImageText(raw []byte, n int) string {
	s := strings.TrimSpace(string(raw))
	if len(s) <= n {
		return s
	}
	return s[:n]
}
