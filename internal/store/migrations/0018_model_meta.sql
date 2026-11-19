-- 生图模型的元数据声明。
--
-- 上游的 /v1/models 只有 id / object / created / owned_by 四个字段，
-- 「支持参考图、最多几张、长边上限、能否写中文」这些信息既拿不到也无法可靠探测
-- （探测一次就是一次真实计费出图），因此完全由人工在面板里声明，按公开模型名存一份。
--
-- 主键是公开模型名（与客户端请求里的 model 字段严格一致，不做归一化）：
-- 同一个模型挂在多个渠道时只声明一次，不随渠道增减而变化。
-- 渠道全部删除后记录保留，管理接口会把这类「无渠道」的条目单独列出供清理。
CREATE TABLE IF NOT EXISTS model_meta (
    model      TEXT PRIMARY KEY,
    spec       TEXT NOT NULL DEFAULT '{}',  -- JSON：见 model.ModelMeta
    updated_at INTEGER NOT NULL
);
