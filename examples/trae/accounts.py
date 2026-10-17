# -*- coding: utf-8 -*-
"""账号存储（对应 Go 版 accounts.go）。

凭证落盘在 <data>/auths/trae-<uid>.json（UTF-8、0600、tmp+rename 原子写），
账号状态落在 <data>/state.json。磁盘形态与参考项目一致（嵌套形
{"auth":{...},"account":{...}}），解析时同时兼容扁平形。
"""

import glob
import json
import os
import threading
import time

from util import as_str, first_non_empty

STATE_SCHEMA = 1


class Account:
    """「凭证 + 状态」的合并视图。"""

    __slots__ = (
        "uid", "nickname", "enterprise_id", "access_token", "refresh_token",
        "expires_at", "domain", "api_host", "machine_id", "device_id",
        "enabled", "hard_disabled", "credits", "credits_at",
        "checked_in_today", "streak_days", "last_error",
    )

    def __init__(self, **kwargs):
        self.uid = kwargs.get("uid", "")
        self.nickname = kwargs.get("nickname", "")
        self.enterprise_id = kwargs.get("enterprise_id", "")
        self.access_token = kwargs.get("access_token", "")
        self.refresh_token = kwargs.get("refresh_token", "")
        self.expires_at = int(kwargs.get("expires_at", 0) or 0)  # Unix 秒（0 表示不过期）
        self.domain = kwargs.get("domain", "")
        self.api_host = kwargs.get("api_host", "")
        self.machine_id = kwargs.get("machine_id", "")
        self.device_id = kwargs.get("device_id", "")
        self.enabled = bool(kwargs.get("enabled", True))
        self.hard_disabled = bool(kwargs.get("hard_disabled", False))
        self.credits = float(kwargs.get("credits", 0) or 0)
        self.credits_at = int(kwargs.get("credits_at", 0) or 0)
        self.checked_in_today = bool(kwargs.get("checked_in_today", False))
        self.streak_days = int(kwargs.get("streak_days", 0) or 0)
        self.last_error = kwargs.get("last_error", "")

    def copy(self):
        clone = Account()
        for name in self.__slots__:
            setattr(clone, name, getattr(self, name))
        return clone

    def name(self):
        """展示名：昵称为空时回退 uid。"""
        return first_non_empty((self.nickname or "").strip(), self.uid)

    def usable(self):
        """是否可被选号使用（软开关开启且未被硬禁用）。"""
        return self.enabled and not self.hard_disabled

    def token_alive(self):
        """accessToken 是否仍在有效期内。"""
        if not (self.access_token or "").strip():
            return False
        return self.expires_at == 0 or self.expires_at > int(time.time())

    def to_auth(self):
        return Auth(
            uid=self.uid, nickname=self.nickname, enterprise_id=self.enterprise_id,
            access_token=self.access_token, refresh_token=self.refresh_token,
            expires_at=self.expires_at, domain=self.domain, api_host=self.api_host,
            machine_id=self.machine_id, device_id=self.device_id,
        )


class Auth:
    """单账号凭证。"""

    __slots__ = (
        "uid", "nickname", "enterprise_id", "access_token", "refresh_token",
        "expires_at", "domain", "api_host", "machine_id", "device_id",
    )

    def __init__(self, **kwargs):
        self.uid = kwargs.get("uid", "")
        self.nickname = kwargs.get("nickname", "")
        self.enterprise_id = kwargs.get("enterprise_id", "")
        self.access_token = kwargs.get("access_token", "")
        self.refresh_token = kwargs.get("refresh_token", "")
        self.expires_at = int(kwargs.get("expires_at", 0) or 0)
        self.domain = kwargs.get("domain", "")
        self.api_host = kwargs.get("api_host", "")
        self.machine_id = kwargs.get("machine_id", "")
        self.device_id = kwargs.get("device_id", "")


