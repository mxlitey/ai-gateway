# -*- coding: utf-8 -*-
"""对话转发与选号（对应 Go 版 chat.go）。

上游只接受流式（非流式会返回 code=11101），所以统一以 stream=true 请求；
客户端要流式就照原样透传 SSE，要非流式就在本地把 SSE 聚合成一次响应。
"""

import json
import threading
import time

from upstream import chat_headers, do_json, ensure_token, extract_models, realm_config
from util import as_str, first_non_empty, truncate_runes


class NoAccountError(Exception):
    pass


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
