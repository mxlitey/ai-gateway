import { verifyApiKey } from './auth.js';
import { LoadBalancer, enabledKeys } from '../lb/balancer.js';
import { applyChannelHeaders, applyPassthroughHeaders } from './headers.js';
import { claudeToOpenAI, openAIToClaude, openAIStreamToClaudeStream } from './claude.js';
import { rid, resolveChatPath } from './utils.js';

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

  // ---- OpenAI-compatible passthrough ----
  return handleOpenAIProxy(request, url, path, body, store, allowedChannelIds);
}

// 转发循环中「本条上游响应无效，继续尝试下一个目标」的信号
const RETRY = Symbol('retry');
function retryNext(message) {
  return { [RETRY]: message };
}

/**
 * 校验上游返回的 chat/completions 结果是否有效：HTTP 200 也可能内嵌错误
 * （如向非多模态模型发送图片）或返回空 choices。无效时记录日志并返回重试信号。
 * @returns 无效时的 retryNext(...) 对象；有效时返回 null。
 */
function validateUpstreamResult(data, store, target, model) {
  if (data && data.error && typeof data.error === 'object') {
    const msg = (data.error.message || data.error.code || data.error.type) || 'upstream error';
    const le = `HTTP 200 with error: ${msg}`;
    logError(store, target, model, 200, le);
    return retryNext(le);
  }
  if (!Array.isArray(data.choices)) {
    const le = `upstream returned invalid response (choices=${data.choices})`;
    logError(store, target, model, 200, le);
    return retryNext(le);
  }
  if (data.choices.length === 0) {
    const le = `upstream returned empty choices (likely error)`;
    logError(store, target, model, 200, le);
    return retryNext(le);
  }
  return null;
}

/**
 * 公共转发 + 故障转移循环（Claude / OpenAI 透传两种协议共用）。
 *
 * 统一处理：404 跳过、429 立即换下一个目标、5xx 与网络错误跳过。
 * 不做限流记忆、不等待、不重试整轮：任一目标不可用就立刻尝试下一个，
 * 全部失败即返回错误（由 onAllFailed 生成）。协议差异通过回调注入：
 * - buildPath(target)                ：目标请求路径
 * - applyModel(target)               ：改写请求体里的模型名
 * - onUpstreamResponse(ctx)          ：上游有响应（ok 或 4xx）时的协议相关处理，
 *                                      返回 Response；返回 retryNext(msg) 则继续下一个目标
 * - onAllFailed({lastError,last429Body})：全部失败时生成协议相关的错误响应
 *
 * @returns {Promise<Response>}
 */
