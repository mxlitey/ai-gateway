# -*- coding: utf-8 -*-
"""对话转发与选号。

网关只对插件说 **OpenAI Chat Completions**，而 MiniMax 上游是 **Anthropic Messages**，
故本文件做两层协议转换：
  - 请求：OpenAI messages/tools → Anthropic `system` + `messages` + `tools`
  - 响应：Anthropic SSE 事件 → OpenAI `chat.completion.chunk`
非流式请求由插件把上游 SSE 聚合成一条完整响应。

转换只做**形状映射**，不改语义：
  - `tool` 消息 → user 消息里的 `tool_result` 块（Anthropic 没有 tool 角色）
  - assistant 的 `tool_calls` → `tool_use` 块
  - 历史里的 `reasoning_content` 丢弃（Anthropic 的 thinking 块需要签名）
"""

import json
import time
import uuid

import util
from upstream import UpstreamError


class NoAccountError(UpstreamError):
    def __init__(self, msg):
        super().__init__(503, msg)


def account_for_request(plugin, headers):
    account_id = (headers.get("X-Plugin-Account") or "").strip()
    if account_id:
        acc = plugin.store.get(account_id)
        if acc is not None:
            if not acc.usable():
                raise NoAccountError("账号 %s 已在插件侧停用" % acc.name())
            ensure_token(plugin, acc)
            return acc
    return pick_account(plugin)


def pick_account(plugin):
    for acc in plugin.store.list():
        if not acc.usable():
            continue
        try:
            ensure_token(plugin, acc)
        except Exception:
            continue
        return acc
    raise NoAccountError("没有可用账号：请先在插件控制台里完成 MiniMax 登录")


def ensure_token(plugin, acc):
    if acc.token_alive():
        return acc
    if not acc.refreshable():
        raise UpstreamError(401, "账号 %s 的令牌已过期且没有 refresh_token，请重新登录" % acc.name(),
                            disable=True)
    try:
        payload = plugin.upstream.refresh_token(acc.refresh_token)
    except UpstreamError as err:
        raise UpstreamError(401, "账号 %s 续期失败：%s" % (acc.name(), util.truncate_runes(err.msg, 70)),
                            disable=err.disable)
    acc.token = payload["access_token"]
    if payload.get("refresh_token"):
        acc.refresh_token = payload["refresh_token"]
    if payload.get("expires_at"):
        acc.expires_at = payload["expires_at"]
    if payload.get("scope"):
        acc.scope = payload["scope"]
    plugin.store.save_cred(_credential_of(acc))
    plugin.log("[token] %s 已续期" % acc.name())
    return acc


def _credential_of(acc):
    from accounts import Credential

    return Credential(
        uid=acc.uid, nickname=acc.nickname, token=acc.token,
        refresh_token=acc.refresh_token, expires_at=acc.expires_at,
        scope=acc.scope, source=acc.source,
    )


def forward_chat(plugin, raw, headers):
    """返回 (kind, payload)：json → (status, body)；stream → (生成器, 账号)。"""
    try:
        body = json.loads(raw.decode("utf-8"))
    except Exception:
        return "json", (400, {"error": "请求体不是合法 JSON"})
    if not isinstance(body, dict):
        return "json", (400, {"error": "请求体不是合法 JSON"})

    model = body.get("model")
    if not isinstance(model, str) or not model.strip():
        return "json", (400, {"error": "缺少 model"})
    want_stream = body.get("stream") is True

    try:
        acc = account_for_request(plugin, headers)
    except UpstreamError as err:
        plugin.note_error(err)
        return "json", (err.status, {"error": util.clamp_bytes(err.msg, 240)})

    payload = build_messages_payload(plugin, body, model.strip())
    try:
        resp = plugin.upstream.messages(acc.token, payload)
    except UpstreamError as err:
        plugin.note_error(err, acc)
        return "json", (err.status, {"error": util.clamp_bytes(err.msg, 240)})

    if want_stream:
        return "stream", (resp, acc, model.strip())

    try:
        merged, err = aggregate(resp, model.strip())
    finally:
        resp.close()
    if err is not None:
        plugin.note_error(UpstreamError(502, err), acc)
        return "json", (502, {"error": util.clamp_bytes(err, 240)})
    if not merged.get("choices"):
        return "json", (502, {"error": "上游未返回任何内容"})
    plugin.note_success(acc.uid)
    return "json", (200, merged)


# ---------- 请求转换 ----------


