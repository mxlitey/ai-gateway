-- 客户端密钥的权限范围：all=全部可用，selected=仅勾选项，none=完全不授权。
-- 默认 all 保持旧版语义（空列表即全部），老密钥升级后行为不变。
ALTER TABLE api_keys ADD COLUMN channel_scope TEXT NOT NULL DEFAULT 'all';
ALTER TABLE api_keys ADD COLUMN mcp_scope TEXT NOT NULL DEFAULT 'all';
