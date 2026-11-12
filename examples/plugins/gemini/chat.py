# -*- coding: utf-8 -*-
"""对话转发：OpenAI ↔ Gemini Cloud Code Assist 信封。

请求侧（OpenAI → 上游信封）：
    contents（role 只有 user / model）、systemInstruction、tools.functionDeclarations、
    generationConfig（maxOutputTokens + thinkingConfig）
响应侧（上游 → OpenAI）：
    上游 SSE 的每帧是 `{"response": {candidates: [...], usageMetadata}}`，
    其中 `parts[].text`（`thought: true` 的进 reasoning）、`parts[].functionCall`。

⚠️ 信封必须**逐层字母序序列化**（见 util.sorted_dumps_bytes）：上游看到的是字节。
"""

import json
import time
import uuid

import util
from upstream import UpstreamError, canonical_model_id, derive_session_id, new_request_id, tier_for

SUPPORTED_MODEL = "gemini-3.8-flash"

# functionDeclaration.parameters 接受的键白名单（白名单外的键上游硬 400）
SCHEMA_KEYS = {
    "type", "format", "description", "nullable", "enum", "items", "minItems",
    "maxItems", "properties", "required", "minProperties", "maxProperties",
    "minLength", "maxLength", "pattern", "anyOf", "propertyOrdering",
    "minimum", "maximum",
}


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
    raise NoAccountError("没有可用账号：请先在插件控制台里粘贴 refresh_token")


def ensure_token(plugin, acc):
    if acc.token_alive():
        return acc
    if not acc.refreshable():
        raise UpstreamError(
            401, "账号 %s 的令牌已过期且没有 refresh_token，请重新登录" % acc.name(),
            disable=True)
    try:
        payload = plugin.upstream.refresh(acc.refresh_token)
    except UpstreamError as err:
        raise UpstreamError(
            401, "账号 %s 续期失败：%s" % (acc.name(), util.truncate_runes(err.msg, 70)),
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
        uid=acc.uid, nickname=acc.nickname, email=acc.email, token=acc.token,
        refresh_token=acc.refresh_token, expires_at=acc.expires_at,
        scope=acc.scope, project=acc.project, source=acc.source,
    )


def ensure_project(plugin, acc):
    """项目号：凭据里缓存过就用它，否则现探并回写。"""
    if (acc.project or "").strip():
        return acc.project.strip()
    project = plugin.upstream.probe_project(acc.token)
    acc.project = project
    try:
        plugin.store.save_cred(_credential_of(acc))
    except Exception as exc:
        plugin.log("[project] 回写项目号失败（不影响推理）：%s" % util.truncate_runes(str(exc), 60))
    return project


def forward_chat(plugin, raw, headers):
    try:
        body = json.loads(raw.decode("utf-8"))
    except Exception:
        return "json", (400, {"error": "请求体不是合法 JSON"})
    if not isinstance(body, dict):
        return "json", (400, {"error": "请求体不是合法 JSON"})

    model = body.get("model")
    if not isinstance(model, str) or not model.strip():
        return "json", (400, {"error": "缺少 model"})
    model = model.strip()
    if canonical_model_id(model) != SUPPORTED_MODEL:
        return "json", (400, {
            "error": "模型 %s 不在本插件目录中（仅支持 %s）" % (model, SUPPORTED_MODEL)})
    want_stream = body.get("stream") is True

    try:
        acc = account_for_request(plugin, headers)
        project = ensure_project(plugin, acc)
    except UpstreamError as err:
        plugin.note_error(err)
        return "json", (err.status, {"error": util.clamp_bytes(err.msg, 240)})

    try:
        envelope = build_envelope(plugin, body, project, model)
    except ValueError as exc:
        return "json", (400, {"error": str(exc)})

    try:
        resp = plugin.upstream.stream(acc.token, envelope)
    except UpstreamError as err:
        plugin.note_error(err, acc)
        return "json", (err.status, {"error": util.clamp_bytes(err.msg, 240)})

    if want_stream:
        return "stream", (resp, acc, model)

    try:
        merged, err = aggregate(resp, model)
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


