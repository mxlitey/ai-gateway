# -*- coding: utf-8 -*-
"""SOLO 自定义 SSE → OpenAI 的转换（对应 Go 版 solosse.go）。

上游事件序列（实测）：
    event:metadata      data:{"model":"","session_id":...}
    event:timing_cost   data:{"name":"llm_raw_chat_v2",...}
    event:notify_usage  data:{"billing_mode":"credits","cn_credits_remain_info":{"ide_credits":N,...}}
    event:output        data:{"response":"<增量>","reasoning_content":"<思考增量>","tool_calls":...}  ×N
    event:token_usage   data:{"prompt_tokens":..,"completion_tokens":..,"total_tokens":..}
    event:done          data:{"finish_reason":"stop"}
    event:error         data:{"code":4008,"message":"..."}

上游只接受流式，所以插件一律以 stream=true 请求上游：客户端要流式就边收边发，
要非流式就在本地把 SSE 聚合成一个完整 chat.completion。
"""

import json
import time

from upstream import UpstreamError, classify_code, classify_upstream
from util import number_field, truncate_runes


class SoloEvent:
    __slots__ = ("event", "response", "reasoning", "tool_calls", "usage",
                 "notify", "finish_reason", "error_code", "error_message")

    def __init__(self, event=""):
        self.event = event
        self.response = ""
        self.reasoning = ""
        self.tool_calls = None      # 已序列化的原始 JSON（dict/list/None）
        self.usage = None
        self.notify = None
        self.finish_reason = ""
        self.error_code = 0
        self.error_message = ""


def parse_solo_event(event_name, data_line):
    """解析一条事件。事件名归一化：thought → output、turn_completion → done。"""
    ev = SoloEvent((event_name or "").strip())
    if not (data_line or "").strip():
        return ev
    try:
        raw = json.loads(data_line)
    except Exception:
        return ev
    if not isinstance(raw, dict):
        return ev

    # 事件名归一化（AgentHub 实证）：上游把思考增量发在 event:thought（与 output 同构）、
    # 收尾发在 event:turn_completion（等价于 done）。
    if ev.event == "thought":
        ev.event = "output"
    elif ev.event == "turn_completion":
        ev.event = "done"

    if ev.event == "output":
        value = raw.get("response")
        if isinstance(value, str):
            ev.response = value
        value = raw.get("reasoning_content")
        if isinstance(value, str):
            ev.reasoning = value
        if "tool_calls" in raw:
            ev.tool_calls = raw.get("tool_calls")
    elif ev.event == "token_usage":
        ev.usage = raw
    elif ev.event == "notify_usage":
        ev.notify = raw
    elif ev.event == "done":
        value = raw.get("finish_reason")
        if isinstance(value, str):
            ev.finish_reason = value
        if not ev.finish_reason:
            value = raw.get("finishReason")
            if isinstance(value, str):
                ev.finish_reason = value
    elif ev.event == "error":
        ev.error_code = number_field(raw, "code")
        value = raw.get("message")
        if isinstance(value, str):
            ev.error_message = value
    return ev


class SSEScan:
    """维护跨行的 event/data 累积。

    data 是跨行**直接拼接**（无分隔符）；空行是事件边界。
    """

    def __init__(self):
        self.event = ""
        self.data = ""

    def reset(self):
        self.event = ""
        self.data = ""

    def feed(self, line):
        """处理一行，返回事件边界处解析出的事件（无则 None）。"""
        if line == "":
            if not self.event:
                self.reset()
                return None
            ev = parse_solo_event(self.event, self.data)
            self.reset()
            return ev
        if line.startswith("event:"):
            self.event = line[len("event:"):].strip()
        elif line.startswith("data:"):
            self.data += line[len("data:"):].strip()
        elif line.startswith(":"):
            pass  # 注释行忽略
        return None


# ---------- 非流式：聚合 ----------