def build_messages_payload(plugin, body, model):
    """OpenAI 请求 → Anthropic Messages 请求。"""
    messages, system_parts = convert_messages(body.get("messages") or [])
    payload = {
        "model": model,
        "messages": messages,
        "stream": True,
        "max_tokens": _max_tokens(plugin, body),
    }
    if system_parts:
        payload["system"] = "\n\n".join(system_parts)
    temperature = body.get("temperature")
    if isinstance(temperature, (int, float)) and not isinstance(temperature, bool):
        payload["temperature"] = float(temperature)
    stop = body.get("stop")
    if isinstance(stop, str) and stop:
        payload["stop_sequences"] = [stop]
    elif isinstance(stop, list) and stop:
        payload["stop_sequences"] = [s for s in stop if isinstance(s, str) and s]

    tools = body.get("tools")
    if isinstance(tools, list) and tools:
        converted = []
        for tool in tools:
            if not isinstance(tool, dict):
                continue
            fn = tool.get("function")
            if not isinstance(fn, dict) or not util.as_str(fn.get("name")).strip():
                continue
            converted.append({
                "name": util.as_str(fn.get("name")).strip(),
                "description": util.as_str(fn.get("description")),
                "input_schema": fn.get("parameters") if isinstance(fn.get("parameters"), dict)
                else {"type": "object", "properties": {}},
            })
        if converted:
            payload["tools"] = converted

    effort = body.get("reasoning_effort")
    thinking = _thinking_for(model, effort)
    if thinking is not None:
        payload["thinking"] = thinking
        if isinstance(effort, str) and effort not in ("none", "off", "on"):
            payload["output_config"] = {"effort": effort}
    return payload


def _max_tokens(plugin, body):
    for key in ("max_completion_tokens", "max_tokens"):
        value = body.get(key)
        if isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0:
            return int(value)
    return int(plugin.eps.default_max_tokens)


def _thinking_for(model, effort):
    """思考形态：M3.1 必须 adaptive；M3 可开关；其余不发（服务端默认）。"""
    value = effort if isinstance(effort, str) else ""
    if value in ("none", "off"):
        if model.startswith("MiniMax-M3.1"):
            # M3.1 传 disabled 会硬 400，只能 adaptive
            return {"type": "adaptive"}
        return {"type": "disabled"}
    if model.startswith("MiniMax-M3.1") or value in ("on", "low", "medium", "high", "xhigh", "max"):
        return {"type": "adaptive"}
    return None


def convert_messages(messages):
    """返回 (Anthropic messages, system 文本片段)。"""
    system_parts = []
    out = []
    pending_results = []

    def flush_results():
        if pending_results:
            out.append({"role": "user", "content": list(pending_results)})
            pending_results.clear()

    for message in messages:
        if not isinstance(message, dict):
            continue
        role = util.as_str(message.get("role"))
        content = message.get("content")
        if role == "system":
            text = _content_text(content)
            if text:
                system_parts.append(text)
            continue
        if role == "tool":
            # 连续的 tool 消息合并进同一个 user 消息：Anthropic 不接受连续同角色消息
            pending_results.append({
                "type": "tool_result",
                "tool_use_id": util.as_str(message.get("tool_call_id")),
                "content": _content_text(content) or "(empty)",
            })
            continue
        flush_results()
        if role == "assistant":
            blocks = []
            text = _content_text(content)
            if text:
                blocks.append({"type": "text", "text": text})
            for call in message.get("tool_calls") or []:
                if not isinstance(call, dict):
                    continue
                fn = call.get("function") if isinstance(call.get("function"), dict) else {}
                blocks.append({
                    "type": "tool_use",
                    "id": util.as_str(call.get("id")) or ("call_" + uuid.uuid4().hex[:12]),
                    "name": util.as_str(fn.get("name")),
                    "input": _parse_args(fn.get("arguments")),
                })
            if blocks:
                out.append({"role": "assistant", "content": blocks})
            continue
        # user
        blocks = _user_blocks(content)
        if blocks:
            out.append({"role": "user", "content": blocks})
    flush_results()
    if not out:
        out.append({"role": "user", "content": [{"type": "text", "text": "你好"}]})
    return out, system_parts


def _content_text(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict):
                if block.get("type") in (None, "text") and isinstance(block.get("text"), str):
                    parts.append(block["text"])
                elif block.get("type") == "input_text" and isinstance(block.get("text"), str):
                    parts.append(block["text"])
        return "".join(parts)
    return ""


