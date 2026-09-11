/**
 * MySQL 原子计数适配层。
 *
 * 职责：承载 usage:* 与 apikey-usage:*（每请求一次写入的计数/Token 统计）。
 *   - 写入用原子 SQL（INSERT ... ON DUPLICATE KEY UPDATE cnt = cnt + 1），
 *     在存储层消灭 read-modify-write 竞态（跨实例、并发下计数不丢）。
 *   - 读取按前缀聚合回与 KV 版相同的数据结构，管理面板/调用方无感知。
 *
 * 表结构（扁平计数，避免嵌套 JSON 的原子更新难题）：
 *   usage_counter(pk, cnt, prompt_tokens, completion_tokens, cached_tokens)
 *   - usage 行        pk = usage:{channelId}:{date}:{keyId后8位}:{model|*}
 *   - apikey-usage 行 pk = apikey-usage:{date}:{keyId}:{model|*}
 *   - * 后缀行 = 该 key 当日总计数；model 后缀行 = 按模型分项
 *   - prompt_tokens = 未命中缓存的输入；cached_tokens = 命中缓存的输入；completion_tokens = 输出
 *
 * 连接：惰性创建连接池（首次调用时建池 + 建表），实例内复用。
 */
import mysql from 'mysql2/promise';

const USAGE_PREFIX = 'usage:';
const APIKEY_USAGE_PREFIX = 'apikey-usage:';

export class MysqlKV {
  constructor({ url, maxConnections = 2 }) {
    if (!url) throw new Error('MysqlKV requires a connection url');
    this.url = url;
    this.maxConnections = maxConnections;
    this.pool = null;
    this.tableReady = null;
  }

  _getPool() {
    if (!this.pool) {
      this.pool = mysql.createPool({
        uri: this.url,
        connectionLimit: this.maxConnections,
        waitForConnections: true,
        queueLimit: 0,
        charset: 'utf8mb4',
        supportBigNumbers: true,
      });
    }
    return this.pool;
  }

  /** 惰性建表/补列（幂等），首次任何操作前调用一次。 */
  _ensureTable() {
    if (!this.tableReady) {
      this.tableReady = this._getPool().query(`
        CREATE TABLE IF NOT EXISTS usage_counter (
          pk VARCHAR(255) NOT NULL PRIMARY KEY,
          cnt INT UNSIGNED NOT NULL DEFAULT 0,
          prompt_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
          completion_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0,
          cached_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
      `)
        // 兼容旧表：若表已存在（无 cached_tokens 列），幂等补列
        .then(() => this._ensureCachedTokensColumn())
        .then(() => true)
        .catch(err => {
          console.error('[mysql-kv] ensureTable failed:', err);
          throw err;
        });
    }
    return this.tableReady;
  }

  /** 检查并补齐 cached_tokens 列（对已按旧 schema 建表的数据库生效）。 */
  async _ensureCachedTokensColumn() {
    const [cols] = await this._getPool().query(
      `SELECT COLUMN_NAME FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'usage_counter' AND COLUMN_NAME = 'cached_tokens'`,
    );
    if (cols.length === 0) {
      await this._getPool().query(
        `ALTER TABLE usage_counter
         ADD COLUMN cached_tokens BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER completion_tokens`,
      );
      console.log('[mysql-kv] added missing cached_tokens column to usage_counter');
    }
  }

  async _run(sql, params) {
    const [rows] = await this._getPool().execute(sql, params);
    return rows;
  }

  // ── usage:{channelId}:{date} ───────────────────────────────

  /** 原子累加：同一 key 当日请求数 +1（总计数 + 按模型分项）。 */
  async incrementUsage(channelId, date, kid, model) {
    const base = `${USAGE_PREFIX}${channelId}:${date}:${kid}`;
    await this._ensureTable();
    if (!model) {
      await this._run(
        `INSERT INTO usage_counter (pk, cnt) VALUES (?, 1)
         ON DUPLICATE KEY UPDATE cnt = cnt + 1`,
        [base + ':*'],
      );
      return;
    }
    await this._run(
      `INSERT INTO usage_counter (pk, cnt) VALUES (?, 1), (?, 1)
       ON DUPLICATE KEY UPDATE cnt = cnt + 1`,
      [base + ':*', base + ':' + model],
    );
  }

