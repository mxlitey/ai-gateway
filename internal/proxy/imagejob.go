package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/czg95/ai-gateway-v2/internal/id"
	"github.com/czg95/ai-gateway-v2/internal/model"
	"github.com/czg95/ai-gateway-v2/internal/penalty"
	"github.com/czg95/ai-gateway-v2/internal/pricing"
	"github.com/czg95/ai-gateway-v2/internal/store"
)

// imageJobTTL 作业记录的保留时长。图片本身由上游或对象存储决定有效期，
// 但「取餐号」要比图片活得久，方便客户端在图片过期后仍能查到结果与失败原因。
const imageJobTTL = 24 * time.Hour

// imageJobObject 作业对象的类型名，便于客户端识别。
const imageJobObject = "image.generation.job"

// isImageJobRequest 判断是否走异步作业模式。
func (h *Handler) isImageJobRequest(r *http.Request, body map[string]any) bool {
	if v, ok := body["async"].(bool); ok && v {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-AIGW-Async"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// submitImageJob 创建作业并立即返回 202，实际生图在后台执行。
//
// 提交阶段只返回作业号，不代表已经产生图片，因此这条记录不记用量；
// 作业成功结束时由 finishJob 补写一条按次计费的用量。
func (h *Handler) submitImageJob(w http.ResponseWriter, r *http.Request, body map[string]any, modelName string, allowed []string, apiKey *model.APIKey, rec *Recorder) {
	defer rec.Discard()
	if h.store == nil {
		rec.Finish(http.StatusInternalServerError, "store unavailable")
		jsonError(w, http.StatusInternalServerError, "网关未就绪", "server_error")
		return
	}

	// 网关内部字段不能透传给上游，也不能参与 base64 参考图重写后的落库。
	payload := make(map[string]any, len(body))
	for k, v := range body {
		payload[k] = v
	}
	delete(payload, "async")

	requestID, _ := body["request_id"].(string)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = strings.TrimSpace(r.Header.Get("X-AIGW-Request-ID"))
	}
	delete(payload, "request_id")

	if requestID != "" {
		existing, err := h.store.FindImageJobByRequestID(apiKey.ID, requestID)
		if err != nil {
			slog.Warn("[image] 查询作业幂等记录失败", "err", err)
		} else if existing != nil {
			h.writeImageJob(w, existing, http.StatusOK, true)
			rec.Finish(http.StatusOK, "")
			return
		}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		rec.Finish(http.StatusBadRequest, "请求体编码失败")
		jsonError(w, http.StatusBadRequest, "请求体编码失败: "+err.Error(), "invalid_request_error")
		return
	}

	now := time.Now()
	job := &store.ImageJob{
		ID:                "imgjob_" + id.Hex(12),
		APIKeyID:          apiKey.ID,
		RequestID:         requestID,
		Model:             modelName,
		Status:            store.ImageJobQueued,
		Stage:             "queued",
		Payload:           raw,
		AllowedChannelIDs: allowed,
		CreatedAt:         now.UnixMilli(),
		UpdatedAt:         now.UnixMilli(),
		ExpiresAt:         now.Add(imageJobTTL).UnixMilli(),
	}
	if err := h.store.CreateImageJob(job); err != nil {
		// 并发提交同一个 request_id 时唯一索引会冲突，返回先写入的那条
		if requestID != "" {
			if existing, findErr := h.store.FindImageJobByRequestID(apiKey.ID, requestID); findErr == nil && existing != nil {
				h.writeImageJob(w, existing, http.StatusOK, true)
				rec.Finish(http.StatusOK, "")
				return
			}
		}
		slog.Error("[image] 创建生图作业失败", "err", err)
		rec.Finish(http.StatusInternalServerError, "create job failed")
		jsonError(w, http.StatusInternalServerError, "创建生图作业失败", "server_error")
		return
	}

	go h.launchImageJob(job.ID)
	h.writeImageJob(w, job, http.StatusAccepted, false)
	rec.Finish(http.StatusAccepted, "")
}

// HandleImageJob 处理 GET /v1/images/jobs/{id}：查询作业状态与结果。
func (h *Handler) HandleImageJob(w http.ResponseWriter, r *http.Request) {
	apiKey, errMsg := h.VerifyAPIKey(r)
	if apiKey == nil {
		jsonError(w, http.StatusUnauthorized, errMsg, "invalid_request_error")
		return
	}
	if h.store == nil {
		jsonError(w, http.StatusInternalServerError, "网关未就绪", "server_error")
		return
	}

	job, err := h.store.GetImageJob(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		slog.Warn("[image] 查询生图作业失败", "err", err)
	}
	// 不区分「不存在」与「不属于该密钥」，避免泄露他人的作业 id
	if job == nil || job.APIKeyID != apiKey.ID || job.ExpiresAt < time.Now().UnixMilli() {
		jsonError(w, http.StatusNotFound, "生图作业不存在或已过期", "not_found")
		return
	}
	h.writeImageJob(w, job, http.StatusOK, false)
}

// CancelImageJob 处理 DELETE /v1/images/jobs/{id}：取消作业。
func (h *Handler) CancelImageJob(w http.ResponseWriter, r *http.Request) {
	apiKey, errMsg := h.VerifyAPIKey(r)
	if apiKey == nil {
		jsonError(w, http.StatusUnauthorized, errMsg, "invalid_request_error")
		return
	}
	if h.store == nil {
		jsonError(w, http.StatusInternalServerError, "网关未就绪", "server_error")
		return
	}

	job, err := h.store.GetImageJob(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		slog.Warn("[image] 查询生图作业失败", "err", err)
	}
	if job == nil || job.APIKeyID != apiKey.ID || job.ExpiresAt < time.Now().UnixMilli() {
		jsonError(w, http.StatusNotFound, "生图作业不存在或已过期", "not_found")
		return
	}

	if job.Status == store.ImageJobQueued || job.Status == store.ImageJobRunning {
		if err := h.store.FinishImageJob(job.ID, store.ImageJobCanceled, nil, "作业已取消", "canceled"); err != nil {
			slog.Warn("[image] 取消生图作业失败", "err", err)
		}
		h.cancelImageJob(job.ID)
		if updated, err := h.store.GetImageJob(job.ID); err == nil && updated != nil {
			job = updated
		}
	}
	h.writeImageJob(w, job, http.StatusOK, false)
}

// writeImageJob 输出作业视图。
//
// 成功的作业把上游响应体顶层字段原样平铺进视图（data / created / usage /
// output_images 等），网关自带字段（id/status/stage/object/expires_at 等）同名优先，
// 保证作业视图既能被轮询客户端识别，也不丢失上游原始信息。
func (h *Handler) writeImageJob(w http.ResponseWriter, job *store.ImageJob, status int, reused bool) {
	view := map[string]any{}
	if job.Status == store.ImageJobSucceeded && len(job.Result) > 0 {
		var body map[string]any
		if err := json.Unmarshal(job.Result, &body); err == nil {
			for k, v := range body {
				view[k] = v
			}
		}
	}

	view["id"] = job.ID
	view["object"] = imageJobObject
	view["status"] = job.Status
	view["stage"] = job.Stage
	view["model"] = job.Model
	view["created_at"] = job.CreatedAt / 1000
	view["updated_at"] = job.UpdatedAt / 1000
	view["expires_at"] = job.ExpiresAt / 1000
	view["poll_count"] = job.PollCount
	if reused {
		view["reused"] = true
	}
	if job.Error != "" {
		view["error"] = map[string]any{
			"code":      job.ErrorCode,
			"message":   job.Error,
			"retryable": imageJobRetryable(job.ErrorCode),
		}
	}
	raw, err := json.Marshal(view)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "编码作业响应失败", "server_error")
		return
	}
	if status == http.StatusAccepted {
		w.Header().Set("Location", "/v1/images/jobs/"+job.ID)
	}
	writeRawJSON(w, status, raw)
}

