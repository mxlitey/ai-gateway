# -*- coding: utf-8 -*-
"""对话转发与选号（对应 Go 版 chat.go）。

上游只接受流式（非流式会返回 code=11101），所以统一以 stream=true 请求；
客户端要流式就照原样透传 SSE，要非流式就在本地把 SSE 聚合成一次响应。
"""

import hashlib
import json
import threading
import time

from upstream import chat_headers, do_json, ensure_token, extract_models, realm_config
from util import as_str, first_non_empty, truncate_runes


class NoAccountError(Exception):
    pass


# ---------- 最后一问的标签规范化 ----------

# WorkBuddy 客户端把当次提问包成 <user_query>…</user_query>，隐藏上下文包成
# <system-reminder data-role="…">…</system-reminder>（见客户端 app.asar）
TARGET_QUESTION_TAG = "user_query"
PACKAGING_TAGS = ("system-reminder",)
# 其它客户端用来包裹提问的标签，统一改写为 user_query
SOURCE_QUESTION_TAGS = ("user_query", "user_input", "user_message", "user_request", "query")


def _lower_ascii(text):
    """只把 ASCII 大写转小写，保证字节长度不变（与切片下标对齐）。"""
    return "".join(chr(ord(c) + 32) if "A" <= c <= "Z" else c for c in text)


def _is_tag_end(ch):
    return ch in (">", " ", "\t", "\n", "\r")


def _take_blocks(text, name):
    """摘出 text 中所有 <name …>…</name> 块，返回 (剩余文本, 块列表)。"""
    lower = _lower_ascii(text)
    open_tag = "<" + name
    close_tag = "</" + name + ">"
    rest = []
    blocks = []
    i = 0
    while i < len(text):
        oi = lower.find(open_tag, i)
        if oi < 0:
            rest.append(text[i:])
            break
        after = oi + len(open_tag)
        if after < len(text) and not _is_tag_end(lower[after]):
            rest.append(text[i:after])
            i = after
            continue
        gt = lower.find(">", after)
        if gt < 0:
            rest.append(text[i:])
            break
        ci = lower.find(close_tag, gt + 1)
        if ci < 0:
            rest.append(text[i:])  # 没闭合：其余原样保留
            break
        blocks.append(text[oi:ci + len(close_tag)])
        rest.append(text[i:oi])
        i = ci + len(close_tag)
    return "".join(rest), blocks


def _unwrap_whole(text):
    """整段被一对已知提问标签包裹时返回内部文本，否则原样返回。

    只认 SOURCE_QUESTION_TAGS：用户消息本身以 <html> / <div> 开头很常见，
    按任意同名标签拆会错误吞掉用户粘贴的代码。
    """
    if not text.startswith("<"):
        return text
    lower = _lower_ascii(text)
    gt = lower.find(">")
    if gt < 0:
        return text
    name = lower[1:gt].strip()
    for sep in (" ", "\t", "\r", "\n"):
        pos = name.find(sep)
        if pos >= 0:
            name = name[:pos]
    if name not in SOURCE_QUESTION_TAGS:
        return text
    close_tag = "</" + name + ">"
    if not lower.endswith(close_tag):
        return text
    inner = text[gt + 1: len(text) - len(close_tag)]
    if close_tag in _lower_ascii(inner):
        return text  # 内部还有同名闭标签：结构异常，不猜
    return inner.strip()


def format_question(text):
    """把一段用户输入改写成 WorkBuddy 的形态：上下文块在前，提问包进 <user_query>。

    客户端自己的包装标签（<user_input> 等）会被换成 WorkBuddy 的 <user_query>；
    无包装的纯文本同样补上这层包装，与官方客户端的出站形态保持一致。
    标签出现在段中间（后面还跟着 <advisor_tool_policy> 之类的内容）时，
    也只替换标签本身，不把后面的内容一起吞进 <user_query>。
    """
    rest = text
    blocks = []
    for name in PACKAGING_TAGS:
        rest, found = _take_blocks(rest, name)
        blocks.extend(found)

    questions = []
    for name in SOURCE_QUESTION_TAGS:
        rest, found = _take_blocks(rest, name)
        for block in found:
            inner = _unwrap_whole(block)
            if inner:
                questions.append("<%s>%s</%s>" % (TARGET_QUESTION_TAG, inner, TARGET_QUESTION_TAG))

    leftover = rest.strip()
    if questions:
        return " ".join(blocks + questions + ([leftover] if leftover else []))

    inner = _unwrap_whole(leftover)
    if not inner:
        return text  # 整段都是上下文，没有提问：原样转发
    wrapped = "<%s>%s</%s>" % (TARGET_QUESTION_TAG, inner, TARGET_QUESTION_TAG)
    return " ".join(blocks + [wrapped])


