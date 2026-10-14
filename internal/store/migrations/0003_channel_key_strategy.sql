-- 渠道多密钥的选用策略：rotate=轮询（随机起点轮换） | ordered=按序退化
ALTER TABLE channels ADD COLUMN key_strategy TEXT NOT NULL DEFAULT 'rotate';