-- 密钥的对象存储权限：all=可签发预签名上传/下载地址，none=禁止调用对象存储接口。
-- 默认 all，保证升级后已有密钥的行为不变。
ALTER TABLE api_keys ADD COLUMN storage_scope TEXT NOT NULL DEFAULT 'all';
