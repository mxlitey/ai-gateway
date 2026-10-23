# -*- coding: utf-8 -*-
"""对话转发 + 模型表 + 选号（对应 Go 版 chat.go）。

选号：X-Plugin-Account 命中且启用 → 用它；命中但已停用 → 403；头缺失 → 回退自动挑号。
插件**不做任何调度**（不按积分挑号、不做冷却降级、不跳号），选择哪个账号全部归网关。
"""

import hashlib
import json
import threading
import time

from upstream import UpstreamError
from util import first_positive, random_uuid

# 拉不到上游模型表时的回退清单
STATIC_MODELS = ["glm-5.2", "glm-5.3"]

# 客户端用来塞「内部提示」的包装块标签（环境 / 时间 / 模式 / 待办等）
PACKAGING_TAGS = {"system-reminder"}


def account_for_request(plugin, headers):
    """决定这次请求用哪个账号。"""
    account_id = (headers.get("X-Plugin-Account") or "").strip()
    if account_id:
        account = plugin.store.get(account_id)
        if account is not None:
            if not account.usable():
                raise UpstreamError(403, "账号 " + account.name() + " 已在插件侧停用")
            plugin.upstream.ensure_token(account, plugin.store)
            return account
    return pick_account(plugin)


def pick_account(plugin):
    """回退挑号：取第一个可用（启用且能续期）的账号。仅用于直连调试。"""
    for account in plugin.store.list():
        if not account.usable():
            continue
        try:
            plugin.upstream.ensure_token(account, plugin.store)
        except Exception:
            continue
        return account
    raise UpstreamError(503, "没有可用账号：请先在插件控制台登录或导入 TRAE 账号")


def workspace_id_for(account):
    """按账号派生 workspace_id。

    客户端这里填真实工作区的 6 位短 id；插件没有工作区，用账号身份派生一个固定值，
    保证同一账号恒定、账号之间互异，且形态与客户端一致。
    """
    basis = account.uid or account.device_id or account.machine_id or "anonymous"
    return hashlib.sha256(("workspace:" + basis).encode("utf-8")).hexdigest()[:6]


# 会话复用窗口：同一提问在窗口内的重复视为同一场会话（客户端重试），
# 超过窗口的重复按官方客户端语义开新会话（用户重新提问 = 新会话）。
SESSION_REUSE_SECONDS = 120

_session_cache = {}
_session_lock = threading.Lock()


def session_ident(account, seed):
    """为（账号, 会话种子）分配 conversation/session uuid。

    上游对会话有两条风控规则（均回业务码 4011）：
      1. 按会话维度限流：同一会话短时间请求过多即被限流；
      2. 会话与账号绑定：同一场会话被另一个账号使用（如多账号共用同一探测语）
         会被判为异常，第二个账号立即 4011。
    官方客户端的语义是：一场会话属于一个账号，重试复用同一会话，重新提问开新会话。
    插件从请求本身无法区分「重试」与「时隔较久的再次提问」，用「（账号, 种子）为
    键、复用窗口内同会话、过期新开」近似官方语义——重试仍归入同一场会话，而反复
    到来的相同提问（如网关诊断对每个密钥发的同一句探测语）各自拿到全新会话。
    """
    if not seed:
        return random_uuid()
    key = (account.uid or "?", seed)
    now = time.time()
    with _session_lock:
        item = _session_cache.get(key)
        if item is not None and item[1] > now:
            _session_cache[key] = (item[0], now + SESSION_REUSE_SECONDS)
            return item[0]
        if len(_session_cache) >= 4096:
            for stale in [k for k, v in _session_cache.items() if v[1] <= now]:
                _session_cache.pop(stale, None)
            if len(_session_cache) >= 4096:
                _session_cache.clear()
        ident = random_uuid()
        _session_cache[key] = (ident, now + SESSION_REUSE_SECONDS)
        return ident