  /**
   * 聚合读取，返回 { kid: { total, models: { m: N } } }。
   * @returns {Promise<Record<string, { total: number, models: Record<string, number> }>>}
   */
  async getUsage(channelId, date) {
    const prefix = `${USAGE_PREFIX}${channelId}:${date}:`;
    await this._ensureTable();
    const rows = await this._run(
      'SELECT pk, cnt FROM usage_counter WHERE pk LIKE ?',
      [prefix + '%'],
    );
    const result = {};
    for (const row of rows) {
      // pk = usage:{cid}:{date}:{kid}:{suffix}
      const parts = row.pk.split(':');
      const kid = parts[3];
      const suffix = parts.slice(4).join(':') || '*';
      if (!result[kid]) result[kid] = { total: 0, models: {} };
      if (suffix === '*') {
        result[kid].total = row.cnt;
      } else {
        result[kid].models[suffix] = row.cnt;
      }
    }
    return result;
  }

  // ── apikey-usage:{date} ────────────────────────────────────

  /**
   * 原子累加：客户端 key 当日请求数 +1，prompt/completion/cached tokens 累加。
   * @param {string} keyId 客户端 API key 的 id
   * @param {string} [model]
   * @param {number} [promptTokens] 未命中缓存的输入 token
   * @param {number} [completionTokens] 输出 token
   * @param {number} [cachedTokens] 命中缓存的输入 token
   */
  async incrementApiKeyUsage(keyId, model, promptTokens = 0, completionTokens = 0, cachedTokens = 0) {
    const date = new Date(new Date().getTime() + 8 * 60 * 60 * 1000).toISOString().slice(0, 10);
    const base = `${APIKEY_USAGE_PREFIX}${date}:${keyId}`;
    await this._ensureTable();
    if (!model) {
      await this._run(
        `INSERT INTO usage_counter (pk, cnt, prompt_tokens, completion_tokens, cached_tokens)
           VALUES (?, 1, ?, ?, ?)
         ON DUPLICATE KEY UPDATE
           cnt = cnt + 1,
           prompt_tokens = prompt_tokens + VALUES(prompt_tokens),
           completion_tokens = completion_tokens + VALUES(completion_tokens),
           cached_tokens = cached_tokens + VALUES(cached_tokens)`,
        [base + ':*', promptTokens, completionTokens, cachedTokens],
      );
      return;
    }
    await this._run(
      `INSERT INTO usage_counter (pk, cnt, prompt_tokens, completion_tokens, cached_tokens)
         VALUES (?, 1, ?, ?, ?), (?, 1, ?, ?, ?)
       ON DUPLICATE KEY UPDATE
         cnt = cnt + 1,
         prompt_tokens = prompt_tokens + VALUES(prompt_tokens),
         completion_tokens = completion_tokens + VALUES(completion_tokens),
         cached_tokens = cached_tokens + VALUES(cached_tokens)`,
      [base + ':*', promptTokens, completionTokens, cachedTokens,
       base + ':' + model, promptTokens, completionTokens, cachedTokens],
    );
  }

  /**
   * 聚合读取，返回
   * { keyId: { requests, prompt_tokens, completion_tokens, cached_tokens,
   *            models: { m: { requests, prompt_tokens, completion_tokens, cached_tokens } } } }
   */
  async getApiKeyUsage(date) {
    const prefix = `${APIKEY_USAGE_PREFIX}${date}:`;
    await this._ensureTable();
    const rows = await this._run(
      'SELECT pk, cnt, prompt_tokens, completion_tokens, cached_tokens FROM usage_counter WHERE pk LIKE ?',
      [prefix + '%'],
    );
    const result = {};
    for (const row of rows) {
      // pk = apikey-usage:{date}:{keyId}:{suffix}
      const parts = row.pk.split(':');
      const keyId = parts[2];
      const suffix = parts.slice(3).join(':') || '*';
      if (!result[keyId]) {
        result[keyId] = { requests: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, models: {} };
      }
      if (suffix === '*') {
        result[keyId].requests = row.cnt;
        result[keyId].prompt_tokens = row.prompt_tokens;
        result[keyId].completion_tokens = row.completion_tokens;
        result[keyId].cached_tokens = row.cached_tokens;
      } else {
        result[keyId].models[suffix] = {
          requests: row.cnt,
          prompt_tokens: row.prompt_tokens,
          completion_tokens: row.completion_tokens,
          cached_tokens: row.cached_tokens,
        };
      }
    }
    return result;
  }
}
