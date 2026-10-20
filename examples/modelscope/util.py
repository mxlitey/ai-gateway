# -*- coding: utf-8 -*-
"""通用小工具：文本截断 / 脱敏 / JSON 取值 / 原子落盘 / 时间。"""

import os
import re
import secrets
import time

# 疑似凭据的长串（连续 24+ 个 URL 安全字符），用于 last_error 脱敏。
_TOKEN_LIKE = re.compile(r"[A-Za-z0-9_\-./+=]{24,}")


def first_non_empty(*values):
    for value in values:
        if isinstance(value, str) and value.strip():
            return value
    return ""


def truncate_runes(text, limit):
    """按字符数截断（不会把中文截成半个字符）。"""
    text = (text or "").strip()
    if limit <= 0:
        return ""
    return text[:limit] if len(text) > limit else text


def clamp_bytes(text, limit):
    """截断到不超过 limit 个 UTF-8 字节（按字符边界切）。"""
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


def sanitize_error(msg):
    """脱敏错误文案：抹掉疑似 token 片段并压到 100 字内。"""
    msg = (msg or "").strip()
    if not msg:
        return ""
    return truncate_runes(_TOKEN_LIKE.sub("***", msg), 100)


def now_millis():
    return int(time.time() * 1000)


def now_seconds():
    return int(time.time())


def new_state():
    """登录会话 state（URL 安全、每次不同、不承载凭据语义）。"""
    return secrets.token_hex(16)


# ---------- JSON 取值 ----------


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


def to_float(value):
    return to_float_ok(value)[0]


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


def dig_value(value, key):
    """任意深度按（不区分大小写的）键名查找值，兼容上游结构变动。"""
    if isinstance(value, dict):
        for k, v in value.items():
            if str(k).lower() == key.lower():
                return v, True
        for v in value.values():
            found, ok = dig_value(v, key)
            if ok:
                return found, True
    elif isinstance(value, list):
        for item in value:
            found, ok = dig_value(item, key)
            if ok:
                return found, True
    return None, False


def dig_number(value, key):
    found, ok = dig_value(value, key)
    return to_float_ok(found) if ok else (0.0, False)


# ---------- 落盘 ----------


def atomic_write(path, raw, mode=0o600):
    """tmp + rename 原子写：进程被强杀时不会留下半个文件。"""
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
