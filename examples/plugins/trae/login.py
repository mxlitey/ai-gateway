# -*- coding: utf-8 -*-
"""网页登录（link 类，对应 Go 版 login.go）。

**回调地址必须是 http://127.0.0.1:<port>/authorize**：
TRAE 官方授权页（www.trae.cn/authorization）对 auth_callback_url 做硬校验，
线上页面代码为 `/^http:\\/\\/127\\.0\\.0\\.1:(\\d+)\\/authorize$/`，
不匹配就直接进入「登录失败 / 网络错误，请刷新页面重试」。

网关的回调入口是 `http://127.0.0.1:<网关监听端口+1>`，它把 /authorize 原样透传到本插件，
因此上面的正则依然匹配（路径与回环主机都对，端口只要是数字即可）。

流程：
  ① POST /console/login/web → 拼出 <网关注入的回调基址>/authorize，返回 {status,url,state}
  ② 面板打开 url，用户在浏览器授权，浏览器跳回网关回调入口的 /authorize?<授权参数>
  ③ state 无法放进回调地址（正则要求精确 /authorize），改由服务端单会话跟踪
  ④ 回调里取 refreshToken → ExchangeToken → GetUserInfo → 原子落盘 → 会话标记 ok
  ⑤ 面板轮询 GET /console/login/web?state=xxx 拿到 ok → 自动刷新账号列表

授权使用「单活会话」模型（回调地址带不了 state）：90 秒截止 + 一次性消费，
见 docs/plugin-spec.md §6.7 硬要求 1。

插件只监听清单里的那一个端口，不新增额外端口。
"""

import threading
import time

from accounts import Account
from upstream import as_upstream_error, build_login_url, parse_callback
from util import new_checkin_device_id, random_hex, random_state, truncate_runes

# 无 state 回调必须自己保留截止时间（网关重启后计时登记会丢失，见 plugin-spec §6.7）
LOGIN_TTL_SECONDS = 90


def loopback_base(headers=None, port=0):
    """回调基址：网关回调入口（127.0.0.1:<监听端口+1>）优先，其次插件自身端口。

    官方授权页只接受 `http://127.0.0.1:<任意端口>/authorize` 形态，
    路径部分由本插件自行指定，网关只做原样透传（plugin-spec §6.7）。
    """
    import os

    for name in ("X-Gateway-Callback-Base", "x-gateway-callback-base"):
        value = ((headers or {}).get(name) or "").strip()
        if value:
            return value.rstrip("/")
    value = (os.environ.get("AI_GATEWAY_CALLBACK_BASE") or "").strip()
    if value:
        return value.rstrip("/")
    # 直连调试：网关不在时退回自身端口，回调只能由本机浏览器直达
    return "http://127.0.0.1:%d" % int(port or 0)


class LoginSession:
    __slots__ = ("state", "auth_url", "status", "message", "machine_id", "device_id", "started_at")

    def __init__(self, state, auth_url, machine_id, device_id):
        self.state = state
        self.auth_url = auth_url
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.machine_id = machine_id
        self.device_id = device_id
        self.started_at = time.monotonic()

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
        # 回调地址被官方授权页硬校验为 /authorize，无法再塞 state；
        # 会话由 broker 单条跟踪（面板同一时间也只有一个登录会话）。
        callback_url = callback_base + "/authorize"
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
        """登录状态：有效期由网关计时（90 秒），插件只如实回报会话状态。"""
        with self.mu:
            session = self.by_state.get(state)
        if session is None:
            return "error", "登录会话不存在，请重新发起"
        return session.status, session.message

    def lookup(self, state):
        with self.mu:
            return self.by_state.get(state)

    def current(self):
        """当前登录会话（回调地址无法携带 state，按唯一会话匹配）。"""
        with self.mu:
            return self.cur

    def close_all(self):
        with self.mu:
            self.by_state = {}
            self.cur = None

    def _consume(self, session):
        """一次性消费：处理完即摘掉活动会话，后续回调只能拿到失败页。"""
        with self.mu:
            if self.cur is session:
                self.cur = None

    def handle_callback(self, state, raw_url, raw_query):
        """处理浏览器打到插件 /authorize 的授权回调。

        state 无法出现在回调地址里（官方只接受 /authorize），因此按
        「当前唯一会话」匹配；若带了 state 仍做一致性校验。
        安全模型见 plugin-spec §6.7 硬要求 1：单活会话 + 90 秒截止 + 一次性消费。
        """
        key = (state or "").strip()
        session = self.lookup(key) if key else self.current()
        if session is None:
            return False, "登录会话不存在或已过期，请回到网关控制台重试"
        if session.status != "pending":
            return False, "该登录会话已使用，请回到网关控制台重新发起"
        if time.monotonic() - session.started_at > LOGIN_TTL_SECONDS:
            session.set_status("error", "登录会话已超时，请重新发起")
            self._consume(session)
            return False, "登录会话已超时，请回到网关控制台重试"

        if "refreshToken" not in (raw_query or "") and "userJwt" not in (raw_query or ""):
            # 可能是浏览器预取 /favicon.ico 之类：不消费会话，继续等待真正的回调
            return False, "等待授权回调…"

        try:
            info = parse_callback(raw_url)
        except Exception as exc:
            session.set_status("error", "回调解析失败：" + truncate_runes(str(exc), 60))
            self._consume(session)
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
                self._consume(session)
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
            self._consume(session)
            return False, "登录失败，请回到网关控制台重试"
        if not (account.device_id or "").strip():
            account.device_id = new_checkin_device_id()

        saved, created = plugin.store.upsert(account.to_auth())
        verb = "已更新" if not created else "已新增"
        session.set_status("ok", "登录成功，账号 %s %s" % (verb, saved.name()))
        self._consume(session)
        return True, "登录成功，可关闭此窗口"


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
