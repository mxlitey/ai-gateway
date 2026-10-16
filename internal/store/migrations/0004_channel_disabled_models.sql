-- 按模型维度停用：记录本渠道上被单独停用的公开模型名（JSON 字符串数组）
ALTER TABLE channels ADD COLUMN disabled_models TEXT NOT NULL DEFAULT '[]';
