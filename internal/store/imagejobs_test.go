package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestImageJobLifecycle 覆盖作业创建、提交、完成与清理。
func TestImageJobLifecycle(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	job := &ImageJob{
		ID:                "imgjob_test",
		APIKeyID:          "key-1",
		RequestID:         "req-1",
		Model:             "img",
		Status:            ImageJobQueued,
		Stage:             "queued",
		Payload:           []byte(`{"prompt":"a cat"}`),
		AllowedChannelIDs: []string{"ch-1"},
		BaseURL:           "https://gw.example.com",
		ResponseFormat:    "url",
		CreatedAt:         now.UnixMilli(),
		UpdatedAt:         now.UnixMilli(),
		ExpiresAt:         now.Add(time.Hour).UnixMilli(),
	}
	if err := s.CreateImageJob(job); err != nil {
		t.Fatalf("create: %v", err)
	}

	// request_id 幂等查询
	found, err := s.FindImageJobByRequestID("key-1", "req-1")
	if err != nil || found == nil {
		t.Fatalf("find by request id: %v, %v", found, err)
	}
	if len(found.AllowedChannelIDs) != 1 || found.AllowedChannelIDs[0] != "ch-1" {
		t.Fatalf("allowed = %v", found.AllowedChannelIDs)
	}

	if err := s.MarkImageJobRunning(job.ID, "ch-1", "sk-1"); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	running, _ := s.GetImageJob(job.ID)
	if running.Stage != "submitting" {
		t.Fatalf("stage after running = %q, want submitting", running.Stage)
	}
	if err := s.SetImageJobTask(job.ID, "ch-1", "sk-1", "task-9"); err != nil {
		t.Fatalf("set task: %v", err)
	}
	current, err := s.GetImageJob(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.UpstreamTaskID != "task-9" || len(current.Payload) != 0 {
		t.Fatalf("task = %q payload = %q", current.UpstreamTaskID, current.Payload)
	}
	if current.Stage != "polling" {
		t.Fatalf("stage after task = %q, want polling", current.Stage)
	}

	if err := s.FinishImageJob(job.ID, ImageJobSucceeded, []byte(`{"data":[]}`), "", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	current, _ = s.GetImageJob(job.ID)
	if current.Status != ImageJobSucceeded || len(current.Result) == 0 {
		t.Fatalf("status = %q result = %q", current.Status, current.Result)
	}
	// 终态不再保留进度阶段，避免出现 succeeded + submitting/polling 的组合
	if current.Stage != "" {
		t.Fatalf("stage after finish = %q, want empty", current.Stage)
	}

	// 已完成作业不再出现在待恢复列表里
	jobs, err := s.ListResumableImageJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("resumable = %v, err = %v", jobs, err)
	}

	// 过期后清理
	if err := s.DeleteExpiredImageJobs(now.Add(2 * time.Hour).UnixMilli()); err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if got, _ := s.GetImageJob(job.ID); got != nil {
		t.Fatalf("job should be deleted")
	}
}

// TestImageJobResumable 覆盖重启后的恢复分支：有 task_id 继续轮询，无 task_id 的同步任务标记中断。
func TestImageJobResumable(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UnixMilli()
	jobs := []*ImageJob{
		{ID: "a", APIKeyID: "k", Model: "img", Status: ImageJobRunning,
			ChannelID: "ch", ChannelKey: "sk", UpstreamTaskID: "task-1",
			CreatedAt: now, UpdatedAt: now, ExpiresAt: now + 3600_000},
		{ID: "b", APIKeyID: "k", Model: "img", Status: ImageJobRunning,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: now + 3600_000},
		{ID: "c", APIKeyID: "k", Model: "img", Status: ImageJobQueued,
			CreatedAt: now, UpdatedAt: now, ExpiresAt: now + 3600_000},
	}
	for _, j := range jobs {
		if err := s.CreateImageJob(j); err != nil {
			t.Fatalf("create %s: %v", j.ID, err)
		}
	}
	got, err := s.ListResumableImageJobs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("resumable = %d, want 3", len(got))
	}

	// 取消先到，迟到的完成写入不应覆盖终态
	if err := s.FinishImageJob("b", ImageJobCanceled, nil, "已取消", "canceled"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := s.FinishImageJob("b", ImageJobSucceeded, []byte(`{}`), "", ""); err != nil {
		t.Fatalf("late finish: %v", err)
	}
	b, _ := s.GetImageJob("b")
	if b.Status != ImageJobCanceled {
		t.Fatalf("status = %q, want canceled", b.Status)
	}
}
