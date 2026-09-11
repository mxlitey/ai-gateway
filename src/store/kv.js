const CACHE_TTL = 5 * 60 * 1000; // 5 minutes
const DEFAULT_COOLDOWN_MS = 90 * 1000;

class KVStore {
  /**
   * @param {object} kv   Blob/File 适配层（config、ratelimit、errors）
   * @param {object} [mysql]  MySQL 适配层（usage、apikey-usage 原子计数）；缺省时不启用用量统计
   */
  constructor(kv, mysql) {
    this.kv = kv;
    this.mysql = mysql;
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

  // ── Per-key usage tracking (MySQL 原子计数；无 MySQL 时回退 KV 读-改-写) ──
  // 存储格式（MySQL）：usage_counter 表扁平计数，见 mysql-kv.js
  // 兼容格式（KV 回退）：usage:{channelId}:{date} → { "keyId1": { total, models: { m: N } }, ... }

  _todayKey() {
    const now = new Date();
    const beijing = new Date(now.getTime() + 8 * 60 * 60 * 1000);
    return beijing.toISOString().slice(0, 10);
  }

  _keyId(apiKey) {
    return apiKey.slice(-8);
  }

  async getUsage(channelId, date) {
    if (!this.mysql) return {};
    return this.mysql.getUsage(channelId, date || this._todayKey());
  }

  async incrementUsage(channelId, apiKey, model) {
    if (!this.mysql) return;
    await this.mysql.incrementUsage(channelId, this._todayKey(), this._keyId(apiKey), model);
  }

  // ── 客户端 API Key 用量统计（仅 MySQL 可用；未配置时统计禁用）──
  // Storage: apikey-usage:{date} → { "keyId": { requests, prompt_tokens, completion_tokens, cached_tokens, models: { m: { requests, prompt_tokens, completion_tokens, cached_tokens } } } }
  // prompt_tokens = 未命中缓存的输入；cached_tokens = 命中缓存的输入；completion_tokens = 输出

  async getApiKeyUsage(date) {
    if (!this.mysql) return {};
    return this.mysql.getApiKeyUsage(date || this._todayKey());
  }

  async incrementApiKeyUsage(apiKeyId, model, promptTokens = 0, completionTokens = 0, cachedTokens = 0) {
    if (!this.mysql) return;
    await this.mysql.incrementApiKeyUsage(apiKeyId, model, promptTokens, completionTokens, cachedTokens);
  }

  // ── Error logs (per-channel, per-day, last 100 entries) ──

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

  // ── Rate-limit state (per key+model, per day) ──
  // Storage: ratelimit:{channelId}:{date} →
  // {
  //   "keyId": {
  //     "daily_models": ["modelA"],              // exhausted for current day
  //     "cooldowns": { "modelB": 1770000000000 },// temporary cooldown until timestamp(ms)
  //     "header": {
  //       "user_limit": 2000,
  //       "user_remaining": 1500,
  //       "model_limits": { "modelA": { "limit": 100, "remaining": 12, "updated_at": 1770000000000 } },
  //       "updated_at": 1770000000000
  //     }
  //   }
  // }

  _normalizeRateLimitData(rawData) {
    const data = rawData && typeof rawData === 'object' ? rawData : {};
    const normalized = {};

    for (const [kid, entry] of Object.entries(data)) {
      // Backward compatibility: old format was { keyId: ["model1", ...] }
      if (Array.isArray(entry)) {
        normalized[kid] = {
          daily_models: [...new Set(entry.filter(Boolean))],
          cooldowns: {},
          header: { model_limits: {} },
        };
        continue;
      }

      const dailyModels = Array.isArray(entry?.daily_models)
        ? [...new Set(entry.daily_models.filter(Boolean))]
        : [];
      const cooldowns = (entry?.cooldowns && typeof entry.cooldowns === 'object')
        ? { ...entry.cooldowns }
        : {};
      const header = (entry?.header && typeof entry.header === 'object')
        ? { ...entry.header, model_limits: { ...(entry.header.model_limits || {}) } }
        : { model_limits: {} };

      normalized[kid] = { daily_models: dailyModels, cooldowns, header };
    }

    return normalized;
  }

  async _loadRateLimitData(channelId, date) {
    const kvKey = `ratelimit:${channelId}:${date || this._todayKey()}`;
    let data;
    try {
      data = (await this.kv.get(kvKey, 'json')) || {};
    } catch {
      data = {};
    }
    return { kvKey, data: this._normalizeRateLimitData(data) };
  }

  async _saveRateLimitData(kvKey, data) {
    await this.kv.put(kvKey, JSON.stringify(data));
    this.cache.set(kvKey, { value: data, time: Date.now() });
  }

  _ensureRateLimitEntry(data, kid) {
    if (!data[kid]) {
      data[kid] = {
        daily_models: [],
        cooldowns: {},
        header: { model_limits: {} },
      };
    }
    if (!Array.isArray(data[kid].daily_models)) data[kid].daily_models = [];
    if (!data[kid].cooldowns || typeof data[kid].cooldowns !== 'object') data[kid].cooldowns = {};
    if (!data[kid].header || typeof data[kid].header !== 'object') data[kid].header = { model_limits: {} };
    if (!data[kid].header.model_limits || typeof data[kid].header.model_limits !== 'object') {
      data[kid].header.model_limits = {};
    }
    return data[kid];
  }

  async markRateLimited(channelId, apiKey, model) {
    const date = this._todayKey();
    const { kvKey, data } = await this._loadRateLimitData(channelId, date);
    const kid = this._keyId(apiKey);
    const entry = this._ensureRateLimitEntry(data, kid);
    const modelKey = model || '*';
    if (!entry.daily_models.includes(modelKey)) {
      entry.daily_models.push(modelKey);
    }
    await this._saveRateLimitData(kvKey, data);
  }

  async markRateLimitedTemporary(channelId, apiKey, model, cooldownMs = DEFAULT_COOLDOWN_MS) {
    const date = this._todayKey();
    const { kvKey, data } = await this._loadRateLimitData(channelId, date);
    const kid = this._keyId(apiKey);
    const entry = this._ensureRateLimitEntry(data, kid);
    const modelKey = model || '*';
    const until = Date.now() + Math.max(1, cooldownMs);
    entry.cooldowns[modelKey] = Math.max(until, Number(entry.cooldowns[modelKey]) || 0);
    await this._saveRateLimitData(kvKey, data);
  }

  async getRateLimits(channelId, date) {
    const { data } = await this._loadRateLimitData(channelId, date);
    return data;
  }

  isRateLimitedWithData(apiKey, model, rateLimitData) {
    const kid = this._keyId(apiKey);
    const entry = this._normalizeRateLimitData(rateLimitData)[kid];
    if (!entry) return false;

    if (entry.daily_models.includes(model) || entry.daily_models.includes('*')) return true;

    const now = Date.now();
    const modelUntil = Number(entry.cooldowns?.[model]) || 0;
    const globalUntil = Number(entry.cooldowns?.['*']) || 0;
    return modelUntil > now || globalUntil > now;
  }

  getRateLimitInfoWithData(apiKey, rateLimitData) {
    const kid = this._keyId(apiKey);
    const entry = this._normalizeRateLimitData(rateLimitData)[kid];
    if (!entry) {
      return {
        daily_models: [],
        cooldowns: {},
        header: { model_limits: {} },
      };
    }
    return entry;
  }
}

export function createStore(kv, mysql) {
  return new KVStore(kv, mysql);
}
