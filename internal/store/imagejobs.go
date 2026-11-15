package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// 生图作业状态。
const (
	ImageJobQueued    = "queued"
	ImageJobRunning   = "running"
	ImageJobSucceeded = "succeeded"
	ImageJobFailed    = "failed"
	ImageJobCanceled  = "canceled"
)

// ImageJob 一条生图作业记录。
//
// Payload 是提交给上游的请求体（含参考图），拿到上游 task_id 或作业结束后清空，
// 避免大体积 base64 长期占用数据库。ChannelKey 与渠道表一样是明文存储。
type ImageJob struct {
	ID                string
	APIKeyID          string
	RequestID         string
	Model             string
	Status            string
	Stage             string
	Payload           []byte
	AllowedChannelIDs []string
	ChannelID         string
	ChannelKey        string
	BaseURL           string
	ResponseFormat    string
	UpstreamTaskID    string
	Result            []byte
	Error             string
	ErrorCode         string
	PollCount         int
	CancelRequested   bool
	CreatedAt         int64
	UpdatedAt         int64
	ExpiresAt         int64
}

const imageJobCols = `id, api_key_id, request_id, model, status, stage, payload,
	allowed_channels, channel_id, channel_key, base_url, response_format,
	upstream_task_id, result, error, error_code, poll_count, cancel_requested,
	created_at, updated_at, expires_at`

func scanImageJob(sc scanner) (*ImageJob, error) {
	j := &ImageJob{}
	var payload, allowed, result string
	var cancel int
	if err := sc.Scan(
		&j.ID, &j.APIKeyID, &j.RequestID, &j.Model, &j.Status, &j.Stage, &payload,
		&allowed, &j.ChannelID, &j.ChannelKey, &j.BaseURL, &j.ResponseFormat,
		&j.UpstreamTaskID, &result, &j.Error, &j.ErrorCode, &j.PollCount, &cancel,
		&j.CreatedAt, &j.UpdatedAt, &j.ExpiresAt,
	); err != nil {
		return nil, err
	}
	j.Payload = []byte(payload)
	j.Result = []byte(result)
	j.CancelRequested = cancel != 0
	if allowed != "" {
		_ = json.Unmarshal([]byte(allowed), &j.AllowedChannelIDs)
	}
	if j.AllowedChannelIDs == nil {
		j.AllowedChannelIDs = []string{}
	}
	return j, nil
}

// CreateImageJob 写入一条新作业。
func (s *Store) CreateImageJob(j *ImageJob) error {
	allowed, err := marshalJSON(j.AllowedChannelIDs)
	if err != nil {
		return err
	}
	cancel := 0
	if j.CancelRequested {
		cancel = 1
	}
	_, err = s.db.Exec(`INSERT INTO image_jobs(`+imageJobCols+`)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.APIKeyID, j.RequestID, j.Model, j.Status, j.Stage, string(j.Payload),
		allowed, j.ChannelID, j.ChannelKey, j.BaseURL, j.ResponseFormat,
		j.UpstreamTaskID, string(j.Result), j.Error, j.ErrorCode, j.PollCount, cancel,
		j.CreatedAt, j.UpdatedAt, j.ExpiresAt,
	)
	return err
}

// GetImageJob 按 id 查询作业，不存在返回 (nil, nil)。
func (s *Store) GetImageJob(id string) (*ImageJob, error) {
	j, err := scanImageJob(s.db.QueryRow(`SELECT `+imageJobCols+` FROM image_jobs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// FindImageJobByRequestID 按「密钥 + request_id」查询，用于提交幂等。
func (s *Store) FindImageJobByRequestID(apiKeyID, requestID string) (*ImageJob, error) {
	if requestID == "" {
		return nil, nil
	}
	j, err := scanImageJob(s.db.QueryRow(
		`SELECT `+imageJobCols+` FROM image_jobs WHERE api_key_id = ? AND request_id = ?`,
		apiKeyID, requestID,
	))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// MarkImageJobRunning 记录作业开始执行时选中的渠道与密钥。
func (s *Store) MarkImageJobRunning(id, channelID, key string) error {
	_, err := s.db.Exec(
		`UPDATE image_jobs SET status = ?, stage = 'submitting', channel_id = ?, channel_key = ?, updated_at = ?
		 WHERE id = ? AND status IN (?, ?)`,
		ImageJobRunning, channelID, key, time.Now().UnixMilli(), id,
		ImageJobQueued, ImageJobRunning,
	)
	return err
}

// SetImageJobTask 保存上游 task_id 并清空请求体（后续轮询不再需要参考图）。
func (s *Store) SetImageJobTask(id, channelID, key, taskID string) error {
	_, err := s.db.Exec(
		`UPDATE image_jobs SET upstream_task_id = ?, channel_id = ?, channel_key = ?,
			payload = '', stage = 'polling', updated_at = ?
		 WHERE id = ? AND status IN (?, ?)`,
		taskID, channelID, key, time.Now().UnixMilli(), id,
		ImageJobQueued, ImageJobRunning,
	)
	return err
}

// SetImageJobStage 更新进度（阶段 + 已轮询次数）。
func (s *Store) SetImageJobStage(id, stage string, pollCount int) error {
	_, err := s.db.Exec(
		`UPDATE image_jobs SET stage = ?, poll_count = ?, updated_at = ?
		 WHERE id = ? AND status = ?`,
		stage, pollCount, time.Now().UnixMilli(), id, ImageJobRunning,
	)
	return err
}

// FinishImageJob 写入终态。已处于终态的作业不会被覆盖（取消与迟到的结果竞争时以先到的为准）。
func (s *Store) FinishImageJob(id, status string, result []byte, errMsg, errCode string) error {
	_, err := s.db.Exec(
		`UPDATE image_jobs SET status = ?, result = ?, error = ?, error_code = ?,
			payload = '', updated_at = ?
		 WHERE id = ? AND status IN (?, ?)`,
		status, string(result), errMsg, errCode, time.Now().UnixMilli(), id,
		ImageJobQueued, ImageJobRunning,
	)
	return err
}

// ListResumableImageJobs 返回重启后需要处理的作业（排队中或执行中）。
func (s *Store) ListResumableImageJobs() ([]*ImageJob, error) {
	rows, err := s.db.Query(
		`SELECT `+imageJobCols+` FROM image_jobs WHERE status IN (?, ?) ORDER BY created_at ASC`,
		ImageJobQueued, ImageJobRunning,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*ImageJob{}
	for rows.Next() {
		j, err := scanImageJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// DeleteExpiredImageJobs 清理过期且已结束的作业。
func (s *Store) DeleteExpiredImageJobs(nowMS int64) error {
	_, err := s.db.Exec(
		`DELETE FROM image_jobs WHERE expires_at < ? AND status NOT IN (?, ?)`,
		nowMS, ImageJobQueued, ImageJobRunning,
	)
	return err
}
