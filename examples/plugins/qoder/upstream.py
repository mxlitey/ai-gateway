"""Qoder 上游数据面：请求构造、COSY 签名、SSE 信封解包。

协议实现参考 https://github.com/shuishuipingan/qoder2api-hub ：
  - 以官方 baseprompt.json 为请求骨架
  - COSY 签名头由 qoder_sign.CosySession 生成
  - 请求体经 qoder_encode 自定义 Base64 变体编码后发送
  - 上游返回 {"headers","body","statusCodeValue"} 嵌套 SSE 信封
"""

import copy
import json
import os
import re
import time
import urllib.error
import urllib.request
import uuid

import qoder_accounts
import qoder_catalog
import qoder_sign

BASE_DIR = os.path.dirname(os.path.abspath(__file__))

with open(os.path.join(BASE_DIR, "baseprompt.json"), encoding="utf-8") as _fh:
    BASE_PROMPT = json.load(_fh)

CHAT_PATH = ("/algo/api/v2/service/pro/sse/agent_chat_generation"
             "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1")

# 模型清单：官方桌面版选择器同源。响应是明文 JSON（不是 qoder_encode 的自定义编码），
# 结构为 {"chat":[...], "app":[...]}，条目字段与本地快照的 catalog 完全同构。
MODEL_LIST_PATH = "/algo/api/v2/model/list?Encode=1"
MODEL_LIST_TIMEOUT = 15

DEFAULT_SYSTEM_PROMPT = "You are a helpful assistant."

NOISE_KEYS = ("extra_fields", "refusal", "reasoning_content")
TOOL_RESULT_MARKER = "[工具结果"
LEAK_MARKER = "[assistant 请求调用工具]"
DEEPSEEK_MODEL_KEYS = ("dmodel", "dfmodel")
STRUCTURED_TOOL_HISTORY_WHITELIST = (
    "qwen", "glm", "gm4", "gm5", "deepseek", "dfmodel")

UPSTREAM_TIMEOUT = 600


class UpstreamError(Exception):
    """上游/传输层错误，交给 main 映射为插件契约状态码。"""

    def __init__(self, status, message):
        self.status = int(status or 502)
        self.message = message or ""
        super(UpstreamError, self).__init__(self.message)


class UpstreamStatus(UpstreamError):
    """HTTP 200 但信封 statusCodeValue 非 200。"""


# ---------------------------------------------------------------------------
# 模型解析
# ---------------------------------------------------------------------------
def resolve_model(model, realm):
    return qoder_catalog.resolve_upstream_key(model or "auto", realm) or "auto"


def catalog_meta(model_key, realm):
    for entry in qoder_catalog.models_for_realm(realm):
        if isinstance(entry, dict) and entry.get("key") == model_key:
            return entry
    return {}


def fetch_live_catalog(account):
    """实时拉取上游模型清单，返回原始 {chat, app} 字典。

    每次都真实请求 gateway 上的 /algo/api/v2/model/list：不写缓存、不读快照、
    不解密本机 catalog-v6。签名走与对话同一套 COSY（签名只覆盖 path，
    因此换推理主机后依旧有效）。GET 无请求体，签名用空串。

    任何失败都抛异常，由调用方决定降级策略——这里绝不悄悄返回旧数据。
    """
    cfg = qoder_accounts.account_realm_config(account)
    hosts = qoder_accounts.gateway_candidates(account.realm, cfg)
    sess = qoder_sign.SESSIONS.get(account)
    last_exc = None
    for host in hosts[:3]:
        raw_url = host.rstrip("/") + MODEL_LIST_PATH
        headers = sess.headers("", raw_url, model_key="", sse=False,
                               accept="application/json")
        try:
            url = qoder_accounts.validate_public_http_url(raw_url)
            req = urllib.request.Request(url, method="GET", headers=headers)
            with urllib.request.urlopen(req, timeout=MODEL_LIST_TIMEOUT) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as exc:
            detail = ""
            try:
                detail = exc.read(300).decode("utf-8", "replace")
            except Exception:
                pass
            last_exc = UpstreamError(exc.code, "模型清单请求失败：HTTP %d %s"
                                     % (exc.code, detail))
            if 500 <= int(exc.code) < 600:
                continue          # 主机级故障：换下一个候选网关
            raise last_exc        # 4xx 是业务拒绝，换主机也一样
        except Exception as exc:
            last_exc = UpstreamError(503, "连接上游失败：%s" % exc)
            continue
        try:
            payload = json.loads(raw.decode("utf-8"))
        except Exception:
            raise UpstreamError(502, "模型清单不是合法 JSON")
        if not isinstance(payload, dict):
            raise UpstreamError(502, "模型清单结构异常")
        # 动态显示名要与解析表对齐：官方改名后客户端拿新名字来请求也能命中
        pairs = []
        for scene in ("chat", "app"):
            entries = payload.get(scene)
            if not isinstance(entries, list):
                continue
            for entry in entries:
                if isinstance(entry, dict) and entry.get("key"):
                    pairs.append((entry.get("display_name") or "", entry["key"]))
        if pairs:
            qoder_catalog.register_live_display_names(pairs)
        return payload
    raise last_exc or UpstreamError(503, "无法连接 Qoder 上游")


