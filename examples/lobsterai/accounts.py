# -*- coding: utf-8 -*-
"""账号存储：凭据与状态分开落盘，凭据永不进入控制台响应。

LobsterAI 的续期请求体不止 refreshToken，还要带 `uuid` / `firstKeyfrom` /
`latestKeyfrom`（客户端生成、贯穿账号生命周期），故这三个字段必须随凭据持久化。
"""

import glob
import json
import os
import threading

from util import as_str, atomic_write, first_non_empty, now_millis

STATE_SCHEMA = 1
AUTH_PREFIX = "lb-"


class Account:
    __slots__ = (
        "uid", "nickname", "phone", "token", "refresh_token", "expires_at",
        "uuid", "first_keyfrom", "latest_keyfrom", "user_id", "source",
        "enabled", "hard_disabled", "credits", "credits_at", "plan", "last_error",
    )

    def __init__(self, **kwargs):
        self.uid = kwargs.get("uid", "")
        self.nickname = kwargs.get("nickname", "")
        self.phone = kwargs.get("phone", "")
        self.token = kwargs.get("token", "")
        self.refresh_token = kwargs.get("refresh_token", "")
        self.expires_at = int(kwargs.get("expires_at", 0) or 0)
        self.uuid = kwargs.get("uuid", "")
        self.first_keyfrom = kwargs.get("first_keyfrom", "")
        self.latest_keyfrom = kwargs.get("latest_keyfrom", "")
        self.user_id = kwargs.get("user_id", "")
        self.source = kwargs.get("source", "oauth")
        self.enabled = bool(kwargs.get("enabled", True))
        self.hard_disabled = bool(kwargs.get("hard_disabled", False))
        self.credits = float(kwargs.get("credits", 0) or 0)
        self.credits_at = int(kwargs.get("credits_at", 0) or 0)
        self.plan = kwargs.get("plan", "")
        self.last_error = kwargs.get("last_error", "")

    def copy(self):
        clone = Account()
        for name in self.__slots__:
            setattr(clone, name, getattr(self, name))
        return clone

    def name(self):
        return first_non_empty((self.nickname or "").strip(), (self.phone or "").strip(), self.uid)

    def usable(self):
        return self.enabled and not self.hard_disabled

    def token_alive(self):
        if not (self.token or "").strip():
            return False
        return self.expires_at == 0 or self.expires_at > now_millis()

    def refreshable(self):
        return bool((self.refresh_token or "").strip())


class Credential:
    __slots__ = ("uid", "nickname", "phone", "token", "refresh_token", "expires_at",
                 "uuid", "first_keyfrom", "latest_keyfrom", "user_id", "source")

    def __init__(self, **kwargs):
        for name in self.__slots__:
            setattr(self, name, kwargs.get(name, ""))
        self.expires_at = int(self.expires_at or 0)

    def to_json(self):
        return {
            "uid": self.uid,
            "nickname": self.nickname,
            "phone": self.phone,
            "accessToken": self.token,
            "refreshToken": self.refresh_token,
            "expiresAt": self.expires_at,
            "uuid": self.uuid,
            "firstKeyfrom": self.first_keyfrom,
            "latestKeyfrom": self.latest_keyfrom,
            "userId": self.user_id,
            "source": self.source,
            "savedAt": now_millis(),
        }

    @staticmethod
    def from_json(doc):
        if not isinstance(doc, dict):
            raise RuntimeError("凭据不是 JSON 对象")
        return Credential(
            uid=as_str(doc.get("uid")),
            nickname=as_str(doc.get("nickname")),
            phone=as_str(doc.get("phone")),
            token=as_str(doc.get("accessToken") or doc.get("access_token")),
            refresh_token=as_str(doc.get("refreshToken") or doc.get("refresh_token")),
            expires_at=int(doc.get("expiresAt") or doc.get("expires_at") or 0),
            uuid=as_str(doc.get("uuid")),
            first_keyfrom=as_str(doc.get("firstKeyfrom")),
            latest_keyfrom=as_str(doc.get("latestKeyfrom")),
            user_id=as_str(doc.get("userId")),
            source=as_str(doc.get("source")),
        )


class AccountState:
    __slots__ = ("enabled", "hard_disabled", "credits", "credits_at", "plan", "last_error")

    def __init__(self, enabled=True):
        self.enabled = enabled
        self.hard_disabled = False
        self.credits = 0.0
        self.credits_at = 0
        self.plan = ""
        self.last_error = ""

    def to_json(self):
        doc = {"enabled": self.enabled, "credits": self.credits}
        if self.hard_disabled:
            doc["hard_disabled"] = True
        if self.credits_at:
            doc["credits_at"] = self.credits_at
        if self.plan:
            doc["plan"] = self.plan
        if self.last_error:
            doc["last_error"] = self.last_error
        return doc

    @staticmethod
    def from_json(doc):
        state = AccountState(enabled=bool(doc.get("enabled", True)))
        state.hard_disabled = bool(doc.get("hard_disabled", False))
        state.credits = float(doc.get("credits", 0) or 0)
        state.credits_at = int(doc.get("credits_at", 0) or 0)
        state.plan = as_str(doc.get("plan"))
        state.last_error = as_str(doc.get("last_error"))
        return state


