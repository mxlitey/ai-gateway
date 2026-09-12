import { enabledKeys } from '../lb/balancer.js';
import { normalizeHeaders, applyChannelHeaders } from '../proxy/headers.js';
import { resolveChatPath, resolveModelsPath } from '../proxy/utils.js';

/** 北京时区日期（用于用量/错误日志的读写保持一致，避免 UTC 跨日错位）。 */
function beijingToday() {
  return new Date(new Date().getTime() + 8 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

/**
 * 按与真实转发相同的标准检查上游 200 响应体：
 * 内嵌 error / choices 不是数组 / choices 为空，都视为失败。
 * 否则「状态码 200 但内容其实是错误」的渠道会被诊断显示成健康的。
 * @returns 失败原因；正常或无法判断时返回 ''
 */
function describeInvalidChatBody(bodyText) {
  let data;
  try { data = JSON.parse(bodyText); } catch { return ''; }
  if (!data || typeof data !== 'object') return '';
  if (data.error && typeof data.error === 'object') {
    return String(data.error.message || data.error.code || data.error.type || 'upstream error');
  }
  if (!Array.isArray(data.choices)) return 'choices 字段异常（' + JSON.stringify(data.choices) + '）';
  if (data.choices.length === 0) return 'choices 为空';
  return '';
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
        headers: normalizeHeaders(data.headers),
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
          headers: (data.headers !== undefined)
            ? normalizeHeaders(data.headers)
            : (Array.isArray(ch.headers) ? ch.headers : []),
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

    // Match /channels/:id/priority
    // 设置「某公开模型 → 该渠道」的尝试优先级（越大越先试）。priority 为 0 时删除条目回退默认值。
    const priMatch = path.match(/^\/channels\/([^/]+)\/priority$/);
    if (priMatch && method === 'PATCH') {
      const id = priMatch[1];
      const data = await request.json();
      const model = String((data && data.model) || '').trim();
      const raw = Number(data && data.priority);
      if (!model || !Number.isFinite(raw)) {
        return jsonRes({ error: 'model and numeric priority are required' }, 400);
      }
      const channels = await store.getChannels();
      const idx = channels.findIndex(ch => ch.id === id);
      if (idx === -1) return jsonRes({ error: 'Channel not found' }, 404);

      const priority = Math.max(0, Math.min(9999, Math.floor(raw)));
      const mp = (channels[idx].model_priority && typeof channels[idx].model_priority === 'object')
        ? { ...channels[idx].model_priority }
        : {};
      if (priority === 0) delete mp[model];
      else mp[model] = priority;
      channels[idx].model_priority = mp;
      await store.saveChannels(channels);
      return jsonRes({ success: true, model, priority });
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
      if (!ch.base_url || enabledKeys(ch).length === 0) {
        return jsonRes({ error: '基础 URL 和密钥不能为空' }, 400);
      }
      const { models, error } = await fetchUpstreamModels(ch);
      if (error) return jsonRes({ error }, 400);
      return jsonRes({ models });
    }

    // --- Test Upstream Connectivity (diagnostic) ---
    // 支持两种入参：
    //   A) tasks: [{ channel_id?, key?, model }]  批量诊断（指定渠道 + 指定 key + 具体模型）
    //   B) 旧单点: { channel_id?, key?, model }
    if (path === '/test-upstream' && method === 'POST') {
      const data = await request.json();
      const channels = await store.getChannels();

      let tasks;
      if (Array.isArray(data.tasks) && data.tasks.length) {
        tasks = data.tasks;
      } else if (data.model || data.channel_id || data.key) {
        tasks = [{ channel_id: data.channel_id, key: data.key, model: data.model }];
      } else {
        tasks = [];
      }

      if (tasks.length === 0) {
        return jsonRes({ error: '请指定要诊断的模型或任务' }, 400);
      }

      const results = [];
      for (const tk of tasks) {
        const model = String(tk.model || '').trim();
        const targetChannels = tk.channel_id
          ? channels.filter(ch => ch.id === tk.channel_id)
          : channels.filter(ch => ch.enabled && enabledKeys(ch).length > 0);
        for (const ch of targetChannels) {
          let keys = enabledKeys(ch);
          if (tk.key) keys = keys.filter(k => k === tk.key);
          if (keys.length === 0) continue;
          if (!model) {
            // 不再偷偷用某个写死的模型兜底：那会把「没指定模型」显示成渠道故障
            results.push({
              model: '', channel: ch.name, channel_id: ch.id, key_hint: '',
              status: 0, duration_ms: 0, error: '未指定模型，无法诊断',
            });
            continue;
          }
          for (const key of keys) {
            const keyHint = key.length > 12 ? key.slice(0, 7) + '...' + key.slice(-4) : key;
            const baseUrl = ch.base_url.replace(/\/+$/, '');
            const testUrl = baseUrl + resolveChatPath(ch);
            const start = Date.now();
            try {
              const reqHeaders = new Headers();
              reqHeaders.set('Content-Type', 'application/json');
              reqHeaders.set('Authorization', `Bearer ${key}`);
              applyChannelHeaders(reqHeaders, ch, null);
              const resp = await fetch(testUrl, {
                method: 'POST',
                headers: reqHeaders,
                body: JSON.stringify({
                  model: model,
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
              let rawText = '';
              try { rawText = await resp.text(); } catch {}
              // 与真实转发同一判定标准：200 也可能是「内容为错误」的假成功
              const invalid = resp.status === 200 ? describeInvalidChatBody(rawText) : '';
              results.push({
                model: model, channel: ch.name, channel_id: ch.id, key_hint: keyHint,
                status: resp.status, duration_ms: duration,
                rate_headers: rateHeaders, body: rawText.slice(0, 300),
                ...(invalid ? { error: invalid } : {}),
              });
            } catch (err) {
              results.push({
                model: model, channel: ch.name, channel_id: ch.id, key_hint: keyHint,
                status: 0, duration_ms: Date.now() - start,
                error: err.message,
              });
            }
          }
        }
      }

      if (results.length === 0) {
        return jsonRes({ error: '没有可诊断的目标（渠道/key/模型不匹配）' }, 404);
      }

      const count429 = results.filter(r => r.status === 429).length;
      const countOk = results.filter(r => r.status === 200 && !r.error).length;
      return jsonRes({
        summary: { total: results.length, ok: countOk, rate_limited: count429 },
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

/** 调用上游「模型列表」接口拉取模型（兼容 OpenAI / Claude 响应格式）。
 *  接口路径与转发用同一套推导（resolveModelsPath），不再无脑拼 /models。 */
async function fetchUpstreamModels(ch) {
  const baseUrl = String(ch.base_url || '').replace(/\/+$/, '');
  const modelsUrl = baseUrl + resolveModelsPath(ch);
  let lastErr = null;
  const keys = enabledKeys(ch);
  for (const key of keys) {
    try {
      const reqHeaders = new Headers();
      reqHeaders.set('Content-Type', 'application/json');
      reqHeaders.set('Authorization', `Bearer ${key}`);
      applyChannelHeaders(reqHeaders, ch, null);
      const resp = await fetch(modelsUrl, {
        headers: reqHeaders,
      });
      const text = await resp.text();
      if (resp.ok) {
        let data = null;
        try { data = JSON.parse(text); } catch { data = null; }
        const items = Array.isArray(data?.data) ? data.data.filter(m => m && m.id) : [];
        return { models: items.map(m => String(m.id)) };
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
