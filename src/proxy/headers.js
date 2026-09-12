/**
 * 渠道自定义请求头：允许为任意上游渠道配置请求头，同名覆盖内置头。
 * 值支持动态占位符：
 *   {{session}}   会话 ID（优先透传客户端会话头，缺失时生成，保证同一对话稳定）
 *   {{uuid}}      每次请求随机 UUID
 *   {{timestamp}} 当前毫秒时间戳
 *   {{random}}    每次请求随机十六进制串
 */

const HEADER_NAME_RE = /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/;

/** 生成十六进制随机串 */
function randomHex(bytes = 16) {
  const arr = new Uint8Array(bytes);
  crypto.getRandomValues(arr);
  return Array.from(arr).map(b => b.toString(16).padStart(2, '0')).join('');
}

/** 会话 ID：优先透传客户端已有会话头，缺失时生成 */
export function resolveSessionId(request) {
  const names = ['x-opencode-session', 'x-session-id', 'session_id', 'x-conversation-id', 'conversation_id'];
  try {
    if (request && request.headers) {
      for (const n of names) {
        const v = request.headers.get(n);
        if (v) return v;
      }
    }
  } catch {}
  return 'gw-' + randomHex(16);
}

/** 解析请求头值中的占位符；未知占位符原样保留 */
export function resolveHeaderValue(value, request) {
  return String(value == null ? '' : value).replace(/\{\{(\w+)\}\}/g, (m, key) => {
    switch (key) {
      case 'session': return resolveSessionId(request);
      case 'uuid': return crypto.randomUUID();
      case 'timestamp': return String(Date.now());
      case 'random': return randomHex(16);
      default: return m;
    }
  });
}

/** 规范化渠道自定义请求头为 [{ name, value }]：兼容数组与对象两种输入，过滤非法名/值并去重 */
export function normalizeHeaders(raw) {
  let entries = [];
  if (Array.isArray(raw)) {
    entries = raw.map(h => ({ name: h && h.name, value: h && h.value }));
  } else if (raw && typeof raw === 'object') {
    entries = Object.entries(raw).map(([name, value]) => ({ name, value }));
  }
  const out = [];
  const seen = new Set();
  for (const e of entries) {
    const name = String((e && e.name) || '').trim();
    const value = String((e && e.value) == null ? '' : e.value);
    if (!name || !HEADER_NAME_RE.test(name)) continue;
    if (/[\r\n]/.test(value)) continue;
    const lower = name.toLowerCase();
    if (seen.has(lower)) continue;
    seen.add(lower);
    out.push({ name, value });
  }
  return out;
}

/** 把渠道自定义请求头写入 Headers（同名覆盖内置头）；request 可为空（如后台诊断） */
export function applyChannelHeaders(headers, channel, request) {
  const list = normalizeHeaders(channel && channel.headers);
  for (const h of list) {
    headers.set(h.name, resolveHeaderValue(h.value, request));
  }
  return headers;
}
