/** 北京时区日期（用于用量/错误日志的读写保持一致，避免 UTC 跨日错位）。 */
function beijingToday() {
  return new Date(new Date().getTime() + 8 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

export async function handleAdminApi(request, env, store) {
  const url = new URL(request.url);
  const path = url.pathname.replace('/admin/api', '');
  const method = request.method;

  try {
    // --- Channels ---
    if (path === '/channels' && method === 'GET') {
      return jsonRes(await store.getChannels());
    }

    if (path === '/channels' && method === 'POST') {
      const data = await request.json();
      if (!data.name || !data.base_url) {
        return jsonRes({ error: 'name and base_url are required' }, 400);
      }
      const channels = await store.getChannels();
      const channel = {
        id: crypto.randomUUID(),
        name: data.name.trim(),
        base_url: data.base_url.trim(),
        keys: Array.isArray(data.keys) ? data.keys.filter(Boolean) : [],
        models: Array.isArray(data.models) ? data.models.filter(Boolean) : [],
        enabled: data.enabled !== false,
        priority: parseInt(data.priority) || 0,
        weight: Math.max(1, parseInt(data.weight) || 1),
        created_at: new Date().toISOString(),
      };
      channels.push(channel);
      await store.saveChannels(channels);
      return jsonRes(channel, 201);
    }

    // Match /channels/:id
    const chMatch = path.match(/^\/channels\/([^/]+)$/);
    if (chMatch) {
      const id = chMatch[1];

      if (method === 'PUT') {
        const data = await request.json();
        const channels = await store.getChannels();
        const idx = channels.findIndex(ch => ch.id === id);
        if (idx === -1) return jsonRes({ error: 'Channel not found' }, 404);

        const ch = channels[idx];
        channels[idx] = {
          ...ch,
          name: data.name?.trim() ?? ch.name,
          base_url: data.base_url?.trim() ?? ch.base_url,
          keys: Array.isArray(data.keys) ? data.keys.filter(Boolean) : ch.keys,
          models: Array.isArray(data.models) ? data.models.filter(Boolean) : ch.models,
          enabled: data.enabled ?? ch.enabled,
          priority: data.priority !== undefined ? (parseInt(data.priority) || 0) : ch.priority,
          weight: data.weight !== undefined ? Math.max(1, parseInt(data.weight) || 1) : ch.weight,
          id,
        };
        await store.saveChannels(channels);
        return jsonRes(channels[idx]);
      }

      if (method === 'DELETE') {
        const channels = await store.getChannels();
        const filtered = channels.filter(ch => ch.id !== id);
        if (filtered.length === channels.length) return jsonRes({ error: 'Channel not found' }, 404);
        await store.saveChannels(filtered);
        return jsonRes({ success: true });
      }
    }

    // Match /channels/:id/toggle
    const toggleMatch = path.match(/^\/channels\/([^/]+)\/toggle$/);
    if (toggleMatch && method === 'PATCH') {
      const id = toggleMatch[1];
      const channels = await store.getChannels();
      const idx = channels.findIndex(ch => ch.id === id);
      if (idx === -1) return jsonRes({ error: 'Channel not found' }, 404);
      channels[idx].enabled = !channels[idx].enabled;
      await store.saveChannels(channels);
      return jsonRes(channels[idx]);
    }

    // --- Model Routes（模型路由表）---
    if (path === '/routes' && method === 'GET') {
      return jsonRes(await store.getRoutes());
    }

    if (path === '/routes' && method === 'POST') {
      const data = await request.json();
      if (!data.model || !data.channel_id) {
        return jsonRes({ error: 'model and channel_id are required' }, 400);
      }
      const routes = await store.getRoutes();
      const route = {
        id: crypto.randomUUID(),
        name: data.name?.trim() || data.model.trim(),
        model: data.model.trim(),
        channel_id: data.channel_id,
        upstream_model: (data.upstream_model || '').trim() || data.model.trim(),
        priority: parseInt(data.priority) || 0,
        weight: Math.max(1, parseInt(data.weight) || 1),
        enabled: data.enabled !== false,
        created_at: new Date().toISOString(),
      };
      routes.push(route);
      await store.saveRoutes(routes);
      return jsonRes(route, 201);
    }

    // Match /routes/:id
    const routeMatch = path.match(/^\/routes\/([^/]+)$/);
    if (routeMatch) {
      const id = routeMatch[1];

      if (method === 'PUT') {
        const data = await request.json();
        const routes = await store.getRoutes();
        const idx = routes.findIndex(r => r.id === id);
        if (idx === -1) return jsonRes({ error: 'Route not found' }, 404);

        const r = routes[idx];
        routes[idx] = {
          ...r,
          name: data.name?.trim() ?? r.name,
          model: data.model?.trim() ?? r.model,
          channel_id: data.channel_id ?? r.channel_id,
          upstream_model: data.upstream_model !== undefined
            ? ((data.upstream_model || '').trim() || routes[idx].model || '')
            : r.upstream_model,
          priority: data.priority !== undefined ? (parseInt(data.priority) || 0) : r.priority,
          weight: data.weight !== undefined ? Math.max(1, parseInt(data.weight) || 1) : r.weight,
          enabled: data.enabled ?? r.enabled,
          id,
        };
        await store.saveRoutes(routes);
        return jsonRes(routes[idx]);
      }

      if (method === 'DELETE') {
        const routes = await store.getRoutes();
        const filtered = routes.filter(r => r.id !== id);
        if (filtered.length === routes.length) return jsonRes({ error: 'Route not found' }, 404);
        await store.saveRoutes(filtered);
        return jsonRes({ success: true });
      }

      if (method === 'PATCH') {
        const data = await request.json();
        const routes = await store.getRoutes();
        const idx = routes.findIndex(r => r.id === id);
        if (idx === -1) return jsonRes({ error: 'Route not found' }, 404);
        if (data.enabled !== undefined) routes[idx].enabled = data.enabled;
        await store.saveRoutes(routes);
        return jsonRes(routes[idx]);
      }
    }

    // --- Usage ---
    if (path === '/usage' && method === 'GET') {
      const date = url.searchParams.get('date') || beijingToday();
      const channels = await store.getChannels();
      const usageData = await Promise.all(
        channels.map(async ch => {
          const rawUsage = await store.getUsage(ch.id, date);
          const rawRate = await store.getRateLimits(ch.id, date);
          const keys = (ch.keys || []).map(k => {
            const kid = k.slice(-8);
            const usage = rawUsage[kid] || { total: 0, models: {} };
            const rateInfo = store.getRateLimitInfoWithData(k, rawRate);
            return {
              key_id: kid,
              key_hint: k.length > 12 ? k.slice(0, 7) + '...' + k.slice(-4) : k,
              usage,
              rate_state: {
                daily_models: rateInfo?.daily_models || [],
                cooldowns: rateInfo?.cooldowns || {},
              },
            };
          });
          return {
            channel_id: ch.id,
            channel_name: ch.name,
            enabled: ch.enabled,
            keys,
          };
        })
      );
      return jsonRes({ date, channels: usageData });
    }

    // --- API Key Usage (客户端密钥用量统计) ---
    if (path === '/apikeys/usage' && method === 'GET') {
      const date = url.searchParams.get('date') || beijingToday();
      const rawUsage = await store.getApiKeyUsage(date);
      return jsonRes({ date, keys: rawUsage });
    }

    // --- Error Logs ---
    if (path === '/errors' && method === 'GET') {
      const date = url.searchParams.get('date') || beijingToday();
      const channels = await store.getChannels();
      const errorData = await Promise.all(
        channels.map(async ch => ({
          channel_id: ch.id,
          channel_name: ch.name,
          errors: await store.getErrors(ch.id, date),
        }))
      );
      return jsonRes({ date, channels: errorData.filter(d => d.errors.length > 0) });
    }

    // --- API Keys ---
    if (path === '/apikeys' && method === 'GET') {
      return jsonRes(await store.getApiKeys());
    }

    if (path === '/apikeys' && method === 'POST') {
      const data = await request.json();
      const keys = await store.getApiKeys();
      const apiKey = {
        id: crypto.randomUUID(),
        name: data.name?.trim() || 'Unnamed',
        key: generateApiKeyString(),
        channel_ids: Array.isArray(data.channel_ids) ? data.channel_ids.filter(Boolean) : [],
        enabled: true,
        created_at: new Date().toISOString(),
      };
      keys.push(apiKey);
      await store.saveApiKeys(keys);
      return jsonRes(apiKey, 201);
    }

    // Match /apikeys/:id
    const keyMatch = path.match(/^\/apikeys\/([^/]+)$/);
    if (keyMatch) {
      const id = keyMatch[1];

      if (method === 'DELETE') {
        const keys = await store.getApiKeys();
        const filtered = keys.filter(k => k.id !== id);
        if (filtered.length === keys.length) return jsonRes({ error: 'API key not found' }, 404);
        await store.saveApiKeys(filtered);
        return jsonRes({ success: true });
      }

      if (method === 'PATCH') {
        const data = await request.json();
        const keys = await store.getApiKeys();
        const idx = keys.findIndex(k => k.id === id);
        if (idx === -1) return jsonRes({ error: 'API key not found' }, 404);
        if (data.enabled !== undefined) keys[idx].enabled = data.enabled;
        if (data.name !== undefined) keys[idx].name = data.name.trim();
        if (data.channel_ids !== undefined) keys[idx].channel_ids = Array.isArray(data.channel_ids) ? data.channel_ids.filter(Boolean) : [];
        await store.saveApiKeys(keys);
        return jsonRes(keys[idx]);
      }
    }

    // --- Test Upstream Connectivity (diagnostic) ---
    if (path === '/test-upstream' && method === 'POST') {
      const data = await request.json();
      const channels = await store.getChannels();
      const channelId = data.channel_id;
      const model = data.model || '';

      const testChannels = channelId
        ? channels.filter(ch => ch.id === channelId)
        : channels.filter(ch => ch.enabled && ch.keys?.length > 0);

      if (testChannels.length === 0) {
        return jsonRes({ error: 'No matching channels found' }, 404);
      }

      const results = [];
      for (const ch of testChannels) {
        for (const key of (ch.keys || [])) {
          const keyHint = key.length > 12 ? key.slice(0, 7) + '...' + key.slice(-4) : key;
          const baseUrl = ch.base_url.replace(/\/+$/, '');
          const testUrl = baseUrl + '/chat/completions';
          const start = Date.now();
          try {
            const resp = await fetch(testUrl, {
              method: 'POST',
              headers: {
                'Content-Type': 'application/json',
                'Authorization': `Bearer ${key}`,
              },
              body: JSON.stringify({
                model: model || 'gpt-3.5-turbo',
                messages: [{ role: 'user', content: 'say ok' }],
                max_tokens: 3,
              }),
            });
            const duration = Date.now() - start;
            const rateHeaders = {};
            for (const h of ['modelscope-ratelimit-requests-limit', 'modelscope-ratelimit-requests-remaining',
              'modelscope-ratelimit-model-requests-limit', 'modelscope-ratelimit-model-requests-remaining',
              'retry-after', 'x-ratelimit-limit-requests', 'x-ratelimit-remaining-requests']) {
              const v = resp.headers.get(h);
              if (v != null) rateHeaders[h] = v;
            }
            let body = '';
            try { body = (await resp.text()).slice(0, 300); } catch {}
            results.push({
              channel: ch.name, channel_id: ch.id, key_hint: keyHint,
              status: resp.status, duration_ms: duration,
              rate_headers: rateHeaders, body,
            });
          } catch (err) {
            results.push({
              channel: ch.name, channel_id: ch.id, key_hint: keyHint,
              status: 0, duration_ms: Date.now() - start,
              error: err.message,
            });
          }
          await new Promise(r => setTimeout(r, 1000));
        }
      }

      const count429 = results.filter(r => r.status === 429).length;
      const count200 = results.filter(r => r.status === 200).length;
      return jsonRes({
        summary: { total: results.length, ok: count200, rate_limited: count429 },
        results,
      });
    }

    return jsonRes({ error: 'Not found' }, 404);
  } catch (err) {
    console.error('Admin API error:', err);
    return jsonRes({ error: err.message || 'Internal error' }, 500);
  }
}

function generateApiKeyString() {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  const hex = Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
  return 'sk-' + hex;
}

function jsonRes(body, status = 200) {
  return new Response(JSON.stringify(body, null, 2), {
    status,
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
    },
  });
}
