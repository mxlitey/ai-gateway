-- 工具级每日调用上限：不同工具可分配不同配额。
-- 存为 JSON 对象 {"工具名": 上限}，未列出的工具不受工具维度限制（0 或缺失 = 不限）。
ALTER TABLE mcp_servers ADD COLUMN tool_limits TEXT NOT NULL DEFAULT '{}';
