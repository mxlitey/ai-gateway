# -*- coding: utf-8 -*-
"""思考强度（reasoning_effort / thinking）改写。

移植自参考实现（AgentHub proxy/util.cjs，其源头为参考项目的 thinking.go）：

  - 档位降级：模型目录（/v3/config）声明了 supportedEfforts 时，把请求的 reasoning_effort
    收敛到受支持档位——只认 high 的模型收到 low 会 400，所以必须先降级再发上游；
  - deepseek 注入：deepseek 系必须显式 thinking:{type:enabled} 才会给思维链，
    且缺 reasoning_effort 时要补上目录里的默认档；
  - deepseek 回填：会话里出现 reasoning 痕迹时，所有 assistant 消息都要带 reasoning_content
    （可为空串），否则上游对后续每条消息都 400。

非 deepseek 模型只做档位降级；"off" 是「关思考」开关而不是档位，原样保留（真不认 off
的上游由网关侧按渠道改写，不该在这层被静默抬成最低档继续思考）。
"""

# 档位排序词表：唯一出处，避免别处再抄一份字面量数组
EFFORT_RANK = {"off": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}


def is_deepseek_model(model):
    return str(model or "").strip().lower().startswith("deepseek")


def inject_thinking(obj, default_effort):
    """deepseek 思维链开关注入。

    显式 disabled：尊重客户端「关思考」并撤掉 effort；
    显式 enabled：缺 effort 时补默认档；
    无 thinking 字段：注入 enabled + 补默认档。非 deepseek 零改动。
    """
    if not isinstance(obj, dict) or not is_deepseek_model(obj.get("model")):
        return
    dft = str(default_effort or "") or "high"
    thinking = obj.get("thinking")
    if not isinstance(thinking, dict):
        thinking = None
    typ = str(thinking.get("type") or "").strip() if thinking else ""

    def ensure_effort():
        if obj.get("reasoning_effort") is not None or obj.get("reasoningEffort") is not None:
            return
        obj["reasoning_effort"] = dft

    if typ:
        if typ.lower() == "disabled":
            obj.pop("reasoning_effort", None)
            obj.pop("reasoningEffort", None)
            return
        ensure_effort()
        return
    if thinking is not None:
        thinking["type"] = "enabled"
    else:
        obj["thinking"] = {"type": "enabled"}
    ensure_effort()


def normalize_reasoning_effort(obj, meta):
    """按模型目录 supportedEfforts 收敛档位。

    目录没声明支持档 / 请求档不在词表里 / 请求档已受支持时都不动；
    否则降到「≤ 请求档的最高支持档」，支持档全高于请求档时取最低档。
    """
    supported = []
    if isinstance(meta, dict):
        raw = meta.get("supportedEfforts")
        if isinstance(raw, list):
            supported = [str(s) for s in raw if str(s) in EFFORT_RANK]
    if not supported or not isinstance(obj, dict):
        return
    cur = obj.get("reasoning_effort")
    if not isinstance(cur, str) or cur not in EFFORT_RANK:
        return
    if cur == "off":
        return
    uniq = sorted(set(supported), key=lambda s: EFFORT_RANK[s])
    if cur in uniq:
        return
    cur_rank = EFFORT_RANK[cur]
    lower = [s for s in uniq if EFFORT_RANK[s] <= cur_rank]
    obj["reasoning_effort"] = lower[-1] if lower else uniq[0]


def backfill_reasoning_content(obj):
    """deepseek 多轮回填：会话含 reasoning 痕迹时，每条 assistant 消息都要有 reasoning_content。"""
    if not isinstance(obj, dict) or not is_deepseek_model(obj.get("model")):
        return
    messages = obj.get("messages")
    if not isinstance(messages, list) or not messages:
        return

    def has_trace(message):
        if not isinstance(message, dict):
            return False
        if isinstance(message.get("reasoning"), str) and message["reasoning"]:
            return True
        return "reasoning_content" in message

    if not any(has_trace(message) for message in messages):
        return
    for message in messages:
        if not isinstance(message, dict) or message.get("role") != "assistant":
            continue
        if "reasoning_content" in message:
            continue
        reasoning = message.get("reasoning")
        message["reasoning_content"] = reasoning if isinstance(reasoning, str) else ""


def apply(obj, meta):
    """对一次转发请求执行完整思考强度管线（就地修改 obj）。"""
    inject_thinking(obj, (meta or {}).get("defaultEffort") if isinstance(meta, dict) else "")
    if is_deepseek_model(obj.get("model")):
        backfill_reasoning_content(obj)
    normalize_reasoning_effort(obj, meta)
    # 显式空档位最后删除（上游把空串当非法档位）
    for key in ("reasoning_effort", "reasoningEffort"):
        if obj.get(key) is not None and not obj.get(key):
            obj.pop(key, None)
