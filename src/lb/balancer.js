/**
 * 展开一条路由的目标集合。
 *
 * 新格式：route.targets = [{ channel_id, upstream_model }, ...]，
 * 一个公开模型可路由到不同渠道的不同模型。
 * 兼容旧格式：route 顶层持有 channel_id / upstream_model。
 * 返回原始目标行（不校验渠道可用性）。
 */
export function expandRouteTargets(route) {
  if (Array.isArray(route.targets) && route.targets.length > 0) {
    return route.targets
      .filter(t => t && t.channel_id)
      .map(t => ({
        channel_id: t.channel_id,
        upstream_model: String(t.upstream_model || '').trim(),
      }));
  }
  if (route.channel_id) {
    return [{
      channel_id: route.channel_id,
      upstream_model: String(route.upstream_model || '').trim(),
    }];
  }
  return [];
}

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
   *   - 命中后按路由内目标顺序展开
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

    // 严格只走路由：逐条展开路由的目标（渠道+上游模型）并过滤可用渠道
    const targetRows = [];
    for (const r of routes) {
      if (r.enabled === false) continue;
      if (String(r.model || '').trim() !== requested) continue;
      const rowTargets = expandRouteTargets(r);
      for (const t of rowTargets) {
        const ch = channelMap.get(t.channel_id);
        if (!ch || ch.enabled === false || !ch.keys || ch.keys.length === 0) continue;
        if (allowedSet && !allowedSet.has(ch.id)) continue;
        targetRows.push({ ...t, channel: ch, routeId: r.id, publicModel: requested });
      }
    }

    if (targetRows.length === 0) {
      return { targets: [], error: 'No available route for model: ' + model };
    }

    // 预加载相关渠道限流状态
    const rateLimitMap = new Map();
    const chIds = new Set(targetRows.map(t => t.channel.id));
    await Promise.all([...chIds].map(id =>
      this.store.getRateLimits(id).then(d => rateLimitMap.set(id, d))
    ));

    const publicModel = requested;

    // 按路由内目标顺序展开（画在前的目标优先），同一渠道+key+上游模型去重
    const allTargets = [];
    const seen = new Set(); // 去重：channelId:key:upstreamModel
    for (const tr of targetRows) {
      const upstream = String(tr.upstream_model || '').trim() || requested;
      const keys = await this.getOrderedKeys(tr.channel);
      for (const key of keys) {
        const dedupeKey = `${tr.channel.id}:${key}:${upstream}`;
        if (seen.has(dedupeKey)) continue;
        seen.add(dedupeKey);
        allTargets.push({ channel: tr.channel, key, model: upstream, routeId: tr.routeId, publicModel });
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
