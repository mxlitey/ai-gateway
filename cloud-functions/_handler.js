/**
 * EdgeOne Cloud Functions 共享入口（私有模块：`_` 前缀不生成路由）。
 *
 * 平台约定：
 *   - 目录   cloud-functions/          请求模式 Runtime（Node.js v20+，完整 npm）
 *   - 入口   index.js → 根路径 / ；[[default]].js → 其余所有路径
 *   - 调用   onRequest(context)，context.request 为标准 Request
 *   - 环境变量经 context.env 注入（在 Makers 控制台配置）
 *   - 最大执行时长：默认 30s，edgeone.json 的 cloudFunctions.nodejs.maxDuration 可设为 10-120s
 *   - 请求/响应 body 上限 6MB，代码包上限 128MB
 *
 * 存储：
 *   - EdgeOne Blob（@edgeone/pages-blob），强一致读取 → config / ratelimit / errors
 *   - MySQL（MYSQL_URL，原子计数）→ usage / apikey-usage 请求次数与 Token 统计
 *   - 命名空间固定为 ai-gateway，函数内自动鉴权，首次调用自动创建
 */
import worker from '../src/index.js';
import { BlobKV } from '../src/store/blob-kv.js';
import { MysqlKV } from '../src/store/mysql-kv.js';

let kvPromise = null;
let mysqlPromise = null;

function getKv() {
  if (!kvPromise) {
    kvPromise = Promise.resolve(new BlobKV());
  }
  return kvPromise;
}

function getMysql(env) {
  if (!env.MYSQL_URL) return null;
  if (!mysqlPromise) {
    mysqlPromise = Promise.resolve(new MysqlKV({ url: env.MYSQL_URL }));
  }
  return mysqlPromise;
}

/**
 * 平台入口统一调用此函数。
 * @param {{ request: Request, env?: Record<string, string> }} context
 * @returns {Promise<Response>}
 */
export async function handleRequest(context) {
  const env = context.env || {};
  const kv = await getKv(env);
  const mysql = await getMysql(env);
  try {
    return await worker.fetch(context.request, {
      ADMIN_PASSWORD: env.ADMIN_PASSWORD || '',
      KV: kv,
      MYSQL: mysql,
    });
  } finally {
    // 请求结束立即断开 MySQL 连接并重建单例，避免实例间堆积连接撞到 max_user_connections
    if (mysql) {
      await mysql.close().catch(() => {});
      mysqlPromise = null;
    }
  }
}
