/** 生成 24 位十六进制随机串（用于响应/消息 ID）。 */
export function rid() {
  const b = new Uint8Array(12);
  crypto.getRandomValues(b);
  return Array.from(b, v => v.toString(16).padStart(2, '0')).join('');
}

/** 渠道对话接口路径：未配置协议接口时默认 /chat/completions。 */
export function resolveChatPath(channel) {
  const trimmed = ((channel && channel.path) || '').trim();
  return trimmed ? (trimmed.startsWith('/') ? trimmed : '/' + trimmed) : '/chat/completions';
}

/**
 * 渠道「模型列表」接口路径：与转发共用同一套路径推导。
 * 对话端点约定为 `<前缀>/chat/completions`，模型列表是其同级 `<前缀>/models`：
 * 先去掉结尾的 chat/completions，否则去掉最后一段（兼容 /v1/messages 这类端点）。
 * 例：/chat/completions → /models；/v1/chat/completions → /v1/models；/v1/messages → /v1/models。
 * 未配置路径时默认 /models（base_url 通常已含 /v1 之类的版本前缀）。
 */
export function resolveModelsPath(channel) {
  const trimmed = ((channel && channel.path) || '').trim();
  if (!trimmed) return '/models';
  let p = (trimmed.startsWith('/') ? trimmed : '/' + trimmed).replace(/\/+$/, '');
  const m = p.match(/^(.*?)\/?chat\/completions$/);
  if (m) {
    p = m[1];
  } else {
    const idx = p.lastIndexOf('/');
    p = idx > 0 ? p.slice(0, idx) : '';
  }
  return p + '/models';
}
