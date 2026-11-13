# -*- coding: utf-8 -*-
"""通用小工具：文本截断 / 脱敏 / JSON 取值 / 原子落盘 / 时间 / 字母序序列化。"""

import os
import re
import secrets
import time

_TOKEN_LIKE = re.compile(r"[A-Za-z0-9_\-./+=]{24,}")


def first_non_empty(*values):
    for value in values:
        if isinstance(value, str) and value.strip():
            return value
    return ""


def truncate_runes(text, limit):
    text = (text or "").strip()
    if limit <= 0:
        return ""
    return text[:limit] if len(text) > limit else text


def clamp_bytes(text, limit):
    text = (text or "").strip()
    if len(text.encode("utf-8")) <= limit:
        return text
    out, total = [], 0
    for ch in text:
        size = len(ch.encode("utf-8"))
        if total + size > limit:
            break
        out.append(ch)
        total += size
    return "".join(out)


# plugin-spec §5.3：机器判定类型，必须与状态码一一对应
_ERROR_TYPES = {
    400: "invalid_request",
    401: "auth_failed",
    403: "forbidden",
    404: "not_found",
    429: "rate_limited",
    500: "plugin_error",
}


def error_type(status):
    """把状态码映射成错误体里的 `type`（plugin-spec §5.3）。"""
    if status in _ERROR_TYPES:
        return _ERROR_TYPES[status]
    return "upstream_error" if status >= 500 else "invalid_request"


def error_body(status, msg, limit=240):
    """统一错误体：`error` 给人看（按字节截断），`type` 给机器判定。"""
    return {"error": clamp_bytes(msg, limit), "type": error_type(status)}


def console_status(status):
    """控制台状态码：401/403/404 一律折成 400（plugin-spec §6.2）。"""
    return 400 if status in (401, 403, 404) else status


def sanitize_error(msg):
    msg = (msg or "").strip()
    if not msg:
        return ""
    return truncate_runes(_TOKEN_LIKE.sub("***", msg), 100)


def now_millis():
    return int(time.time() * 1000)


def new_state():
    return secrets.token_hex(16)


def as_str(value):
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, float):
        return str(int(value)) if value.is_integer() else repr(value)
    if isinstance(value, int):
        return str(value)
    return str(value)


def to_float_ok(value):
    if isinstance(value, bool):
        return 0.0, False
    if isinstance(value, (int, float)):
        return float(value), True
    if isinstance(value, str):
        try:
            return float(value.strip()), True
        except ValueError:
            return 0.0, False
    return 0.0, False


def number_field(obj, key):
    if not isinstance(obj, dict):
        return 0
    value = obj.get(key)
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)):
        return int(value)
    if isinstance(value, str):
        try:
            return int(float(value.strip()))
        except ValueError:
            return 0
    return 0


def parse_timestamp_ms(value):
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)):
        if value <= 0:
            return 0
        return int(value * 1000) if value < 1e12 else int(value)
    if isinstance(value, str) and value.strip():
        text = value.strip()
        if text.isdigit():
            num = int(text)
            return num * 1000 if num < 1e12 else num
        try:
            from datetime import datetime

            return int(datetime.fromisoformat(text.replace("Z", "+00:00")).timestamp() * 1000)
        except Exception:
            return 0
    return 0


def atomic_write(path, raw, mode=0o600):
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    try:
        with os.fdopen(fd, "wb") as fh:
            fh.write(raw)
    except Exception:
        try:
            os.remove(tmp)
        except OSError:
            pass
        raise
    os.replace(tmp, path)


# ---------- 字母序序列化（Gemini 信封） ----------


def _sorted_value(value):
    """递归按键名升序重建值（数组保持顺序）。"""
    if isinstance(value, list):
        return [_sorted_value(item) for item in value]
    if not isinstance(value, dict):
        return value
    return {key: _sorted_value(value[key]) for key in sorted(value.keys())}


def sorted_stringify(value):
    """按键名升序序列化。

    ⚠️ 这不是为了好看：上游看到的是**字节**。Go 的 `encoding/json` 对 map 键
    自动字母序排，而 Python 的 `json.dumps` 保持插入序 —— 不排序就与官方客户端
    发出的字节不同，信封可能被判定为非官方客户端。
    """
    import json

    return json.dumps(_sorted_value(value), ensure_ascii=False, separators=(",", ":"))


def sorted_dumps_bytes(value):
    return sorted_stringify(value).encode("utf-8")