def _is_text_part(part):
    return (isinstance(part, dict) and part.get("type", "text") == "text"
            and isinstance(part.get("text"), str))


def _has_question(text):
    """这一段是否真的含提问（format_question 能改动它即是有提问）。"""
    return format_question(text) != text


def _question_part_index(parts):
    """从后往前找第一个含真实提问的 text part。

    Trae 等客户端在会话中途会把多条 <system-reminder> 拆成独立 text part，
    提问被挤到中间；只看最后一段会踩空（那一段纯上下文，改写后原样返回）。
    """
    for index in range(len(parts) - 1, -1, -1):
        part = parts[index]
        if not _is_text_part(part):
            continue
        if _has_question(part["text"]):
            return index
    return -1


def _normalize_parts(parts):
    """改写含提问的那一段，并把它前面的纯上下文段并入同一段（对齐官方形态）。"""
    index = _question_part_index(parts)
    if index < 0:
        return
    contexts = []
    rebuilt = []
    for i, part in enumerate(parts):
        if i == index:
            part["text"] = format_question("".join(contexts) + part["text"])
            rebuilt.append(part)
        elif i < index and _is_text_part(part):
            contexts.append(part["text"])
        else:
            rebuilt.append(part)
    parts[:] = rebuilt


def normalize_user_messages(body):
    """规范化所有 user 消息：上下文块提到前面，提问统一包成 <user_query>。

    历史消息也要清洗：客户端只在首轮把提问当作「最后一条」送来，之后每轮都把
    它当成历史，仍带着 <user_input> 等标签；不清洗的话这些标签会随每次请求
    反复上送，在官网明细里显示为未清理的原始标签。
    """
    messages = body.get("messages")
    if not isinstance(messages, list):
        return
    for message in messages:
        if not isinstance(message, dict) or message.get("role") != "user":
            continue
        content = message.get("content")
        if isinstance(content, str):
            message["content"] = format_question(content)
        elif isinstance(content, list):
            _normalize_parts(content)


# ---------- 会话头族 ----------


def _message_text(message):
    """取一条 message 的纯文本 content（兼容字符串与数组两种形态）。"""
    if not isinstance(message, dict):
        return ""
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for part in content:
            if isinstance(part, dict) and isinstance(part.get("text"), str):
                parts.append(part["text"])
        return "".join(parts)
    return ""


def _question_of(message):
    """取一条 user 消息的「提问本体」。

    先摘掉 <system-reminder> 等上下文块，再把 <user_query> / <user_input> 之类
    的提问标签脱壳；没有标签时退回摘掉上下文后的纯文本。
    """
    text = _message_text(message)
    if not text:
        return ""
    rest = text
    for name in PACKAGING_TAGS:
        rest, _ = _take_blocks(rest, name)
    for name in SOURCE_QUESTION_TAGS:
        rest, found = _take_blocks(rest, name)
        for block in found:
            inner = _unwrap_whole(block)
            if inner:
                return inner
    return _unwrap_whole(rest.strip())


def _last_question(body):
    """最后一条 user 消息里的提问本体（取不到提问时退回它的完整文本）。

    上下文块里带时间戳、环境信息这类每次都会变的内容，先剔掉再派生标识，
    否则同一个问题会算出一串不同的 ID。
    """
    messages = body.get("messages")
    if not isinstance(messages, list):
        return ""
    for index in range(len(messages) - 1, -1, -1):
        message = messages[index]
        if not isinstance(message, dict) or message.get("role") != "user":
            continue
        return _question_of(message) or _message_text(message)
    return ""


def _hash32(seed):
    """种子的 32 位 hex 摘要（会话头族各级标识的公共原语）。"""
    return hashlib.sha256(seed.encode("utf-8")).hexdigest()[:32]


def _stable_uuid(seed):
    """由种子确定性派生 8-4-4-4-12 形态的标识（同种子恒等）。"""
    h = _hash32(seed)
    return "%s-%s-%s-%s-%s" % (h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])


