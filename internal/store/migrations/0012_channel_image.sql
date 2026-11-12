-- 生图能力：模型标记 + 渠道级生图配置（JSON）
ALTER TABLE channels ADD COLUMN image_models TEXT NOT NULL DEFAULT '[]';
ALTER TABLE channels ADD COLUMN image_generation TEXT NOT NULL DEFAULT '{}';