def inject_solo_fields(plugin, body, account):
    """注入 llm_utils_chat 的官方客户端必填字段。

    官方客户端除 stream / function / config_name / model 外还会带一组 uuid 会话标识
    与设备/版本指纹，缺这些字段会被上游识别为非官方调用。
    """
    value = first_positive(body.get("max_tokens"), body.get("max_completion_tokens"))
    body["max_tokens"] = value if value > 0 else 4096
    value = first_positive(body.get("prompt_max_tokens"), body.get("context_length"))
    body["prompt_max_tokens"] = value if value > 0 else 168000

    # 会话标识：按（账号, 种子）在复用窗口内映射到同一场会话，过期后开新会话
    # ——对齐官方客户端「一场会话属于一个账号、重试同会话、重新提问新会话」的语义。
    ident = session_ident(account, session_seed(body))
    body["conversation_id"] = ident
    body["session_id"] = ident
    body["project_id"] = random_uuid()

    user_input = last_user_text(body)
    if user_input:
        body["user_input"] = user_input
    body["user_id"] = account.uid
    body["device_id"] = account.device_id
    body["machine_id"] = account.machine_id
    body["workspace_id"] = workspace_id_for(account)
    body["mode"] = "FunctionCall"
    body["ide_version"] = plugin.eps.ide_version
    body["ide_version_code"] = plugin.eps.ide_version_code
    body["app_id"] = plugin.eps.app_id
    body["package_type"] = "stable_cn"


def lower_ascii(text):
    """只把 ASCII 大写字母转小写，保证字节长度不变。

    不用 str.lower()：它对个别 Unicode 字符会改变字节长度，让后续按字节切片的索引错位。
    """
    return "".join(chr(ord(c) + 32) if "A" <= c <= "Z" else c for c in text)


def _is_tag_end(ch):
    return ch in (">", " ", "\t", "\n", "\r")


def strip_blocks(text, name):
    """删除 text 中所有 <name …>…</name> 块（开标签可带属性，按最近的同名闭标签配对）。"""
    lower = lower_ascii(text)
    open_tag = "<" + name
    close_tag = "</" + name + ">"
    out = []
    i = 0
    while i < len(text):
        oi = lower.find(open_tag, i)
        if oi < 0:
            out.append(text[i:])
            break
        after = oi + len(open_tag)
        if after < len(text) and not _is_tag_end(lower[after]):
            out.append(text[i:after])
            i = after
            continue
        gt = lower.find(">", after)
        if gt < 0:
            out.append(text[i:])
            break
        body_start = gt + 1
        ci = lower.find(close_tag, body_start)
        if ci < 0:
            out.append(text[i:])  # 没闭合：其余原样保留
            break
        out.append(text[i:oi])
        i = ci + len(close_tag)
    return "".join(out)


def ask_text(message):
    """提取一条 user 消息里的「当次提问」。

    剥掉包装块后，若剩下的是被一对同名标签包裹的内容则取其内部，否则整段即提问。
    结构异常（无闭合、内部又嵌了同名闭标签）一律返回空串——宁可不上报，也不猜错。
    """
    text = message_text(message)
    for name in PACKAGING_TAGS:
        text = strip_blocks(text, name)
    text = text.strip()
    if not text or not text.startswith("<"):
        return text
    lower = lower_ascii(text)
    gt = lower.find(">")
    if gt < 0:
        return ""
    name = lower[1:gt].strip()
    for sep in (" ", "\t", "\r", "\n"):
        pos = name.find(sep)
        if pos >= 0:
            name = name[:pos]
    if not name or name in PACKAGING_TAGS:
        return ""
    close_tag = "</" + name + ">"
    if not lower.endswith(close_tag):
        return ""
    inner = text[gt + 1: len(text) - len(close_tag)]
    if close_tag in lower_ascii(inner):
        return ""  # 内部还有同名闭标签：结构异常，不猜
    return inner.strip()


