const CACHE_TTL = 5 * 60 * 1000; // 5 minutes

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

  _todayKey() {
    const now = new Date();
    const beijing = new Date(now.getTime() + 8 * 60 * 60 * 1000);
    return beijing.toISOString().slice(0, 10);
  }

  async appendError(channelId, entry) {
    const date = this._todayKey();
    const key = `errors:${channelId}:${date}`;
    let logs;
    try { logs = (await this.kv.get(key, 'json')) || []; } catch { logs = []; }
    logs.push({ ...entry, time: new Date().toISOString() });
    if (logs.length > 100) logs = logs.slice(-100);
    await this.kv.put(key, JSON.stringify(logs));
  }

  async getErrors(channelId, date) {
    const key = `errors:${channelId}:${date || this._todayKey()}`;
    try { return (await this.kv.get(key, 'json')) || []; } catch { return []; }
  }

}

export function createStore(kv) {
  return new KVStore(kv);
}