def list_models(account):
    """返回 /v1/models 的 data 列表。

    有账号就实时拉上游清单（每次调用都请求，不缓存）；没有可用账号时才退到
    本地快照，否则连模型的「名字」都给不出来。
    """
    if account is not None:
        try:
            entries = qoder_catalog.dialogue_models(fetch_live_catalog(account))
            if entries:
                return _models_payload(entries, account.realm)
        except Exception:
            pass  # 落到下面的快照兜底
    realms = [account.realm] if account and account.realm else ["cn", "intl"]
    out = []
    for realm in realms:
        out.extend(_models_payload(qoder_catalog.models_for_realm(realm), realm))
    return _dedupe_models(out)


def _models_payload(entries, realm):
    """把目录条目转成 OpenAI 模型对象（id 用官方显示名）。"""
    out = []
    for entry in entries:
        if not isinstance(entry, dict):
            continue
        key = entry.get("key") or ""
        if not key:
            continue
        display = entry.get("display_name") or key
        out.append({
            "id": display,
            "object": "model",
            "owned_by": "qoder",
            "name": display,
            "upstream_key": key,
            "realm": realm,
        })
    return out


def _dedupe_models(items):
    seen = set()
    out = []
    for item in items:
        if item["id"] in seen:
            continue
        seen.add(item["id"])
        out.append(item)
    return out


# ---------------------------------------------------------------------------
# 消息清洗
# ---------------------------------------------------------------------------
def _flatten_text(content):
    """content -> (纯文本, 图片 URL 列表)。"""
    if content is None:
        return "", []
    if isinstance(content, str):
        return content, []
    if isinstance(content, list):
        texts, images = [], []
        for part in content:
            if isinstance(part, str):
                texts.append(part)
                continue
            if not isinstance(part, dict):
                continue
            ptype = part.get("type")
            if ptype in ("text", "input_text"):
                texts.append(str(part.get("text") or ""))
            elif ptype in ("image_url", "input_image"):
                ref = part.get("image_url")
                if isinstance(ref, dict):
                    url = ref.get("url") or ""
                else:
                    url = ref or part.get("url") or ""
                if url:
                    images.append(str(url))
        return "\n".join(t for t in texts if t), images
    return str(content), []


def _structured_parts(content):
    """带图消息保留结构化 parts（上游只读 content 里的 image_url parts）。"""
    if isinstance(content, str) or not isinstance(content, list):
        return None
    parts = []
    for part in content:
        if isinstance(part, str):
            parts.append({"type": "text", "text": part})
            continue
        if not isinstance(part, dict):
            continue
        ptype = part.get("type")
        if ptype in ("text", "input_text"):
            parts.append({"type": "text", "text": str(part.get("text") or "")})
        elif ptype in ("image_url", "input_image"):
            ref = part.get("image_url")
            url = ref.get("url") if isinstance(ref, dict) else (ref or part.get("url"))
            if url:
                parts.append({"type": "image_url",
                              "image_url": {"url": str(url)}})
    return parts or None