// imageJobRetryable 参数类错误重试也不会成功，其余（网络、限流、5xx）可重试。
func imageJobRetryable(code string) bool {
	switch code {
	case "invalid_request", "canceled", "auth_error":
		return false
	}
	return true
}

// launchImageJob 启动后台执行，并登记取消函数。
func (h *Handler) launchImageJob(jobID string) {
	ctx, cancel := context.WithCancel(context.Background())
	h.jobMu.Lock()
	if h.jobCancels == nil {
		h.jobCancels = map[string]context.CancelFunc{}
	}
	h.jobCancels[jobID] = cancel
	h.jobMu.Unlock()

	defer func() {
		cancel()
		h.jobMu.Lock()
		delete(h.jobCancels, jobID)
		h.jobMu.Unlock()
	}()
	h.runImageJob(ctx, jobID)
}

// cancelImageJob 取消正在执行的作业（已结束或已消失时什么也不做）。
func (h *Handler) cancelImageJob(jobID string) {
	h.jobMu.Lock()
	cancel := h.jobCancels[jobID]
	h.jobMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runImageJob 执行一条作业：提交 → 落 task_id → 轮询。
func (h *Handler) runImageJob(ctx context.Context, jobID string) {
	job, err := h.store.GetImageJob(jobID)
	if err != nil || job == nil {
		return
	}
	if job.Status != store.ImageJobQueued && job.Status != store.ImageJobRunning {
		return
	}
	if job.UpstreamTaskID != "" {
		h.pollResumedJob(ctx, job)
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		h.recordJobFailure(job.ID, "", "", 0, "作业请求体已损坏: "+err.Error())
		h.finishJob(job.ID, store.ImageJobFailed, nil, "作业请求体已损坏: "+err.Error(), "internal_error")
		return
	}
	targets, err := h.balancer.SelectImageTarget(job.Model, job.AllowedChannelIDs)
	if err != nil {
		h.recordJobFailure(job.ID, "", "", 0, "选择生图渠道失败: "+err.Error())
		h.finishJob(job.ID, store.ImageJobFailed, nil, "选择生图渠道失败: "+err.Error(), "internal_error")
		return
	}
	if len(targets) == 0 {
		msg := "模型 " + job.Model + " 没有可用的生图渠道（未标记生图、渠道停用或密钥都在熔断中）"
		h.recordJobFailure(job.ID, "", "", 0, msg)
		h.finishJob(job.ID, store.ImageJobFailed, nil, msg, "no_channel")
		return
	}

	cdCfg := h.coolerConfig()
	var lastErr, lastCode string
	for _, t := range targets {
		if ctx.Err() != nil {
			h.finishJob(job.ID, store.ImageJobCanceled, nil, "作业已取消", "canceled")
			return
		}
		if err := h.store.MarkImageJobRunning(job.ID, t.Channel.ID, t.Key); err != nil {
			slog.Warn("[image] 更新作业状态失败", "job", job.ID, "err", err)
		}
		task := SubmitImageTask(ctx, h.imageClient, t.Channel, t.Key, payload, job.Model, nil)
		if task.Result != nil {
			if task.Result.Canceled {
				h.finishJob(job.ID, store.ImageJobCanceled, nil, "作业已取消", "canceled")
				return
			}
			if task.Result.StatusCode >= 200 && task.Result.StatusCode < 300 {
				h.finishJob(job.ID, store.ImageJobSucceeded, task.Result.Body, "", "")
				return
			}
			lastErr = task.Result.Error
			lastCode = imageJobErrorCode(task.Result)
			if shouldCoolImageFailure(task.Result.UpstreamStatus) {
				h.cooler.Fail(t.Channel.ID, t.Key, cdCfg, failStatusOf(task.Result))
			}
			// 每一次失败的上游尝试都要留痕，故障转移的每一跳各算一次
			h.recordJobFailure(job.ID, t.Channel.ID, t.Key, failStatusOf(task.Result), task.Result.Error)
			continue
		}

		if err := h.store.SetImageJobTask(job.ID, t.Channel.ID, t.Key, task.TaskID); err != nil {
			slog.Warn("[image] 保存上游任务 ID 失败", "job", job.ID, "err", err)
		}
		res := PollImageTask(ctx, h.imageClient, t.Channel, t.Key, t.Channel.ImageConfig(),
			task.TaskID, nil, h.jobProgress(job.ID))
		h.finishFromResult(job.ID, t.Channel.ID, t.Key, res, cdCfg)
		return
	}
	h.finishJob(job.ID, store.ImageJobFailed, nil, "所有生图渠道均失败。最后错误: "+lastErr, lastCode)
}

// pollResumedJob 网关重启后继续轮询已提交的异步任务。
func (h *Handler) pollResumedJob(ctx context.Context, job *store.ImageJob) {
	ch, err := h.store.GetChannel(job.ChannelID)
	if err != nil || ch == nil || !ch.Enabled {
		msg := "作业所属渠道已不可用，无法继续轮询上游任务"
		// 轮询确实打到了上游，失败要留痕；渠道已删除时归到（网关）只写错误日志
		h.recordJobFailure(job.ID, job.ChannelID, job.ChannelKey, 0, msg)
		h.finishJob(job.ID, store.ImageJobFailed, nil, msg, "resume_failed")
		return
	}
	if err := h.store.SetImageJobStage(job.ID, "polling", job.PollCount); err != nil {
		slog.Warn("[image] 更新作业进度失败", "job", job.ID, "err", err)
	}
	res := PollImageTask(ctx, h.imageClient, ch, job.ChannelKey, ch.ImageConfig(),
		job.UpstreamTaskID, nil, h.jobProgress(job.ID))
	h.finishFromResult(job.ID, ch.ID, job.ChannelKey, res, h.coolerConfig())
}

// jobProgress 返回轮询进度回调，每 3 次写一次库，避免频繁落盘。
func (h *Handler) jobProgress(jobID string) ImagePollFunc {
	return func(n int) {
		if n != 1 && n%3 != 0 {
			return
		}
		if err := h.store.SetImageJobStage(jobID, "polling", n); err != nil {
			slog.Warn("[image] 更新作业进度失败", "job", jobID, "err", err)
		}
	}
}

// finishFromResult 把一次上游结果写入作业终态，并按策略冷却失败密钥。
func (h *Handler) finishFromResult(jobID, channelID, key string, res ImageGenResult, cdCfg penalty.Config) {
	switch {
	case res.Canceled:
		h.finishJob(jobID, store.ImageJobCanceled, nil, "作业已取消", "canceled")
	case res.StatusCode >= 200 && res.StatusCode < 300:
		h.finishJob(jobID, store.ImageJobSucceeded, res.Body, "", "")
	default:
		if shouldCoolImageFailure(res.UpstreamStatus) {
			h.cooler.Fail(channelID, key, cdCfg, failStatusOf(&res))
		}
		h.recordJobFailure(jobID, channelID, key, failStatusOf(&res), res.Error)
		h.finishJob(jobID, store.ImageJobFailed, nil, res.Error, imageJobErrorCode(&res))
	}
}

// finishJob 写终态；写入失败只记录日志，作业仍会按原状态过期清理。
//
// 成功时补写一条用量：生图按请求次数计费，一次作业算一次调用，
// 与这次请求实际返回几张图无关。失败痕迹由 recordJobFailure 按「每次上游尝试」
// 单独写入，因此这里不处理失败。
func (h *Handler) finishJob(jobID, status string, result []byte, errMsg, errCode string) {
	// 终态会清空多数字段，先取出作业元数据供计费使用
	job, jobErr := h.store.GetImageJob(jobID)
	if err := h.store.FinishImageJob(jobID, status, result, errMsg, errCode); err != nil {
		slog.Error("[image] 写入作业终态失败", "job", jobID, "status", status, "err", err)
		return
	}
	if status == store.ImageJobSucceeded && jobErr == nil && job != nil {
		h.recordJobUsage(job)
	}
}

// recordJobFailure 为一次失败的上游尝试补写错误日志与调用明细。
//
// 异步作业的故障转移同样是「每次都真实请求了上游」，因此每次尝试各写一条，
// 与同步生图的调用明细口径一致。channelID 为空表示失败发生在上游请求之前
// （作业请求体损坏、无可用渠道等网关级拒绝），只写错误日志。
func (h *Handler) recordJobFailure(jobID, channelID, key string, upstreamStatus int, errMsg string) {
	job, _ := h.store.GetImageJob(jobID)
	if job == nil {
		return
	}
	status := upstreamStatus
	if status == 0 {
		status = http.StatusBadGateway
	}

	channelName, baseURL := "（网关）", ""
	if channelID != "" {
		channelName = ""
		if ch, err := h.store.GetChannel(channelID); err == nil && ch != nil {
			channelName, baseURL = ch.Name, ch.BaseURL
		}
	}

	msg := truncate(errMsg, 2000)
	e := &model.ErrorLog{
		ChannelID:   channelID,
		ChannelName: channelName,
		BaseURL:     baseURL,
		Model:       job.Model,
		Status:      status,
		KeyHint:     model.KeyHint(key),
		Message:     msg,
	}
	go func() {
		if err := h.store.InsertErrorLog(e); err != nil {
			slog.Error("[errorlog] 写入失败", "err", err)
		}
	}()

	if channelID == "" {
		return
	}

	row := model.RequestLog{
		CreatedAt:       time.Now().UnixMilli(),
		APIKeyID:        job.APIKeyID,
		ChannelID:       channelID,
		ChannelName:     channelName,
		Endpoint:        "images",
		Model:           job.Model,
		UpstreamKeyHint: model.KeyHint(key),
		Status:          status,
		Attempts:        1,
		Error:           msg,
		PriceSource:     pricing.SourceNone,
	}
	if apiKey, err := h.store.GetAPIKey(job.APIKeyID); err == nil && apiKey != nil {
		row.APIKeyName = apiKey.Name
	}
	RecordUsage(h.store, h.pricer, row)
}

// recordJobUsage 作业成功时补写一条用量，按一次请求计费。
func (h *Handler) recordJobUsage(job *store.ImageJob) {
	row := model.RequestLog{
		CreatedAt:   job.CreatedAt,
		APIKeyID:    job.APIKeyID,
		ChannelID:   job.ChannelID,
		Endpoint:    "images",
		Model:       job.Model,
		Status:      http.StatusOK,
		Attempts:    1,
		PriceSource: pricing.SourceNone,
	}
	if key, err := h.store.GetAPIKey(job.APIKeyID); err == nil && key != nil {
		row.APIKeyName = key.Name
	}
	if ch, err := h.store.GetChannel(job.ChannelID); err == nil && ch != nil {
		row.ChannelName = ch.Name
		row.UpstreamKeyHint = model.KeyHint(job.ChannelKey)
	}
	// 上游响应里若有 usage（部分服务商返回），照实记入 token；没有则保持 0
	ExtractUsage(&row, job.Result)
	RecordUsage(h.store, h.pricer, row)
}

// failStatusOf 归一化失败状态码，供冷却器使用。
func failStatusOf(res *ImageGenResult) int {
	if res.UpstreamStatus > 0 {
		return res.UpstreamStatus
	}
	if res.StatusCode > 0 {
		return res.StatusCode
	}
	return http.StatusBadGateway
}

// imageJobErrorCode 把一次失败映射成稳定的错误码，供客户端判断是否值得重试。
func imageJobErrorCode(res *ImageGenResult) string {
	switch {
	case res.StatusCode == http.StatusGatewayTimeout:
		return "timeout"
	case res.UpstreamStatus == 0:
		return "network_error"
	case res.UpstreamStatus == http.StatusUnauthorized || res.UpstreamStatus == http.StatusForbidden:
		return "auth_error"
	case res.UpstreamStatus == http.StatusNotFound:
		return "not_found"
	case res.UpstreamStatus == http.StatusTooManyRequests:
		return "rate_limited"
	case res.UpstreamStatus == http.StatusBadRequest,
		res.UpstreamStatus == http.StatusRequestEntityTooLarge,
		res.UpstreamStatus == http.StatusUnsupportedMediaType,
		res.UpstreamStatus == http.StatusUnprocessableEntity:
		return "invalid_request"
	}
	return "upstream_error"
}

// ResumeImageJobs 启动时恢复未完成的作业：
//   - 已拿到上游 task_id 的异步任务，继续轮询；
//   - 仍在排队的任务，重新执行；
//   - 提交途中被重启打断的同步任务无法恢复，标记为 gateway_restarted，不自动重跑。
func (h *Handler) ResumeImageJobs() {
	if h.store == nil {
		return
	}
	if err := h.store.DeleteExpiredImageJobs(time.Now().UnixMilli()); err != nil {
		slog.Warn("[image] 清理过期作业失败", "err", err)
	}
	jobs, err := h.store.ListResumableImageJobs()
	if err != nil {
		slog.Error("[image] 读取待恢复作业失败", "err", err)
		return
	}
	for _, job := range jobs {
		switch {
		case job.UpstreamTaskID != "":
			slog.Info("[image] 恢复异步生图作业", "job", job.ID, "task", job.UpstreamTaskID)
			go h.launchImageJob(job.ID)
		case job.Status == store.ImageJobQueued:
			slog.Info("[image] 重新执行排队中的生图作业", "job", job.ID)
			go h.launchImageJob(job.ID)
		case job.CancelRequested:
			_ = h.store.FinishImageJob(job.ID, store.ImageJobCanceled, nil, "作业已取消", "canceled")
		default:
			_ = h.store.FinishImageJob(job.ID, store.ImageJobFailed, nil,
				"网关重启导致同步生图作业中断，请重新提交", "gateway_restarted")
		}
	}
}

// CleanupImageJobs 定期清理过期作业记录。
func (h *Handler) CleanupImageJobs(ctx context.Context) {
	if h.store == nil {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.store.DeleteExpiredImageJobs(time.Now().UnixMilli()); err != nil {
				slog.Warn("[image] 清理过期作业失败", "err", err)
			}
		}
	}
}