class AccountState:
    """state.json 里单个账号的可持久化状态（不含任何凭据）。"""

    __slots__ = ("enabled", "hard_disabled", "credits", "credits_at",
                 "checked_in_today", "streak_days", "last_error")

    def __init__(self, enabled=True):
        self.enabled = enabled
        self.hard_disabled = False
        self.credits = 0.0
        self.credits_at = 0
        self.checked_in_today = False
        self.streak_days = 0
        self.last_error = ""

    def to_json(self):
        doc = {
            "enabled": self.enabled,
            "credits": self.credits,
            "checked_in_today": self.checked_in_today,
        }
        if self.hard_disabled:
            doc["hard_disabled"] = True
        if self.credits_at:
            doc["credits_at"] = self.credits_at
        if self.streak_days:
            doc["streak_days"] = self.streak_days
        if self.last_error:
            doc["last_error"] = self.last_error
        return doc

    @staticmethod
    def from_json(doc):
        state = AccountState(enabled=bool(doc.get("enabled", True)))
        state.hard_disabled = bool(doc.get("hard_disabled", False))
        state.credits = float(doc.get("credits", 0) or 0)
        state.credits_at = int(doc.get("credits_at", 0) or 0)
        state.checked_in_today = bool(doc.get("checked_in_today", False))
        state.streak_days = int(doc.get("streak_days", 0) or 0)
        state.last_error = doc.get("last_error", "") or ""
        return state


class AccountStore:
    """统一管理 auths/ 下的凭证文件与 state.json 里的账号状态。"""

    def __init__(self, data_dir):
        self.mu = threading.RLock()
        self.data_dir = data_dir
        self.auth_dir = os.path.join(data_dir, "auths")
        self.state_path = os.path.join(data_dir, "state.json")
        self.order = []
        self.auths = {}
        self.states = {}
        try:
            os.makedirs(self.auth_dir, mode=0o700, exist_ok=True)
        except OSError as exc:
            raise RuntimeError("创建数据目录失败: %s" % exc)
        self._load_state()
        self._load_auths()

    # ---------- 载入 ----------

    def _load_state(self):
        try:
            with open(self.state_path, "rb") as fh:
                raw = fh.read()
        except OSError:
            return
        try:
            doc = json.loads(raw.decode("utf-8"))
        except Exception:
            return
        if not isinstance(doc, dict):
            return
        accounts = doc.get("accounts")
        if not isinstance(accounts, dict):
            return
        for uid, item in accounts.items():
            if isinstance(item, dict):
                self.states[uid] = AccountState.from_json(item)

    def _load_auths(self):
        files = sorted(glob.glob(os.path.join(self.auth_dir, "trae-*.json")))
        uids = []
        for path in files:
            try:
                with open(path, "rb") as fh:
                    raw = fh.read()
            except OSError:
                continue
            try:
                auth = parse_auth(raw)
            except Exception:
                continue
            if auth is None or not (auth.access_token or "").strip():
                continue
            if not auth.uid:
                auth.uid = uid_from_path(path)
            if not auth.uid:
                continue
            self.auths[auth.uid] = auth
            if auth.uid not in self.states:
                self.states[auth.uid] = AccountState(enabled=True)
            uids.append(auth.uid)
        uids.sort()
        self.order = uids

    # ---------- 读 ----------

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
        auth = self.auths.get(uid)
        if auth is None:
            return None
        acc = Account(
            uid=auth.uid, nickname=auth.nickname, enterprise_id=auth.enterprise_id,
            access_token=auth.access_token, refresh_token=auth.refresh_token,
            expires_at=auth.expires_at, domain=auth.domain, api_host=auth.api_host,
            machine_id=auth.machine_id, device_id=auth.device_id, enabled=True,
        )
        state = self.states.get(uid)
        if state is not None:
            acc.enabled = state.enabled
            acc.hard_disabled = state.hard_disabled
            acc.credits = state.credits
            acc.credits_at = state.credits_at
            acc.checked_in_today = state.checked_in_today
            acc.streak_days = state.streak_days
            acc.last_error = state.last_error
        return acc

    # ---------- 写 ----------

    def upsert(self, auth):
        """以 uid 为准合并账号：凭证整体替换，状态字段保留既有值。

        返回 (Account, created)。
        """
        with self.mu:
            existed = auth.uid in self.auths
            self.auths[auth.uid] = auth
            if auth.uid not in self.states:
                self.states[auth.uid] = AccountState(enabled=True)
            if auth.uid not in self.order:
                self.order.append(auth.uid)
            self._save_auth_locked(auth.uid)
            self._save_state_locked()
            return self._snapshot_locked(auth.uid), not existed

    def save_auth(self, auth):
        """写回更新后的凭证（token 刷新后调用）。"""
        with self.mu:
            if auth.uid not in self.auths:
                raise RuntimeError("账号 %s 不存在" % auth.uid)
            self.auths[auth.uid] = auth
            self._save_auth_locked(auth.uid)

    def remove(self, account_id):
        with self.mu:
            if account_id not in self.auths:
                return False
            del self.auths[account_id]
            self.states.pop(account_id, None)
            if account_id in self.order:
                self.order.remove(account_id)
            try:
                os.remove(os.path.join(self.auth_dir, "trae-%s.json" % account_id))
            except OSError:
                pass
            self._save_state_locked()
            return True

    def update_state(self, account_id, mutate):
        """在锁内改一个账号的状态并落盘，返回改完的快照。"""
        with self.mu:
            if account_id not in self.auths:
                return None
            state = self.states.get(account_id)
            if state is None:
                state = AccountState(enabled=True)
                self.states[account_id] = state
            mutate(state)
            self._save_state_locked()
            return self._snapshot_locked(account_id)

    # ---------- 落盘 ----------

    def _save_auth_locked(self, uid):
        auth = self.auths.get(uid)
        if auth is None:
            raise RuntimeError("账号 %s 不存在" % uid)
        raw = marshal_auth_nested(auth)
        atomic_write(os.path.join(self.auth_dir, "trae-%s.json" % uid), raw)

    def _save_state_locked(self):
        doc = {
            "schema": STATE_SCHEMA,
            "accounts": {uid: st.to_json() for uid, st in self.states.items()},
        }
        raw = json.dumps(doc, ensure_ascii=False, indent=2).encode("utf-8")
        atomic_write(self.state_path, raw)