def _tool_ids_ok(messages):
    for m in messages or []:
        if not isinstance(m, dict):
            continue
        if m.get("role") == "tool" and not str(m.get("tool_call_id") or "").strip():
            return False
        if m.get("role") == "assistant" and m.get("tool_calls"):
            for tc in m["tool_calls"] or []:
                if not isinstance(tc, dict) or not str(tc.get("id") or "").strip():
                    return False
    return True


def is_kimi_model(model, model_key):
    for cand in (model_key, model):
        low = str(cand or "").strip().lower()
        if low in ("kmodel", "kmodel_latest") or \
                re.search(r"(?:^|[^a-z0-9])kimi(?:$|[^a-z0-9])", low):
            return True
    return False


def is_deepseek_model(model, model_key):
    key = str(model_key or "").strip().lower()
    if key in DEEPSEEK_MODEL_KEYS:
        return True
    low = str(model or "").strip().lower()
    if not low:
        return False
    return low in DEEPSEEK_MODEL_KEYS or "deepseek" in low


def structured_enabled(model, model_key, realm, messages):
    realm = str(realm or "").strip().lower()
    if realm not in ("cn", "intl"):
        return False
    if not _tool_ids_ok(messages):
        return False
    for cand in (str(model_key or ""), str(model or "")):
        low = cand.strip().lower()
        if low and (low.startswith(STRUCTURED_TOOL_HISTORY_WHITELIST)
                    or is_kimi_model(cand, "")):
            return True
    return False


def flatten_messages(messages, keep_reasoning=False, structured=False):
    system_text = None
    flat, images = [], []
    for m in messages or []:
        if not isinstance(m, dict):
            continue
        role = m.get("role")
        text, imgs = _flatten_text(m.get("content"))
        images.extend(imgs)
        if role in ("system", "developer"):
            if system_text is None and text:
                system_text = text
            continue
        if role == "tool":
            name = (" (%s)" % m["name"]) if m.get("name") else ""
            body = text or ""
            if structured:
                flat.append({"role": "tool",
                             "tool_call_id": str(m.get("tool_call_id") or ""),
                             "content": body})
            else:
                flat.append({"role": "user",
                             "content": TOOL_RESULT_MARKER + name + "]\n" + body})
            continue
        if role == "assistant" and m.get("tool_calls"):
            if structured:
                calls = []
                for tc in m["tool_calls"]:
                    tc = tc if isinstance(tc, dict) else {}
                    fn = tc.get("function") or {}
                    args = fn.get("arguments")
                    if isinstance(args, (dict, list)):
                        args = json.dumps(args, ensure_ascii=False)
                    elif not isinstance(args, str):
                        args = "" if args is None else str(args)
                    calls.append({"id": str(tc.get("id") or ""),
                                  "type": tc.get("type") or "function",
                                  "function": {"name": fn.get("name") or "",
                                               "arguments": args}})
                item = {"role": "assistant", "content": text or "",
                        "tool_calls": calls}
                if keep_reasoning and m.get("reasoning_content") is not None:
                    item["reasoning_content"] = m.get("reasoning_content") or ""
                flat.append(item)
                continue
            calls = []
            for tc in m["tool_calls"]:
                fn = (tc or {}).get("function") or {}
                calls.append({"name": fn.get("name") or "",
                              "arguments": fn.get("arguments") or ""})
            if calls:
                text = (text or "") + "\n\n" + LEAK_MARKER + "\n" + \
                    json.dumps(calls, ensure_ascii=False)
            item = {"role": "assistant", "content": text or ""}
            if keep_reasoning and m.get("reasoning_content") is not None:
                item["reasoning_content"] = m.get("reasoning_content") or ""
            flat.append(item)
            continue
        if role in ("user", "assistant"):
            parts = _structured_parts(m.get("content")) if imgs else None
            item = {"role": role, "content": parts if parts else (text or "")}
            if role == "assistant" and keep_reasoning \
                    and m.get("reasoning_content") is not None:
                item["reasoning_content"] = m.get("reasoning_content") or ""
            flat.append(item)
        elif role:
            flat.append({"role": "user", "content": text or ""})
    return system_text, flat, images