def _user_blocks(content):
    if isinstance(content, str):
        return [{"type": "text", "text": content}] if content else []
    if not isinstance(content, list):
        return []
    blocks = []
    for block in content:
        if not isinstance(block, dict):
            continue
        kind = block.get("type")
        if kind in (None, "text", "input_text"):
            text = block.get("text")
            if isinstance(text, str) and text:
                blocks.append({"type": "text", "text": text})
        elif kind == "image_url":
            image = block.get("image_url")
            url = util.as_str(image.get("url") if isinstance(image, dict) else image)
            decoded = _decode_data_url(url)
            if decoded is not None:
                blocks.append({"type": "image", "source": decoded})
    return blocks


def _decode_data_url(url):
    """`data:image/png;base64,xxx` → Anthropic 的 image.source（裸 base64）。"""
    text = (url or "").strip()
    if not text.startswith("data:") or ";base64," not in text:
        return None
    header, payload = text[5:].split(";base64,", 1)
    if not header.startswith("image/") or not payload.strip():
        return None
    return {"type": "base64", "media_type": header, "data": payload.strip()}


def _parse_args(raw):
    if isinstance(raw, dict):
        return raw
    if not isinstance(raw, str) or not raw.strip():
        return {}
    try:
        doc = json.loads(raw)
    except Exception:
        return {}
    return doc if isinstance(doc, dict) else {}


# ---------- 响应转换 ----------


def iterate_sse(resp):
    """逐行产出 SSE 的 (event, data) 二元组。"""
    event = ""
    buffer = b""
    while True:
        chunk = resp.read(4096)
        if not chunk:
            break
        buffer += chunk
        while b"\n" in buffer:
            raw_line, buffer = buffer.split(b"\n", 1)
            line = raw_line.decode("utf-8", "replace").rstrip("\r")
            if line == "":
                event = ""
                continue
            if line.startswith("event:"):
                event = line[6:].strip()
                continue
            if not line.startswith("data:"):
                continue
            data = line[5:].strip()
            if not data:
                continue
            yield event, data
    if buffer.strip():
        line = buffer.decode("utf-8", "replace").rstrip("\r")
        if line.startswith("data:"):
            yield event, line[5:].strip()


def aggregate(resp, model):
    """把 Anthropic SSE 聚合成 OpenAI 的 chat.completion。返回 (doc, error)。"""
    text_parts, reasoning_parts = [], []
    tool_calls = {}
    finish_reason = ""
    usage = None

    for event, data in iterate_sse(resp):
        try:
            frame = json.loads(data)
        except Exception:
            continue
        if not isinstance(frame, dict):
            continue
        kind = util.as_str(frame.get("type")) or event
        if kind == "error":
            err = frame.get("error")
            message = util.as_str(err.get("message") if isinstance(err, dict) else err)
            return {}, "MiniMax 流内错误：%s" % (message or "未知错误")
        if kind == "message_start":
            message = frame.get("message")
            if isinstance(message, dict) and isinstance(message.get("usage"), dict):
                usage = _usage(message["usage"], usage)
        elif kind == "content_block_start":
            block = frame.get("content_block")
            index = util.number_field(frame, "index")
            if isinstance(block, dict) and block.get("type") == "tool_use":
                tool_calls[index] = {
                    "id": util.as_str(block.get("id")) or ("call_" + uuid.uuid4().hex[:12]),
                    "type": "function",
                    "function": {"name": util.as_str(block.get("name")), "arguments": ""},
                }
        elif kind == "content_block_delta":
            delta = frame.get("delta")
            index = util.number_field(frame, "index")
            if not isinstance(delta, dict):
                continue
            dtype = delta.get("type")
            if dtype == "text_delta" and isinstance(delta.get("text"), str):
                text_parts.append(delta["text"])
            elif dtype == "thinking_delta" and isinstance(delta.get("thinking"), str):
                reasoning_parts.append(delta["thinking"])
            elif dtype == "input_json_delta" and isinstance(delta.get("partial_json"), str):
                slot = tool_calls.get(index)
                if slot is not None:
                    slot["function"]["arguments"] += delta["partial_json"]
        elif kind == "message_delta":
            delta = frame.get("delta")
            if isinstance(delta, dict):
                finish_reason = _finish_reason(delta.get("stop_reason"))
            if isinstance(frame.get("usage"), dict):
                usage = _usage(frame["usage"], usage)
        elif kind == "message_stop":
            break

    if not text_parts and not reasoning_parts and not tool_calls:
        return {}, "上游没有返回任何内容"

    message = {"role": "assistant", "content": "".join(text_parts)}
    if reasoning_parts:
        message["reasoning_content"] = "".join(reasoning_parts)
    if tool_calls:
        message["tool_calls"] = [tool_calls[k] for k in sorted(tool_calls)]
        if not message["content"]:
            message["content"] = None
        finish_reason = "tool_calls"
    out = {
        "id": "chatcmpl-" + uuid.uuid4().hex[:24],
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [{
            "index": 0,
            "message": message,
            "finish_reason": finish_reason or "stop",
        }],
    }
    if usage is not None:
        out["usage"] = usage
    return out, None