class AccountStore:
    def __init__(self, data_dir):
        self.mu = threading.RLock()
        self.data_dir = data_dir
        self.auth_dir = os.path.join(data_dir, "auths")
        self.state_path = os.path.join(data_dir, "state.json")
        self.order = []
        self.creds = {}
        self.states = {}
        os.makedirs(self.auth_dir, mode=0o700, exist_ok=True)
        self._migrate()
        self._load_state()
        self._load_auths()

    def _migrate(self):
        if not os.path.exists(self.state_path):
            return
        try:
            doc = json.loads(open(self.state_path, "rb").read().decode("utf-8"))
        except Exception:
            return
        if isinstance(doc, dict) and int(doc.get("schema") or 1) > STATE_SCHEMA:
            raise RuntimeError(
                "账号数据来自更新版本的插件（schema=%s），请升级插件后再启动" % doc.get("schema"))

    def _load_state(self):
        try:
            with open(self.state_path, "rb") as fh:
                doc = json.loads(fh.read().decode("utf-8"))
        except (OSError, ValueError):
            return
        accounts = doc.get("accounts") if isinstance(doc, dict) else None
        if not isinstance(accounts, dict):
            return
        for uid, item in accounts.items():
            if isinstance(item, dict):
                self.states[uid] = AccountState.from_json(item)

    def _load_auths(self):
        uids = []
        for path in sorted(glob.glob(os.path.join(self.auth_dir, AUTH_PREFIX + "*.json"))):
            try:
                cred = Credential.from_json(json.loads(open(path, "rb").read().decode("utf-8")))
            except Exception:
                continue
            if not cred.uid:
                cred.uid = _uid_from_path(path)
            if not cred.uid or not (cred.token or "").strip():
                continue
            self.creds[cred.uid] = cred
            self.states.setdefault(cred.uid, AccountState())
            uids.append(cred.uid)
        self.order = sorted(set(uids))

    def list(self):
        with self.mu:
            out = []
            for uid in self.order:
                snap = self._snapshot_locked(uid)
                if snap is not None:
                    out.append(snap)
            return out

    def get(self, account_id):
        with self.mu:
            return self._snapshot_locked(account_id)

    def _snapshot_locked(self, uid):
        cred = self.creds.get(uid)
        if cred is None:
            return None
        acc = Account(
            uid=cred.uid, nickname=cred.nickname, phone=cred.phone, token=cred.token,
            refresh_token=cred.refresh_token, expires_at=cred.expires_at,
            uuid=cred.uuid, first_keyfrom=cred.first_keyfrom,
            latest_keyfrom=cred.latest_keyfrom, user_id=cred.user_id, source=cred.source,
        )
        state = self.states.get(uid)
        if state is not None:
            acc.enabled = state.enabled
            acc.hard_disabled = state.hard_disabled
            acc.credits = state.credits
            acc.credits_at = state.credits_at
            acc.plan = state.plan
            acc.last_error = state.last_error
        return acc

    def upsert(self, cred):
        with self.mu:
            existed = cred.uid in self.creds
            old = self.creds.get(cred.uid)
            if old is not None:
                for name in ("nickname", "phone", "token", "refresh_token", "uuid",
                             "first_keyfrom", "latest_keyfrom", "user_id", "source"):
                    if not getattr(cred, name):
                        setattr(cred, name, getattr(old, name))
                if not cred.expires_at:
                    cred.expires_at = old.expires_at
            self.creds[cred.uid] = cred
            state = self.states.get(cred.uid) or AccountState()
            state.enabled = True
            state.hard_disabled = False
            state.last_error = ""
            self.states[cred.uid] = state
            if cred.uid not in self.order:
                self.order.append(cred.uid)
            self._save_cred_locked(cred.uid)
            self._save_state_locked()
            return self._snapshot_locked(cred.uid), not existed

    def save_cred(self, cred):
        with self.mu:
            if cred.uid not in self.creds:
                raise RuntimeError("账号 %s 不存在" % cred.uid)
            self.creds[cred.uid] = cred
            self._save_cred_locked(cred.uid)

    def remove(self, account_id):
        with self.mu:
            if account_id not in self.creds:
                return False
            del self.creds[account_id]
            self.states.pop(account_id, None)
            if account_id in self.order:
                self.order.remove(account_id)
            try:
                os.remove(os.path.join(self.auth_dir, AUTH_PREFIX + account_id + ".json"))
            except OSError:
                pass
            self._save_state_locked()
            return True

    def update_state(self, account_id, mutate):
        with self.mu:
            if account_id not in self.creds:
                return None
            state = self.states.get(account_id) or AccountState()
            mutate(state)
            self.states[account_id] = state
            self._save_state_locked()
            return self._snapshot_locked(account_id)

    def _save_cred_locked(self, uid):
        cred = self.creds.get(uid)
        if cred is None:
            raise RuntimeError("账号 %s 不存在" % uid)
        raw = json.dumps(cred.to_json(), ensure_ascii=False, indent=2).encode("utf-8")
        atomic_write(os.path.join(self.auth_dir, AUTH_PREFIX + uid + ".json"), raw)

    def _save_state_locked(self):
        doc = {
            "schema": STATE_SCHEMA,
            "accounts": {uid: st.to_json() for uid, st in self.states.items()},
        }
        atomic_write(self.state_path,
                     json.dumps(doc, ensure_ascii=False, indent=2).encode("utf-8"))


def _uid_from_path(path):
    base = os.path.basename(path)
    if base.endswith(".json"):
        base = base[: -len(".json")]
    if base.startswith(AUTH_PREFIX):
        base = base[len(AUTH_PREFIX):]
    return base
