# -*- coding: utf-8 -*-
"""登录会话：OAuth 设备码 + PKCE（MiniMax Code）。

⚠️ 「还在等」有两种形态，两种都认：
    HTTP 200 + {"status": "pending"}      （MiniMax 账号服务）
    非 2xx + {"error": "authorization_pending"}（OAuth 标准）
`slow_down` 必须真的退避，否则会持续被限流。
"""

import threading
import time

import util
from accounts import Credential
from upstream import as_upstream_error

SESSION_TTL = 15 * 60
MAX_POLL_FAILURES = 5


class LoginSession:
    __slots__ = ("state", "device_code", "code_verifier", "url", "user_code",
                 "status", "message", "created", "interval", "deadline", "failures")

    def __init__(self, state, device_code, code_verifier, url, user_code, interval, ttl):
        self.state = state
        self.device_code = device_code
        self.code_verifier = code_verifier
        self.url = url
        self.user_code = user_code
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.created = time.time()
        self.interval = interval
        self.deadline = self.created + ttl
        self.failures = 0


class LoginBroker:
    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        self.sessions = {}

    def start(self):
        auth = self.plugin.upstream.start_device()
        state = util.new_state()
        session = LoginSession(
            state, auth["device_code"], auth["code_verifier"], auth["url"],
            auth["user_code"], auth["interval"], min(auth["expires_in"], SESSION_TTL))
        with self.mu:
            self.sessions[state] = session
        threading.Thread(target=self._poll_loop, args=(session,), daemon=True).start()
        return {
            "status": "pending",
            "message": "请在浏览器中完成授权（用户码 %s）" % session.user_code,
            "url": session.url,
            "state": session.state,
        }

    def status(self, state):
        with self.mu:
            session = self.sessions.get(state)
        if session is None:
            return "expired", "登录会话不存在或已过期，请重新发起"
        if session.status == "pending" and time.time() > session.deadline:
            session.status = "expired"
            session.message = "授权等待超时，请重新发起登录"
        return session.status, session.message

    def _poll_loop(self, session):
        upstream = self.plugin.upstream
        while time.time() < session.deadline:
            if session.status != "pending":
                return
            time.sleep(max(1.0, float(session.interval)))
            try:
                kind, payload = upstream.poll_device_token(
                    session.device_code, session.code_verifier)
            except Exception as exc:
                session.failures += 1
                if session.failures >= MAX_POLL_FAILURES:
                    self._fail(session, as_upstream_error(exc).msg)
                    return
                continue
            session.failures = 0
            if kind == "pending":
                continue
            if kind == "slow_down":
                session.interval += 5.0
                continue
            if kind == "error":
                self._fail(session, util.truncate_runes(payload, 120))
                return
            self._finish(session, payload)
            return
        if session.status == "pending":
            self._fail(session, "授权等待超时，请重新发起登录")

    def _fail(self, session, message):
        session.status = "error"
        session.message = message or "登录失败"

    def _finish(self, session, payload):
        token = payload["access_token"]
        uid = _uid_of(token)
        cred = Credential(
            uid=uid,
            nickname=uid,
            token=token,
            refresh_token=payload["refresh_token"],
            expires_at=payload["expires_at"],
            scope=payload.get("scope", ""),
            source="oauth",
        )
        saved, created = self.plugin.store.upsert(cred)
        session.status = "ok"
        session.message = "%s账号 %s" % ("已新增" if created else "已更新", saved.name())

    def close_all(self):
        with self.mu:
            for session in self.sessions.values():
                if session.status == "pending":
                    session.status = "error"
                    session.message = "插件正在停止"


def _uid_of(token):
    """access_token 是 `mmoat_` 前缀的随机串、不是 JWT ⇒ 用摘要做稳定 id。"""
    import hashlib

    return hashlib.sha256((token or "").encode("utf-8")).hexdigest()[:16]


__all__ = ["LoginBroker", "LoginSession"]
