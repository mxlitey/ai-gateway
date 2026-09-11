export class LoadBalancer {
  constructor(store) {
    this.store = store;
  }

  /**
   * 基于模型路由表解析有序目标列表（严格只走路由，无匹配则报错）。
   *
   * 路由规则：
   *   - 路由公开模型名与请求 model 完全一致（trim 后比较）才命中
   *   - 仅启用、且目标渠道启用且有 key 的路由参与
   *   - 客户端 key 的 channel_ids 限定时，非允许渠道的路由被过滤
   *   - 命中后按 priority 分组（数值越小越优先），组内按权重重放
   *   - 每个目标渠道按自身 key 顺序（随机起点轮换）展开，key 与上游模型
   *     组合去重，避免同一渠道下多条同名路由重复
   *
   * 返回 target 结构：{ channel, key, model（上游模型名）, routeId, publicModel（公开名） }。
   *
   * @param {string} model 客户端请求的公开模型名
   * @param {string[]|null} allowedChannelIds - 客户端 key 允许的渠道，缺省不限制
   */
  async selectTarget(model, allowedChannelIds = null) {
    const routes = (await this.store.getRoutes()) || [];
    const channels = await this.store.getChannels();
    const channelMap = new Map(channels.map(ch => [ch.id, ch]));

    const allowedSet = (allowedChannelIds && allowedChannelIds.length > 0)
      ? new Set(allowedChannelIds)
      : null;

    const requested = (model || '').trim();

    // 严格只走路由
    const matched = routes.filter(r => {
      if (r.enabled === false) return false;
      if (String(r.model || '').trim() !== requested) return false;
      const ch = channelMap.get(r.channel_id);
      if (!ch || ch.enabled === false || !ch.keys || ch.keys.length === 0) return false;
      if (allowedSet && !allowedSet.has(ch.id)) return false;
      return true;
    });

    if (matched.length === 0) {
      return { targets: [], error: 'No available route for model: ' + model };
    }

    // 预加载相关渠道限流状态
    const rateLimitMap = new Map();
    const chIds = new Set(matched.map(r => r.channel_id));
    await Promise.all([...chIds].map(id =>
      this.store.getRateLimits(id).then(d => rateLimitMap.set(id, d))
    ));

    const publicModel = requested;

    // 按优先级分组（数值越小越优先），组内按权重重放
    const groups = {};
    for (const r of matched) {
      const p = r.priority ?? 0;
      if (!groups[p]) groups[p] = [];
      groups[p].push(r);
    }
    const priorities = Object.keys(groups).map(Number).sort((a, b) => a - b);

    const allTargets = [];
    const seen = new Set(); // 去重：channelId:key:upstreamModel
    for (const p of priorities) {
      const ordered = this.weightedShuffle(groups[p]);
      for (const route of ordered) {
        const ch = channelMap.get(route.channel_id);
        const upstream = String(route.upstream_model || route.model || '').trim() || requested;
        const keys = await this.getOrderedKeys(ch);
        for (const key of keys) {
          const dedupeKey = `${ch.id}:${key}:${upstream}`;
          if (seen.has(dedupeKey)) continue;
          seen.add(dedupeKey);
          allTargets.push({ channel: ch, key, model: upstream, routeId: route.id, publicModel });
        }
      }
    }

    // 按 429 限流状态过滤
    const targets = allTargets.filter(t => {
      const rlData = rateLimitMap.get(t.channel.id) || {};
      return !this.store.isRateLimitedWithData(t.key, publicModel, rlData);
    });

    if (targets.length === 0 && allTargets.length > 0) {
      return { targets: [], error: 'All keys are rate-limited for model: ' + model };
    }

    if (targets.length === 0) {
      return { targets: [], error: 'No available route for model: ' + model };
    }

    return { targets };
  }

  /**
   * Weighted random shuffle: channels with higher weight
   * have proportionally higher chance of being picked first.
   */
  weightedShuffle(channels) {
    const items = channels.map(ch => ({ ch, w: ch.weight || 1 }));
    const result = [];
    while (items.length > 0) {
      const total = items.reduce((sum, i) => sum + i.w, 0);
      let rand = Math.random() * total;
      let idx = 0;
      for (let i = 0; i < items.length; i++) {
        rand -= items[i].w;
        if (rand <= 0) { idx = i; break; }
      }
      result.push(items[idx].ch);
      items.splice(idx, 1);
    }
    return result;
  }

  /**
   * Key order within a channel: random start then round-robin order.
   * Uses random start to avoid race on shared RR counter under concurrency;
   * still returns keys in a deterministic cyclic order for failover.
   */
  async getOrderedKeys(channel) {
    const keys = channel.keys || [];
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
