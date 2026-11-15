-- 生图作业：网关异步生图的持久化记录。
--
-- 与密钥表、渠道表放在同一个 SQLite 里，重启后可以恢复：
--   - 已拿到上游 task_id 的异步任务，重启后继续轮询；
--   - 尚未开始的排队任务，重启后重新执行；
--   - 同步任务提交途中被打断的，标记为 gateway_restarted，不自动重跑（避免重复计费）。
CREATE TABLE IF NOT EXISTS image_jobs (
    id                TEXT PRIMARY KEY,
    api_key_id        TEXT NOT NULL,
    request_id        TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL,
    status            TEXT NOT NULL,
    stage             TEXT NOT NULL DEFAULT '',
    payload           TEXT NOT NULL DEFAULT '',
    allowed_channels  TEXT NOT NULL DEFAULT '[]',
    channel_id        TEXT NOT NULL DEFAULT '',
    channel_key       TEXT NOT NULL DEFAULT '',
    base_url          TEXT NOT NULL DEFAULT '',
    response_format   TEXT NOT NULL DEFAULT '',
    upstream_task_id  TEXT NOT NULL DEFAULT '',
    result            TEXT NOT NULL DEFAULT '',
    error             TEXT NOT NULL DEFAULT '',
    error_code        TEXT NOT NULL DEFAULT '',
    poll_count        INTEGER NOT NULL DEFAULT 0,
    cancel_requested  INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    expires_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_image_jobs_expires ON image_jobs(expires_at);
CREATE INDEX IF NOT EXISTS idx_image_jobs_status ON image_jobs(status);

-- 同一把密钥下的 request_id 幂等：重复提交返回同一个作业。
CREATE UNIQUE INDEX IF NOT EXISTS idx_image_jobs_request
    ON image_jobs(api_key_id, request_id) WHERE request_id <> '';