def _conversation_id(body, question):
    """取会话 ID：客户端自带优先，否则按「最后一问」稳定派生。

    上游按会话归组用量；不派生的话，所有无会话标识的客户端每次请求都会
    在官网明细里裂成互不相干的一条。只用提问本体（不带 system、不带上下文块）：
    部分客户端每轮都会改写提示词，带上它会让标识逐轮漂移。
    """
    meta = body.get("metadata")
    if isinstance(meta, dict):
        value = first_non_empty(as_str(meta.get("conversation_id")),
                                as_str(meta.get("conversationId")))
        if value:
            return value
    value = first_non_empty(as_str(body.get("conversation_id")),
                            as_str(body.get("conversationId")))
    if value:
        return value

    if not question:
        return ""
    return _stable_uuid("conversation|" + question)


def _header_value(headers, name):
    """大小写不敏感地取一个请求头值（客户端发来的头大小写不统一）。"""
    if not isinstance(headers, dict):
        return ""
    target = name.lower()
    for key, value in headers.items():
        if isinstance(key, str) and key.lower() == target:
            return as_str(value).strip()
    return ""


def conversation_meta(body, headers=None):
    """生成官方客户端的会话头族元数据（由 chat_headers 写入请求头）。

    对齐官方桌面端的三层标识：会话级（X-Conversation-ID，多轮稳定）、
    对话轮级（X-Conversation-Request-ID，一次发送内稳定）、消息级（每条独立）。

    三者统一由「最后一问」派生：同一个提问恒定得到同一组 ID，提问一变整组即变。
    客户端自己发了这套头就原样沿用：会话中途换到本插件时，上游看到的层级
    才与官方客户端一致（否则同一场会话会被当成新会话重新归组）。
    """
    question = _last_question(body)
    conversation_id = _header_value(headers, "X-Conversation-ID") or _conversation_id(body, question)
    turn_id = _header_value(headers, "X-Conversation-Request-ID")
    if not turn_id and question:
        seed = "turn|" + conversation_id + "|" + _hash32("question|" + question)
        turn_id = _hash32(seed)
    message_id = _header_value(headers, "X-Conversation-Message-ID")
    if not message_id and question:
        message_id = _hash32("message|" + conversation_id + "|" + _hash32("question|" + question))
    return {
        "conversation_id": conversation_id,
        "conversation_request_id": turn_id,
        "message_id": message_id,
    }


# 模型思考档位表缓存：同一站点在 TTL 内复用，避免每次转发都多打一次上游
_CATALOG_TTL_SECONDS = 600
_CATALOG_FAIL_TTL_SECONDS = 60   # 拉失败也短缓存，免得每次转发都阻塞在配置端点上
_CATALOG_TIMEOUT = 15
_catalog_lock = threading.Lock()
_catalog_cache = {}


def pick_account(eps, store):
    """选一个可用账号（启用中且登录态没过期）。"""
    for acc in store.list():
        if not acc.enabled:
            continue
        try:
            ensure_token(eps, acc, store)
        except Exception:
            continue
        return acc
    raise NoAccountError("没有可用账号：请先在插件控制台里导入或授权登录")


def account_for_request(eps, store, headers):
    """决定这次请求用哪个账号。

    网关把「账号 id」放在 X-Plugin-Account 头里（渠道密钥 = 账号 id），命中账号时直接用它——
    「轮询 / 故障转移 / 启用停用」因此全部由网关的渠道密钥策略决定；没命中则回退到自动挑号，
    兼容尚未同步账号的直连场景。
    """
    account_id = (headers.get("X-Plugin-Account") or "").strip()
    if account_id:
        acc = store.get(account_id)
        if acc is not None:
            if not acc.enabled:
                raise NoAccountError("账号 %s 已在插件侧停用" % first_non_empty(acc.name, account_id))
            try:
                ensure_token(eps, acc, store)
            except Exception as exc:
                raise NoAccountError("账号 %s 不可用：%s" % (
                    first_non_empty(acc.name, account_id), truncate_runes(str(exc), 80)))
            return acc
    return pick_account(eps, store)


