# -*- coding: utf-8 -*-
"""对话转发与选号（LobsterAI 是标准 OpenAI 兼容 + 标准 SSE）。"""

import json

import net
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
    raise NoAccountError("没有可用账号：请先在插件控制台里点「LobsterAI 授权登录」")


def ensure_token(plugin, acc):
    if acc.token_alive():
        return acc
    if not acc.refreshable():
        raise UpstreamError(
            401, "账号 %s 的令牌已过期且没有 refresh_token，请重新登录" % acc.name(),
            disable=True)
    try:
        payload = plugin.upstream.refresh(
            acc.refresh_token, acc.uuid, acc.first_keyfrom, acc.latest_keyfrom, acc.user_id)
    except UpstreamError as err:
        raise UpstreamError(
            401, "账号 %s 续期失败：%s" % (acc.name(), util.truncate_runes(err.msg, 70)),
            disable=err.disable)
    if not payload["access_token"]:
        raise UpstreamError(401, "账号 %s 续期响应缺少令牌，请重新登录" % acc.name(), disable=True)
    acc.token = payload["access_token"]
    if payload["refresh_token"]:
        acc.refresh_token = payload["refresh_token"]
    if payload["expires_at"]:
        acc.expires_at = payload["expires_at"]
    # 续期体里要带「最近一次使用时间」，续期成功后立刻推进
    acc.latest_keyfrom = str(util.now_millis())
    plugin.store.save_cred(_credential_of(acc))
    plugin.log("[token] %s 已续期" % acc.name())
    return acc


def _credential_of(acc):
    from accounts import Credential

    return Credential(
        uid=acc.uid, nickname=acc.nickname, phone=acc.phone, token=acc.token,
        refresh_token=acc.refresh_token, expires_at=acc.expires_at,
        uuid=acc.uuid, first_keyfrom=acc.first_keyfrom,
        latest_keyfrom=acc.latest_keyfrom, user_id=acc.user_id, source=acc.source,
    )


def forward_chat(plugin, raw, headers):
    try:
        body = json.loads(raw.decode("utf-8"))
    except Exception:
        return "json", (400, {"error": "请求体不是合法 JSON"})
    if not isinstance(body, dict):
        return "json", (400, {"error": "请求体不是合法 JSON"})

    want_stream = body.get("stream") is True
    model = body.get("model")
    if not isinstance(model, str) or not model.strip():
        return "json", (400, {"error": "缺少 model"})

    try:
        acc = account_for_request(plugin, headers)
    except UpstreamError as err:
        plugin.note_error(err)
        return "json", (err.status, {"error": util.clamp_bytes(err.msg, 240)})

    try:
        resp = plugin.upstream.chat(acc.token, body)
    except UpstreamError as err:
        plugin.note_error(err, acc)
        return "json", (err.status, {"error": util.clamp_bytes(err.msg, 240)})

    if want_stream and resp.is_event_stream():
        return "stream", (resp, acc)

    try:
        raw_body = net.read_all(resp)
        status = resp.status
        ctype = resp.content_type()
    finally:
        resp.close()

    if status >= 400:
        return "json", (status, {"error": util.clamp_bytes(_error_text(raw_body), 240)})

    if "text/event-stream" in ctype.lower():
        merged, err = aggregate_sse(raw_body)
        if err is not None:
            plugin.note_error(UpstreamError(502, err), acc)
            return "json", (502, {"error": util.clamp_bytes(err, 240)})
        plugin.note_success(acc.uid)
        return "json", (200, merged)

    doc = net.parse_json(raw_body)
    if not isinstance(doc, dict):
        return "json", (502, {"error": "上游返回了无法解析的响应体"})
    if doc.get("error"):
        return "json", (502, {"error": util.clamp_bytes(_error_from_doc(doc), 240)})
    if not doc.get("choices"):
        return "json", (502, {"error": "上游未返回任何内容"})
    plugin.note_success(acc.uid)
    return "json", (200, doc)


def aggregate_sse(raw):
    text = raw.decode("utf-8", "replace")
    chunk_id, model, created = "", "", 0
    content_parts, reasoning_parts, tool_calls = [], [], {}
    finish_reason, usage = "", None

    for line in text.split("\n"):
        line = line.strip()
        if not line.startswith("data:"):
            continue
        payload = line[len("data:"):].strip()
        if not payload or payload == "[DONE]":
            continue
        try:
            chunk = json.loads(payload)
        except Exception:
            continue
        if not isinstance(chunk, dict):
            continue
        if chunk.get("error"):
            return None, _error_from_doc(chunk)
        chunk_id = chunk.get("id") or chunk_id
        model = chunk.get("model") or model
        if isinstance(chunk.get("created"), (int, float)) and chunk["created"] > 0:
            created = int(chunk["created"])
        if isinstance(chunk.get("usage"), dict):
            usage = chunk["usage"]
        for choice in chunk.get("choices") or []:
            if not isinstance(choice, dict):
                continue
            delta = choice.get("delta")
            if isinstance(delta, dict):
                if delta.get("content"):
                    content_parts.append(delta["content"])
                reasoning = delta.get("reasoning_content") or delta.get("reasoning")
                if isinstance(reasoning, str) and reasoning:
                    reasoning_parts.append(reasoning)
                for call in delta.get("tool_calls") or []:
                    if not isinstance(call, dict):
                        continue
                    index = call.get("index", len(tool_calls))
                    slot = tool_calls.setdefault(index, {
                        "id": "", "type": "function",
                        "function": {"name": "", "arguments": ""},
                    })
                    if call.get("id"):
                        slot["id"] = call["id"]
                    fn = call.get("function") or {}
                    if isinstance(fn, dict):
                        if fn.get("name"):
                            slot["function"]["name"] = fn["name"]
                        if fn.get("arguments"):
                            slot["function"]["arguments"] += fn["arguments"]
            if choice.get("finish_reason"):
                finish_reason = choice["finish_reason"]

    if not chunk_id and created == 0:
        return None, "上游没有返回任何 chunk"

    message = {"role": "assistant", "content": "".join(content_parts)}
    if reasoning_parts:
        message["reasoning_content"] = "".join(reasoning_parts)
    if tool_calls:
        message["tool_calls"] = [tool_calls[k] for k in sorted(tool_calls)]
        if not message["content"]:
            message["content"] = None
    if finish_reason == "" and tool_calls:
        finish_reason = "tool_calls"

    out = {
        "id": chunk_id,
        "object": "chat.completion",
        "created": created,
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


def models_payload(ids):
    data = []
    seen = set()
    for mid in ids:
        mid = util.as_str(mid).strip()
        if not mid or mid in seen:
            continue
        seen.add(mid)
        data.append({"id": mid, "object": "model", "owned_by": "lobsterai"})
    return {"object": "list", "data": data}


def _error_text(raw):
    doc = net.parse_json(raw)
    if isinstance(doc, dict):
        return _error_from_doc(doc)
    return util.truncate_runes((raw or b"").decode("utf-8", "replace"), 200) or "上游返回错误"


def _error_from_doc(doc):
    err = doc.get("error") if isinstance(doc, dict) else None
    if isinstance(err, dict):
        for key in ("message", "msg", "type", "code"):
            value = err.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    if isinstance(err, str) and err.strip():
        return err.strip()
    for key in ("msg", "message", "error_description"):
        value = doc.get(key) if isinstance(doc, dict) else None
        if isinstance(value, str) and value.strip():
            return value.strip()
    return "上游返回错误"
