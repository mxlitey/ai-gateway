-- MCP 上游服务器：与渠道同构的管理方式（多密钥、轮询/故障转移、每日调用次数限制）
CREATE TABLE IF NOT EXISTS mcp_servers (
  id               TEXT    PRIMARY KEY,
  name             TEXT    NOT NULL,
  alias            TEXT    NOT NULL DEFAULT '',  -- 工具命名空间前缀，留空时由 name 推导
  url              TEXT    NOT NULL,             -- Streamable HTTP 端点
  headers          TEXT    NOT NULL DEFAULT '[]',
  keys             TEXT    NOT NULL DEFAULT '[]', -- [{key, enabled, label, daily_limit}]
  key_strategy     TEXT    NOT NULL DEFAULT 'rotate',
  daily_limit      INTEGER NOT NULL DEFAULT 0,   -- 整台服务器每日成功调用上限，0 = 不限
  timeout_ms       INTEGER NOT NULL DEFAULT 120000,
  enabled          INTEGER NOT NULL DEFAULT 1,
  source           TEXT    NOT NULL DEFAULT 'manual',
  sort_order       INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_servers_source ON mcp_servers(source);

-- 工具缓存：保存上游返回的原始工具对象，便于原样透传 title / outputSchema / annotations 等字段
CREATE TABLE IF NOT EXISTS mcp_tools (
  server_id    TEXT    NOT NULL,
  name         TEXT    NOT NULL,                -- 上游工具名
  exposed_name TEXT    NOT NULL,                -- 对外暴露名（alias__tool）
  raw          TEXT    NOT NULL DEFAULT '{}',
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (server_id, name)
);
CREATE INDEX IF NOT EXISTS idx_mcp_tools_exposed ON mcp_tools(exposed_name);

-- 工具调用日志：一次调用链只记一条，attempts 表示依次试过的密钥数
CREATE TABLE IF NOT EXISTS mcp_call_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at  INTEGER NOT NULL,
  server_id   TEXT    NOT NULL DEFAULT '',
  server_name TEXT    NOT NULL DEFAULT '',
  server_url  TEXT    NOT NULL DEFAULT '',
  tool        TEXT    NOT NULL DEFAULT '',
  key_hint    TEXT    NOT NULL DEFAULT '',
  success     INTEGER NOT NULL DEFAULT 0,
  http_status INTEGER NOT NULL DEFAULT 0,
  attempts    INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  error       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_created ON mcp_call_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_mcp_call_logs_server ON mcp_call_logs(server_id, created_at);
