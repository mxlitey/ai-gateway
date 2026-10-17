# -*- coding: utf-8 -*-
"""通用小工具（对应 Go 版分散在各文件里的 helper）。

包含：文本截断 / 脱敏、JSON 取值、随机 id 与指纹生成、时区无关的时间工具。
"""

import base64
import hashlib
import json
import os
import re
import secrets
import time

# 疑似凭据的长串（连续 24+ 个 URL 安全字符），用于 error 脱敏。
_TOKEN_LIKE = re.compile(r"[A-Za-z0-9_\-./+=]{24,}")

# 上游在「今天已签到」时也用成功码返回，文案形如「今日已签到」；
# 按规范「幂等友好」的要求，这种情况必须当成功语义，不能当硬错误。
_CHECKED_IN_RE = re.compile(r"已签到|已经签到|already", re.IGNORECASE)


def first_non_empty(*values):
    """返回第一个去空白后非空的字符串。"""
    for value in values:
        if isinstance(value, str) and value.strip():
            return value
    return ""


def truncate_runes(text, limit):
    """按字符数截断，避免把多字节中文截成半个字符。"""
    text = (text or "").strip()
    if limit <= 0:
        return ""
    if len(text) <= limit:
        return text
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
    """脱敏错误文案：抹掉疑似 token 片段并压到 100 字内，保证 last_error 里绝不出现凭据。"""
    msg = (msg or "").strip()
    if not msg:
        return ""
    msg = _TOKEN_LIKE.sub("***", msg)
    return truncate_runes(msg, 100)


def is_checked_in_message(msg):
    return bool(_CHECKED_IN_RE.search(msg or ""))


def now_millis():
    return int(time.time() * 1000)


def now_seconds():
    return int(time.time())


# ---------- JSON 取值 ----------


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
        return 0.0
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        try:
            return float(value.strip())
        except ValueError:
            return 0.0
    return 0.0


def to_float_ok(value):
    """同 to_float，但返回 (值, 是否成功)。"""
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
    """取整数型字段（兼容数字与数字字符串）。"""
    if not isinstance(obj, dict):
        return 0
    value = obj.get(key)
    if isinstance(value, bool):
        return 0
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        try:
            return int(value.strip())
        except ValueError:
            return 0
    return 0


def get_int64(obj, key):
    if not isinstance(obj, dict):
        return 0
    return number_field(obj, key)


def first_positive(*values):
    """返回第一个为正数的值（非正数 / 非数值忽略）。"""
    for value in values:
        num = to_float(value)
        if num > 0:
            return num
    return 0.0


def dig_value(value, key):
    """在任意深度按（不区分大小写的）键名查找值，兼容上游结构变动。"""
    if isinstance(value, dict):
        for k, v in value.items():
            if k.lower() == key.lower():
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
    if not ok:
        return 0.0, False
    return to_float_ok(found)


def dig_bool(value, key):
    found, ok = dig_value(value, key)
    if not ok:
        return False
    return found is True


def deep_find_number(obj, key):
    """在嵌套 JSON 里按 key 名找第一个数值（键遍历顺序固定，结果可复现）。"""
    if not isinstance(obj, dict) or not obj:
        return 0.0, False
    if key in obj:
        return to_float_ok(obj[key])
    for k in sorted(obj.keys()):
        item = obj[k]
        if isinstance(item, dict):
            value, ok = deep_find_number(item, key)
            if ok:
                return value, True
        elif isinstance(item, list):
            for entry in item:
                if isinstance(entry, dict):
                    value, ok = deep_find_number(entry, key)
                    if ok:
                        return value, True
    return 0.0, False


def parse_json_param(raw):
    """解回调里 URL 编码的 JSON 参数（已解一层，这里再容错解一层）。"""
    if not raw:
        return None
    from urllib.parse import unquote

    candidates = [raw]
    try:
        decoded = unquote(raw)
        if decoded != raw:
            candidates.append(decoded)
    except Exception:
        pass
    for candidate in candidates:
        try:
            obj = json.loads(candidate)
        except Exception:
            continue
        if isinstance(obj, dict):
            return obj
    return None


# ---------- 随机与指纹 ----------


def random_hex(nbytes):
    """生成 nbytes 字节的随机 hex 串（2n 个 hex 字符）。"""
    return secrets.token_hex(nbytes)


def random_uuid():
    """标准 8-4-4-4-12 形态的 v4 UUID。"""
    b = bytearray(secrets.token_bytes(16))
    b[6] = (b[6] & 0x0F) | 0x40
    b[8] = (b[8] & 0x3F) | 0x80
    h = b.hex()
    return "%s-%s-%s-%s-%s" % (h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])


def stable_uuid(seed):
    """从种子确定性派生 8-4-4-4-12 形态的标识（同种子恒等）。"""
    h = hashlib.sha256(seed.encode("utf-8")).hexdigest()[:32]
    return "%s-%s-%s-%s-%s" % (h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])


def random_state():
    """登录会话用的随机 state（URL 安全、不承载凭据语义）。"""
    return random_hex(16)


def new_checkin_device_id():
    """16 位纯数字签到 deviceId（首字符非 0）。

    每个账号必须互异，否则会被上游按「该设备已签到」拦截。
    """
    first = "123456789"[secrets.randbelow(9)]
    rest = "".join("0123456789"[secrets.randbelow(10)] for _ in range(15))
    return first + rest


def uid_from_token(tok):
    """尽力从 accessToken（Cloud-IDE-JWT）解析用户 id，供手工导入兜底。"""
    parts = (tok or "").strip().split(".")
    if len(parts) < 2:
        return ""
    payload = None
    for decoder in (base64.urlsafe_b64decode,):
        try:
            segment = parts[1]
            padded = segment + "=" * (-len(segment) % 4)
            payload = decoder(padded.encode("ascii"))
            break
        except Exception:
            continue
    if payload is None:
        return ""
    try:
        claims = json.loads(payload.decode("utf-8"))
    except Exception:
        return ""
    if not isinstance(claims, dict):
        return ""
    for key in ("sub", "uid", "user_id", "userId", "UserID"):
        value = claims.get(key)
        if isinstance(value, str) and value.strip():
            return value
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            return "%.0f" % float(value)
    return ""


def normalize_expire_seconds(value):
    """把上游的 TokenExpireAt（毫秒）归一化为 Unix 秒。"""
    num = int(value or 0)
    if num > 1_000_000_000_000:
        return num // 1000
    return num


def has_cjk(text):
    return any(0x4E00 <= ord(ch) <= 0x9FFF for ch in text)


def fix_mojibake(text, uid):
    """修复回调 userInfo 中文被双重编码导致的昵称乱码。"""
    text = (text or "").strip()
    if not text:
        return ""
    if not has_cjk(text) and any(ord(ch) > 0x7F for ch in text):
        # 无 CJK 但有非 ASCII（典型乱码）→ 回退为「用户 + uid 末 4 位」。
        suffix = uid[-4:] if len(uid) > 4 else uid
        return "用户" + suffix
    return text


# ---------- 进程信息 ----------


def executable_path():
    """当前进程的可执行文件路径（等价 Go 的 os.Executable()）。"""
    try:
        return os.path.abspath(__file__)
    except Exception:
        return "."
