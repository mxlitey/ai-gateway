# -*- coding: utf-8 -*-
"""通用小工具（对应 Go 版散落在各文件里的 helper）。"""

import re
import secrets
import time

# 疑似凭据的长串（连续 24+ 个 URL 安全字符），用于 last_error 脱敏。
_TOKEN_LIKE = re.compile(r"[A-Za-z0-9_\-./+=]{24,}")


def first_non_empty(*values):
    """返回第一个去空白后非空的字符串。"""
    for value in values:
        if isinstance(value, str) and value.strip():
            return value
    return ""


def as_str(value):
    """把 JSON 值转成字符串（数字按原样、None 变空串）。"""
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, float):
        if value.is_integer():
            return str(int(value))
        return repr(value)
    if isinstance(value, int):
        return str(value)
    return str(value)


def to_float(value):
    """把 JSON 值转成 float（非数值返回 0）。"""
    if isinstance(value, bool):
        return 1.0 if value else 0.0
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        try:
            return float(value.strip())
        except ValueError:
            return 0.0
    return 0.0


def truncate_runes(text, limit):
    """按字符数截断，避免把多字节中文截成半个字符。"""
    text = (text or "").strip()
    if limit <= 0:
        return ""
    return text[:limit]


def clamp_bytes(text, limit):
    """截断到不超过 limit 个 UTF-8 字节（按字符边界切，不会截出半个汉字）。"""
    text = (text or "").strip()
    if len(text.encode("utf-8")) <= limit:
        return text
    out = []
    total = 0
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


# ---------- 展示 ----------


def short_uid(uid):
    uid = uid or ""
    return uid[:8] if len(uid) > 8 else uid


def site_label(site):
    return "国际" if site == "intl" else "国内"


def default_domain(site):
    return "www.workbuddy.ai" if site == "intl" else "www.codebuddy.cn"


def mask_token(token):
    """只保留头尾，避免日志里出现完整凭据。"""
    token = token or ""
    if len(token) <= 12:
        return "***"
    return token[:6] + "***" + token[-4:]


def format_time(ms):
    """毫秒时间戳 → 本地时间文案（<=0 显示未知）。"""
    ms = int(ms or 0)
    if ms <= 0:
        return "未知"
    return time.strftime("%Y-%m-%d %H:%M", time.localtime(ms / 1000.0))


def format_credits(value):
    """额度文案：整数不带小数，否则保留两位。"""
    value = float(value or 0)
    if value == int(value):
        return "%d" % int(value)
    return "%.2f" % value


# ---------- JSON 取值 ----------


def decode_data(raw):
    """上游响应外壳里的 data 段（解析失败按空对象处理）。"""
    if isinstance(raw, dict):
        return raw
    return {}


def pick_float(m, *keys):
    """按候选键名取第一个非零数值（上游字段命名不稳定）。"""
    if not isinstance(m, dict):
        return 0.0
    for key in keys:
        if key in m and m[key] not in (None, ""):
            num = to_float(m[key])
            if num != 0:
                return num
    return 0.0


# ---------- 随机 ----------


def random_hex(nbytes):
    """生成 nbytes 字节的随机 hex 串（2n 个 hex 字符）。"""
    return secrets.token_hex(nbytes)
