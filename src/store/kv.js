const CACHE_TTL = 5 * 60 * 1000; // 5 minutes

const ERROR_RETENTION_DAYS = 7;                   // 错误日志保留天数
const CLEANUP_INTERVAL = 6 * 60 * 60 * 1000;      // 惰性清理最小间隔（6 小时）
const CLEANUP_META_KEY = 'meta:errors_cleanup';   // 上次清理时间戳

class KVStore {
  /**
   * @param {object} kv   Blob/File 适配层（config、errors）
   */
  constructor(kv) {
    this.kv = kv;
    this.cache = new Map();
  }

  async get(key) {
    const cached = this.cache.get(key);
    if (cached && Date.now() - cached.time < CACHE_TTL) {
      return cached.value;
    }
    const value = await this.kv.get(key, 'json');
    this.cache.set(key, { value, time: Date.now() });
    return value;
  }

  async set(key, value) {
    await this.kv.put(key, JSON.stringify(value));
    this.cache.set(key, { value, time: Date.now() });
  }

  invalidate(key) {
    this.cache.delete(key);
  }

  async getChannels() {
    return (await this.get('config:channels')) || [];
  }

  async saveChannels(channels) {
    await this.set('config:channels', channels);
  }

  async getApiKeys() {
    return (await this.get('config:apikeys')) || [];
  }

  async saveApiKeys(keys) {
    await this.set('config:apikeys', keys);
  }

  // ── Error logs (per-channel, per-day, last 100 entries) ──

  /** 北京时区日期字符串（YYYY-MM-DD），offsetDays 为天数偏移（负数表示过去） */
  _dateKeyString(offsetDays = 0) {
    const beijing = new Date(Date.now() + 8 * 60 * 60 * 1000 + offsetDays * 86400000);
    return beijing.toISOString().slice(0, 10);
  }

  _todayKey() {
    return this._dateKeyString(0);
  }

  async appendError(channelId, entry) {
    const date = this._todayKey();
    const key = `errors:${channelId}:${date}`;
    let logs;
    try { logs = (await this.kv.get(key, 'json')) || []; } catch { logs = []; }
    logs.push({ ...entry, time: new Date().toISOString() });
    if (logs.length > 100) logs = logs.slice(-100);
    await this.kv.put(key, JSON.stringify(logs));
    // 惰性清理：超过保留期的旧日志顺带删除（有节流，见 maybeCleanupOldErrors）
    await this.maybeCleanupOldErrors();
  }

  async getErrors(channelId, date) {
    const key = `errors:${channelId}:${date || this._todayKey()}`;
    try { return (await this.kv.get(key, 'json')) || []; } catch { return []; }
  }

  /** 删除超过保留期（默认 7 天）的错误日志，返回删除的 key 数量 */
  async cleanupOldErrors(retentionDays = ERROR_RETENTION_DAYS) {
    const cutoff = this._dateKeyString(-retentionDays);
    let keys = [];
    try { keys = await this.kv.list('errors:'); } catch { return 0; }
    const stale = keys.filter(k => {
      const d = k.slice(k.lastIndexOf(':') + 1);
      return /^\d{4}-\d{2}-\d{2}$/.test(d) && d < cutoff;
    });
    await Promise.all(stale.map(k => this.kv.delete(k)));
    return stale.length;
  }

  /** 带节流的惰性清理：最多每 6 小时执行一次 */
  async maybeCleanupOldErrors() {
    try {
      const last = Number(await this.kv.get(CLEANUP_META_KEY)) || 0;
      const now = Date.now();
      if (now - last < CLEANUP_INTERVAL) return;
      await this.kv.put(CLEANUP_META_KEY, String(now));
      await this.cleanupOldErrors();
    } catch { /* 清理失败不影响错误记录 */ }
  }

}

export function createStore(kv) {
  return new KVStore(kv);
}
