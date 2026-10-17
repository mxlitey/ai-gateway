# -*- coding: utf-8 -*-
"""网页登录（link 类，对应 Go 版 login.go）。

回调走**网关统一入口**，插件不再监听额外端口：
  ① POST /console/login/web → 从 X-Gateway-Callback-Base（或 AI_GATEWAY_CALLBACK_BASE）取基址，
     拼出 <基址>/web/<state> 作为回调地址，返回 {status,url,state}
  ② 面板打开 url，用户在浏览器授权，浏览器跳回网关的 /plugin-callback/<id>/web/<state>
  ③ 网关原样转发到本插件的 /console/callback/web/<state>?<授权参数>
  ④ 回调里取 refreshToken → ExchangeToken → GetUserInfo → 原子落盘 → 会话标记 ok
  ⑤ 面板轮询 GET /console/login/web?state=xxx 拿到 ok → 自动刷新账号列表
"""

import os
import threading
import time

from accounts import Account
from upstream import as_upstream_error, build_login_url, parse_callback
from util import new_checkin_device_id, random_hex, random_state, truncate_runes

LOGIN_TTL = 10 * 60  # 10 分钟


class LoginSession:
    __slots__ = ("state", "auth_url", "status", "message", "created", "machine_id", "device_id")

    def __init__(self, state, auth_url, machine_id, device_id):
        self.state = state
        self.auth_url = auth_url
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.created = time.time()
        self.machine_id = machine_id
        self.device_id = device_id

    def set_status(self, status, message):
        self.status = status
        self.message = message


class LoginBroker:
    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        self.cur = None
        self.by_state = {}

    def start(self, callback_base):
        """发起一次登录：拼出回调地址并返回会话（含 url 与 state）。"""
        machine_id = random_hex(16)
        # deviceID 必须是「16 位纯数字」，且与落盘凭证共用同一个值
        device_id = new_checkin_device_id()
        state = random_state()
        # state 放进回调**路径**（而非查询串）：部分上游要求 redirect_uri 精确匹配
        callback_url = callback_base + "/web/" + state
        auth_url = build_login_url(self.plugin.eps, machine_id, device_id, callback_url)

        session = LoginSession(state, auth_url, machine_id, device_id)
        with self.mu:
            # 面板同时只跟踪一个登录会话：新的替换旧的（旧 state 立即终止）
            if self.cur is not None:
                self.cur.set_status("error", "登录会话已被新的登录取消")
            self.cur = session
            self.by_state[state] = session
        return session

    def status(self, state):
        with self.mu:
            session = self.by_state.get(state)
        if session is None:
            return "error", "登录会话不存在，请重新发起"
        if session.status == "pending" and time.time() - session.created > LOGIN_TTL:
            session.set_status("expired", "授权窗口已过期，请重新发起登录")
        return session.status, session.message

    def lookup(self, state):
        with self.mu:
            return self.by_state.get(state)

    def close_all(self):
        with self.mu:
            self.by_state = {}
            self.cur = None

    def handle_callback(self, state, raw_url, raw_query):
        """处理网关转发来的浏览器回调。免鉴权入口：先按 state 找回会话。"""
        session = self.lookup((state or "").strip())
        if session is None:
            return False, "登录会话不存在或已过期，请回到网关控制台重试"
        if time.time() - session.created > LOGIN_TTL:
            session.set_status("expired", "授权窗口已过期，请重新发起登录")
            return False, "授权窗口已过期，请回到网关控制台重新发起"

        if "refreshToken" not in (raw_query or "") and "userJwt" not in (raw_query or ""):
            # 可能是浏览器预取 /favicon.ico 之类，直接忽略
            return False, "等待授权回调…"

        try:
            info = parse_callback(raw_url)
        except Exception as exc:
            session.set_status("error", "回调解析失败：" + truncate_runes(str(exc), 60))
            return False, "回调解析失败，请回到网关控制台重试"

        plugin = self.plugin
        account = Account(
            access_token=info.access_token, refresh_token=info.refresh_token,
            uid=info.uid, nickname=info.nickname, enterprise_id=info.enterprise_id,
            expires_at=info.expires_at, domain="trae.cn", api_host=plugin.eps.oauth_host,
            machine_id=session.machine_id, device_id=session.device_id, enabled=True,
        )
        if (account.refresh_token or "").strip():
            try:
                plugin.upstream.refresh_token(account)
            except Exception as exc:
                session.set_status("error", "换取 token 失败：" + as_upstream_error(exc).msg)
                return False, "登录失败，请回到网关控制台重试"
        try:
            uid, nick, ent = plugin.upstream.get_user_info(account)
            if uid.strip():
                account.uid = uid
            if nick.strip():
                account.nickname = nick
            if ent.strip():
                account.enterprise_id = ent
        except Exception:
            pass

        if not (account.uid or "").strip() or not (account.access_token or "").strip():
            session.set_status("error", "未能从回调确定账号信息，请重试或改用手工导入")
            return False, "登录失败，请回到网关控制台重试"
        if not (account.device_id or "").strip():
            account.device_id = new_checkin_device_id()

        saved, created = plugin.store.upsert(account.to_auth())
        verb = "已更新" if not created else "已新增"
        session.set_status("ok", "登录成功，账号 %s %s" % (verb, saved.name()))
        return True, "登录成功，可关闭此窗口"


def callback_base(headers):
    """解析本次登录应使用的回调基址。

    优先级：请求头 X-Gateway-Callback-Base（网关每次登录现算）→ 环境变量
    AI_GATEWAY_CALLBACK_BASE（进程启动时算好的兜底）→ 空串（插件被单独调试）。
    """
    value = (headers.get("X-Gateway-Callback-Base") or "").strip()
    if value:
        return value.rstrip("/")
    return (os.environ.get("AI_GATEWAY_CALLBACK_BASE") or "").strip().rstrip("/")


LOGIN_PAGE_HTML = """<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{TITLE}}</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#0f1115;color:#e6e6e6;
display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#181b21;padding:40px 56px;border-radius:14px;text-align:center;box-shadow:0 8px 30px rgba(0,0,0,.4)}
h1{color:{{COLOR}};font-size:22px;margin:0 0 12px}p{color:#9aa4b2;margin:0;font-size:14px}</style>
</head><body><div class="card"><h1>{{TITLE}}</h1><p>{{MSG}}</p></div></body></html>"""


def render_login_page(ok, message):
    """返回友好的 HTML 提示（不返回 JSON）。"""
    title = "登录成功" if ok else "登录失败"
    color = "#1a7f37" if ok else "#d93025"
    return (LOGIN_PAGE_HTML
            .replace("{{TITLE}}", title)
            .replace("{{COLOR}}", color)
            .replace("{{MSG}}", message))
