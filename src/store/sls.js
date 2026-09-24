/**
 * 阿里云 SLS（日志服务）投递层，仅用于「请求成功」流水日志。
 *
 * 与错误日志的分工：错误日志仍写 EdgeOne Blob（kv.js → errors:*），供管理面板即时查看；
 * 成功流水量大、以统计与对账为主，投递到 SLS。
 *
 * 未配置时 createAccessLogger 返回 null，调用方据此跳过成功日志（零开销）。
 * 必填环境变量（缺任一即视为未启用）：
 *   SLS_ENDPOINT          服务入口，必须是公网地址，如 cn-hangzhou.log.aliyuncs.com
 *   SLS_PROJECT           Project 名称
 *   SLS_LOGSTORE          Logstore 名称
 *   SLS_ACCESS_KEY_ID     RAM AccessKey ID
 *   SLS_ACCESS_KEY_SECRET RAM AccessKey Secret
 * 可选：
 *   SLS_TOPIC             日志主题，默认 gateway-access
 *   SLS_SOURCE            日志来源，默认 edgeone-ai-gateway
 *
 * 为什么不用官方 @alicloud/log SDK：它在模块加载时用 fs 读取 lib/sls.proto
 * （protobuf.loadSync(__dirname + '/sls.proto')），而 EdgeOne 打包后 __dirname 指向
 * /var/user、.proto 未随包发布，运行时报 ENOENT。此处改为直接实现 PutLogs
 * （protobuf 编码 + HMAC-SHA1 签名），无 fs 依赖、无第三方依赖。
 * 请求格式与官方 SDK 保持一致（见 lib/client.js postLogStoreLogs / _sign）。
 */

import { createHash, createHmac } from 'node:crypto';

const DEFAULT_TOPIC = 'gateway-access';
const DEFAULT_SOURCE = 'edgeone-ai-gateway';

// 投递超时上限：投递失败只丢一条日志，绝不长时间占用请求生命周期
const SEND_TIMEOUT_MS = 2000;

const API_VERSION = '0.6.0';
const SIGNATURE_METHOD = 'hmac-sha1';
const CONTENT_TYPE = 'application/x-protobuf';

/** 读取并校验 SLS 配置；缺任一必填项返回 null（视为未启用） */
function readConfig(env) {
  const e = env || {};
  const endpoint = String(e.SLS_ENDPOINT || '').trim();
  const project = String(e.SLS_PROJECT || '').trim();
  const logstore = String(e.SLS_LOGSTORE || '').trim();
  const accessKeyId = String(e.SLS_ACCESS_KEY_ID || '').trim();
  const accessKeySecret = String(e.SLS_ACCESS_KEY_SECRET || '').trim();
  if (!endpoint || !project || !logstore || !accessKeyId || !accessKeySecret) return null;
  return {
    endpoint,
    project,
    logstore,
    accessKeyId,
    accessKeySecret,
    topic: String(e.SLS_TOPIC || '').trim() || DEFAULT_TOPIC,
    source: String(e.SLS_SOURCE || '').trim() || DEFAULT_SOURCE,
  };
}

// ── protobuf 编码（只实现 SLS LogGroup 所需的最小子集）─────────────
// 对应 lib/sls.proto：
//   Log      { Time = 1(uint32); Contents = 2(repeated Content) }
//   Content  { Key = 1(string);  Value = 2(string) }
//   LogTag   { Key = 1(string);  Value = 2(string) }
//   LogGroup { Logs = 1(repeated Log); Topic = 3; Source = 4; LogTags = 6 }

/** varint 编码（wire type 0），用于长度与 uint32 */
function varint(n) {
  const out = [];
  let v = n >>> 0;
  while (v > 0x7f) {
    out.push((v & 0x7f) | 0x80);
    v >>>= 7;
  }
  out.push(v);
  return out;
}

const encoder = new TextEncoder();

/** length-delimited 字段（wire type 2）：tag + 长度 + 内容 */
function lenDelim(field, bytes) {
  return [...varint((field << 3) | 2), ...varint(bytes.length), ...bytes];
}

/** varint 字段的 tag（wire type 0）+ 值 */
function varintField(field, value) {
  return [...varint(field << 3), ...varint(value)];
}

/** Log.Content */
function encodeContent(key, value) {
  return [...lenDelim(1, encoder.encode(String(key))), ...lenDelim(2, encoder.encode(String(value)))];
}

/** LogTag（结构与 Content 相同） */
const encodeLogTag = encodeContent;