def normalize_tools(tools):
    out = []
    for tool in tools or []:
        if not isinstance(tool, dict):
            continue
        if tool.get("type") == "function" and isinstance(tool.get("function"), dict):
            out.append(tool)
        elif tool.get("type") == "function" and tool.get("name"):
            out.append({"type": "function", "function": {
                "name": tool.get("name"),
                "description": tool.get("description") or "",
                "parameters": tool.get("parameters") or
                {"type": "object", "properties": {}},
            }})
        elif tool.get("type") == "custom" and tool.get("name"):
            continue
        else:
            out.append(tool)
    return out


# ---------------------------------------------------------------------------
# 请求体构造
# ---------------------------------------------------------------------------
def build_body(payload, account, model_key):
    realm = account.realm or "cn"
    body = copy.deepcopy(BASE_PROMPT)
    messages = payload.get("messages") or []
    keep_reasoning = is_deepseek_model(payload.get("model"), model_key)
    structured = structured_enabled(payload.get("model"), model_key,
                                    realm, messages)
    system_text, flat, images = flatten_messages(
        messages, keep_reasoning=keep_reasoning, structured=structured)

    tmpl_system = [m for m in (body.get("messages") or [])
                   if isinstance(m, dict) and m.get("role") == "system"]
    if system_text is not None:
        body["messages"] = [{"role": "system", "content": system_text}]
    else:
        body["messages"] = tmpl_system
    body["messages"].extend(flat)

    prompt = ""
    for m in reversed(flat):
        if m.get("role") == "user" and m.get("content"):
            content = m["content"]
            if isinstance(content, list):
                content = "\n".join(
                    str(p.get("text") or "") for p in content
                    if isinstance(p, dict) and p.get("type") == "text")
            prompt = content
            break

    nid = str(uuid.uuid4())
    body["request_id"] = nid
    body["chat_record_id"] = nid
    body["request_set_id"] = str(uuid.uuid4())
    body["session_id"] = str(uuid.uuid4())
    body["stream"] = True
    body["agent_id"] = "agent_common"
    body["aliyun_user_type"] = getattr(account, "user_type", "") or \
        qoder_sign.DEFAULT_USER_TYPE

    meta = catalog_meta(model_key, realm)
    mc = body.get("model_config") or {}
    mc["key"] = model_key
    mc["display_name"] = meta.get("display_name") or model_key
    mc["model"] = ""
    mc["format"] = "openai"
    mc["is_vl"] = bool(meta.get("is_vl"))
    mc["is_reasoning"] = bool(meta.get("is_reasoning"))
    mc["api_key"] = ""
    mc["url"] = ""
    mc["source"] = "system"
    mc["max_input_tokens"] = meta.get("max_input_tokens") or 180000
    body["model_config"] = mc

    cc = body.get("chat_context") or {}
    text = cc.get("text") or {}
    text["text"] = prompt
    cc["text"] = text
    extra = cc.get("extra") or {}
    original = extra.get("originalContent") or {}
    original["text"] = prompt
    extra["originalContent"] = original
    extra["modelConfig"] = dict(mc)
    cc["extra"] = extra
    body["chat_context"] = cc

    params = body.get("parameters") or {}
    max_tokens = payload.get("max_tokens") or payload.get("max_completion_tokens")
    if max_tokens:
        try:
            params["max_tokens"] = int(max_tokens)
        except (TypeError, ValueError):
            pass
    effort = payload.get("reasoning_effort")
    if isinstance(effort, str) and effort.strip():
        params["reasoning_effort"] = effort.strip()
    body["parameters"] = params

    body["tools"] = normalize_tools(payload.get("tools"))

    biz = body.get("business") or {}
    biz["id"] = str(uuid.uuid4())
    biz["begin_at"] = int(time.time() * 1000)
    biz["name"] = (prompt[:30] if prompt else "chat")
    biz.setdefault("product", "cli")
    biz.setdefault("type", "agent")
    biz.setdefault("stage", "start")
    biz["model_key"] = model_key
    biz["version"] = qoder_sign.COSY_VERSION
    body["business"] = biz
    return body


