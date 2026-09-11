export class LoadBalancer {
  constructor(store) {
    this.store = store;
  }

  /**
   * Select ordered list of targets for a given model.
   * Targets are ordered by: priority group → weighted shuffle → round-robin keys.
   * The caller should try them in order (failover).
   * @param {string} model
   * @param {string[]|null} allowedChannelIds - if set, only these channels are used
   */
  async selectTarget(model, allowedChannelIds = null) {
    const channels = await this.store.getChannels();
    let enabled = channels.filter(ch => ch.enabled && ch.keys?.length > 0);

    if (allowedChannelIds && allowedChannelIds.length > 0) {
      enabled = enabled.filter(ch => allowedChannelIds.includes(ch.id));
    }

    // Only channels with a manually configured model list are eligible.
    // A channel that matches is one whose configured list contains the
    // requested model (exact or prefix match).
    let compatible = enabled.filter(ch =>
      ch.models && ch.models.length > 0 &&
      ch.models.some(m => m === model || model.startsWith(m))
    );

    if (compatible.length === 0) {
      return { targets: [], error: 'No available channel for model: ' + model };
    }

    // Pre-load rate-limit data in parallel
    const rateLimitMap = new Map();
    const preloadTasks = [];
    for (const ch of compatible) {
      preloadTasks.push(
        this.store.getRateLimits(ch.id).then(d => rateLimitMap.set(ch.id, d))
      );
    }
    if (preloadTasks.length > 0) {
      await Promise.all(preloadTasks);
    }

    // Group by priority (lower number = higher priority)
    const groups = {};
    for (const ch of compatible) {
      const p = ch.priority ?? 0;
      if (!groups[p]) groups[p] = [];
      groups[p].push(ch);
    }

    const priorities = Object.keys(groups).map(Number).sort((a, b) => a - b);

    // Build ordered target list
    const allTargets = [];
    for (const p of priorities) {
      const group = groups[p];
      const sorted = this.weightedShuffle(group);
      for (const ch of sorted) {
        const keys = await this.getOrderedKeys(ch);
        for (const key of keys) {
          allTargets.push({ channel: ch, key });
        }
      }
    }

    // Filter targets by 429 rate-limit state
    const targets = allTargets.filter(t => {
      const rlData = rateLimitMap.get(t.channel.id) || {};
      return !this.store.isRateLimitedWithData(t.key, model, rlData);
    });

    if (targets.length === 0 && allTargets.length > 0) {
      return { targets: [], error: 'All keys are rate-limited for model: ' + model };
    }

    if (targets.length === 0) {
      return { targets: [], error: 'No available channel for model: ' + model };
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
