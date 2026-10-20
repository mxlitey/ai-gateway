# -*- coding: utf-8 -*-
"""登录：OAuth 授权（link 类）+ 粘贴令牌 / Cookie（fields 类）。

OAuth 回调不自己监听端口，走网关统一入口
    <X-Gateway-Callback-Base>/oauth/<state>  ->  /console/callback/oauth/<state>
state 一次性、带 TTL、不可猜；不认识或已过期的 state 绝不产生任何副作用。
"""

import html
import threading
import time

import util
from accounts import Credential
from upstream import UpstreamError, as_upstream_error

SESSION_TTL = 10 * 60  # 秒；面板每 2 秒轮询一次，10 分钟足够完成授权
MAX_SESSIONS = 32

LOGIN_METHODS = [
    {
        "id": "oauth",
        "label": "OAuth 授权登录",
        "hint": "浏览器打开魔搭授权页；授权后自动回到本机，无需复制粘贴",
    },
    {
        "id": "paste",
        "label": "粘贴令牌登录",
        "hint": "在魔搭「访问令牌」页生成 ms- 令牌；也可粘贴 OAuth 访问令牌，并附上浏览器 Cookie 以解锁每日任务",
        "fields": [
            {"name": "token", "label": "访问令牌", "kind": "secret",
             "placeholder": "ms-xxxxxxxx 或 ms_oauth...", "required": True},
            {"name": "cookie", "label": "浏览器 Cookie（可选）", "kind": "multiline",
             "placeholder": "MSESSION=...; ...", "required": False,
             "hint": "只有携带 Web 会话才能触发每日登录奖励与点赞；不填也能正常对话"},
            {"name": "name", "label": "备注名（可选）", "required": False},
        ],
    },
    {
        "id": "cookie",
        "label": "补充 Web 会话 Cookie",
        "hint": "给已有账号补一份浏览器 Cookie（用于每日登录奖励与点赞），不改变推理令牌",
        "fields": [
            {"name": "account", "label": "账号 id", "required": True,
             "hint": "账号列表里的 id 列"},
            {"name": "cookie", "label": "Cookie", "kind": "multiline",
             "placeholder": "MSESSION=...; ...", "required": True},
        ],
    },
]


class LoginSession:
    __slots__ = ("state", "status", "message", "client_id", "client_secret",
                 "redirect_uri", "created_at", "finished_at")

    def __init__(self, state, client_id, client_secret, redirect_uri):
        self.state = state
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.client_id = client_id
        self.client_secret = client_secret
        self.redirect_uri = redirect_uri
        self.created_at = time.time()
        self.finished_at = 0.0

    def expired(self):
        if self.status != "pending":
            return False
        return time.time() - self.created_at > SESSION_TTL

    def finish(self, status, message):
        self.status = status
        self.message = message
        self.finished_at = time.time()
        # 完成后立刻丢弃 OAuth 客户端凭据以外的会话字段没有意义，
        # 但 client_secret 不再需要使用，主动清掉，缩小内存里的凭据面
        self.client_secret = ""