# ---------------------------------------------------------------------------
# 上游请求
# ---------------------------------------------------------------------------
MAX_UPSTREAM_BODY_BYTES = 32 * 1024 * 1024


def open_chat(account, model_key, body_obj):
    encoded = qoder_sign.qoder_encode(
        json.dumps(body_obj, ensure_ascii=False).encode("utf-8"))
    if len(encoded) > MAX_UPSTREAM_BODY_BYTES:
        raise UpstreamError(413, "请求体过大，压缩后仍超过 32MB，请减少图片数量或尺寸")
    cfg = qoder_accounts.account_realm_config(account)
    hosts = qoder_accounts.gateway_candidates(account.realm, cfg)
    last_exc = None
    for host in hosts[:3]:
        raw_url = host.rstrip("/") + CHAT_PATH
        sess = qoder_sign.SESSIONS.get(account)
        headers = sess.headers(encoded, raw_url, model_key=model_key,
                               sse=True)
        chat_url = qoder_accounts.validate_public_http_url(raw_url)
        req = urllib.request.Request(chat_url,
                                     data=encoded.encode("utf-8"),
                                     method="POST", headers=headers)
        try:
            return urllib.request.urlopen(req, timeout=UPSTREAM_TIMEOUT)
        except urllib.error.HTTPError as exc:
            detail = ""
            try:
                detail = exc.read(600).decode("utf-8", "replace")
            except Exception:
                pass
            if 500 <= int(exc.code) < 600:
                last_exc = UpstreamError(exc.code, detail)
                continue
            raise UpstreamError(exc.code, detail)
        except Exception as exc:
            last_exc = UpstreamError(503, "连接上游失败：%s" % exc)
            continue
    raise last_exc or UpstreamError(503, "无法连接 Qoder 上游")


def strip_data_prefix(line):
    line = (line or "").strip()
    if not line or line.startswith(":"):
        return ""
    while line.startswith("data:"):
        line = line[5:].strip()
    if not line or line.startswith(":"):
        return ""
    return line


def normalize_usage(usage):
    if not isinstance(usage, dict):
        return usage
    details = usage.get("completion_tokens_details") or {}
    prompt_details = usage.get("prompt_tokens_details")
    prompt_details = dict(prompt_details) if isinstance(prompt_details, dict) else {}
    cached = None
    for cand in (usage.get("cached_tokens"), prompt_details.get("cached_tokens"),
                 details.get("cached_tokens"), usage.get("prompt_cache_hit_tokens")):
        if isinstance(cand, (int, float)) and not isinstance(cand, bool):
            cached = int(cand)
            break
    if cached is not None and "cached_tokens" not in prompt_details:
        prompt_details["cached_tokens"] = cached
        usage["prompt_tokens_details"] = prompt_details
    return usage


def clean_chunk(chunk):
    for choice in chunk.get("choices") or []:
        delta = choice.get("delta")
        if not isinstance(delta, dict):
            continue
        fc = delta.get("function_call")
        if fc is not None and ((not fc) if not isinstance(fc, dict)
                               else not fc.get("name")):
            delta.pop("function_call", None)
        if isinstance(delta.get("tool_calls"), list) and not delta["tool_calls"]:
            delta.pop("tool_calls")
        for key in NOISE_KEYS:
            if key in delta and not delta.get(key):
                delta.pop(key, None)
    return chunk


