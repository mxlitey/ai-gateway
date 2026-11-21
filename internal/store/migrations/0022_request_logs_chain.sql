-- 同一次客户端请求发生故障转移时，各次上游尝试共享同一个 request_id，
-- attempt_no 为尝试序号（从 1 开始），用于在用量明细里还原调用链。
ALTER TABLE request_logs ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN attempt_no INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_request_logs_request ON request_logs(request_id);