def build_envelope(plugin, body, project, model):
    messages = body.get("messages") or []
    contents, system_parts = convert_messages(messages)
    if not contents:
        raise ValueError("messages 里没有可用内容")

    # 档位优先取模型名后缀（-low / -medium / -high / -tiered），否则看 reasoning_effort
    suffix = model[len(SUPPORTED_MODEL) + 1:] if model != SUPPORTED_MODEL else ""
    effort = suffix or (body.get("reasoning_effort")
                        if isinstance(body.get("reasoning_effort"), str) else None)
    tier, budget = tier_for(effort)
    generation = {
        "maxOutputTokens": _max_tokens(plugin, body),
        # 「关闭思考」是假关（关掉照样思考照样计费），故 includeThoughts 恒 True
        "thinkingConfig": {"includeThoughts": True},
    }
    if budget >= 0:
        generation["thinkingConfig"]["thinkingBudget"] = budget
    temperature = body.get("temperature")
    if isinstance(temperature, (int, float)) and not isinstance(temperature, bool):
        generation["temperature"] = float(temperature)

    request = {
        "contents": contents,
        "generationConfig": generation,
        "sessionId": derive_session_id(project, _first_user_text(contents)),
    }
    if system_parts:
        request["systemInstruction"] = {
            "role": "system",
            "parts": [{"text": "\n\n".join(system_parts)}],
        }
    tools = body.get("tools")
    if isinstance(tools, list) and tools:
        declarations = []
        for tool in tools:
            if not isinstance(tool, dict):
                continue
            fn = tool.get("function")
            if not isinstance(fn, dict) or not util.as_str(fn.get("name")).strip():
                continue
            declarations.append({
                "name": util.as_str(fn.get("name")).strip(),
                "description": util.as_str(fn.get("description")),
                "parameters": sanitize_schema(
                    fn.get("parameters") if isinstance(fn.get("parameters"), dict)
                    else {"type": "object", "properties": {}}),
            })
        if declarations:
            request["tools"] = [{"functionDeclarations": declarations}]
            mode = _tool_mode(body.get("tool_choice"))
            request["toolConfig"] = {"functionCallingConfig": {"mode": mode}}

    return {
        "model": "%s-%s" % (SUPPORTED_MODEL, tier),
        "project": project,
        "request": request,
        "requestId": new_request_id(),
        "userAgent": plugin.eps.upstream_user_agent,
    }


def _max_tokens(plugin, body):
    for key in ("max_completion_tokens", "max_tokens"):
        value = body.get(key)
        if isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0:
            return int(value)
    return int(plugin.eps.max_output_tokens)


def _tool_mode(tool_choice):
    if tool_choice == "none":
        return "NONE"
    if tool_choice in ("any", "required"):
        return "ANY"
    if isinstance(tool_choice, dict) and tool_choice.get("type") in ("any", "tool"):
        return "ANY"
    return "AUTO"


def convert_messages(messages):
    """返回 (contents, system 文本片段)。

    Gemini 只有 `user` / `model` 两种 role；工具结果必须回成
    `functionResponse` 部件（且 name 要与声明一致），不能当成普通 user 文本。
    """
    contents = []
    system_parts = []
    names = {}
    pending = []

    def flush():
        if pending:
            contents.append({"role": "user", "parts": list(pending)})
            pending.clear()

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
            text = _content_text(content)
            call_id = util.as_str(message.get("tool_call_id"))
            pending.append({"functionResponse": {
                "name": names.get(call_id) or "tool",
                "response": {"result": text or ""},
            }})
            continue
        flush()
        if role == "assistant":
            parts = _parts_of(content)
            for call in message.get("tool_calls") or []:
                if not isinstance(call, dict):
                    continue
                fn = call.get("function") if isinstance(call.get("function"), dict) else {}
                name = util.as_str(fn.get("name"))
                call_id = util.as_str(call.get("id"))
                if call_id and name:
                    names[call_id] = name
                parts.append({
                    "functionCall": {"name": name, "args": _parse_args(fn.get("arguments"))}})
            if parts:
                contents.append({"role": "model", "parts": parts})
            continue
        parts = _parts_of(content)
        if parts:
            contents.append({"role": "user", "parts": parts})
    flush()
    return contents, system_parts


def _parts_of(content):
    if isinstance(content, str):
        return [{"text": content}] if content else []
    if not isinstance(content, list):
        return []
    parts = []
    for block in content:
        if not isinstance(block, dict):
            continue
        kind = block.get("type")
        if kind in (None, "text", "input_text"):
            text = block.get("text")
            if isinstance(text, str) and text:
                parts.append({"text": text})
        elif kind == "image_url":
            image = block.get("image_url")
            url = util.as_str(image.get("url") if isinstance(image, dict) else image)
            decoded = _decode_data_url(url)
            if decoded is not None:
                parts.append({"inlineData": decoded})
    return parts


