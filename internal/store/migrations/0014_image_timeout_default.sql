-- 生图整体超时默认值：5 分钟 → 6 分钟。
--
-- 旧版本在保存渠道时会把默认值 300000 写进 image_generation；
-- ModelScope Qwen 实测最长约 244 秒，5 分钟余量不足，这里统一抬到 360000。
-- 只动仍然是旧默认值的渠道，手工改过的值保持不变。
UPDATE channels
SET image_generation = json_set(image_generation, '$.timeout_ms', 360000)
WHERE json_valid(image_generation)
  AND json_extract(image_generation, '$.timeout_ms') = 300000;
