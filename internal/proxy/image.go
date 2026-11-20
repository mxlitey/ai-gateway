package proxy

import (
	"bytes"
	"context"
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

// ImageGenResult 一次生图调用的最终结果。
type ImageGenResult struct {
	// StatusCode 对外返回的状态码；2xx 表示成功。
	StatusCode int
	// Body 上游原始响应体（成功时），网关不做任何形态转换。
	Body []byte
	// UpstreamStatus 上游最后一次响应的状态码（0 表示网络错误），用于错误日志与冷却。
	UpstreamStatus int
	// Canceled 表示作业被调用方取消（而不是失败）；此时 StatusCode 无意义。
	Canceled bool
	// Error 失败原因（成功时为空）。
	Error string
}

// GenerateImage 按渠道配置调用上游生图接口，成功时原样返回上游响应体。
//
// 支持两种模式：
//   - sync：一次 POST 直接返回图片；
//   - async：POST 提交任务拿到 task_id，再按配置轮询任务直到成功/失败/超时。
//
// clientReq 仅用于解析渠道请求头占位符，可为 nil。
// 网关不落盘、不改写：上游返回什么就透传什么（url、b64_json、usage、output_images 等）。
// 唯一的校验是「必须有图」：上游 2xx 但取不到图片时按失败处理。
func GenerateImage(ctx context.Context, client *http.Client, ch *model.Channel, key string, input map[string]any, publicModel string, clientReq *http.Request) ImageGenResult {
	cfg := ch.ImageConfig()
	task := SubmitImageTask(ctx, client, ch, key, input, publicModel, clientReq)
	if task.Result != nil {
		return *task.Result
	}
	return PollImageTask(ctx, client, ch, key, cfg, task.TaskID, clientReq, nil)
}

// ImageTask 提交阶段的结果：TaskID 非空表示需要继续轮询；
// Result 非空表示已有最终结果（同步模式、提交失败或提交即出图）。
type ImageTask struct {
	TaskID string
	Result *ImageGenResult
}

// SubmitImageTask 完成一次提交：调用上游生图/编辑接口，返回 task_id 或已完成的响应。
//
// 拆分出来是为了让异步作业在拿到 task_id 后立即落库，网关重启后可以继续轮询。
func SubmitImageTask(ctx context.Context, client *http.Client, ch *model.Channel, key string, input map[string]any, publicModel string, clientReq *http.Request) ImageTask {
	cfg := ch.ImageConfig()
	if cfg.Mode == "" {
		return ImageTask{Result: &ImageGenResult{StatusCode: http.StatusBadRequest, Error: "渠道未配置生图能力"}}
	}
	upstreamModel, ok := ch.ResolveUpstreamModel(publicModel)
	if !ok {
		return ImageTask{Result: &ImageGenResult{StatusCode: http.StatusBadRequest, Error: "模型 " + publicModel + " 未在该渠道配置"}}
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
		if ctx.Err() == context.Canceled {
			return ImageTask{Result: &ImageGenResult{Canceled: true, Error: "生图作业已取消"}}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return ImageTask{Result: &ImageGenResult{
				StatusCode: http.StatusGatewayTimeout,
				Error:      fmt.Sprintf("生图上游响应超时（%s）", time.Duration(syncTimeout)*time.Millisecond),
			}}
		}
		return ImageTask{Result: &ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: 0, Error: "请求上游失败: " + err.Error()}}
	}
	if status < 200 || status >= 300 {
		return ImageTask{Result: &ImageGenResult{
			StatusCode:     http.StatusBadGateway,
			UpstreamStatus: status,
			Error:          fmt.Sprintf("生图上游拒绝 (HTTP %d): %s", status, truncateImageText(raw, 800)),
		}}
	}

	var submit map[string]any
	_ = json.Unmarshal(raw, &submit)

	if cfg.Mode == model.ImageGenSync {
		if err := requireImage(submit, cfg.ImagesField); err != nil {
			return ImageTask{Result: &ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: status, Error: err.Error()}}
		}
		return ImageTask{Result: &ImageGenResult{StatusCode: http.StatusOK, Body: raw}}
	}

	// 兼容「提交即返回图片」的服务商：有图片就直接返回，不再轮询
	if images := extractImages(submit, cfg.ImagesField); len(images) > 0 {
		return ImageTask{Result: &ImageGenResult{StatusCode: http.StatusOK, Body: raw}}
	}

	taskID := stringField(submit, cfg.TaskIDField)
	if taskID == "" {
		return ImageTask{Result: &ImageGenResult{
			StatusCode:     http.StatusBadGateway,
			UpstreamStatus: status,
			Error:          "上游未返回任务 ID（字段 " + cfg.TaskIDField + "）: " + truncateImageText(raw, 400),
		}}
	}
	return ImageTask{TaskID: taskID}
}

