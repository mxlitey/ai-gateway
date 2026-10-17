# -*- coding: utf-8 -*-
"""桌面端 at-rest 加密（suite 1）的信封解析与解密（对应 Go 版 atrest.go）。

算法（与客户端一致，AAD 必须逐字节相同，否则 GCM 校验必然失败）：

    key    = sha256(atRestSecretKey 的 44 字符 base64 字符串)
    keyId  = sha256(key).hex[:16]
    AAD    = "WB-AAD\\0" | 0x01 | LP(magic) | LP("sym-v1") | u32(suite) | LP(keyId) | code | 0x00 | 0x00
    信封   = base64(JSON{suite,keyId,nonce,authTag,ciphertext})，AES-256-GCM

LP(s) = u32be(utf8 长度) + utf8(s)
"""

import base64
import hashlib
import json
import struct

import aesgcm

ENVELOPE_KEY_NAME = "$wbEncrypted"
SCHEME_NAME = "sym-v1"
SUITE_V1 = 1

# frame 表示信封所处的层级，两层用了不同的魔数与编码：
# 文件级（keyblob 的 wrapped）与字段级（.info 里的 accessToken 等）。
FRAME_FILE = ("WBEF1", 1)
FRAME_FIELD = ("WBEV1", 2)


def _lp_bytes(text):
    raw = text.encode("utf-8")
    return struct.pack(">I", len(raw)) + raw


def _u32be(value):
    return struct.pack(">I", value & 0xFFFFFFFF)


def build_aad(key_id, suite, frame):
    magic, code = frame
    out = b"WB-AAD\x00"
    out += b"\x01"
    out += _lp_bytes(magic)
    out += _lp_bytes(SCHEME_NAME)
    out += _u32be(suite)
    out += _lp_bytes(key_id)
    out += bytes([code, 0x00, 0x00])
    return out


def derive_key_id(key):
    """sha256(key) 的前 16 个 hex，即客户端口中的 keyId。"""
    return hashlib.sha256(key).hexdigest()[:16]


def is_envelope(value):
    """判断某个 JSON 值是不是 $wbEncrypted 信封（而不是明文）。"""
    if isinstance(value, dict):
        if "envelope" not in value:
            return False
        return ENVELOPE_KEY_NAME in value
    if isinstance(value, str):
        head = value.lstrip(" \t\r\n")
        if not head.startswith("{"):
            return False
        return ENVELOPE_KEY_NAME in head[:64]
    return False


def _b64_decode_field(value, name):
    try:
        return base64.b64decode(_as_text(value), validate=True)
    except Exception as exc:
        raise ValueError("信封字段 %s 不是合法 base64: %s" % (name, exc))


def _as_text(value):
    if isinstance(value, str):
        return value
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def parse_envelope(value):
    """解析信封，返回 (suite, key_id, nonce, tag, ciphertext)。"""
    raw = value.get("envelope") if isinstance(value, dict) else value
    raw = _as_text(raw)
    if not raw.strip():
        raise ValueError("信封为空")
    try:
        decoded = base64.b64decode(raw, validate=True)
    except Exception as exc:
        raise ValueError("信封不是合法 base64: %s" % exc)
    try:
        doc = json.loads(decoded.decode("utf-8"))
    except Exception as exc:
        raise ValueError("信封不是合法 JSON: %s" % exc)
    if not isinstance(doc, dict):
        raise ValueError("信封不是合法 JSON 对象")

    suite = SUITE_V1
    num = doc.get("suite")
    if isinstance(num, (int, float)) and not isinstance(num, bool) and num > 0:
        suite = int(num)
    key_id = _as_text(doc.get("keyId")).strip().lower()

    nonce = _b64_decode_field(doc.get("nonce"), "nonce")
    tag = _b64_decode_field(doc.get("authTag"), "authTag")
    ciphertext = _b64_decode_field(doc.get("ciphertext"), "ciphertext")

    if len(nonce) != 12 or len(tag) != 16:
        raise ValueError("信封 nonce(%d)/authTag(%d) 长度不符" % (len(nonce), len(tag)))
    return suite, key_id, nonce, tag, ciphertext


def open_envelope(key, value, frame):
    """用 key 解开一个信封，返回明文字节。"""
    suite, key_id, nonce, tag, ciphertext = parse_envelope(value)

    expect = derive_key_id(key)
    if not key_id:
        key_id = expect
    elif key_id != expect:
        raise ValueError("信封 keyId(%s) 与密钥(%s) 不匹配" % (key_id, expect))

    try:
        plain = aesgcm.AESGCM(key).decrypt(nonce, ciphertext, tag, build_aad(key_id, suite, frame))
    except ValueError as exc:
        raise ValueError("GCM 解密失败（密钥或 AAD 不匹配）: %s" % exc)
    return plain


def decrypt_field(key, value):
    """解密 .info 里的一个字段；第二个返回值表示结果是否可信。

    字段本来可能是明文（老版本客户端），这种情况原样返回。
    """
    if value is None:
        return "", True
    if not is_envelope(value):
        if isinstance(value, str):
            return value, True
        return "", False
    try:
        plain = open_envelope(key, value, FRAME_FIELD)
    except ValueError:
        return "", False
    return plain.decode("utf-8", "replace"), True