def _finish_reason(reason):
    text = util.as_str(reason)
    if text == "tool_use":
        return "tool_calls"
    if text == "max_tokens":
        return "length"
    return "stop"


def _usage(raw, previous):
    """Anthropic usage → OpenAI usage（input/output + 思考量）。"""
    out = dict(previous or {})
    prompt = raw.get("input_tokens")
    completion = raw.get("output_tokens")
    if isinstance(prompt, (int, float)) and not isinstance(prompt, bool):
        out["prompt_tokens"] = int(prompt)
        out["input_tokens"] = int(prompt)
    if isinstance(completion, (int, float)) and not isinstance(completion, bool):
        out["completion_tokens"] = int(completion)
        out["output_tokens"] = int(completion)
    out["total_tokens"] = int(out.get("prompt_tokens", 0)) + int(out.get("completion_tokens", 0))
    details = raw.get("output_tokens_details")
    if isinstance(details, dict):
        thinking = details.get("thinking_tokens")
        if isinstance(thinking, (int, float)) and not isinstance(thinking, bool):
            out["completion_tokens_details"] = {"reasoning_tokens": int(thinking)}
    return out


def stream_chunks(resp, model):
    """把 Anthropic SSE 逐事件转成 OpenAI 的 SSE data 行（不含 `data: ` 前缀）。"""
    chunk_id = "chatcmpl-" + uuid.uuid4().hex[:24]
    created = int(time.time())
    started = False
    tool_index = {}
    saw_content = False

    def head(delta, finish=None):
        return {
            "id": chunk_id,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
        }

    for event, data in iterate_sse(resp):
        try:
            frame = json.loads(data)
        except Exception:
            continue
        if not isinstance(frame, dict):
            continue
        kind = util.as_str(frame.get("type")) or event
        if kind == "error":
            err = frame.get("error")
            message = util.as_str(err.get("message") if isinstance(err, dict) else err)
            yield {"error": {"message": "MiniMax 流内错误：%s" % (message or "未知错误")}}
            return
        if kind == "message_start":
            if not started:
                started = True
                yield head({"role": "assistant", "content": ""})
            continue
        if kind == "content_block_start":
            block = frame.get("content_block")
            index = util.number_field(frame, "index")
            if isinstance(block, dict) and block.get("type") == "tool_use":
                tool_index[index] = len(tool_index)
                saw_content = True
                yield head({"tool_calls": [{
                    "index": tool_index[index],
                    "id": util.as_str(block.get("id")) or ("call_" + uuid.uuid4().hex[:12]),
                    "type": "function",
                    "function": {"name": util.as_str(block.get("name")), "arguments": ""},
                }]})
            continue
        if kind == "content_block_delta":
            delta = frame.get("delta")
            index = util.number_field(frame, "index")
            if not isinstance(delta, dict):
                continue
            dtype = delta.get("type")
            if dtype == "text_delta" and isinstance(delta.get("text"), str):
                saw_content = True
                yield head({"content": delta["text"]})
            elif dtype == "thinking_delta" and isinstance(delta.get("thinking"), str):
                saw_content = True
                yield head({"reasoning_content": delta["thinking"]})
            elif dtype == "input_json_delta" and isinstance(delta.get("partial_json"), str):
                slot = tool_index.get(index)
                if slot is not None:
                    saw_content = True
                    yield head({"tool_calls": [{
                        "index": slot,
                        "function": {"arguments": delta["partial_json"]},
                    }]})
            continue
        if kind == "message_delta":
            delta = frame.get("delta")
            reason = _finish_reason(delta.get("stop_reason") if isinstance(delta, dict) else "")
            frame_usage = frame.get("usage")
            tail = head({}, reason)
            if isinstance(frame_usage, dict):
                tail["usage"] = _usage(frame_usage, None)
            yield tail
            continue
        if kind == "message_stop":
            break

    if not saw_content:
        yield {"error": {"message": "MiniMax 没有返回任何内容"}}
        return
    yield "[DONE]"


def models_payload(entries):
    """OpenAI /v1/models 形状（只保留 id 与 owned_by）。"""
    data = []
    seen = set()
    for entry in entries:
        mid = util.as_str(entry.get("id") if isinstance(entry, dict) else entry).strip()
        if not mid or mid in seen:
            continue
        seen.add(mid)
        data.append({"id": mid, "object": "model", "owned_by": "minimax"})
    return {"object": "list", "data": data}
