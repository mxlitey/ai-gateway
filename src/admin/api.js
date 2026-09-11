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
        path: (data.path || '').trim(),
        keys: Array.isArray(data.keys) ? data.keys.filter(Boolean) : [],
        models: Array.isArray(data.models) ? data.models.filter(Boolean) : [],
        model_map: normalizeModelMap(data),
        enabled: data.enabled !== false,
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
        const nextChannel = {
          ...ch,
          name: data.name?.trim() ?? ch.name,
          base_url: data.base_url?.trim() ?? ch.base_url,
          path: data.path?.trim() ?? ch.path,
          keys: Array.isArray(data.keys) ? data.keys.filter(Boolean) : ch.keys,
          models: Array.isArray(data.models) ? data.models.filter(Boolean) : ch.models,
          model_map: (data.model_map !== undefined)
            ? normalizeModelMap(data)
            : ((ch.model_map && typeof ch.model_map === 'object') ? ch.model_map : {}),
          enabled: data.enabled ?? ch.enabled,
          id,
        };
        channels[idx] = nextChannel;
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

    // --- Fetch Upstream Models (渠道模型列表：通过上游 /models 获取) ---
    if (path === '/fetch-models' && method === 'POST') {
      const data = await request.json();
      let ch = null;
      if (data.channel_id) {
        const channels = await store.getChannels();
        ch = channels.find(c => c.id === data.channel_id);
        if (!ch) return jsonRes({ error: 'Channel not found' }, 404);
      } else {
        ch = {
          base_url: (data.base_url || '').trim(),
          keys: Array.isArray(data.keys) ? data.keys.filter(Boolean) : [],
        };
      }
      if (!ch.base_url || !(ch.keys || []).length) {
        return jsonRes({ error: '基础 URL 和密钥不能为空' }, 400);
      }
      const { models, error } = await fetchUpstreamModels(ch);
      if (error) return jsonRes({ error }, 400);
      return jsonRes({ models });
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
          const testUrl = baseUrl + resolveChatPath(ch);
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

/** 将前端提交的"公开模型 → 上游模型"映射规范化为 { 公开名: 上游模型 }。
 *  仅保留公开名与上游模型均非空的条目；上游模型缺省回退为公开名。 */
function normalizeModelMap(data) {
  const src = (data && typeof data.model_map === 'object' && !Array.isArray(data.model_map)) ? data.model_map : {};
  const out = {};
  for (const key of Object.keys(src)) {
    const pub = String(key || '').trim();
    const um = String(src[key] || '').trim();
    if (!pub) continue;
    out[pub] = um || pub;
  }
  return out;
}

/** 渠道对话接口路径：未配置协议接口时默认 /chat/completions */
function resolveChatPath(channel) {
  const p = (channel && channel.path) || '';
  const trimmed = p.trim();
  return trimmed ? (trimmed.startsWith('/') ? trimmed : '/' + trimmed) : '/chat/completions';
}

/** 调用上游 base_url + /models 拉取模型列表（兼容 OpenAI / Claude 响应格式）。 */
async function fetchUpstreamModels(ch) {
  const baseUrl = String(ch.base_url || '').replace(/\/+$/, '');
  let lastErr = null;
  const keys = ch.keys || [];
  for (const key of keys) {
    try {
      const resp = await fetch(baseUrl + '/models', {
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${key}`,
        },
      });
      const text = await resp.text();
      if (resp.ok) {
        let data = null;
        try { data = JSON.parse(text); } catch { data = null; }
        const ids = Array.isArray(data?.data) ? data.data.map(m => m && m.id).filter(Boolean) : [];
        return { models: ids };
      }
      lastErr = `HTTP ${resp.status}: ${text.slice(0, 200)}`;
    } catch (e) {
      lastErr = e.message;
    }
  }
  return { error: lastErr || '获取模型失败' };
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