def iter_chunks(resp):
    for raw in resp:
        if isinstance(raw, bytes):
            try:
                line = raw.decode("utf-8")
            except Exception:
                continue
        else:
            line = raw
        data = strip_data_prefix(line)
        if not data:
            continue
        try:
            outer = json.loads(data)
        except Exception:
            continue
        if not isinstance(outer, dict):
            continue
        status = outer.get("statusCodeValue")
        body = outer.get("body")
        if status not in (None, 200, "200"):
            detail = body if isinstance(body, str) else \
                json.dumps(outer, ensure_ascii=False)[:400]
            raise UpstreamStatus(status, detail)
        if not isinstance(body, str):
            continue
        if body == "[DONE]":
            break
        try:
            chunk = json.loads(body)
        except Exception:
            continue
        if not isinstance(chunk, dict):
            continue
        if chunk.get("usage"):
            normalize_usage(chunk["usage"])
        yield clean_chunk(chunk)


def _remap_tool_indexes(chunk, state):
    for choice in chunk.get("choices") or []:
        delta = choice.get("delta")
        if not isinstance(delta, dict):
            continue
        calls = delta.get("tool_calls")
        if not isinstance(calls, list):
            continue
        for position, call in enumerate(calls):
            if not isinstance(call, dict):
                continue
            wire = call.get("index")
            if not isinstance(wire, int) or isinstance(wire, bool) or wire < 0:
                wire = position
            call_id = call.get("id")
            call_id = call_id if isinstance(call_id, str) and call_id.strip() else ""
            slot = state["by_id"].get(call_id) if call_id else None
            if slot is None:
                slot = state["by_wire"].get(wire)
            if slot is None:
                slot = state["next"]
                state["next"] += 1
            if call_id:
                state["by_id"][call_id] = slot
            state["by_wire"][wire] = slot
            call["index"] = slot


def _fill_tool_ids(chunk, id_by_slot):
    for choice in chunk.get("choices") or []:
        delta = choice.get("delta")
        if not isinstance(delta, dict):
            continue
        calls = delta.get("tool_calls")
        if not isinstance(calls, list):
            continue
        for call in calls:
            if not isinstance(call, dict):
                continue
            slot = call.get("index")
            if not isinstance(slot, int) or isinstance(slot, bool):
                continue
            call_id = call.get("id")
            if isinstance(call_id, str) and call_id.strip():
                id_by_slot[slot] = call_id
            elif slot in id_by_slot:
                call["id"] = id_by_slot[slot]
            else:
                call["id"] = "call_" + uuid.uuid4().hex[:24]
                id_by_slot[slot] = call["id"]
            if not isinstance(call.get("function"), dict):
                call["function"] = {"name": "", "arguments": ""}


def stream_sse(resp):
    state = {"by_id": {}, "by_wire": {}, "next": 0}
    id_by_slot = {}
    for chunk in iter_chunks(resp):
        _remap_tool_indexes(chunk, state)
        _fill_tool_ids(chunk, id_by_slot)
        yield ("data: " + json.dumps(chunk, ensure_ascii=False) + "\n\n") \
            .encode("utf-8")
    yield b"data: [DONE]\n\n"