def aggregate_sse(raw):
    """把上游的 chat.completion.chunk 流合并成一次非流式响应。

    返回 (响应体 dict, 错误文案)；两者互斥。
    """
    text = raw.decode("utf-8", "replace")
    chunk_id = ""
    model = ""
    created = 0
    content_parts = []
    reasoning_parts = []
    finish_reason = ""
    usage = None

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
        if chunk.get("id"):
            chunk_id = chunk["id"]
        if chunk.get("model"):
            model = chunk["model"]
        if isinstance(chunk.get("created"), (int, float)) and chunk["created"] > 0:
            created = int(chunk["created"])
        if isinstance(chunk.get("usage"), dict):
            usage = chunk["usage"]
        choices = chunk.get("choices")
        if isinstance(choices, list):
            for choice in choices:
                if not isinstance(choice, dict):
                    continue
                delta = choice.get("delta")
                if isinstance(delta, dict):
                    if delta.get("content"):
                        content_parts.append(delta["content"])
                    if delta.get("reasoning_content"):
                        reasoning_parts.append(delta["reasoning_content"])
                if choice.get("finish_reason"):
                    finish_reason = choice["finish_reason"]

    if not chunk_id and created == 0:
        return None, "上游没有返回任何 chunk"

    message = {"role": "assistant", "content": "".join(content_parts)}
    if reasoning_parts:
        message["reasoning_content"] = "".join(reasoning_parts)
    out = {
        "id": chunk_id,
        "object": "chat.completion",
        "created": created,
        "model": model,
        "choices": [{
            "index": 0,
            "message": message,
            "finish_reason": first_non_empty(finish_reason, "stop"),
        }],
    }
    if usage is not None:
        out["usage"] = usage
    return out, None


def fetch_models(eps, store, headers):
    """供网关「获取模型」使用；拉不到就返回空列表（不影响转发）。"""
    try:
        acc = account_for_request(eps, store, headers)
    except Exception:
        return []
    cfg = realm_config(eps, acc.site)
    try:
        env, _ = do_json("GET", cfg.chat_origin + eps.models, None, chat_headers(acc, cfg))
    except Exception:
        return []
    if env["code"] != 0:
        return []
    return extract_models(env["data"])


def extract_reasoning_catalog(data):
    """从产品配置（/v3/config）里抽出每个模型声明的思考档位。

    形状对齐参考实现：模型项上的 reasoning.supportedEfforts / reasoning.defaultEffort
    （兼容 snake_case）。取不到元数据的模型不落条目——等价于「没声明」，不做档位降级。
    """
    out = {}
    if not isinstance(data, dict):
        return out

    def add(item):
        if not isinstance(item, dict):
            return
        mid = first_non_empty(as_str(item.get("id")), as_str(item.get("model")),
                              as_str(item.get("name")))
        reasoning = item.get("reasoning")
        if not mid or not isinstance(reasoning, dict):
            return
        supported = reasoning.get("supportedEfforts")
        if not isinstance(supported, list):
            supported = reasoning.get("supported_efforts")
        supported = [as_str(s) for s in supported] if isinstance(supported, list) else []
        default = reasoning.get("defaultEffort")
        if default is None:
            default = reasoning.get("default_effort")
        out[mid.strip().lower()] = {
            "supportedEfforts": supported,
            "defaultEffort": as_str(default),
        }

    def add_list(value):
        if isinstance(value, list):
            for item in value:
                add(item)

    add_list(data.get("models"))
    add_list(data.get("availableModels"))
    agents = data.get("agents")
    if isinstance(agents, list):
        for agent in agents:
            if isinstance(agent, dict):
                add_list(agent.get("models"))
                add_list(agent.get("availableModels"))
    return out


def reasoning_catalog(eps, acc):
    """拿某账号站点上的模型思考档位表（带 TTL 缓存）；失败返回空表（不影响转发）。"""
    now = time.time()
    with _catalog_lock:
        entry = _catalog_cache.get(acc.site)
        if entry and entry[0] > now:
            return entry[1]
    catalog = {}
    ok = False
    cfg = realm_config(eps, acc.site)
    try:
        env, _ = do_json("GET", cfg.chat_origin + eps.models, None, chat_headers(acc, cfg),
                         timeout=_CATALOG_TIMEOUT)
        if env.get("code") == 0:
            catalog = extract_reasoning_catalog(env.get("data"))
            ok = True
    except Exception:
        pass
    with _catalog_lock:
        _catalog_cache[acc.site] = (
            now + (_CATALOG_TTL_SECONDS if ok else _CATALOG_FAIL_TTL_SECONDS), catalog)
    return catalog


def model_list_payload(ids):
    """OpenAI 兼容的 /v1/models 响应体。"""
    data = []
    for mid in ids:
        mid = (mid or "").strip()
        if mid:
            data.append({"id": mid, "object": "model", "owned_by": "workbuddy"})
    return {"object": "list", "data": data}



