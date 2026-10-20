# -*- coding: utf-8 -*-
"""登录会话：本地回调 + authCode 换 token。

LobsterAI 登录门户要求 `redirect_uri` 是 `http://127.0.0.1:<端口>/auth/callback`
形态（路径逐字固定）。插件**不额外监听端口**，而是复用宿主拉起的那个唯一端口 ——
与仓库里 TRAE 插件处理「上游只认固定回环回调」的方式一致（plugin-spec §3
禁止的是**额外**监听，不是禁止在同一端口上多挂一个路径）。
"""

import threading
import time
import urllib.parse
import uuid

import util
from accounts import Credential
from upstream import UpstreamError, as_upstream_error, resolve_uid

SESSION_TTL = 10 * 60
CALLBACK_PATH = "/auth/callback"


class LoginSession:
    __slots__ = ("state", "uuid", "first_keyfrom", "url", "status", "message",
                 "created", "deadline")

    def __init__(self, state, flow_uuid, first_keyfrom, url):
        self.state = state
        self.uuid = flow_uuid
        self.first_keyfrom = first_keyfrom
        self.url = url
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.created = time.time()
        self.deadline = self.created + SESSION_TTL


class LoginBroker:
    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        self.sessions = {}

    def start(self):
        """返回 {url, state}；回调落在**插件同一端口**的 /auth/callback。"""
        state = util.new_state()
        flow_uuid = str(uuid.uuid4())
        first_keyfrom = str(util.now_millis())
        redirect = "http://127.0.0.1:%d%s" % (int(self.plugin.port), CALLBACK_PATH)
        query = urllib.parse.urlencode({
            "source": "electron",
            "redirect_uri": redirect,
            "state": state,
        })
        url = "%s/portal#/login?%s" % (self.plugin.eps.portal_base, query)
        session = LoginSession(state, flow_uuid, first_keyfrom, url)
        with self.mu:
            self.sessions[state] = session
        return {
            "status": "pending",
            "message": "请在浏览器中完成授权",
            "url": url,
            "state": state,
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

    def handle_callback(self, state, query):
        """浏览器回调：用 code 换 token 并落库，返回给人看的 HTML。"""
        with self.mu:
            session = self.sessions.get(state)
        if session is None:
            return False, "登录会话不存在或已过期，请回到面板重新发起"
        if session.status != "pending":
            return session.status == "ok", session.message
        if time.time() > session.deadline:
            session.status = "expired"
            session.message = "授权等待超时，请重新发起登录"
            return False, session.message

        values = query.get("code") or [""]
        code = (values[0] or "").strip()
        if not code:
            failure = (query.get("error_description") or query.get("error") or [""])[0]
            session.status = "error"
            session.message = failure or "回调缺少授权码"
            return False, session.message

        try:
            payload = self.plugin.upstream.exchange_code(
                code, session.uuid, session.first_keyfrom)
        except UpstreamError as err:
            session.status = "error"
            session.message = err.msg
            return False, err.msg
        except Exception as exc:
            session.status = "error"
            session.message = as_upstream_error(exc).msg
            return False, session.message

        uid = resolve_uid(payload)
        cred = Credential(
            uid=_safe_uid(uid),
            nickname=payload["nickname"] or _safe_uid(uid),
            token=payload["access_token"],
            refresh_token=payload["refresh_token"],
            expires_at=payload["expires_at"],
            uuid=session.uuid,
            first_keyfrom=session.first_keyfrom,
            latest_keyfrom=str(util.now_millis()),
            user_id=payload["user_id"],
            source="oauth",
        )
        saved, created = self.plugin.store.upsert(cred)
        session.status = "ok"
        session.message = "%s账号 %s" % ("已新增" if created else "已更新", saved.name())
        return True, session.message

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


def render_page(ok, message):
    title = "登录成功" if ok else "登录失败"
    color = "#0f9d58" if ok else "#d93025"
    return (
        "<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">"
        "<title>%s</title><style>body{font:15px/1.7 -apple-system,'Segoe UI',sans-serif;"
        "display:flex;align-items:center;justify-content:center;height:100vh;margin:0;"
        "background:#0f1115;color:#e6e8eb}.b{text-align:center;max-width:520px;"
        "padding:0 24px}.k{font-size:34px;margin-bottom:8px;color:%s}</style></head>"
        "<body><div class=\"b\"><div class=\"k\">%s</div><div>%s</div></div></body></html>"
    ) % (title, color, "✓" if ok else "✗", _escape(message))


def _escape(text):
    return (text or "").replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
