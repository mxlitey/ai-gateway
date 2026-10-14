-- 初始表结构
CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS channels (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  base_url       TEXT NOT NULL,
  path           TEXT NOT NULL DEFAULT '',
  keys           TEXT NOT NULL DEFAULT '[]',
  models         TEXT NOT NULL DEFAULT '[]',
  model_map      TEXT NOT NULL DEFAULT '{}',
  model_priority TEXT NOT NULL DEFAULT '{}',
  headers        TEXT NOT NULL DEFAULT '[]',
  enabled        INTEGER NOT NULL DEFAULT 1,
  source         TEXT NOT NULL DEFAULT 'manual',
  sort_order     INTEGER NOT NULL DEFAULT 0,
  created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_channels_source ON channels(source);

CREATE TABLE IF NOT EXISTS api_keys (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  key         TEXT NOT NULL UNIQUE,
  channel_ids TEXT NOT NULL DEFAULT '[]',
  enabled     INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS error_logs (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id     TEXT NOT NULL,
  channel_name   TEXT NOT NULL,
  base_url       TEXT NOT NULL,
  model          TEXT NOT NULL,
  upstream_model TEXT NOT NULL DEFAULT '',
  status         INTEGER NOT NULL,
  key_hint       TEXT NOT NULL DEFAULT '',
  message        TEXT NOT NULL,
  created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_error_logs_created ON error_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_error_logs_channel ON error_logs(channel_id, created_at);

CREATE TABLE IF NOT EXISTS plugins (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  version      TEXT NOT NULL DEFAULT '',
  dir          TEXT NOT NULL,
  port         INTEGER NOT NULL DEFAULT 0,
  enabled      INTEGER NOT NULL DEFAULT 1,
  autostart    INTEGER NOT NULL DEFAULT 1,
  last_status  TEXT NOT NULL DEFAULT 'discovered',
  last_error   TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  installed_at INTEGER NOT NULL
);