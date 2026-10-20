-- MCP 每日上限语义调整：0 现在表示「不允许调用」，-1 才表示「不限」。
-- 历史数据里的 0 一律按「不限」理解，这里统一改写成 -1，
-- 否则升级后既有服务器会被当成 0 次配额，所有调用直接被拒。
-- （工具级上限此前不会落库 0，故只需处理密钥与整台上限。）
UPDATE mcp_servers
SET daily_limit = CASE WHEN daily_limit = 0 THEN -1 ELSE daily_limit END,
    keys = replace(keys, '"daily_limit":0', '"daily_limit":-1');
