# -*- coding: utf-8 -*-
"""登录会话：WorkOS 设备码轮询。

Cline 官方桌面端在 prod 下走的就是设备码分支，**不需要本地回调端口**：
  1) POST {workos}/user_management/authorize/device  → device_code / user_code / verification_uri
  2) 用户在浏览器完成授权
  3) POST {workos}/user_management/authenticate      → access_token / refresh_token
     ⚠️ `authorization_pending` 表示「用户还没点授权」，不是失败，必须继续轮询；
        `slow_down` 必须真的退避（服务端要求降速）。
  4) POST {api}/api/v1/auth/register                 → Cline token（自带 `workos:` 前缀）

对面板而言这是 link 类登录：POST 返回 `{url, state}`，前端打开 url 并轮询状态。
设备码的用户码也拼进 url（`verification_uri_complete`），用户不必手输。
"""

import threading
import time

import util
from accounts import Credential, digest_uid
from upstream import UpstreamError, as_upstream_error

SESSION_TTL = 15 * 60

# 连续网络失败多少次后放弃
MAX_POLL_FAILURES = 5


class LoginSession:
    __slots__ = ("state", "device_code", "url", "user_code", "status", "message",
                 "created", "interval", "deadline", "failures", "uid")

    def __init__(self, state, device_code, url, user_code, interval, ttl):
        self.state = state
        self.device_code = device_code
        self.url = url
        self.user_code = user_code
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.created = time.time()
        self.interval = interval
        self.deadline = self.created + ttl
        self.failures = 0
        self.uid = ""


class LoginBroker:
    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        self.sessions = {}

    def start(self):
        """申请设备码并起一个后台轮询线程。"""
        auth = self.plugin.upstream.device_authorization()
        state = util.new_state()
        session = LoginSession(
            state, auth["device_code"], auth["url"], auth["user_code"],
            auth["interval"], min(auth["expires_in"], SESSION_TTL))
        with self.mu:
            self.sessions[state] = session
        thread = threading.Thread(target=self._poll_loop, args=(session,), daemon=True)
        thread.start()
        return {
            "status": "pending",
            "message": ("请在浏览器中完成授权（用户码 %s）" % session.user_code
                        if session.user_code else "请在浏览器中完成授权"),
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
        """后台轮询直到完成 / 失败 / 超时。"""
        upstream = self.plugin.upstream
        while time.time() < session.deadline:
            if session.status != "pending":
                return
            time.sleep(max(1.0, float(session.interval)))
            try:
                kind, payload = upstream.poll_device_token(session.device_code)
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
                session.interval += 1.0
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

    def _finish(self, session, tokens):
        upstream = self.plugin.upstream
        try:
            payload = upstream.register_tokens(tokens["access_token"], tokens["refresh_token"])
        except UpstreamError as err:
            self._fail(session, err.msg)
            return
        token = payload["access_token"]
        if not token:
            self._fail(session, "登录响应缺少访问令牌")
            return
        uid = payload["account_id"] or payload["email"] or digest_uid(token)
        uid = _safe_uid(uid)
        cred = Credential(
            uid=uid,
            nickname=payload["display_name"] or payload["email"] or uid,
            email=payload["email"],
            token=token,
            refresh_token=payload["refresh_token"] or tokens["refresh_token"],
            expires_at=payload["expires_at"],
            account_id=payload["account_id"],
            source="oauth",
        )
        saved, created = self.plugin.store.upsert(cred)
        session.uid = uid
        session.status = "ok"
        session.message = "%s账号 %s" % ("已新增" if created else "已更新", saved.name())

    def close_all(self):
        with self.mu:
            for session in self.sessions.values():
                if session.status == "pending":
                    session.status = "error"
                    session.message = "插件正在停止"


def _safe_uid(value):
    text = (value or "").strip()
    for ch in ("/", "\\", "?", "#", " "):
        text = text.replace(ch, "_")
    return "".join(ch for ch in text if ch.isprintable()) or util.new_state()
