/**
 * 渠道自定义请求头：允许为任意上游渠道配置请求头，同名覆盖内置头。
 * 值支持占位符 {{名称}}：直接取「客户端同名的请求头」（大小写不敏感），
 * 例如 {{x-session-id}}、{{x-conversation-id}}，可适配任意客户端私有头。
 * 支持多候选回退 {{a | b | c}}：从左到右取第一个「非空命中」的客户端请求头，
 * 例如 {{x-session-id | x-conversation-id | session_id}}，用于适配不同客户端
 * 对同一语义（如会话ID）使用不同请求头名的情况；全部落空则置空。
 * 另保留三个与请求头无关的生成器（适合放在候选末尾作兜底）：
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

/** 解析单个候选：先取客户端同名请求头（非空），再尝试内置生成器；未命中返回 null */
function resolveCandidate(key, request) {
  if (request && request.headers) {
    try {
      const v = request.headers.get(key);
      if (v != null && v !== '') return v;
    } catch {}
  }
  switch (key.toLowerCase()) {
    case 'uuid': return crypto.randomUUID();
    case 'timestamp': return String(Date.now());
    case 'random': return randomHex(16);
    default: return null;
  }
}

/**
 * 解析请求头值中的占位符：
 *   {{名称}}        取客户端同名请求头（大小写不敏感）
 *   {{a | b | c}}   从左到右取第一个非空命中，全部落空则用 fallback
 * 生成器 {{uuid}}/{{timestamp}}/{{random}} 恒为非空，宜放在候选末尾作兜底。
 *
 * fallback 供「无真实客户端请求」的场景使用（如后台诊断/拉模型）：此时占位符
 * 解析不到来源，若不兜底就会发出空值头，导致上游拒绝。真实转发不传该参数（默认空）。
 */
export function resolveHeaderValue(value, request, fallback = '') {
  return String(value == null ? '' : value).replace(/\{\{([^{}]+)\}\}/g, (m, rawKey) => {
    if (!rawKey.trim()) return m;
    for (const cand of rawKey.split('|')) {
      const key = cand.trim();
      if (!key) continue;
      const v = resolveCandidate(key, request);
      if (v != null) return v;
    }
    return fallback;
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

/** 把渠道自定义请求头写入 Headers（同名覆盖内置头）。
 *  request 可为空（如后台诊断/拉模型），此时占位符解析不到来源，用 fallback 兜底。 */
export function applyChannelHeaders(headers, channel, request, fallback = '') {
  const list = normalizeHeaders(channel && channel.headers);
  for (const h of list) {
    headers.set(h.name, resolveHeaderValue(h.value, request, fallback));
  }
  return headers;
}

/**
 * 诊断/拉模型等「无真实客户端请求」场景下，占位符解析不到来源时的固定兜底值。
 * 目的仅是让请求头非空、能通过上游的基本校验，并不代表真实会话。
 */
export const DIAG_PLACEHOLDER_FALLBACK = 'diag';

// 透传时排除的头：连接类、由网关接管、以及客户端对网关的认证凭据
const PASSTHROUGH_BLOCKED = new Set([
  'connection', 'keep-alive', 'proxy-connection', 'transfer-encoding',
  'te', 'trailer', 'upgrade',
  'host', 'content-length', 'content-encoding', 'accept-encoding',
  'authorization', 'x-api-key',
  'cookie',
]);

/** 透传客户端请求头（排除连接类/网关接管/认证凭据等头），用于「在原有基础上」叠加 */
export function applyPassthroughHeaders(headers, request) {
  if (!request || !request.headers) return headers;
  try {
    for (const [name, value] of request.headers) {
      const lower = name.toLowerCase();
      if (PASSTHROUGH_BLOCKED.has(lower)) continue;
      if (/[\r\n]/.test(value)) continue;
      headers.set(name, value);
    }
  } catch {}
  return headers;
}
