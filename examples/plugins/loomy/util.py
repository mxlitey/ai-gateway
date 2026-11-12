# -*- coding: utf-8 -*-
"""通用小工具：文本截断 / 脱敏 / JSON 取值 / 原子落盘 / 时间。"""

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


def sanitize_error(msg):
    msg = (msg or "").strip()
    if not msg:
        return ""
    return truncate_runes(_TOKEN_LIKE.sub("***", msg), 100)


def now_millis():
    return int(time.time() * 1000)


def new_state():
    return secrets.token_hex(16)


def mask_phone(value, visible_tail=2):
    """手机号统一掩码为只露末 2 位；非手机号形态原样返回（幂等）。

    只对「像手机号」的输入生效：11 位纯数字（1 开头），或 `3 位数字 + 星号 + 数字`
    的已脱敏形态。真实昵称（如 `用户26815487395`）绝不误伤。
    """
    import re

    text = (value or "").strip()
    if not text:
        return text

    def rebuild(prefix, total_length, tail):
        stars = total_length - len(prefix) - len(tail)
        return text if stars < 0 else prefix + "*" * stars + tail

    if re.fullmatch(r"1\d{10}", text):
        return rebuild(text[:3], len(text), text[-visible_tail:])
    masked = re.fullmatch(r"(\d{3})(\*+)(\d+)", text)
    if masked:
        prefix, stars, suffix = masked.groups()
        return rebuild(prefix, len(prefix) + len(stars) + len(suffix), suffix[-visible_tail:])
    return text


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