// ImagePollFunc 轮询进度回调，n 为已完成的轮询次数。
type ImagePollFunc func(n int)

// PollImageTask 轮询异步生图任务，直到成功、失败、超时或上下文取消。
func PollImageTask(ctx context.Context, client *http.Client, ch *model.Channel, key string, cfg model.ImageGenConfig, taskID string, clientReq *http.Request, onPoll ImagePollFunc) ImageGenResult {
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()

	interval := time.Duration(cfg.PollIntervalMS) * time.Millisecond
	pollURL := channel.BaseURL(ch) + strings.ReplaceAll(cfg.PollPath, "{task_id}", taskID)
	deadline := time.Duration(cfg.TimeoutMS) * time.Millisecond
	polls := 0

	for {
		raw, status, err := doImageRequest(pollCtx, client, http.MethodGet, pollURL, nil, ch, key, cfg.PollHeaders, clientReq)
		if err != nil {
			if ctx.Err() == context.Canceled {
				return ImageGenResult{Canceled: true, Error: "生图作业已取消"}
			}
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
			if err := requireImage(task, cfg.ImagesField); err != nil {
				return ImageGenResult{StatusCode: http.StatusBadGateway, UpstreamStatus: status, Error: err.Error()}
			}
			return ImageGenResult{StatusCode: http.StatusOK, Body: raw}

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

		polls++
		if onPoll != nil {
			onPoll(polls)
		}
		select {
		case <-pollCtx.Done():
			if ctx.Err() == context.Canceled {
				return ImageGenResult{Canceled: true, Error: "生图作业已取消"}
			}
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

// requireImage 校验上游响应里确实有图片。
//
// 网关只做透传，不转换响应形态；这里唯一要做的是避免把「上游 200 但没有图」
// 当成成功交付给调用方。图片位置按渠道的 ImagesField 定位，并兼容常见的
// data / output_images 形状。
func requireImage(upstream map[string]any, imagesField string) error {
	if len(extractImages(upstream, imagesField)) > 0 {
		return nil
	}
	if strings.TrimSpace(imagesField) == "" {
		imagesField = "data / output_images"
	}
	return fmt.Errorf("上游未返回图片（字段 %s）", imagesField)
}

// extractImages 从上游对象中提取图片列表，兼容字符串数组与对象数组。
func extractImages(obj map[string]any, path string) []map[string]any {
	if obj == nil {
		return nil
	}
	// 先按渠道配置的字段定位；配置为空时走常见形状兜底
	candidates := []any{lookupPath(obj, path)}
	for _, key := range []string{"data", "output_images", "images"} {
		candidates = append(candidates, obj[key])
	}
	var list []any
	for _, c := range candidates {
		if list = imageListFrom(c); len(list) > 0 {
			break
		}
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		switch v := item.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				out = append(out, map[string]any{"url": v})
			}
		case map[string]any:
			entry := map[string]any{}
			for _, k := range []string{"url", "image_url"} {
				if u, _ := v[k].(string); strings.TrimSpace(u) != "" {
					entry["url"] = strings.TrimSpace(u)
					break
				}
			}
			for _, k := range []string{"b64_json", "base64"} {
				if b, _ := v[k].(string); strings.TrimSpace(b) != "" {
					entry["b64_json"] = b
					break
				}
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
