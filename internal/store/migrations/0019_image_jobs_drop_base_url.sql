-- 生图作业不再记录对外地址：v1.x 用它拼接图床链接，图床下线后该字段无人读取。
ALTER TABLE image_jobs DROP COLUMN base_url;
