# -*- coding: utf-8 -*-
"""账号存储（对应 Go 版 accounts.go）。

凭据是明文保存在插件私有数据目录里的（仅本机、0600），因为上游要求的是原始 token，
而插件自身不做加密存储。落盘形态：<data>/accounts.json = {"accounts":[...]}。
"""

import json
import os
import threading
import time


class Account:
    """插件管理的账号。"""

    __slots__ = (
        "id", "name", "site", "uid", "domain", "access_token", "refresh_token",
        "token_expires_at", "refresh_expires_at", "plan", "credits", "credits_at",
        "checked_in_today", "streak_days", "today_credit", "last_error",
        "source", "added_at", "enabled", "enterprise_id", "device_token",
    )

    def __init__(self, **kwargs):
        self.id = kwargs.get("id", "")
        self.name = kwargs.get("name", "")
        self.site = kwargs.get("site", "")
        self.uid = kwargs.get("uid", "")
        self.domain = kwargs.get("domain", "")
        self.access_token = kwargs.get("access_token", "")
        self.refresh_token = kwargs.get("refresh_token", "")
        self.token_expires_at = int(kwargs.get("token_expires_at", 0) or 0)
        self.refresh_expires_at = int(kwargs.get("refresh_expires_at", 0) or 0)
        self.plan = kwargs.get("plan", "")
        self.credits = float(kwargs.get("credits", 0) or 0)
        self.credits_at = int(kwargs.get("credits_at", 0) or 0)
        self.checked_in_today = bool(kwargs.get("checked_in_today", False))
        self.streak_days = int(kwargs.get("streak_days", 0) or 0)
        self.today_credit = float(kwargs.get("today_credit", 0) or 0)
        self.last_error = kwargs.get("last_error", "")
        self.source = kwargs.get("source", "")
        self.added_at = int(kwargs.get("added_at", 0) or 0)
        self.enabled = bool(kwargs.get("enabled", True))
        self.enterprise_id = kwargs.get("enterprise_id", "")
        self.device_token = kwargs.get("device_token", "")

    def copy(self):
        clone = Account()
        for name in self.__slots__:
            setattr(clone, name, getattr(self, name))
        return clone

    def token_alive(self):
        """accessToken 是否还在有效期内。"""
        if not (self.access_token or "").strip():
            return False
        return self.token_expires_at == 0 or self.token_expires_at > int(time.time() * 1000)

    # ---------- 落盘形态 ----------

    def to_json(self):
        doc = {
            "id": self.id,
            "name": self.name,
            "site": self.site,
            "uid": self.uid,
            "domain": self.domain,
            "access_token": self.access_token,
            "expires_at": self.token_expires_at,
            "plan": self.plan,
            "credits": self.credits,
            "credits_updated_at": self.credits_at,
            "checked_in_today": self.checked_in_today,
            "streak_days": self.streak_days,
            "today_credit": self.today_credit,
            "last_error": self.last_error,
            "added_at": self.added_at,
            "enabled": self.enabled,
        }
        if self.refresh_token:
            doc["refresh_token"] = self.refresh_token
        if self.refresh_expires_at:
            doc["refresh_expires_at"] = self.refresh_expires_at
        if self.source:
            doc["source"] = self.source
        if self.enterprise_id:
            doc["enterprise_id"] = self.enterprise_id
        if self.device_token:
            doc["device_token"] = self.device_token
        return doc

    @staticmethod
    def from_json(doc):
        if not isinstance(doc, dict):
            return None
        return Account(
            id=_text(doc.get("id")),
            name=_text(doc.get("name")),
            site=_text(doc.get("site")),
            uid=_text(doc.get("uid")),
            domain=_text(doc.get("domain")),
            access_token=_text(doc.get("access_token")),
            refresh_token=_text(doc.get("refresh_token")),
            token_expires_at=_int(doc.get("expires_at")),
            refresh_expires_at=_int(doc.get("refresh_expires_at")),
            plan=_text(doc.get("plan")),
            credits=_float(doc.get("credits")),
            credits_at=_int(doc.get("credits_updated_at")),
            checked_in_today=bool(doc.get("checked_in_today", False)),
            streak_days=_int(doc.get("streak_days")),
            today_credit=_float(doc.get("today_credit")),
            last_error=_text(doc.get("last_error")),
            source=_text(doc.get("source")),
            added_at=_int(doc.get("added_at")),
            enabled=bool(doc.get("enabled", True)),
            enterprise_id=_text(doc.get("enterprise_id")),
            device_token=_text(doc.get("device_token")),
        )


def _text(value):
    if isinstance(value, str):
        return value
    if value is None:
        return ""
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def _int(value):
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


def _float(value):
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


class AccountStore:
    def __init__(self, data_dir):
        self.mu = threading.RLock()
        self.path = os.path.join(data_dir, "accounts.json")
        self.accounts = []
        try:
            os.makedirs(data_dir, mode=0o700, exist_ok=True)
        except OSError as exc:
            raise RuntimeError("创建数据目录失败: %s" % exc)
        self._load()

    def _load(self):
        try:
            with open(self.path, "rb") as fh:
                raw = fh.read()
        except OSError:
            return
        try:
            doc = json.loads(raw.decode("utf-8"))
        except Exception as exc:
            raise RuntimeError("解析 %s 失败: %s" % (self.path, exc))
        items = doc.get("accounts") if isinstance(doc, dict) else None
        if not isinstance(items, list):
            return
        for item in items:
            acc = Account.from_json(item)
            if acc is not None:
                self.accounts.append(acc)

    # ---------- 读 ----------

    def list(self):
        with self.mu:
            return [a.copy() for a in self.accounts]

    def get(self, account_id):
        with self.mu:
            for acc in self.accounts:
                if acc.id == account_id:
                    return acc.copy()
            return None

    # ---------- 写 ----------

    def upsert(self, account):
        """按 id 合并：已存在则保留面板上的统计值、只刷新凭据字段。"""
        with self.mu:
            for i, cur in enumerate(self.accounts):
                if cur.id != account.id:
                    continue
                account.credits = cur.credits
                account.credits_at = cur.credits_at
                account.checked_in_today = cur.checked_in_today
                account.streak_days = cur.streak_days
                account.today_credit = cur.today_credit
                account.plan = cur.plan
                account.enabled = cur.enabled
                account.added_at = cur.added_at
                self.accounts[i] = account
                self._save_locked()
                return account.copy(), False

            account.enabled = True
            self.accounts.append(account)
            self._save_locked()
            return account.copy(), True

    def remove(self, account_id):
        with self.mu:
            for i, acc in enumerate(self.accounts):
                if acc.id == account_id:
                    del self.accounts[i]
                    self._save_locked()
                    return True
            return False

    def update(self, account_id, mutate):
        """在锁内改一个账号并落盘，返回改完的副本。"""
        with self.mu:
            for acc in self.accounts:
                if acc.id != account_id:
                    continue
                mutate(acc)
                self._save_locked()
                return acc.copy()
            return None

    def _save_locked(self):
        doc = {"accounts": [a.to_json() for a in self.accounts]}
        raw = json.dumps(doc, ensure_ascii=False, indent=2).encode("utf-8")
        tmp = self.path + ".tmp"
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        try:
            with os.fdopen(fd, "wb") as fh:
                fh.write(raw)
        except Exception:
            try:
                os.remove(tmp)
            except OSError:
                pass
            raise
        os.replace(tmp, self.path)
