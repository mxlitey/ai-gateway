/**
 * 负载均衡：基于渠道的"公开模型 → 上游模型"映射（channel.model_map）解析有序目标列表。
 *
 * 渠道集成模式（不再有独立路由实体）：
 *   - 每个渠道维护 model_map = { 公开模型名: 真实上游模型名 }
 *   - 公开模型可与请求 model 完全一致（trim 后比较）才命中
 *   - 仅启用、且渠道启用且有已启用的 key 的渠道参与
 *   - 客户端 key 的 channel_ids 限定时，非允许渠道被过滤
 *   - 一个公开模型可被多个渠道提供，按渠道存储顺序作为尝试顺序
 *   - 每个渠道按自身已启用 key 顺序（随机起点轮换）展开，key 与上游模型组合去重
 *
 * 返回 target 结构：{ channel, key, model（上游模型名）, publicModel（公开名） }。
 */

/** 单条密钥归一化：兼容旧版 string[] 与新版 { key, enabled }[] */
export function normalizeKey(k) {
  if (k == null) return null;
  if (typeof k === 'string') return { key: k.trim(), enabled: true };
  return { key: String(k.key || '').trim(), enabled: k.enabled !== false };
}

/** 渠道中启用的密钥列表（纯字符串） */
export function enabledKeys(channel) {
  return (channel.keys || [])
    .map(normalizeKey)
    .filter(k => k && k.key && k.enabled)
    .map(k => k.key);
}

export class LoadBalancer {
  constructor(store) {
    this.store = store;
  }

  /**
   * @param {string} model 客户端请求的公开模型名
   * @param {string[]|null} allowedChannelIds - 客户端 key 允许的渠道，缺省不限制
   */
  async selectTarget(model, allowedChannelIds = null) {
    const channels = (await this.store.getChannels()) || [];
    const requested = (model || '').trim();

    const allowedSet = (allowedChannelIds && allowedChannelIds.length > 0)
      ? new Set(allowedChannelIds)
      : null;

    // 依渠道存储顺序，找出能提供该公开模型的渠道
    // 解析顺序：先查 model_map（公开模型 → 上游模型）；若无映射，则回退到 models（同名透传）
    const targetRows = [];
    for (const ch of channels) {
      if (ch.enabled === false || enabledKeys(ch).length === 0) continue;
      if (allowedSet && !allowedSet.has(ch.id)) continue;
      let um = (ch.model_map && typeof ch.model_map === 'object') ? ch.model_map[requested] : null;
      if (!um && Array.isArray(ch.models) && ch.models.includes(requested)) {
        um = requested; // 无显式映射，公开名=上游名（同名透传）
      }
      if (!um) continue;
      targetRows.push({ channel: ch, upstream_model: String(um).trim() || requested, publicModel: requested });
    }

    if (targetRows.length === 0) {
      return { targets: [], error: 'No available channel for model: ' + model };
    }

    // 依渠道顺序展开，同一渠道+key+上游模型去重
    const targets = [];
    const seen = new Set(); // 去重：channelId:key:upstreamModel
    for (const tr of targetRows) {
      const keys = await this.getOrderedKeys(tr.channel);
      for (const key of keys) {
        const dedupeKey = `${tr.channel.id}:${key}:${tr.upstream_model}`;
        if (seen.has(dedupeKey)) continue;
        seen.add(dedupeKey);
        targets.push({ channel: tr.channel, key, model: tr.upstream_model, publicModel: tr.publicModel });
      }
    }

    if (targets.length === 0) {
      return { targets: [], error: 'No available channel for model: ' + model };
    }

    return { targets };
  }

  /**
   * Key order within a channel: random start then round-robin order.
   * Uses random start to avoid race on shared RR counter under concurrency;
   * still returns keys in a deterministic cyclic order for failover.
   */
  async getOrderedKeys(channel) {
    const keys = enabledKeys(channel);
    if (keys.length === 0) return [];

    // Random start index — concurrency-safe, no shared counter read-modify-write
    const start = Math.floor(Math.random() * keys.length);

    const ordered = [];
    for (let i = 0; i < keys.length; i++) {
      ordered.push(keys[(start + i) % keys.length]);
    }
    return ordered;
  }
}