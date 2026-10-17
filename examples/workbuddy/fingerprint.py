# -*- coding: utf-8 -*-
"""设备指纹（对应 Go 版 fingerprint.go）。

上游按设备维度做风控，随机机器码反而容易被判异常；用 uid 稳定派生可以保证
「同一账号永远来自同一台设备」，多账号之间也天然隔离。
"""

import hashlib
import time


def derive_id(uid, salt):
    """md5(salt + ":" + uid) 的 32 位 hex（与官方实现取同样的值）。"""
    if not uid:
        uid = "anonymous"
    return hashlib.md5(("%s:%s" % (salt, uid)).encode("utf-8")).hexdigest()


def request_id(uid):
    """带稳定前缀与微秒后缀的 X-Request-ID（账单/签到类接口用）。"""
    suffix = "%06d" % (time.time_ns() % 1000000)
    return derive_id(uid, "req") + "-" + suffix
