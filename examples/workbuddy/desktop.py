# -*- coding: utf-8 -*-
"""本机桌面端登录态的导入（对应 Go 版 desktop.go）。

链路：keyblob 拿判据 → 扫运行的进程内存找回 protector key → 自检 → 解 .info 字段。
桌面端自 2026-09 起把 token 换成 $wbEncrypted 信封，密钥不落盘，只能从进程内存找回。
"""

import json
import os

import atrest
import procscan
from accounts import Account
from jwtutil import jwt_exp_millis, normalize_epoch_millis, realm_from_token
from util import (
    as_str, default_domain, first_non_empty, now_millis, short_uid, site_label,
)

# ---------- keyblob ----------


def config_roots():
    """桌面端配置目录（国内版 / 国际版各一个）。"""
    home = os.path.expanduser("~")
    if not home or home == "~":
        return []
    return [
        os.path.join(home, ".workbuddy"),
        os.path.join(home, ".workbuddy-ai"),
    ]


class KeyBlob:
    __slots__ = ("key_id", "version", "slots", "path")

    def __init__(self, doc, path):
        self.key_id = as_str(doc.get("keyId"))
        self.version = doc.get("version")
        self.slots = doc.get("slots") if isinstance(doc.get("slots"), list) else []
        self.path = path

    def protector_key_id(self):
        """返回 static-v1 槽的 protectorKeyId，用作内存扫描的判据。"""
        for slot in self.slots:
            if not isinstance(slot, dict):
                continue
            if as_str(slot.get("type")) == "static-v1":
                value = as_str(slot.get("protectorKeyId")).strip()
                if value:
                    return value.lower()
        raise RuntimeError("keyblob 里没有 static-v1 槽：%s" % self.path)

    def unwrap(self, key):
        """用 protector key 解出主密钥并自检；扫内存找到的候选必须过这一关才采信。"""
        for slot in self.slots:
            if not isinstance(slot, dict):
                continue
            if as_str(slot.get("type")) != "static-v1" or slot.get("wrapped") is None:
                continue
            if as_str(slot.get("protectorKeyId")).strip().lower() != atrest.derive_key_id(key):
                raise RuntimeError("密钥与 keyblob 的 protectorKeyId 不匹配")
            master = atrest.open_envelope(key, slot.get("wrapped"), atrest.FRAME_FILE)
            if len(master) != 32 or atrest.derive_key_id(master).lower() != self.key_id.lower():
                raise RuntimeError("keyblob 解出的主密钥未通过校验")
            return master
        raise RuntimeError("keyblob 里没有 static-v1 槽")


def find_key_blob():
    """在桌面端配置目录里找 keyblob。"""
    for root in config_roots():
        path = os.path.join(root, "keyblob")
        try:
            with open(path, "rb") as fh:
                raw = fh.read()
        except OSError:
            continue
        try:
            doc = json.loads(raw.decode("utf-8"))
        except Exception as exc:
            raise RuntimeError("解析 %s 失败: %s" % (path, exc))
        if not isinstance(doc, dict):
            raise RuntimeError("解析 %s 失败：顶层不是 JSON 对象" % path)
        return KeyBlob(doc, path)
    raise RuntimeError("没有找到 keyblob：桌面端没安装或从未登录过")


# ---------- .info 登录态文件 ----------


def desktop_info_files():
    """桌面端登录态文件（国内 / 国际各一个）。"""
    base = (os.environ.get("LOCALAPPDATA") or "").strip()
    if not base:
        return []
    directory = os.path.join(base, "CodeBuddyExtension", "Data", "Public", "auth")
    return [
        ("cn", os.path.join(directory, "workbuddy-desktop.info")),
        ("intl", os.path.join(directory, "workbuddy-desktop-ai.info")),
    ]


def import_from_desktop():
    """导入本机桌面端已登录的账号。

    返回 (accounts, notes, error)；error 非 None 时 accounts 为空。
    """
    try:
        blob = find_key_blob()
        key_id = blob.protector_key_id()
        key = procscan.find_protector_key(key_id)
        blob.unwrap(key)
    except Exception as exc:
        return [], [], exc

    accounts = []
    notes = []
    for site, path in desktop_info_files():
        if not os.path.exists(path):
            continue
        try:
            acc = read_desktop_info(path, site, key)
        except Exception as exc:
            notes.append("%s 版：%s" % (site_label(site), exc))
            continue
        accounts.append(acc)

    if not accounts:
        if not notes:
            notes.append("本机没有找到桌面端登录态文件")
        return [], notes, RuntimeError("没有导入到账号：%s" % "；".join(notes))
    return accounts, notes, None


def read_desktop_info(path, fallback_site, key):
    """解一个 .info 文件，返回 Account。"""
    try:
        with open(path, "rb") as fh:
            raw = fh.read()
    except OSError as exc:
        raise RuntimeError(str(exc))
    try:
        doc = json.loads(raw.decode("utf-8"))
    except Exception as exc:
        raise RuntimeError("解析登录态失败: %s" % exc)
    if not isinstance(doc, dict):
        raise RuntimeError("登录态不是 JSON 对象")

    auth = doc.get("auth") if isinstance(doc.get("auth"), dict) else None
    account = doc.get("account") if isinstance(doc.get("account"), dict) else None
    if auth is None or account is None:
        raise RuntimeError("登录态缺少 auth/account 段")

    token, ok = atrest.decrypt_field(key, auth.get("accessToken"))
    if not ok or not token:
        raise RuntimeError("无法解密 accessToken")
    uid = as_str(account.get("uid"))
    if not uid:
        raise RuntimeError("登录态缺少 uid")

    name, _ = atrest.decrypt_field(key, account.get("nickname"))
    if not name:
        name = short_uid(uid)
    refresh, _ = atrest.decrypt_field(key, auth.get("refreshToken"))

    # 区域以 token 的 issuer 为准（同一台机器可能切区登录过），文件名只作兜底
    site = realm_from_token(token, as_str(auth.get("domain"))) or fallback_site

    acc = Account(
        id=uid,
        name=name,
        site=site,
        uid=uid,
        access_token=token,
        refresh_token=refresh,
        token_expires_at=normalize_epoch_millis(auth.get("expiresAt")),
        refresh_expires_at=normalize_epoch_millis(auth.get("refreshExpiresAt")),
        domain=as_str(auth.get("domain")),
        enterprise_id=as_str(account.get("enterpriseId")),
        source="desktop",
        added_at=now_millis(),
    )
    if not acc.token_expires_at:
        acc.token_expires_at = jwt_exp_millis(token)
    if not acc.domain:
        acc.domain = default_domain(site)
    if acc.token_expires_at > 0 and acc.token_expires_at < now_millis():
        acc.last_error = "登录态已过期，请打开一次桌面端让它自动刷新"
    return acc
