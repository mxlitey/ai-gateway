/**
 * 阿里云 SLS（日志服务）投递层，仅用于「请求成功」流水日志。
 *
 * 与错误日志的分工：错误日志仍写 EdgeOne Blob（kv.js → errors:*），供管理面板即时查看；
 * 成功流水量大、以统计与对账为主，投递到 SLS。
 *
 * 未配置时 createAccessLogger 返回 null，调用方据此跳过成功日志（不引入依赖、零开销）。
 * 必填环境变量（缺任一即视为未启用）：
 *   SLS_ENDPOINT          服务入口，必须是公网地址，如 cn-hangzhou.log.aliyuncs.com
 *   SLS_PROJECT           Project 名称
 *   SLS_LOGSTORE          Logstore 名称
 *   SLS_ACCESS_KEY_ID     RAM AccessKey ID
 *   SLS_ACCESS_KEY_SECRET RAM AccessKey Secret
 * 可选：
 *   SLS_TOPIC             日志主题，默认 gateway-access
 *   SLS_SOURCE            日志来源，默认 edgeone-ai-gateway
 */

const DEFAULT_TOPIC = 'gateway-access';
const DEFAULT_SOURCE = 'edgeone-ai-gateway';

// 投递超时上限：投递失败只丢一条日志，绝不长时间占用请求生命周期
const SEND_TIMEOUT_MS = 2000;

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

let clientPromise = null;

/** 懒加载 SDK：仅在真正启用 SLS 时才引入依赖；构造失败时清空缓存以便下次重试 */
async function getClient(cfg) {
  if (!clientPromise) {
    clientPromise = import('@alicloud/log')
      .then(m => new (m.default || m)({
        accessKeyId: cfg.accessKeyId,
        accessKeySecret: cfg.accessKeySecret,
        endpoint: cfg.endpoint,
      }))
      .catch(err => {
        clientPromise = null;
        throw err;
      });
  }
  return clientPromise;
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

function withTimeout(promise, ms) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new Error(`timeout after ${ms}ms`)), ms)),
  ]);
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
        const client = await getClient(cfg);
        await withTimeout(
          client.postLogStoreLogs(cfg.project, cfg.logstore, {
            logs: [{
              content: toStrFields(fields),
              timestamp: Math.floor(Date.now() / 1000),
            }],
            topic: cfg.topic,
            source: cfg.source,
          }),
          SEND_TIMEOUT_MS
        );
      } catch (err) {
        console.error('[sls] 成功日志投递失败:', (err && err.message) || err);
      }
    },
  };
}