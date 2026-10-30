-- 客户端密钥的 MCP 权限：可访问的 MCP 服务器 id 列表（空数组表示全部服务器）
ALTER TABLE api_keys ADD COLUMN mcp_server_ids TEXT NOT NULL DEFAULT '[]';