class LoginBroker:
    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        self.sessions = {}

    # ---------- 会话 ----------

    def _prune_locked(self):
        now = time.time()
        stale = [k for k, s in self.sessions.items()
                 if s.finished_at and now - s.finished_at > SESSION_TTL]
        for key in stale:
            self.sessions.pop(key, None)
        while len(self.sessions) > MAX_SESSIONS:
            oldest = min(self.sessions.values(), key=lambda s: s.created_at)
            self.sessions.pop(oldest.state, None)

    def start(self, callback_base):
        """发起 OAuth：动态注册 → 生成授权链接。callback_base 由网关请求头给出。"""
        state = util.new_state()
        redirect_uri = callback_base.rstrip("/") + "/oauth/" + state
        client_id, client_secret = self.plugin.upstream.register_client(redirect_uri)
        session = LoginSession(state, client_id, client_secret, redirect_uri)
        with self.mu:
            self._prune_locked()
            self.sessions[state] = session
        url = self.plugin.upstream.authorize_url(client_id, redirect_uri, state)
        self.plugin.log("[login] 发起 OAuth 授权 state=%s…" % state[:8])
        return {
            "status": "pending",
            "message": "请在浏览器中完成授权（10 分钟内有效）",
            "url": url,
            "state": state,
        }

    def status(self, state):
        with self.mu:
            session = self.sessions.get(state)
        if session is None:
            return "expired", "登录会话不存在或已过期，请重新发起登录"
        if session.expired():
            with self.mu:
                session.finish("expired", "授权超时（10 分钟），请重新发起登录")
        return session.status, session.message

    # ---------- 回调 ----------

    def handle_callback(self, state, query):
        """处理 /console/callback/oauth/<state>?code=...&state=... 并返回 HTML。"""
        with self.mu:
            session = self.sessions.get(state)
        if session is None:
            return _page(False, "登录会话不存在或已过期，请回到网关面板重新发起登录")
        if session.status != "pending":
            return _page(session.status == "ok", session.message)
        if session.expired():
            with self.mu:
                session.finish("expired", "授权超时（10 分钟），请重新发起登录")
            return _page(False, "授权超时（10 分钟），请回到网关面板重新发起登录")

        code = (query.get("code") or [""])[0].strip()
        returned_state = (query.get("state") or [""])[0].strip()
        error = (query.get("error") or [""])[0].strip()
        if error:
            description = (query.get("error_description") or [""])[0].strip()
            with self.mu:
                session.finish("error", "授权被拒：%s %s" % (error, description))
            return _page(False, "授权被拒：%s %s" % (html.escape(error), html.escape(description)))
        if not code:
            with self.mu:
                session.finish("error", "回调缺少授权码")
            return _page(False, "回调缺少授权码（code），请重新发起登录")
        if returned_state and returned_state != state:
            with self.mu:
                session.finish("error", "回调 state 不匹配")
            return _page(False, "回调 state 校验失败，请重新发起登录")

        try:
            tokens = self.plugin.upstream.exchange_code(
                code, session.client_id, session.client_secret, session.redirect_uri)
            info = self.plugin.upstream.userinfo(tokens["token"])
            uid = util.first_non_empty(util.as_str(info.get("sub")),
                                       util.as_str(info.get("username")))
            if not uid:
                # userinfo 取不到身份时退回 users/me（同一令牌，两个端点互为兜底）
                uid, _name = self.plugin.upstream.fetch_identity(tokens["token"])
            if not uid:
                raise UpstreamError(502, "未能解析账号身份（uid）")
            uid = _safe_uid(uid)
            if not uid:
                raise UpstreamError(502, "账号身份包含不可用字符（uid）")
            name = util.first_non_empty(
                util.as_str(info.get("nickname")), util.as_str(info.get("username")), uid)
            cred = Credential(
                uid=uid, nickname=name, token=tokens["token"],
                refresh_token=tokens["refresh_token"],
                expires_at=tokens.get("expires_at") or 0,
                cookie="",
                oauth_client_id=session.client_id,
                oauth_client_secret=session.client_secret,
                scope=tokens.get("scope") or self.plugin.eps.oauth_scopes,
                source="oauth",
            )
            saved, created = self.plugin.store.upsert(cred)
        except Exception as exc:
            message = as_upstream_error(exc).msg
            with self.mu:
                session.finish("error", "登录失败：%s" % util.truncate_runes(message, 80))
            self.plugin.log("[login] OAuth 失败：%s" % message)
            return _page(False, "登录失败：" + html.escape(util.truncate_runes(message, 160)))

        with self.mu:
            session.finish("ok", "登录成功：%s" % saved.name())
        self.plugin.log("[login] OAuth 成功 %s（%s）" % (saved.name(), "新增" if created else "更新"))
        return _page(True, "登录成功，账号 %s 已%s；可以关闭本页回到网关面板。"
                     % (html.escape(saved.name()), "加入" if created else "更新"))

    # ---------- 非 OAuth 登录 ----------

    def paste_token(self, payload):
        """粘贴令牌（ms- 用户令牌或 ms_oauth 令牌）。可附带 Cookie。"""
        token = (payload.get("token") or "").strip()
        cookie = (payload.get("cookie") or "").strip()
        name_hint = (payload.get("name") or "").strip()
        if not token:
            raise UpstreamError(400, "请填写访问令牌")
        if " " in token or "\n" in token:
            raise UpstreamError(400, "访问令牌里不应含空格或换行")

        uid, name = self.plugin.upstream.fetch_identity(token)
        if not uid:
            raise UpstreamError(400, "令牌有效但未取到用户名，无法作为账号标识")
        uid = _safe_uid(uid)
        if not uid:
            raise UpstreamError(400, "账号身份包含不可用字符（uid），无法作为账号标识")
        cred = Credential(
            uid=uid, nickname=name_hint or name or uid, token=token,
            refresh_token="", expires_at=0, cookie=cookie,
            oauth_client_id="", oauth_client_secret="", scope="token", source="paste",
        )
        saved, created = self.plugin.store.upsert(cred)
        self.plugin.log("[login] 粘贴令牌 %s（%s，cookie=%s）" % (
            saved.name(), "新增" if created else "更新", "有" if cookie else "无"))
        return saved, created

    def attach_cookie(self, payload):
        """给已有账号补 Web 会话 Cookie（只影响每日任务，不改推理令牌）。"""
        account_id = (payload.get("account") or "").strip()
        cookie = (payload.get("cookie") or "").strip()
        if not account_id:
            raise UpstreamError(400, "请填写账号 id")
        if not cookie:
            raise UpstreamError(400, "请填写 Cookie")
        acc = self.plugin.store.get(account_id)
        if acc is None:
            raise UpstreamError(404, "账号不存在：%s" % account_id)
        from chat import _credential_of

        cred = _credential_of(acc)
        cred.cookie = cookie
        self.plugin.store.save_cred(cred)
        self.plugin.log("[login] 已更新 %s 的 Web 会话 Cookie" % acc.name())
        return self.plugin.store.get(account_id), False


def _safe_uid(uid):
    """账号 id 会出现在控制台 URL 路径里（/console/accounts/{id}/...）：不能含 / 与空白。"""
    value = (uid or "").strip().replace("/", "_").replace("\\", "_")
    return "".join(ch for ch in value if ch.isprintable() and not ch.isspace())


def _page(ok, message):
    """回调结果页：成功 / 失败都返回给人看的 HTML（面板不依赖其内容）。"""
    title = "授权成功" if ok else "授权失败"
    color = "#0a7d32" if ok else "#d93025"
    return """<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
  body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#f5f5f7;color:#1d1d1f;
       display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
  .card{background:#fff;padding:40px 56px;border-radius:18px;text-align:center;
        box-shadow:0 12px 40px rgba(0,0,0,.08);max-width:520px}
  h1{color:%s;font-size:20px;margin:0 0 12px;font-weight:600}
  p{color:#6e6e73;margin:0;font-size:14px;line-height:1.6}
</style></head><body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>""" % (
        title, color, title, message)