def aggregate(resp, fallback_model):
    content, reasoning = [], []
    finish = "stop"
    tool_calls = {}
    usage = None
    rid = None
    model = fallback_model
    created = None
    state = {"by_id": {}, "by_wire": {}, "next": 0}
    for chunk in iter_chunks(resp):
        _remap_tool_indexes(chunk, state)
        if chunk.get("id"):
            rid = chunk["id"]
        if chunk.get("model"):
            model = chunk["model"]
        if chunk.get("created"):
            created = chunk["created"]
        if chunk.get("usage"):
            usage = chunk["usage"]
        for choice in chunk.get("choices") or []:
            delta = choice.get("delta") or {}
            if isinstance(delta.get("content"), str):
                content.append(delta["content"])
            elif isinstance(delta.get("content"), list):
                for part in delta["content"]:
                    if isinstance(part, str):
                        content.append(part)
                    elif isinstance(part, dict) and isinstance(part.get("text"), str):
                        content.append(part["text"])
            if isinstance(delta.get("reasoning_content"), str):
                reasoning.append(delta["reasoning_content"])
            for call in delta.get("tool_calls") or []:
                if not isinstance(call, dict):
                    continue
                idx = call.get("index")
                if not isinstance(idx, int) or isinstance(idx, bool):
                    idx = len(tool_calls)
                fn = call.get("function") or {}
                slot = tool_calls.setdefault(idx, {
                    "id": call.get("id") or ("call_" + uuid.uuid4().hex[:24]),
                    "type": call.get("type") or "function",
                    "function": {"name": "", "arguments": ""},
                })
                if call.get("id"):
                    slot["id"] = call["id"]
                if fn.get("name"):
                    slot["function"]["name"] = fn["name"]
                if fn.get("arguments"):
                    slot["function"]["arguments"] += fn["arguments"]
            if choice.get("finish_reason"):
                finish = choice["finish_reason"]

    message = {"role": "assistant", "content": "".join(content)}
    if reasoning:
        message["reasoning_content"] = "".join(reasoning)
    if tool_calls:
        message["tool_calls"] = [tool_calls[i] for i in sorted(tool_calls)]
        if not message["content"]:
            message["content"] = ""
        if finish in ("stop", None):
            finish = "tool_calls"
    if usage:
        normalize_usage(usage)
    return {
        "id": rid or ("chatcmpl-" + uuid.uuid4().hex),
        "object": "chat.completion",
        "created": created or int(time.time()),
        "model": model,
        "choices": [{"index": 0, "message": message,
                     "finish_reason": finish}],
        "usage": usage or {"prompt_tokens": 0, "completion_tokens": 0,
                           "total_tokens": 0},
    }


# ---------------------------------------------------------------------------
# 错误归类（插件契约）
# ---------------------------------------------------------------------------
_ACCOUNT_CODES = {"112": 403, "110": 429, "10605": 429, "430": 400}


def _detail_message(detail):
    detail = (detail or "").strip()
    if not detail:
        return ""
    try:
        obj = json.loads(detail)
    except Exception:
        return detail
    if isinstance(obj, dict):
        err = obj.get("error")
        if isinstance(err, dict):
            msg = err.get("message") or err.get("msg") or ""
            code = err.get("code")
            return ("%s %s" % (code, msg)).strip() if code else str(msg)
        if isinstance(err, str):
            return err
        for key in ("message", "msg", "detail", "error_message"):
            if obj.get(key):
                return str(obj[key])
    return detail


def map_error(exc):
    """(HTTP 状态码, error.type, 中文说明)。"""
    detail = _detail_message(getattr(exc, "message", str(exc)))
    raw = getattr(exc, "message", "") or ""
    code = None
    try:
        obj = json.loads(raw)
        if isinstance(obj, dict):
            err = obj.get("error")
            if isinstance(err, dict) and err.get("code") is not None:
                code = str(err.get("code"))
            elif obj.get("code") is not None:
                code = str(obj.get("code"))
    except Exception:
        pass

    status = int(getattr(exc, "status", 502) or 502)
    if status in (None, 200):
        status = 502
    if code in _ACCOUNT_CODES:
        status = _ACCOUNT_CODES[code]
    elif 400 <= status < 500:
        if status == 401:
            status = 401
        elif status in (403,):
            status = 403
        elif status == 429:
            status = 429
        else:
            status = 400
    elif status >= 500:
        status = 502

    hints = []
    if code == "112":
        hints.append("上游模型额度/套餐不足")
    elif code in ("110", "10605"):
        hints.append("上游触发当日限额或排队")
    if "TOKEN_EXPIRE" in raw.upper() or "token expire" in raw.lower():
        hints.append("账号凭据已过期，请重新登录")
    message = detail or "上游返回错误"
    if hints:
        message = "；".join(hints) + "：" + message
    type_map = {400: "invalid_request", 401: "auth_failed", 403: "forbidden",
                404: "not_found", 429: "rate_limited", 500: "plugin_error",
                502: "upstream_error", 503: "upstream_error"}
    return status, type_map.get(status, "upstream_error"), message[:400]
