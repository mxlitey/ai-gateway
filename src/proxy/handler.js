import { verifyApiKey } from './auth.js';
import { LoadBalancer, enabledKeys } from '../lb/balancer.js';
import { claudeToOpenAI, openAIToClaude, openAIStreamToClaudeStream } from './claude.js';
import { responsesToChatCompletions, chatCompletionsToResponses, chatCompletionsStreamToResponsesStream } from './responses.js';

export async function handleProxy(request, env, store) {
  // Verify client API key
  const authResult = await verifyApiKey(request, store);
  if (!authResult.valid) {
    return jsonRes({
      error: { message: authResult.error, type: 'invalid_request_error' }
    }, 401);
  }

  const url = new URL(request.url);
  const path = url.pathname;

  const allowedChannelIds = authResult.apiKey.channel_ids || null;

  // GET /v1/models
  if (path.endsWith('/models') && request.method === 'GET') {
    return handleModels(store, allowedChannelIds);
  }

  // Only POST for completions / embeddings / messages
  if (request.method !== 'POST') {
    return jsonRes({ error: { message: 'Method not allowed' } }, 405);
  }

  let body;
  try {
    body = await request.json();
  } catch {
    return jsonRes({ error: { message: 'Invalid JSON body' } }, 400);
  }

  // ---- Claude Messages API (/v1/messages) ----
  if (path.endsWith('/messages')) {
    return handleClaudeMessages(request, url, body, store, allowedChannelIds);
  }

  // ---- OpenAI Responses API (/v1/responses) ----
  if (path.endsWith('/responses')) {
    return handleResponses(request, url, body, store, allowedChannelIds);
  }

  // ---- OpenAI-compatible passthrough ----
  return handleOpenAIProxy(request, url, path, body, store, allowedChannelIds);
}

// ─── Claude Messages API handler ───────────────────────────────────

