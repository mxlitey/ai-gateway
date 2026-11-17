-- 首字耗时：流式请求从发起调用到收到第一个内容增量之间的毫秒数。
-- 非流式请求（含生图）没有「首字」概念，保持 0。
ALTER TABLE request_logs ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0;