def aggregate_solo(reader, model):
    """读完整 SOLO SSE，聚合成一个 OpenAI chat.completion。

    返回 (响应 dict 或 None, credits 或 None, UpstreamError 或 None, 异常或 None)。
    """
    content = []
    reasoning = []
    finish_reason = "stop"
    usage = None
    credits = None
    stream_err = None
    tool_calls = {}
    tool_order = []

    scan = SSEScan()
    while True:
        line = reader.readline()
        if not line:
            break
        ev = scan.feed(line.decode("utf-8", "replace").rstrip("\r\n"))
        if ev is not None:
            if ev.event == "output":
                content.append(ev.response)
                reasoning.append(ev.reasoning)
                merge_tool_call_json(tool_calls, tool_order, ev.tool_calls)
            elif ev.event == "token_usage":
                usage = ev.usage
            elif ev.event == "notify_usage":
                value, src = _extract_ide_credits(ev.notify)
                if src:
                    credits = value
            elif ev.event == "done":
                if ev.finish_reason:
                    finish_reason = ev.finish_reason
            elif ev.event == "error":
                stream_err = code_to_upstream_error(ev.error_code, ev.error_message)

    if stream_err is not None:
        return None, credits, stream_err, None

    message = {"role": "assistant", "content": "".join(content)}
    reasoning_text = "".join(reasoning)
    if reasoning_text:
        message["reasoning_content"] = reasoning_text
    if tool_order:
        tool_order.sort()
        message["tool_calls"] = [tool_calls[idx] for idx in tool_order]

    resp = {
        "id": "chatcmpl-%d" % (time.time_ns()),
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model,
        "choices": [{
            "index": 0,
            "message": message,
            "finish_reason": finish_reason,
        }],
    }
    if usage is not None:
        resp["usage"] = usage
    return resp, credits, None, None


def code_to_upstream_error(code, msg):
    """把流内 event:error 的业务码映射为切号语义错误。"""
    mapped = classify_code(code)
    if mapped is not None:
        return mapped
    text = truncate_runes(msg, 80)
    if not text:
        text = "上游返回错误 code=%d" % code
    return UpstreamError(502, text)


# ---------- 流式：SOLO SSE → OpenAI SSE ----------


def stream_solo(writer, reader, model):
    """把 SOLO SSE 边收边转成 OpenAI chat.completion.chunk 写给 writer。

    返回 (wrote_any, credits, stream_err, io_err)：
      - wrote_any 表示是否已写出过任何 chunk；若流在写出任何 chunk 之前就遇到上游错误，
        调用方仍可改用非 2xx 状态码（此时尚未写响应头）。
    """
    ident = "chatcmpl-%d" % (time.time_ns())
    pending_usage = {"value": None}
    saw_done = False
    wrote_any = False
    credits = None

    def write_chunk(delta, finish=""):
        choice = {"index": 0, "delta": delta}
        if finish:
            choice["finish_reason"] = finish
        chunk = {
            "id": ident,
            "object": "chat.completion.chunk",
            "created": int(time.time()),
            "model": model,
            "choices": [choice],
        }
        if pending_usage["value"] is not None:
            chunk["usage"] = pending_usage["value"]
            pending_usage["value"] = None
        raw = json.dumps(chunk, ensure_ascii=False)
        writer.write(("data: " + raw + "\n\n").encode("utf-8"))
        writer.flush()

    def write_done():
        writer.write(b"data: [DONE]\n\n")
        writer.flush()

    scan = SSEScan()
    while True:
        line = reader.readline()
        if not line:
            break
        ev = scan.feed(line.decode("utf-8", "replace").rstrip("\r\n"))
        if ev is not None:
            if ev.event == "output":
                delta = {}
                if ev.response:
                    delta["content"] = ev.response
                if ev.reasoning:
                    delta["reasoning_content"] = ev.reasoning
                if ev.tool_calls is not None and ev.tool_calls != "null":
                    calls = normalize_stream_tool_calls(ev.tool_calls)
                    if calls is not None:
                        delta["tool_calls"] = calls
                if delta:
                    write_chunk(delta)
                    wrote_any = True
            elif ev.event == "token_usage":
                pending_usage["value"] = ev.usage
            elif ev.event == "notify_usage":
                value, src = _extract_ide_credits(ev.notify)
                if src:
                    credits = value
            elif ev.event == "done":
                write_chunk({}, ev.finish_reason)
                wrote_any = True
                write_done()
                saw_done = True
            elif ev.event == "error":
                se = code_to_upstream_error(ev.error_code, ev.error_message)
                if not wrote_any:
                    # 还没写出任何内容：交给调用方用非 2xx + {'error':...} 回复
                    return False, credits, se, None
                msg = "上游错误：%s" % se.msg
                writer.write(("event: error\ndata: " + json.dumps(msg, ensure_ascii=False) + "\n\n").encode("utf-8"))
                writer.flush()
                write_done()
                saw_done = True

    if not saw_done:
        # 上游中断（无 done）：兜底补一个 [DONE]，保证客户端能正常收尾
        write_done()
    return wrote_any, credits, None, None


