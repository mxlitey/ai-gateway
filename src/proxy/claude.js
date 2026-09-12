/**
 * Claude Messages API <-> OpenAI Chat Completions format adapter.
 *
 * Request:  Claude -> OpenAI  (claudeToOpenAI)
 * Response: OpenAI -> Claude  (openAIToClaude / openAIStreamToClaudeStream)
 */

import { rid } from './utils.js';

// ─── Request conversion ────────────────────────────────────────────

/**
 * Convert a Claude Messages API request body to OpenAI Chat Completions format.
 */
export function claudeToOpenAI(claude) {
  const openai = {
    model: claude.model,
    messages: convertMessages(claude.system, claude.messages),
    stream: claude.stream || false,
  };

  if (claude.max_tokens !== undefined) openai.max_tokens = claude.max_tokens;
  if (claude.temperature !== undefined) openai.temperature = claude.temperature;
  if (claude.top_p !== undefined) openai.top_p = claude.top_p;
  if (claude.stop_sequences !== undefined) openai.stop = claude.stop_sequences;

  if (claude.tools?.length > 0) {
    openai.tools = claude.tools.map(t => ({
      type: 'function',
      function: {
        name: t.name,
        description: t.description || '',
        parameters: cleanSchema(t.input_schema),
      },
    }));
  }

  return openai;
}

function convertMessages(system, claudeMessages) {
  const msgs = [];

  // Claude: system is a top-level param; OpenAI: it's a message with role "system"
  if (system) {
    if (typeof system === 'string') {
      msgs.push({ role: 'system', content: system });
    } else if (Array.isArray(system)) {
      const text = system.map(b => b.text || '').join('\n');
      if (text) msgs.push({ role: 'system', content: text });
    }
  }

  for (const msg of claudeMessages) {
    // Simple string content
    if (typeof msg.content === 'string') {
      msgs.push({ role: msg.role === 'assistant' ? 'assistant' : 'user', content: msg.content });
      continue;
    }

    // Array of content blocks
    const contentParts = [];  // OpenAI multimodal content parts
    const toolCalls = [];
    const toolResults = [];

    for (const block of msg.content) {
      switch (block.type) {
        case 'text':
          contentParts.push({ type: 'text', text: block.text });
          break;
        case 'image':
          // Claude base64 image → OpenAI image_url data URI
          if (block.source?.type === 'base64' && block.source.data) {
            const mime = block.source.media_type || 'image/png';
            contentParts.push({
              type: 'image_url',
              image_url: { url: `data:${mime};base64,${block.source.data}` },
            });
          } else if (block.source?.type === 'url' && block.source.url) {
            contentParts.push({
              type: 'image_url',
              image_url: { url: block.source.url },
            });
          }
          break;
        case 'tool_use':
          toolCalls.push({
            id: block.id,
            type: 'function',
            function: {
              name: block.name,
              arguments: JSON.stringify(block.input),
            },
          });
          break;
        case 'tool_result':
          toolResults.push({
            tool_call_id: block.tool_use_id,
            content: typeof block.content === 'string'
              ? block.content
              : JSON.stringify(block.content),
          });
          break;
      }
    }

    // Build content value: use simple string when text-only, array when multimodal
    const hasImages = contentParts.some(p => p.type === 'image_url');
    const textOnly = contentParts.filter(p => p.type === 'text');
    const contentValue = hasImages
      ? contentParts
      : textOnly.length > 0
        ? textOnly.map(p => p.text).join('\n')
        : null;

    // Tool results must come BEFORE text when in the same user message,
    // because OpenAI requires tool responses immediately after the
    // assistant's tool_calls — no other messages in between.
    if (toolResults.length > 0) {
      for (const tr of toolResults) {
        msgs.push({ role: 'tool', tool_call_id: tr.tool_call_id, content: tr.content });
      }
      if (contentValue) {
        msgs.push({ role: 'user', content: contentValue });
      }
    } else if (contentValue || toolCalls.length > 0) {
      const m = { role: msg.role === 'assistant' ? 'assistant' : 'user' };
      if (contentValue) m.content = contentValue;
      if (toolCalls.length > 0) m.tool_calls = toolCalls;
      msgs.push(m);
    }
  }

  return msgs;
}

// ─── Non-streaming response conversion ─────────────────────────────

/**
 * Convert an OpenAI Chat Completions JSON response to Claude Messages format.
 */
export function openAIToClaude(openai, model) {
  const res = {
    id: 'msg_' + rid(),
    type: 'message',
    role: 'assistant',
    model: model || openai.model || 'unknown',
    content: [],
    stop_reason: null,
    stop_sequence: null,
    usage: { input_tokens: 0, output_tokens: 0 },
  };

  if (openai.choices?.length > 0) {
    const choice = openai.choices[0];
    const message = choice.message;

    if (message.content) {
      res.content.push({ type: 'text', text: message.content });
    }

    if (message.tool_calls) {
      for (const tc of message.tool_calls) {
        res.content.push({
          type: 'tool_use',
          id: tc.id,
          name: tc.function.name,
          input: safeParse(tc.function.arguments),
        });
      }
      res.stop_reason = 'tool_use';
    } else if (choice.finish_reason === 'length') {
      res.stop_reason = 'max_tokens';
    } else {
      res.stop_reason = 'end_turn';
    }
  }

  if (openai.usage) {
    res.usage = {
      input_tokens: openai.usage.prompt_tokens || 0,
      output_tokens: openai.usage.completion_tokens || 0,
    };
  }

  return res;
}