async function handleClaudeMessages(request, url, claudeBody, store, allowedChannelIds) {
  const model = claudeBody.model || '';
  const isStream = claudeBody.stream || false;

  // Convert Claude request to OpenAI format
  const openaiBody = claudeToOpenAI(claudeBody);

  // 参考 one-api：流式请求注入 stream_options，让上游返回 token 用量
  if (isStream) {
    openaiBody.stream_options = { include_usage: true };
  }

  const lb = new LoadBalancer(store);
  const { targets, error } = await lb.selectTarget(model, allowedChannelIds);

  if (error || targets.length === 0) {
    // Return error in Claude format
    return claudeErrorRes(error || 'No available channel for model: ' + model, 503);
  }

  const MAX_429_ROUNDS = 2;
  let lastError = null;
  let last429Body = '';

  for (let round = 0; round < MAX_429_ROUNDS; round++) {
    if (round > 0) {
      console.log(`[proxy][claude] all targets returned 429, retry round ${round + 1} after delay`);
      await sleep(3000 * round);
    }
    let consecutive429 = 0;

    for (const target of targets) {
      let targetUrl = '';
      try {
        const baseUrl = target.channel.base_url.replace(/\/+$/, '');
        targetUrl = baseUrl + resolveChatPath(target.channel) + url.search;

        console.log(`[proxy][claude] -> ${target.channel.name} ${targetUrl}${round > 0 ? ` (retry #${round})` : ''}`);

        // 路由改写：把客户端的公开模型名替换为目标渠道的真实上游模型名
        openaiBody.model = target.model;

        const headers = new Headers();
        headers.set('Content-Type', 'application/json');
        headers.set('Authorization', `Bearer ${target.key}`);
        if (isStream) headers.set('Accept', 'text/event-stream');
        applyOpencodeHeaders(headers, target.channel, request);

        const resp = await fetch(targetUrl, {
          method: 'POST',
          headers,
          body: JSON.stringify(openaiBody),
        });
        const rateHeaders = extractRateLimitHeaders(resp.headers);

        if (resp.status === 404) {
          lastError = `HTTP 404 (model not found)`;
          logError(store, target, model, 404, lastError);
          continue;
        }

        if (resp.status === 429) {
          consecutive429++;
          try { last429Body = await resp.text(); } catch { last429Body = ''; }
          lastError = `HTTP 429 (rate limited)`;
          const rlReason = await classifyAndRecord429(store, target, model, resp, rateHeaders);
          logError(store, target, model, 429, `${lastError}${rlReason ? `: ${rlReason}` : ''}`);
          await sleep(calc429Delay(rateHeaders, consecutive429));
          continue;
        }

        if (resp.ok || resp.status < 500) {
          if (!resp.ok) {
            const errBody = await resp.text();
            logError(store, target, model, resp.status, errBody);
            return claudeErrorRes(`Upstream error: ${errBody}`, resp.status);
          }

          // 上游返回 SSE 流时才走流式处理；某些上游在异常情况下即使收到
          // stream:true 也会返回普通 JSON（choices:null），此时走非流式验证路径
          const upstreamIsSSE = isStream &&
            (resp.headers.get('Content-Type') || '').includes('text/event-stream');

          if (upstreamIsSSE) {
            const processedStream = processStream(resp.body, err => {
              const msg = (err && (err.message || err.code || err.type)) || 'upstream error';
              logError(store, target, model, 200, 'HTTP 200 stream error: ' + msg);
            });
            const claudeStream = openAIStreamToClaudeStream(processedStream, model);

            return new Response(claudeStream, {
              status: 200,
              headers: {
                'Content-Type': 'text/event-stream',
                'Cache-Control': 'no-cache',
                'Connection': 'keep-alive',
                // 禁用 nginx 缓冲，确保代理实时转发流式数据
                'X-Accel-Buffering': 'no',
                'Access-Control-Allow-Origin': '*',
              },
            });
          }

          // 非流式 或 上游未返回 SSE 时（可能是异常 JSON），验证 choices 字段
          if (isStream) {
            console.warn(`[proxy][claude] 上游对 stream:true 返回了非 SSE 响应 (Content-Type: ${resp.headers.get('Content-Type')}), 回退到非流式验证`);
          }
          const openaiData = await resp.json();
          // 上游可能以 HTTP 200 返回错误（如向非多模态模型发送图片）
          if (openaiData && openaiData.error && typeof openaiData.error === 'object') {
            const msg = (openaiData.error.message || openaiData.error.code || openaiData.error.type) || 'upstream error';
            lastError = `HTTP 200 with error: ${msg}`;
            logError(store, target, model, 200, lastError);
            continue;
          }
          if (!Array.isArray(openaiData.choices)) {
            lastError = `upstream returned invalid response (choices=${openaiData.choices})`;
            logError(store, target, model, 200, lastError);
            continue;
          }
          if (openaiData.choices.length === 0) {
            lastError = `upstream returned empty choices (likely error)`;
            logError(store, target, model, 200, lastError);
            continue;
          }

          // 非流式：转换回 Claude 格式
          const claudeResponse = openAIToClaude(openaiData, model);
          return jsonRes(claudeResponse, 200);
        }

        lastError = `HTTP ${resp.status}`;
        logError(store, target, model, resp.status, lastError);
      } catch (err) {
        lastError = `network error: ${err.message}`;
        logError(store, target, model, 0, `${lastError}${targetUrl ? ` (${targetUrl})` : ''}`);
      }
    }

    if (consecutive429 === 0 || consecutive429 < targets.length) break;
  }

  const detail = last429Body ? ` | upstream: ${last429Body.slice(0, 200)}` : '';
  return claudeErrorRes(`All targets failed. Last error: ${lastError}${detail}`, 502);
}

// ─── OpenAI Responses API handler ──────────────────────────────────

async function handleResponses(request, url, body, store, allowedChannelIds) {
  const model = body.model || '';
  const isStream = body.stream || false;

  const openaiBody = responsesToChatCompletions(body);

  if (isStream) {
    openaiBody.stream_options = { include_usage: true };
  }

  const lb = new LoadBalancer(store);
  const { targets, error } = await lb.selectTarget(model, allowedChannelIds);

  if (error || targets.length === 0) {
    return responsesErrorRes(error || 'No available channel for model: ' + model, 503);
  }

  const MAX_429_ROUNDS = 2;
  let lastError = null;
  let last429Body = '';

  for (let round = 0; round < MAX_429_ROUNDS; round++) {
    if (round > 0) {
      console.log(`[proxy][responses] all targets returned 429, retry round ${round + 1} after delay`);
      await sleep(3000 * round);
    }
    let consecutive429 = 0;

    for (const target of targets) {
      let targetUrl = '';
      try {
        const baseUrl = target.channel.base_url.replace(/\/+$/, '');
        targetUrl = baseUrl + resolveChatPath(target.channel) + url.search;

        console.log(`[proxy][responses] -> ${target.channel.name} ${targetUrl}${round > 0 ? ` (retry #${round})` : ''}`);

        // 路由改写：把客户端的公开模型名替换为目标渠道的真实上游模型名
        openaiBody.model = target.model;

        const headers = new Headers();
        headers.set('Content-Type', 'application/json');
        headers.set('Authorization', `Bearer ${target.key}`);
        if (isStream) headers.set('Accept', 'text/event-stream');
        applyOpencodeHeaders(headers, target.channel, request);

        const resp = await fetch(targetUrl, {
          method: 'POST',
          headers,
          body: JSON.stringify(openaiBody),
        });
        const rateHeaders = extractRateLimitHeaders(resp.headers);

        if (resp.status === 404) {
          lastError = `HTTP 404 (model not found)`;
          logError(store, target, model, 404, lastError);
          continue;
        }

        if (resp.status === 429) {
          consecutive429++;
          try { last429Body = await resp.text(); } catch { last429Body = ''; }
          lastError = `HTTP 429 (rate limited)`;
          const rlReason = await classifyAndRecord429(store, target, model, resp, rateHeaders);
          logError(store, target, model, 429, `${lastError}${rlReason ? `: ${rlReason}` : ''}`);
          await sleep(calc429Delay(rateHeaders, consecutive429));
          continue;
        }

        if (resp.ok || resp.status < 500) {
          if (!resp.ok) {
            const errBody = await resp.text();
            logError(store, target, model, resp.status, errBody);
            return responsesErrorRes(`Upstream error: ${errBody}`, resp.status);
          }

          const upstreamIsSSE = isStream &&
            (resp.headers.get('Content-Type') || '').includes('text/event-stream');

          if (upstreamIsSSE) {
            const { stream } = chatCompletionsStreamToResponsesStream(resp.body, model);

            return new Response(stream, {
              status: 200,
              headers: {
                'Content-Type': 'text/event-stream',
                'Cache-Control': 'no-cache',
                'Connection': 'keep-alive',
                'X-Accel-Buffering': 'no',
                'Access-Control-Allow-Origin': '*',
              },
            });
          }

          if (isStream) {
            console.warn(`[proxy][responses] upstream returned non-SSE for stream:true (Content-Type: ${resp.headers.get('Content-Type')}), falling back to non-streaming`);
          }

          const openaiData = await resp.json();
          // 上游可能以 HTTP 200 返回错误（如向非多模态模型发送图片）
          if (openaiData && openaiData.error && typeof openaiData.error === 'object') {
            const msg = (openaiData.error.message || openaiData.error.code || openaiData.error.type) || 'upstream error';
            lastError = `HTTP 200 with error: ${msg}`;
            logError(store, target, model, 200, lastError);
            continue;
          }
          if (!Array.isArray(openaiData.choices)) {
            lastError = `upstream returned invalid response (choices=${openaiData.choices})`;
            logError(store, target, model, 200, lastError);
            continue;
          }
          if (openaiData.choices.length === 0) {
            lastError = `upstream returned empty choices (likely error)`;
            logError(store, target, model, 200, lastError);
            continue;
          }

          const responsesData = chatCompletionsToResponses(openaiData, model);
          return jsonRes(responsesData, 200);
        }

        lastError = `HTTP ${resp.status}`;
        logError(store, target, model, resp.status, lastError);
      } catch (err) {
        lastError = `network error: ${err.message}`;
        logError(store, target, model, 0, `${lastError}${targetUrl ? ` (${targetUrl})` : ''}`);
      }
    }

    if (consecutive429 === 0 || consecutive429 < targets.length) break;
  }

  const detail = last429Body ? ` | upstream: ${last429Body.slice(0, 200)}` : '';
  return responsesErrorRes(`All targets failed. Last error: ${lastError}${detail}`, 502);
}

// ─── OpenAI passthrough handler ────────────────────────────────────

async function handleOpenAIProxy(request, url, path, body, store, allowedChannelIds) {
  const model = body.model || '';
  const lb = new LoadBalancer(store);
  const { targets, error } = await lb.selectTarget(model, allowedChannelIds);

  if (error || targets.length === 0) {
    return jsonRes({
      error: { message: error || 'No available channel', type: 'server_error' }
    }, 503);
  }

  // Strip /v1 prefix, keep the rest (e.g. /chat/completions)
  const upstreamPath = path.replace(/^\/v1/, '');

  // 参考 one-api：流式请求注入 stream_options，让上游在最后一个 chunk 返回 token 用量
  if (body.stream) {
    if (!body.stream_options) body.stream_options = {};
    body.stream_options.include_usage = true;
  }

  // Try each target in order (failover on 5xx / network error)
  // 429 退避：共享 IP 环境（如边缘节点）下上游可能按 IP 限流，
  // 需要在连续 429 之间加入延迟，并支持整轮重试
  const MAX_429_ROUNDS = 2;
  let lastError = null;
  let last429Body = '';

  for (let round = 0; round < MAX_429_ROUNDS; round++) {
    if (round > 0) {
      console.log(`[proxy] all targets returned 429, retry round ${round + 1} after delay`);
      await sleep(3000 * round);
    }
    let consecutive429 = 0;

    for (const target of targets) {
      let targetUrl = '';
      try {
        const baseUrl = target.channel.base_url.replace(/\/+$/, '');
        // 对话接口可被渠道协议接口覆盖（如 /chat/completions）；其他端点（embeddings 等）沿用请求路径
        const opPath = upstreamPath.includes('/chat/completions') ? resolveChatPath(target.channel) : upstreamPath;
        targetUrl = baseUrl + opPath + url.search;

        console.log(`[proxy] -> ${target.channel.name} ${targetUrl}${round > 0 ? ` (retry #${round})` : ''}`);

        // 路由改写：把客户端的公开模型名替换为目标渠道的真实上游模型名
        if (target.model) body.model = target.model;

        const headers = new Headers();
        headers.set('Content-Type', 'application/json');
        headers.set('Authorization', `Bearer ${target.key}`);

        // Forward Accept header (important for streaming)
        const accept = request.headers.get('Accept');
        if (accept) headers.set('Accept', accept);
        applyOpencodeHeaders(headers, target.channel, request);

        const resp = await fetch(targetUrl, {
          method: 'POST',
          headers,
          body: JSON.stringify(body),
        });
        const rateHeaders = extractRateLimitHeaders(resp.headers);

        if (resp.status === 404) {
          lastError = `HTTP 404 (model not found)`;
          logError(store, target, model, 404, lastError);
          continue;
        }

        if (resp.status === 429) {
          consecutive429++;
          try { last429Body = await resp.text(); } catch { last429Body = ''; }
          lastError = `HTTP 429 (rate limited)`;
          const rlReason = await classifyAndRecord429(store, target, model, resp, rateHeaders);
          logError(store, target, model, 429, `${lastError}${rlReason ? `: ${rlReason}` : ''}`);
          await sleep(calc429Delay(rateHeaders, consecutive429));
          continue;
        }

        if (resp.ok || resp.status < 500) {
          if (!resp.ok) {
            let errBody = '';
            try { errBody = (await resp.text()).slice(0, 300); } catch {}
            logError(store, target, model, resp.status, errBody || `HTTP ${resp.status}`);
          }

          const respHeaders = new Headers();
          const ct = resp.headers.get('Content-Type');
          if (ct) respHeaders.set('Content-Type', ct);
          respHeaders.set('Access-Control-Allow-Origin', '*');

          // 上游返回 SSE 流时才走流式处理；某些上游在异常情况下即使收到
          // stream:true 也会返回普通 JSON（choices:null），此时走非流式验证路径
          const upstreamIsSSE = body.stream &&
            (ct || '').includes('text/event-stream');

          if (upstreamIsSSE) {
            // 禁用 nginx 缓冲（边缘 Node 运行时必须），否则代理会缓冲整个流导致 ECONNRESET
            respHeaders.set('Content-Type', 'text/event-stream');
            respHeaders.set('Cache-Control', 'no-cache');
            respHeaders.set('Connection', 'keep-alive');
            respHeaders.set('X-Accel-Buffering', 'no');

            // 处理流：修复 id 字段（参考 one-api 流式处理方案）；
            // 顺带识别 HTTP 200 流中夹带的 error 事件（如向非多模态模型发送图片）并记录日志
            const stream = processStream(resp.body, err => {
              const msg = (err && (err.message || err.code || err.type)) || 'upstream error';
              logError(store, target, model, 200, 'HTTP 200 stream error: ' + msg);
            });

            return new Response(stream, { status: resp.status, headers: respHeaders });
          }

          // 上游对 stream:true 返回了非 SSE 响应，回退到非流式验证
          if (body.stream) {
            console.warn(`[proxy] 上游对 stream:true 返回了非 SSE 响应 (Content-Type: ${ct}), 回退到非流式验证`);
          }

          // Non-streaming: validate chat/completions responses
          if (resp.ok && upstreamPath.includes('/chat/completions')) {
            const respText = await resp.text();
            try {
              const data = JSON.parse(respText);
              // 上游可能以 HTTP 200 返回错误（如向非多模态模型发送图片）：
              // 1) 带 error 字段 2) choices 不是数组 3) choices 为空数组
              if (data && data.error && typeof data.error === 'object') {
                const msg = (data.error.message || data.error.code || data.error.type) || 'upstream error';
                lastError = `HTTP 200 with error: ${msg}`;
                logError(store, target, model, 200, lastError);
                continue;
              }
              if (!Array.isArray(data.choices)) {
                lastError = `upstream returned invalid response (choices=${data.choices})`;
                logError(store, target, model, 200, lastError);
                continue;
              }
              if (data.choices.length === 0) {
                lastError = `upstream returned empty choices (likely error)`;
                logError(store, target, model, 200, lastError);
                continue;
              }
            } catch { /* not valid JSON — pass through as-is */ }
            return new Response(respText, { status: resp.status, headers: respHeaders });
          }

          return new Response(resp.body, { status: resp.status, headers: respHeaders });
        }

        lastError = `HTTP ${resp.status}`;
        logError(store, target, model, resp.status, lastError);
      } catch (err) {
        lastError = `network error: ${err.message}`;
        logError(store, target, model, 0, `${lastError}${targetUrl ? ` (${targetUrl})` : ''}`);
      }
    }

    // Only retry if all failures in this round were 429
    if (consecutive429 === 0 || consecutive429 < targets.length) break;
  }

  const detail = last429Body ? ` | upstream: ${last429Body.slice(0, 200)}` : '';
  return jsonRes({
    error: {
      message: `All targets failed. Last error: ${lastError}${detail}`,
      type: 'server_error',
    }
  }, 502);
}

async function handleModels(store, allowedChannelIds) {
  const channels = (await store.getChannels()) || [];
  const allowedSet = (allowedChannelIds && allowedChannelIds.length > 0)
    ? new Set(allowedChannelIds)
    : null;

  // 从渠道汇总公开模型名（model_map 的公开名 + models 里的同名透传）：
  // 仅统计启用 + 有 key + 且在客户端 key 允许范围内的渠道。
  const modelMap = new Map(); // 公开模型名 -> { id, owned_by }
  for (const c of channels) {
    if (c.enabled === false || enabledKeys(c).length === 0) continue;
    if (allowedSet && !allowedSet.has(c.id)) continue;
    const mm = (c.model_map && typeof c.model_map === 'object') ? c.model_map : {};
    for (const pub of Object.keys(mm)) {
      const p = String(pub || '').trim();
      const um = String(mm[pub] || '').trim();
      if (!p || !um) continue;
      if (!modelMap.has(p)) {
        modelMap.set(p, { id: p, owned_by: c.name || c.id });
      }
    }
    // 无显式映射的模型（同名透传）
    if (Array.isArray(c.models)) {
      for (const pub of c.models) {
        const p = String(pub || '').trim();
        if (!p) continue;
        if (!modelMap.has(p)) {
          modelMap.set(p, { id: p, owned_by: c.name || c.id });
        }
      }
    }
  }

  return jsonRes({
    object: 'list',
    data: Array.from(modelMap.values()).map(m => ({
      id: m.id,
      object: 'model',
      created: 0,
      owned_by: m.owned_by,
    })),
  });
}

function extractRateLimitHeaders(headers) {
  const intOrNull = (v) => {
    if (v == null) return null;
    const n = parseInt(v, 10);
    return Number.isFinite(n) ? n : null;
  };
  const info = {
    user_limit: intOrNull(headers.get('modelscope-ratelimit-requests-limit')),
    user_remaining: intOrNull(headers.get('modelscope-ratelimit-requests-remaining')),
    model_limit: intOrNull(headers.get('modelscope-ratelimit-model-requests-limit')),
    model_remaining: intOrNull(headers.get('modelscope-ratelimit-model-requests-remaining')),
    retry_after: intOrNull(headers.get('retry-after')),
  };
  info.hasAny =
    info.user_limit !== null ||
    info.user_remaining !== null ||
    info.model_limit !== null ||
    info.model_remaining !== null ||
    info.retry_after !== null;
  return info;
}

async function classifyAndRecord429(store, target, model, resp, rateHeaders) {
  let errText = '';
  try { errText = await resp.text(); } catch { errText = ''; }

  let code = '';
  let message = errText;
  try {
    const data = JSON.parse(errText || '{}');
    code = String(data?.error?.code || data?.errors?.code || '').toLowerCase();
    message = String(data?.error?.message || data?.errors?.message || errText || '');
  } catch {
    code = '';
  }

  const msg = message.toLowerCase();
  const modelRemaining = Number.isFinite(rateHeaders?.model_remaining) ? rateHeaders.model_remaining : null;
  const userRemaining = Number.isFinite(rateHeaders?.user_remaining) ? rateHeaders.user_remaining : null;

  if ((modelRemaining !== null && modelRemaining <= 0) || (userRemaining !== null && userRemaining <= 0)) {
    await store.markRateLimited(target.channel.id, target.key, model);
    return 'daily quota exhausted';
  }

  const isBurst = code.includes('limit_burst_rate') || msg.includes('increased too quickly');
  const isRequests = code.includes('limit_requests') || msg.includes('rate limit') || msg.includes('rate limited');
  const retryAfterMs = (Number.isFinite(rateHeaders?.retry_after) && rateHeaders.retry_after > 0)
    ? rateHeaders.retry_after * 1000
    : null;
  const cooldownMs = retryAfterMs || (isBurst ? 45 * 1000 : 90 * 1000);

  if (isBurst || isRequests) {
    await store.markRateLimitedTemporary(target.channel.id, target.key, model, cooldownMs);
    return `temporary cooldown ${Math.ceil(cooldownMs / 1000)}s`;
  }

  await store.markRateLimitedTemporary(target.channel.id, target.key, model, 90 * 1000);
  return 'temporary cooldown 90s';
}

/** 渠道对话接口路径：未配置协议接口时默认 /chat/completions */
function resolveChatPath(channel) {
  const trimmed = ((channel && channel.path) || '').trim();
  return trimmed ? (trimmed.startsWith('/') ? trimmed : '/' + trimmed) : '/chat/completions';
}

/** 判定上游是否为 opencode.ai：其 Go 服务要求 x-opencode-session 头与专属 UA，否则返回 400 MissingSessionID */
function isOpencodeChannel(channel) {
  return /opencode\.ai/i.test((channel && channel.base_url) || '');
}

/** opencode 会话 ID：优先透传客户端的 x-opencode-session，缺失时生成随机值 */
function resolveOpencodeSession(request) {
  try {
    const sid = request && request.headers.get('x-opencode-session');
    if (sid) return sid;
  } catch {}
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return 'gw-' + Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
}

/** 为 opencode 上游补齐专属请求头（会话 ID + 专属 UA）；非 opencode 上游原样返回 */
function applyOpencodeHeaders(headers, channel, request) {
  if (!isOpencodeChannel(channel)) return headers;
  headers.set('x-opencode-session', resolveOpencodeSession(request));
  headers.set('User-Agent', 'ai-gateway/1.0');
  return headers;
}

function jsonRes(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
    },
  });
}

function logError(store, target, model, status, message) {
  const ch = target.channel || {};
  const key = target.key || '';
  const hint = key.length > 12 ? key.slice(0, 7) + '...' + key.slice(-4) : key;
  store.appendError(ch.id || '', {
    channel_id: ch.id || '',
    channel_name: ch.name || '',
    base_url: ch.base_url || '',
    route_id: target.routeId || '',
    model,
    upstream_model: target.model || model,
    status,
    key_hint: hint,
    message: String(message).slice(0, 2000),
  }).catch(e => console.error('[errorlog] write failed:', e));
}

function responsesErrorRes(message, status = 500) {
  return new Response(JSON.stringify({
    id: 'resp_err_' + Date.now(),
    object: 'response',
    created_at: Math.floor(Date.now() / 1000),
    status: 'failed',
    error: {
      code: 'server_error',
      message,
    },
    output: [],
    usage: null,
  }), {
    status,
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
    },
  });
}

function claudeErrorRes(message, status = 500) {
  return new Response(JSON.stringify({
    type: 'error',
    error: {
      type: 'api_error',
      message,
    },
  }), {
    status,
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
    },
  });
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

/**
 * 429 退避策略：根据 retry-after 头或指数退避计算等待时间。
 * 边缘节点等共享 IP 环境下，上游(ModelScope)可能按 IP 限流，
 * 需要在 failover 循环中加入延迟避免连续请求全部被拒。
 */
function calc429Delay(rateHeaders, attempt) {
  if (rateHeaders?.retry_after > 0) {
    return rateHeaders.retry_after * 1000;
  }
  return Math.min(2000 * Math.pow(1.5, attempt), 15000);
}

/**
 * 处理 SSE 流。
 *
 * 功能：
 * 1. 修复上游返回 id: null 的问题（国内模型如 GLM 不遵循 OpenAI 规范）
 * 2. 过滤掉 choices 为 null 的无效 chunk（国内 API 常见异常）
 *
 * 返回修复后的 ReadableStream，可直接返回给客户端。
 */
function processStream(body, onError) {
  const dec = new TextDecoder();
  const enc = new TextEncoder();
  let buf = '';

  return body.pipeThrough(new TransformStream({
    transform(chunk, ctrl) {
      buf += dec.decode(chunk, { stream: true });
      const parts = buf.split('\n\n');
      buf = parts.pop() ?? '';

      for (const part of parts) {
        const trimmed = part.trim();
        if (trimmed.startsWith('data: ') && trimmed !== 'data: [DONE]') {
          try {
            const data = JSON.parse(trimmed.slice(6));
            // 上游在 HTTP 200 的流里返回 error（如向非多模态模型发送图片）：
            // 状态码虽是 2xx，但业务上是失败，需要顺带记录错误日志
            if (data && data.error && typeof data.error === 'object') {
              if (onError) onError(data.error);
              ctrl.enqueue(enc.encode(part + '\n\n'));
              continue;
            }
            // 过滤掉 choices 为 null 的无效 chunk（国内 API 常见异常）
            if ('choices' in data && !Array.isArray(data.choices)) {
              continue;
            }
            if (typeof data.id !== 'string') {
              data.id = data.id != null ? String(data.id) : ('chatcmpl-' + Date.now());
            }
            ctrl.enqueue(enc.encode('data: ' + JSON.stringify(data) + '\n\n'));
            continue;
          } catch { /* JSON 解析失败，原样透传 */ }
        }
        ctrl.enqueue(enc.encode(part + '\n\n'));
      }
    },
    flush(ctrl) {
      if (buf.trim()) ctrl.enqueue(enc.encode(buf));
    },
  }));
}