def session_seed(body):
    """计算「一轮提问」的稳定种子：优先客户端自带的会话标识，否则取最后一条 user 消息的提问文本。"""
    for key in ("session_id", "conversation_id"):
        value = body.get(key)
        if isinstance(value, str) and value.strip():
            return "named:" + value
    messages = body.get("messages")
    if not isinstance(messages, list):
        return ""
    for message in reversed(messages):
        if not isinstance(message, dict):
            continue
        if message.get("role") != "user":
            continue
        text = ask_text(message)
        if text:
            return "ask:" + text
    return ""


def message_text(message):
    """取一条 message 的纯文本 content（兼容字符串与数组两种形态）。"""
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = []
        for part in content:
            if isinstance(part, dict):
                value = part.get("text")
                if isinstance(value, str):
                    parts.append(value)
        return "".join(parts)
    return ""


def last_user_text(body):
    """取「当次提问」纯文本，对应官方 user_input（上游拿它做会话标题）。"""
    messages = body.get("messages")
    if not isinstance(messages, list):
        return ""
    for message in reversed(messages):
        if not isinstance(message, dict):
            continue
        if message.get("role") != "user":
            continue
        text = ask_text(message)
        if text:
            return text
    return ""


def prepare_messages(body):
    """把 OpenAI messages 的 content 字符串改写为上游要求的 [{type:text,text}]。"""
    messages = body.get("messages")
    if not isinstance(messages, list):
        return
    for message in messages:
        if not isinstance(message, dict):
            continue
        # assistant 回传的 tool_calls：OpenAI 的 function → 上游的 function_call
        if message.get("role") == "assistant":
            calls = message.get("tool_calls")
            if isinstance(calls, list):
                kept = []
                for call in calls:
                    if not isinstance(call, dict):
                        continue
                    fn = call.get("function")
                    if isinstance(fn, dict):
                        call["function_call"] = fn
                        call.pop("function", None)
                    fc = call.get("function_call")
                    if isinstance(fc, dict):
                        name = fc.get("name")
                        if not (isinstance(name, str) and name.strip()):
                            continue  # 上游要求 FunctionCall.Name 必填
                    kept.append(call)
                if kept:
                    message["tool_calls"] = kept
                else:
                    message.pop("tool_calls", None)
        content = message.get("content")
        if content is None:
            continue
        if isinstance(content, str):
            message["content"] = [{"type": "text", "text": content}]


def normalize_tool_choice(body):
    """按上游 Go struct（字符串类型）改写 OpenAI tool_choice。"""
    def suppress():
        body.pop("tools", None)
        body.pop("functions", None)

    if "tool_choice" not in body:
        return
    choice = body["tool_choice"]
    if isinstance(choice, str):
        if choice.strip().lower() == "none":
            body.pop("tool_choice", None)
            suppress()
        return
    if isinstance(choice, dict):
        typ = (choice.get("type") or "")
        typ_lower = typ.strip().lower()
        if typ_lower == "none":
            body.pop("tool_choice", None)
            suppress()
        elif typ_lower in ("auto", "required"):
            body["tool_choice"] = typ_lower
        elif typ_lower == "function":
            name = ""
            fn = choice.get("function")
            if isinstance(fn, dict):
                value = fn.get("name")
                if isinstance(value, str):
                    name = value
            if not name:
                value = choice.get("name")
                if isinstance(value, str):
                    name = value
            name = name.strip()
            body["tool_choice"] = name if name else "auto"
        else:
            body.pop("tool_choice", None)
        return
    body.pop("tool_choice", None)


def normalize_tools(body):
    """把 OpenAI tools 的 function.parameters（object）序列化为上游要求的字符串。"""
    raw = body.get("tools")
    if not isinstance(raw, list) or not raw:
        return
    out = []
    for item in raw:
        if not isinstance(item, dict):
            continue
        fn = item.get("function")
        if not isinstance(fn, dict):
            continue
        params = fn.get("parameters")
        if isinstance(params, dict):
            fn["parameters"] = json.dumps(params, ensure_ascii=False)
        out.append(item)
    if out:
        body["tools"] = out
    else:
        body.pop("tools", None)