def normalize_stream_tool_calls(raw):
    """把上游的 function_call 字段规范成 OpenAI 的 function。"""
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except Exception:
            return None
    if isinstance(raw, dict):
        raw = [raw]
    if not isinstance(raw, list):
        return None
    out = []
    for call in raw:
        if not isinstance(call, dict):
            continue
        call = dict(call)
        if isinstance(call.get("function_call"), dict):
            call["function"] = call.pop("function_call")
        fn = call.get("function")
        if isinstance(fn, dict):
            fn.pop("namespace", None)
            fn.pop("partial_arguments", None)
        out.append(call)
    return out or None


# ---------- tool_calls 合并（非流式聚合用） ----------


def merge_tool_call_json(tool_calls, tool_order, raw):
    if raw is None or raw == "null":
        return
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except Exception:
            return
    if isinstance(raw, dict):
        arr = [raw]
    elif isinstance(raw, list):
        arr = raw
    else:
        return
    for call in arr:
        if not isinstance(call, dict):
            continue
        idx = int(call.get("index") or 0)
        merged = tool_calls.get(idx)
        if merged is None:
            merged = {"index": idx}
            tool_calls[idx] = merged
            tool_order.append(idx)
        merge_tool_call_delta(merged, call)


def merge_tool_call_delta(merged, delta):
    """把流式 tool_call 片段合并进累计对象：id/type/name 直覆盖，arguments 拼接。"""
    value = delta.get("id")
    if isinstance(value, str) and value:
        merged["id"] = value
    value = delta.get("type")
    if isinstance(value, str) and value:
        merged["type"] = value
    df = delta.get("function")
    if not isinstance(df, dict):
        df = delta.get("function_call")
    if not isinstance(df, dict):
        return
    df.pop("namespace", None)
    df.pop("partial_arguments", None)
    mf = merged.get("function")
    if not isinstance(mf, dict):
        mf = {}
        merged["function"] = mf
    value = df.get("name")
    if isinstance(value, str) and value:
        mf["name"] = value
    value = df.get("arguments")
    if isinstance(value, str) and value:
        prev = mf.get("arguments")
        mf["arguments"] = (prev + value) if isinstance(prev, str) and prev else value


def _extract_ide_credits(doc):
    """从 notify_usage 里提取 ide_credits（不兜底通用 credits 字段）。"""
    from util import deep_find_number

    if not isinstance(doc, dict) or not doc:
        return 0.0, ""
    value, ok = deep_find_number(doc, "ide_credits")
    if ok:
        return value, "ide_credits"
    value, ok = deep_find_number(doc, "ideCredits")
    if ok:
        return value, "ide_credits"
    return 0.0, ""
