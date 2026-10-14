-- 用量与费用：每次 /v1 调用的最终结果（一次故障转移链只记一条）
CREATE TABLE IF NOT EXISTS request_logs (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at          INTEGER NOT NULL,
  api_key_id          TEXT NOT NULL DEFAULT '',
  api_key_name        TEXT NOT NULL DEFAULT '',
  channel_id          TEXT NOT NULL DEFAULT '',
  channel_name        TEXT NOT NULL DEFAULT '',
  endpoint            TEXT NOT NULL DEFAULT '',   -- chat | messages | responses | embeddings | other
  stream              INTEGER NOT NULL DEFAULT 0,
  model               TEXT NOT NULL DEFAULT '',   -- 客户端请求的公开模型名
  upstream_model      TEXT NOT NULL DEFAULT '',   -- 实际发出的上游模型名
  upstream_key_hint   TEXT NOT NULL DEFAULT '',
  status              INTEGER NOT NULL DEFAULT 0,
  success             INTEGER NOT NULL DEFAULT 0,
  attempts            INTEGER NOT NULL DEFAULT 0, -- 依次尝试过的目标数
  duration_ms         INTEGER NOT NULL DEFAULT 0,
  prompt_tokens       INTEGER NOT NULL DEFAULT 0,
  completion_tokens   INTEGER NOT NULL DEFAULT 0,
  total_tokens        INTEGER NOT NULL DEFAULT 0,
  cached_tokens       INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
  cost_prompt_usd     REAL NOT NULL DEFAULT 0,
  cost_completion_usd REAL NOT NULL DEFAULT 0,
  cost_usd            REAL NOT NULL DEFAULT 0,
  price_model         TEXT NOT NULL DEFAULT '',   -- 命中的价格条目
  price_source        TEXT NOT NULL DEFAULT 'none', -- openrouter | manual | none
  error               TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_request_logs_created ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_channel ON request_logs(channel_id, created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_model ON request_logs(model, created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_apikey ON request_logs(api_key_id, created_at);

-- 模型价格快照：单位统一为「美元 / 百万 token」
CREATE TABLE IF NOT EXISTS model_prices (
  model            TEXT PRIMARY KEY,          -- 归一化后的模型名（匹配键）
  display_name     TEXT NOT NULL DEFAULT '',
  prompt_price     REAL NOT NULL DEFAULT 0,   -- USD / 1M 输入 token
  completion_price REAL NOT NULL DEFAULT 0,   -- USD / 1M 输出 token
  cache_read_price REAL NOT NULL DEFAULT 0,   -- USD / 1M 缓存命中输入 token
  context_length   INTEGER NOT NULL DEFAULT 0,
  source           TEXT NOT NULL DEFAULT 'openrouter', -- openrouter | manual
  updated_at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_prices_source ON model_prices(source);