// ─── Streaming response conversion ─────────────────────────────────

/**
 * Transform an OpenAI SSE stream (Response.body) into a Claude Messages SSE stream.
 * Returns a new ReadableStream.
 */
export function openAIStreamToClaudeStream(body, model) {
  const enc = new TextEncoder();
  const dec = new TextDecoder();

  let buffer = '';
  let textBlockOpen = false;
  let textBlockIdx = -1;
  let nextBlockIdx = 0;
  const toolBlockMap = {};     // OpenAI tc.index -> Claude block index
  const toolCallStarted = {};  // OpenAI tc.index -> true (block_start sent)

  function send(ctrl, event, data) {
    ctrl.enqueue(enc.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`));
  }

  return new ReadableStream({
    async start(ctrl) {
      const reader = body.getReader();

      // message_start
      send(ctrl, 'message_start', {
        type: 'message_start',
        message: {
          id: 'msg_' + rid(),
          type: 'message',
          role: 'assistant',
          model: model || 'unknown',
          content: [],
          stop_reason: null,
          stop_sequence: null,
          usage: { input_tokens: 0, output_tokens: 0 },
        },
      });

      try {
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;

          const chunk = buffer + dec.decode(value, { stream: true });
          const lines = chunk.split('\n');
          buffer = lines.pop() || '';

          for (const line of lines) {
            const trimmed = line.trim();
            if (!trimmed || !trimmed.startsWith('data: ')) continue;
            const jsonStr = trimmed.slice(6);
            if (jsonStr === '[DONE]') continue;

            let data;
            try { data = JSON.parse(jsonStr); } catch { continue; }
            if (!data.choices || data.choices.length === 0) continue;

            const choice = data.choices[0];
            const delta = choice.delta || {};

            // ── Text content ──
            if (delta.content) {
              if (!textBlockOpen) {
                textBlockIdx = nextBlockIdx++;
                textBlockOpen = true;
                send(ctrl, 'content_block_start', {
                  type: 'content_block_start',
                  index: textBlockIdx,
                  content_block: { type: 'text', text: '' },
                });
              }
              send(ctrl, 'content_block_delta', {
                type: 'content_block_delta',
                index: textBlockIdx,
                delta: { type: 'text_delta', text: delta.content },
              });
            }

            // ── Tool calls ──
            if (delta.tool_calls) {
              // Close text block before tool blocks
              if (textBlockOpen) {
                send(ctrl, 'content_block_stop', { type: 'content_block_stop', index: textBlockIdx });
                textBlockOpen = false;
              }

              for (const tc of delta.tool_calls) {
                const tcIdx = tc.index ?? 0;

                if (tc.id && tc.function?.name) {
                  // New tool call — assign a Claude block index
                  const bi = nextBlockIdx++;
                  toolBlockMap[tcIdx] = bi;
                  toolCallStarted[tcIdx] = true;

                  send(ctrl, 'content_block_start', {
                    type: 'content_block_start',
                    index: bi,
                    content_block: { type: 'tool_use', id: tc.id, name: tc.function.name, input: {} },
                  });

                  // First chunk may already contain partial arguments
                  if (tc.function.arguments) {
                    send(ctrl, 'content_block_delta', {
                      type: 'content_block_delta',
                      index: bi,
                      delta: { type: 'input_json_delta', partial_json: tc.function.arguments },
                    });
                  }
                } else if (tc.function?.arguments && toolBlockMap[tcIdx] !== undefined) {
                  // Continuation of existing tool call arguments
                  send(ctrl, 'content_block_delta', {
                    type: 'content_block_delta',
                    index: toolBlockMap[tcIdx],
                    delta: { type: 'input_json_delta', partial_json: tc.function.arguments },
                  });
                }
              }
            }

            // ── Finish ──
            if (choice.finish_reason) {
              // Close text block
              if (textBlockOpen) {
                send(ctrl, 'content_block_stop', { type: 'content_block_stop', index: textBlockIdx });
                textBlockOpen = false;
              }
              // Close all tool blocks
              for (const tcIdx in toolBlockMap) {
                send(ctrl, 'content_block_stop', { type: 'content_block_stop', index: toolBlockMap[tcIdx] });
              }

              let stopReason = 'end_turn';
              if (choice.finish_reason === 'tool_calls') stopReason = 'tool_use';
              else if (choice.finish_reason === 'length') stopReason = 'max_tokens';

              send(ctrl, 'message_delta', {
                type: 'message_delta',
                delta: { stop_reason: stopReason },
                usage: { output_tokens: 0 },
              });
            }
          }
        }
      } finally {
        reader.releaseLock();
        send(ctrl, 'message_stop', { type: 'message_stop' });
        ctrl.close();
      }
    },
  });
}

// ─── Helpers ────────────────────────────────────────────────────────

function safeParse(s) {
  try { return JSON.parse(s); } catch { return {}; }
}

function cleanSchema(schema) {
  if (!schema || typeof schema !== 'object') return schema;
  const out = { ...schema };
  for (const k of ['$schema', 'additionalProperties', 'title', 'examples', 'default']) {
    delete out[k];
  }
  if (out.type === 'string') delete out.format;
  for (const k in out) {
    if (typeof out[k] === 'object' && !Array.isArray(out[k])) {
      out[k] = cleanSchema(out[k]);
    }
  }
  return out;
}
