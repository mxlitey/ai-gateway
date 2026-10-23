-- MCP 计费口径：让网关的次数统计与上游控制台的计费次数对齐。
-- success：每次成功的 tools/call 计一次（默认）。
-- marker ：仅当上游响应包含指定文本时才计费。例如百度 AI 搜索在模型判定
--          无需联网时直接作答、不产生检索，上游也不计费（响应里没有 details 段）。
ALTER TABLE mcp_servers ADD COLUMN billable_mode TEXT NOT NULL DEFAULT 'success';
ALTER TABLE mcp_servers ADD COLUMN billable_marker TEXT NOT NULL DEFAULT '';

-- 调用日志补上计费标记；历史数据里失败的调用一律不计费。
ALTER TABLE mcp_call_logs ADD COLUMN billable INTEGER NOT NULL DEFAULT 1;
UPDATE mcp_call_logs SET billable = success;