/** Log { Time = 1, Contents = 2 } */
function encodeLog(timestamp, content) {
  const out = [...varintField(1, timestamp)];
  for (const [k, v] of Object.entries(content)) {
    out.push(...lenDelim(2, encodeContent(k, v)));
  }
  return out;
}

/** LogGroup { Logs = 1, Topic = 3, Source = 4, LogTags = 6 } */
function encodeLogGroup({ logs, topic, source, tags }) {
  const out = [];
  for (const log of logs) out.push(...lenDelim(1, encodeLog(log.timestamp, log.content)));
  if (topic) out.push(...lenDelim(3, encoder.encode(topic)));
  if (source) out.push(...lenDelim(4, encoder.encode(source)));
  for (const [k, v] of Object.entries(tags || {})) out.push(...lenDelim(6, encodeLogTag(k, v)));
  return Uint8Array.from(out);
}

// ── 请求与签名 ────────────────────────────────────────────────────

/**
 * 签名串（SLS V1 签名）：
 *   verb \n content-md5 \n content-type \n date \n
 *   canonicalizedHeaders(x-log-* / x-acs-*，按 key 升序，每行 "key:value\n")
 *   canonicalizedResource(请求路径，不含域名)
 */
function signHeaders({ method, path, contentMD5, date, headers, accessKeyId, accessKeySecret }) {
  const canonicalizedHeaders = Object.keys(headers)
    .filter(k => k.startsWith('x-log-') || k.startsWith('x-acs-'))
    .sort()
    .map(k => `${k}:${String(headers[k]).trim()}\n`)
    .join('');
  const signString = `${method}\n${contentMD5}\n${CONTENT_TYPE}\n${date}\n${canonicalizedHeaders}${path}`;
  const signature = createHmac('sha1', accessKeySecret).update(signString).digest('base64');
  return `LOG ${accessKeyId}:${signature}`;
}

/** SLS 的 content value 必须是字符串，统一转换并剔除空值 */
function toStrFields(fields) {
  const out = {};
  for (const [k, v] of Object.entries(fields || {})) {
    if (v == null || v === '') continue;
    out[k] = String(v);
  }
  return out;
}

/** 写入一个 LogGroup（PutLogs 的 LoadBalance 模式：/shards/lb） */
async function putLogGroup(cfg, logGroup) {
  const body = encodeLogGroup(logGroup);
  const path = `/logstores/${cfg.logstore}/shards/lb`;
  const date = new Date().toGMTString();

  // 参与签名的 x-log-* 头（顺序不影响，签名函数内会排序）
  const logHeaders = {
    'x-log-apiversion': API_VERSION,
    'x-log-bodyrawsize': String(body.length),
    'x-log-signaturemethod': SIGNATURE_METHOD,
  };

  const contentMD5 = createHash('md5').update(body).digest('hex').toUpperCase();

  const res = await fetch(`https://${cfg.project}.${cfg.endpoint}${path}`, {
    method: 'POST',
    headers: {
      ...logHeaders,
      'Content-Type': CONTENT_TYPE,
      'Content-MD5': contentMD5,
      Date: date,
      Authorization: signHeaders({
        method: 'POST',
        path,
        contentMD5,
        date,
        headers: logHeaders,
        accessKeyId: cfg.accessKeyId,
        accessKeySecret: cfg.accessKeySecret,
      }),
    },
    body,
    signal: AbortSignal.timeout(SEND_TIMEOUT_MS),
  });

  if (!res.ok) {
    const detail = await res.text().catch(() => '');
    throw new Error(`HTTP ${res.status}${detail ? ' ' + detail.slice(0, 300) : ''}`);
  }
}

/**
 * 创建成功日志投递器。
 * @param {Record<string,string>} env
 * @returns {{ log: (fields: object) => Promise<void> } | null} 未配置时返回 null
 */
export function createAccessLogger(env) {
  const cfg = readConfig(env);
  if (!cfg) return null;

  return {
    /** 投递一条成功日志。永不抛错：失败仅打印告警，不干扰请求主流程。 */
    async log(fields) {
      try {
        await putLogGroup(cfg, {
          logs: [{
            content: toStrFields(fields),
            timestamp: Math.floor(Date.now() / 1000),
          }],
          topic: cfg.topic,
          source: cfg.source,
        });
      } catch (err) {
        console.error('[sls] 成功日志投递失败:', (err && err.message) || err);
      }
    },
  };
}