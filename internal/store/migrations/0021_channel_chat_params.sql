-- 渠道级对话请求体参数改写：改名、补齐、剥离
ALTER TABLE channels ADD COLUMN param_map TEXT NOT NULL DEFAULT '{}';
ALTER TABLE channels ADD COLUMN static_params TEXT NOT NULL DEFAULT '{}';
ALTER TABLE channels ADD COLUMN drop_params TEXT NOT NULL DEFAULT '[]';
