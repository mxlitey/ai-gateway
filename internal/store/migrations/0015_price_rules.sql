-- 价格表改为完全手工维护，只按公开模型计费。
--
-- 旧结构是「单档基础价 + OpenRouter 快照」，无法表达输入分档与峰谷折扣，
-- 按约定直接清空重建；历史用量的费用已按当时的价格快照写在 request_logs 里，不受影响。
DROP TABLE IF EXISTS model_prices;

CREATE TABLE model_prices (
    model            TEXT PRIMARY KEY,                    -- 公开模型名（归一化后）
    unit             TEXT NOT NULL DEFAULT 'per_token',   -- per_token | per_request
    tiers            TEXT NOT NULL DEFAULT '[]',          -- JSON: [{up_to,prompt_price,completion_price}]
    cache_read_price REAL NOT NULL DEFAULT 0,             -- 每百万缓存命中输入 token 的单价（不分档）
    request_price    REAL NOT NULL DEFAULT 0,             -- 每次请求单价，unit=per_request 时生效（生图）
    peak_windows     TEXT NOT NULL DEFAULT '[]',          -- JSON: [{start,end,factor}]，空表示无峰谷
    note             TEXT NOT NULL DEFAULT '',
    updated_at       INTEGER NOT NULL
);