# ---------- 落盘工具 ----------


def atomic_write(path, raw):
    """以 tmp+rename 原子写文件（0600），避免中断损坏已有内容。"""
    tmp = path + ".tmp"
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
    os.replace(tmp, path)


def marshal_auth_nested(auth):
    """嵌套形凭证 JSON（与参考项目磁盘形态一致）。"""
    doc = {
        "auth": {
            "accessToken": auth.access_token,
            "refreshToken": auth.refresh_token,
            "expiresAt": auth.expires_at,
            "domain": auth.domain,
            "apiHost": auth.api_host,
            "machineId": auth.machine_id,
            "deviceId": auth.device_id,
        },
        "account": {
            "uid": auth.uid,
            "enterpriseId": auth.enterprise_id,
            "nickname": auth.nickname,
        },
    }
    return json.dumps(doc, ensure_ascii=False, indent=2).encode("utf-8")


def parse_auth(raw):
    """兼容嵌套形与扁平形两种凭证 JSON。"""
    if not raw:
        raise RuntimeError("空凭证")
    try:
        probe = json.loads(raw.decode("utf-8"))
    except Exception:
        raise RuntimeError("凭证不是合法 JSON")
    if not isinstance(probe, dict):
        raise RuntimeError("凭证不是合法 JSON")

    if "auth" in probe:
        section = probe.get("auth") if isinstance(probe.get("auth"), dict) else {}
        account = probe.get("account") if isinstance(probe.get("account"), dict) else {}
        return Auth(
            uid=as_str(account.get("uid")),
            nickname=as_str(account.get("nickname")),
            enterprise_id=as_str(account.get("enterpriseId")),
            access_token=as_str(section.get("accessToken")),
            refresh_token=as_str(section.get("refreshToken")),
            expires_at=_int(section.get("expiresAt")),
            domain=as_str(section.get("domain")),
            api_host=as_str(section.get("apiHost")),
            machine_id=as_str(section.get("machineId")),
            device_id=as_str(section.get("deviceId")),
        )

    return Auth(
        uid=as_str(probe.get("uid")),
        nickname=as_str(probe.get("nickname")),
        enterprise_id=as_str(probe.get("enterpriseId")),
        access_token=as_str(probe.get("accessToken")),
        refresh_token=as_str(probe.get("refreshToken")),
        expires_at=_int(probe.get("expiresAt")),
        domain=as_str(probe.get("domain")),
        api_host=as_str(probe.get("apiHost")),
        machine_id=as_str(probe.get("machineId")),
        device_id=as_str(probe.get("deviceId")),
    )


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


def uid_from_path(path):
    base = os.path.basename(path)
    if base.endswith(".json"):
        base = base[: -len(".json")]
    if base.startswith("trae-"):
        base = base[len("trae-"):]
    return base
