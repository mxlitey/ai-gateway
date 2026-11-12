# -*- coding: utf-8 -*-
"""JWT 工具（对应 Go 版 jwt.go）。

只读 payload 里的信息（sub=uid、exp、iss），不校验签名。
"""

import base64
import json

from util import as_str, to_float

# 区域判定用的字样：国内版历史上换过出口，三个都要认。
CN_REALM_MARKERS = ("copilot.tencent.com", "codebuddy.cn", "workbuddy.cn")
INTL_REALM_MARKERS = ("workbuddy.ai",)


def jwt_claims(token):
    """解出 JWT 的 payload（失败返回空字典）。"""
    parts = (token or "").split(".")
    if len(parts) < 2:
        return {}
    segment = parts[1]
    padded = segment + "=" * (-len(segment) % 4)
    try:
        raw = base64.urlsafe_b64decode(padded.encode("ascii"))
    except Exception:
        return {}
    try:
        claims = json.loads(raw.decode("utf-8"))
    except Exception:
        return {}
    return claims if isinstance(claims, dict) else {}


def jwt_uid(token):
    return as_str(jwt_claims(token).get("sub"))


def jwt_issuer(token):
    return as_str(jwt_claims(token).get("iss"))


def jwt_exp_millis(token):
    """JWT 的 exp（秒）转成毫秒时间戳。"""
    exp = to_float(jwt_claims(token).get("exp"))
    if exp > 0:
        return int(exp) * 1000
    return 0


def realm_from_token(token, domain):
    """从 token 的 issuer（其次域名）判断账号属于哪个区域，判不出返回空串。"""
    issuer = jwt_issuer(token).lower()
    for marker in CN_REALM_MARKERS:
        if marker in issuer:
            return "cn"
    for marker in INTL_REALM_MARKERS:
        if marker in issuer:
            return "intl"
    dom = (domain or "").lower()
    for marker in CN_REALM_MARKERS:
        if marker in dom:
            return "cn"
    for marker in INTL_REALM_MARKERS:
        if marker in dom:
            return "intl"
    return ""


def normalize_epoch_millis(value):
    """把秒或毫秒的时间戳统一成毫秒。"""
    num = to_float(value)
    if num <= 0:
        return 0
    if num > 1e11:
        return int(num)
    return int(num * 1000)