async function forwardWithFailover(ctx) {
  let lastError = null;
  let last429Body = '';

  for (const target of ctx.targets) {
    let targetUrl = '';
    try {
      const baseUrl = target.channel.base_url.replace(/\/+$/, '');
      targetUrl = baseUrl + ctx.buildPath(target) + ctx.url.search;

      console.log(`${ctx.logPrefix} -> ${target.channel.name} ${targetUrl}`);

      // 路由改写：把客户端的公开模型名替换为目标渠道的真实上游模型名
      ctx.applyModel(target);

      const headers = new Headers();
      applyPassthroughHeaders(headers, ctx.request);
      headers.set('Content-Type', 'application/json');
      headers.set('Authorization', `Bearer ${target.key}`);
      if (ctx.isStream && ctx.acceptStreamHeader) headers.set('Accept', 'text/event-stream');
      applyChannelHeaders(headers, target.channel, ctx.request);

      const resp = await fetch(targetUrl, {
        method: 'POST',
        headers,
        body: JSON.stringify(ctx.body),
      });

      if (resp.status === 404) {
        lastError = `HTTP 404 (model not found)`;
        logError(ctx.store, target, ctx.model, 404, lastError);
        continue;
      }

      if (resp.status === 429) {
        try { last429Body = await resp.text(); } catch { last429Body = ''; }
        lastError = `HTTP 429 (rate limited)`;
        logError(ctx.store, target, ctx.model, 429, lastError);
        continue;
      }

      if (resp.ok || resp.status < 500) {
        const out = await ctx.onUpstreamResponse({ resp, target });
        if (out && typeof out === 'object' && RETRY in out) {
          lastError = out[RETRY];
          continue;
        }
        return out;
      }

      lastError = `HTTP ${resp.status}`;
      logError(ctx.store, target, ctx.model, resp.status, lastError);
    } catch (err) {
      lastError = `network error: ${err.message}`;
      logError(ctx.store, target, ctx.model, 0, `${lastError}${targetUrl ? ` (${targetUrl})` : ''}`);
    }
  }

  return ctx.onAllFailed({ lastError, last429Body });
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

  return forwardWithFailover({
    store, request, url, targets, model,
    body: openaiBody,
    isStream,
    acceptStreamHeader: true,
    logPrefix: '[proxy][claude]',
    buildPath: (target) => resolveChatPath(target.channel),
    applyModel: (target) => { openaiBody.model = target.model; },
    onUpstreamResponse: async ({ resp, target }) => {
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
      const invalid = validateUpstreamResult(openaiData, store, target, model);
      if (invalid) return invalid;

      // 非流式：转换回 Claude 格式
      const claudeResponse = openAIToClaude(openaiData, model);
      return jsonRes(claudeResponse, 200);
    },
    onAllFailed: ({ lastError, last429Body }) => {
      const detail = last429Body ? ` | upstream: ${last429Body.slice(0, 200)}` : '';
      return claudeErrorRes(`All targets failed. Last error: ${lastError}${detail}`, 502);
    },
  });
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

  return forwardWithFailover({
    store, request, url, targets, model,
    body,
    isStream: !!body.stream,
    // OpenAI 透传不注入 Accept 头
    acceptStreamHeader: false,
    logPrefix: '[proxy]',
    // 对话接口可被渠道协议接口覆盖（如 /chat/completions）；其他端点（embeddings 等）沿用请求路径
    buildPath: (target) => upstreamPath.includes('/chat/completions') ? resolveChatPath(target.channel) : upstreamPath,
    applyModel: (target) => { if (target.model) body.model = target.model; },
    onUpstreamResponse: async ({ resp, target }) => {
      const ct = resp.headers.get('Content-Type');
      if (!resp.ok) {
        let errBody = '';
        try { errBody = (await resp.clone().text()).slice(0, 300); } catch {}
        logError(store, target, model, resp.status, errBody || `HTTP ${resp.status}`);
      }

      const respHeaders = new Headers();
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
          const invalid = validateUpstreamResult(data, store, target, model);
          if (invalid) return invalid;
        } catch { /* not valid JSON — pass through as-is */ }
        return new Response(respText, { status: resp.status, headers: respHeaders });
      }

      return new Response(resp.body, { status: resp.status, headers: respHeaders });
    },
    onAllFailed: ({ lastError, last429Body }) => {
      const detail = last429Body ? ` | upstream: ${last429Body.slice(0, 200)}` : '';
      return jsonRes({
        error: {
          message: `All targets failed. Last error: ${lastError}${detail}`,
          type: 'server_error',
        }
      }, 502);
    },
  });
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
    model,
    upstream_model: target.model || model,
    status,
    key_hint: hint,
    message: String(message).slice(0, 2000),
  }).catch(e => console.error('[errorlog] write failed:', e));
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

/**
 * 单条 SSE 事件的最小修补：仅在 id 非字符串时补一个（国内模型如 GLM 会返回 id: null），
 * 顺带记录流里夹带的 error 事件（如向非多模态模型发送图片）。
 * 无法识别的内容一律原样返回，不做「猜属性丢包」，避免误丢 usage 等信息。
 */
function rewriteSseEvent(part, streamId, onError) {
  const trimmed = part.trim();
  if (!trimmed.startsWith('data: ') || trimmed === 'data: [DONE]') return part;

  let data;
  try { data = JSON.parse(trimmed.slice(6)); } catch { return part; }
  if (!data || typeof data !== 'object') return part;

  // 上游在 HTTP 200 的流里返回 error：状态码虽是 2xx，但业务上是失败，顺带记录日志
  if (data.error && typeof data.error === 'object') {
    if (onError) onError(data.error);
    return part;
  }

  // id 已是字符串则无需修补，原样透传
  if (typeof data.id === 'string') return part;

  data.id = streamId;
  return 'data: ' + JSON.stringify(data);
}

/**
 * 处理 SSE 流。
 *
 * 只做「最小必要修补」，其余内容原样透传：
 * 1. 兼容 \n\n 与 \r\n\r\n 两种事件分隔符（部分上游使用 Windows 风格换行）
 * 2. 仅在 id 非字符串时补一个，同一次回复内复用同一个 id，保证多包 id 一致
 *
 * 返回修补后的 ReadableStream，可直接返回给客户端。
 */
function processStream(body, onError) {
  const dec = new TextDecoder();
  const enc = new TextEncoder();
  const streamId = 'chatcmpl-' + rid();
  let buf = '';

  return body.pipeThrough(new TransformStream({
    transform(chunk, ctrl) {
      // SSE 里裸 \r\n 只会作为行分隔符出现（JSON 字符串内的换行必被转义为 \r \n 两个字符），
      // 故可安全地归一化为 \n，从而兼容 Windows 风格换行的上游
      buf = (buf + dec.decode(chunk, { stream: true })).replace(/\r\n/g, '\n');
      const parts = buf.split('\n\n');
      buf = parts.pop() ?? '';
      for (const part of parts) {
        ctrl.enqueue(enc.encode(rewriteSseEvent(part, streamId, onError) + '\n\n'));
      }
    },
    flush(ctrl) {
      if (buf.trim()) ctrl.enqueue(enc.encode(buf));
    },
  }));
}