def _decode_data_url(url):
    text = (url or "").strip()
    if not text.startswith("data:") or ";base64," not in text:
        return None
    header, payload = text[5:].split(";base64,", 1)
    if not header.startswith("image/") or not payload.strip():
        return None
    return {"mimeType": header, "data": payload.strip()}


def _content_text(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(
            block.get("text", "") for block in content
            if isinstance(block, dict) and isinstance(block.get("text"), str))
    return ""


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


def _first_user_text(contents):
    for content in contents:
        if content.get("role") != "user":
            continue
        for part in content.get("parts") or []:
            text = part.get("text") if isinstance(part, dict) else None
            if isinstance(text, str) and text:
                return text
    return ""


def sanitize_schema(schema):
    """递归清洗：白名单外的键整键删除；enum 只留字符串。"""
    out = {}
    for key, value in schema.items():
        if key not in SCHEMA_KEYS:
            continue
        if key == "properties" and isinstance(value, dict):
            children = {}
            for name, child in value.items():
                if isinstance(child, dict):
                    children[name] = sanitize_schema(child)
            out["properties"] = children
            continue
        if key == "items":
            if isinstance(value, dict):
                out["items"] = sanitize_schema(value)
            elif isinstance(value, list):
                out["items"] = [sanitize_schema(item) for item in value
                                if isinstance(item, dict)]
            continue
        if key == "anyOf" and isinstance(value, list):
            out["anyOf"] = [sanitize_schema(item) for item in value if isinstance(item, dict)]
            continue
        if key == "type":
            if isinstance(value, str):
                out["type"] = value
            elif isinstance(value, list):
                types = [item for item in value if isinstance(item, str)]
                non_null = [item for item in types if item != "null"]
                if non_null:
                    out["type"] = non_null[0]
                if len(non_null) != len(types):
                    out["nullable"] = True
            continue
        if key == "enum":
            if isinstance(value, list) and all(isinstance(item, str) for item in value):
                out["enum"] = list(value)
            continue
        out[key] = value
    return out


# ---------- 响应转换 ----------


def iterate_sse(resp):
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
            if data and data != "[DONE]":
                yield event, data
    tail = buffer.decode("utf-8", "replace").strip()
    if tail.startswith("data:"):
        payload = tail[5:].strip()
        if payload and payload != "[DONE]":
            yield event, payload


def _unwrap(frame):
    """优先取 `{"response": {...}}` 信封，其次裸 Response。"""
    inner = frame.get("response")
    return inner if isinstance(inner, dict) else frame


def aggregate(resp, model):
    text_parts, reasoning_parts = [], []
    tool_calls = []
    finish_reason = ""
    usage = None

    for _event, data in iterate_sse(resp):
        try:
            frame = json.loads(data)
        except Exception:
            continue
        if not isinstance(frame, dict):
            continue
        inner = _unwrap(frame)
        if isinstance(inner.get("error"), dict):
            message = util.as_str(inner["error"].get("message")) or "未知错误"
            return {}, "Gemini 流内错误：%s" % message
        if isinstance(inner.get("usageMetadata"), dict):
            usage = _usage(inner["usageMetadata"], usage)
        candidates = inner.get("candidates")
        if not isinstance(candidates, list) or not candidates:
            continue
        candidate = candidates[0]
        if not isinstance(candidate, dict):
            continue
        reason = util.as_str(candidate.get("finishReason"))
        if reason:
            finish_reason = reason
        content = candidate.get("content")
        parts = content.get("parts") if isinstance(content, dict) else None
        for part in parts or []:
            if not isinstance(part, dict):
                continue
            call = part.get("functionCall")
            if isinstance(call, dict):
                tool_calls.append({
                    "id": "call_" + uuid.uuid4().hex[:24],
                    "type": "function",
                    "function": {
                        "name": util.as_str(call.get("name")),
                        "arguments": json.dumps(call.get("args") or {}, ensure_ascii=False),
                    },
                })
                continue
            text = part.get("text")
            if not isinstance(text, str) or not text:
                continue
            if part.get("thought") is True:
                reasoning_parts.append(text)
            else:
                text_parts.append(text)

    if not text_parts and not reasoning_parts and not tool_calls:
        return {}, "上游没有返回任何内容"

    message = {"role": "assistant", "content": "".join(text_parts)}
    if reasoning_parts:
        message["reasoning_content"] = "".join(reasoning_parts)
    if tool_calls:
        message["tool_calls"] = tool_calls
        if not message["content"]:
            message["content"] = None
    out = {
        "id": "chatcmpl-" + uuid.uuid4().hex[:24],
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [{
            "index": 0,
            "message": message,
            "finish_reason": _finish_reason(finish_reason, bool(tool_calls)),
        }],
    }
    if usage is not None:
        out["usage"] = usage
    return out, None


def _finish_reason(reason, has_tools):
    if has_tools:
        return "tool_calls"
    if reason == "MAX_TOKENS":
        return "length"
    if reason in ("SAFETY", "RECITATION", "PROHIBITED_CONTENT"):
        return "content_filter"
    return "stop"


def _usage(raw, previous):
    out = dict(previous or {})
    prompt = raw.get("promptTokenCount")
    completion = raw.get("candidatesTokenCount")
    thoughts = raw.get("thoughtsTokenCount")
    total = raw.get("totalTokenCount")
    if isinstance(prompt, (int, float)) and not isinstance(prompt, bool):
        out["prompt_tokens"] = int(prompt)
        out["input_tokens"] = int(prompt)
    if isinstance(completion, (int, float)) and not isinstance(completion, bool):
        out["completion_tokens"] = int(completion)
        out["output_tokens"] = int(completion)
    if isinstance(total, (int, float)) and not isinstance(total, bool):
        out["total_tokens"] = int(total)
    else:
        out["total_tokens"] = int(out.get("prompt_tokens", 0)) + int(
            out.get("completion_tokens", 0))
    if isinstance(thoughts, (int, float)) and not isinstance(thoughts, bool):
        out["completion_tokens_details"] = {"reasoning_tokens": int(thoughts)}
    return out


def stream_chunks(resp, model):
    """把上游 SSE 逐帧转成 OpenAI 的 SSE data 行（不含 `data: ` 前缀）。"""
    chunk_id = "chatcmpl-" + uuid.uuid4().hex[:24]
    created = int(time.time())
    started = False
    tool_index = 0
    saw_content = False
    finish_reason = ""

    def head(delta, finish=None):
        return {
            "id": chunk_id,
            "object": "chat.completion.chunk",
            "created": created,
            "model": model,
            "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
        }

    for _event, data in iterate_sse(resp):
        try:
            frame = json.loads(data)
        except Exception:
            continue
        if not isinstance(frame, dict):
            continue
        inner = _unwrap(frame)
        if isinstance(inner.get("error"), dict):
            message = util.as_str(inner["error"].get("message")) or "未知错误"
            yield {"error": {"message": "Gemini 流内错误：%s" % message}}
            return
        if not started:
            started = True
            yield head({"role": "assistant", "content": ""})
        candidates = inner.get("candidates")
        if isinstance(candidates, list) and candidates:
            candidate = candidates[0]
            if isinstance(candidate, dict):
                reason = util.as_str(candidate.get("finishReason"))
                if reason:
                    finish_reason = reason
                content = candidate.get("content")
                parts = content.get("parts") if isinstance(content, dict) else None
                for part in parts or []:
                    if not isinstance(part, dict):
                        continue
                    call = part.get("functionCall")
                    if isinstance(call, dict):
                        saw_content = True
                        yield head({"tool_calls": [{
                            "index": tool_index,
                            "id": "call_" + uuid.uuid4().hex[:24],
                            "type": "function",
                            "function": {
                                "name": util.as_str(call.get("name")),
                                "arguments": json.dumps(
                                    call.get("args") or {}, ensure_ascii=False),
                            },
                        }]})
                        tool_index += 1
                        continue
                    text = part.get("text")
                    if not isinstance(text, str) or not text:
                        continue
                    saw_content = True
                    if part.get("thought") is True:
                        yield head({"reasoning_content": text})
                    else:
                        yield head({"content": text})
        if isinstance(inner.get("usageMetadata"), dict):
            yield {
                "id": chunk_id,
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [],
                "usage": _usage(inner["usageMetadata"], None),
            }

    if not saw_content:
        yield {"error": {"message": "Gemini 没有返回任何内容"}}
        return
    yield head({}, _finish_reason(finish_reason, tool_index > 0))
    yield "[DONE]"


def models_payload(ids):
    data = []
    seen = set()
    for mid in ids:
        mid = util.as_str(mid).strip()
        if not mid or mid in seen:
            continue
        seen.add(mid)
        data.append({"id": mid, "object": "model", "owned_by": "google"})
    return {"object": "list", "data": data}
