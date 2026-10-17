# -*- coding: utf-8 -*-
"""控制台接口（对应 Go 版 console.go + oauth.go 的控制台部分）。

按 docs/plugin-spec.md §6 的约定实现，界面由网关面板统一渲染：

    GET    /console/accounts                      账号列表（含可用登录方式）
    POST   /console/accounts                      手工新增账号（粘贴 token）
    DELETE /console/accounts/{id}                 删除账号
    POST   /console/accounts/{id}/checkin         签到
    POST   /console/accounts/{id}/quota           刷新额度
    POST   /console/login/{method}                发起登录（method=desktop | oauth-cn | oauth-intl）
    GET    /console/login/oauth-{cn,intl}?state=x 轮询授权结果
"""

import json
import threading
import time

from accounts import Account
from chat import fetch_models, model_list_payload
from jwtutil import jwt_exp_millis, jwt_uid, normalize_epoch_millis, realm_from_token
from upstream import (
    UpstreamError, do_checkin, ensure_token, fetch_checkin_status, fetch_quota,
    realm_config, refresh_token,
)
from util import (
    as_str, clamp_bytes, first_non_empty, now_millis, sanitize_error, short_uid,
)

LOGIN_TTL = 10 * 60  # 授权会话有效期（秒）

# 面板只按方式 id 调用，所以「国内站 / 国际站」用两个方法 id 来表达。
OAUTH_REALM_BY_METHOD = {"oauth-cn": "cn", "oauth-intl": "intl"}


# ---------- 账号视图 ----------


def account_view(acc):
    """账号对外的安全视图：只暴露面板需要的字段，绝不含任何凭据。"""
    return {
        "id": acc.id,
        "name": acc.name,
        "site": acc.site,
        "enabled": acc.enabled,
        "plan": acc.plan,
        "credits": acc.credits,
        "checked_in_today": acc.checked_in_today,
        "streak_days": acc.streak_days,
        "expires_at": acc.token_expires_at,
        "last_error": sanitize_error(acc.last_error),
    }


def account_view_list(accounts):
    return [account_view(a) for a in accounts]


def login_methods(oauth_enabled):
    """可用登录方式：desktop 恒有；OAuth 由插件是否启用决定。"""
    methods = [{
        "id": "desktop",
        "label": "从本机桌面端导入",
        "hint": "读取本机 WorkBuddy 的登录态，需要桌面端正在运行",
    }]
    if oauth_enabled:
        methods.extend([
            {
                "id": "oauth-cn",
                "label": "OAuth 授权登录（国内站）",
                "hint": "在本机浏览器打开授权链接登录国内站（codebuddy.cn），完成后账号自动入库",
            },
            {
                "id": "oauth-intl",
                "label": "OAuth 授权登录（国际站）",
                "hint": "在本机浏览器打开授权链接登录国际站（workbuddy.ai），完成后账号自动入库",
            },
        ])
    return methods


# ---------- 账号接口 ----------


def handle_accounts_list(plugin):
    accounts = account_view_list(plugin.store.list())
    out = {
        "accounts": accounts,
        "login_methods": login_methods(plugin.oauth is not None),
    }
    if not accounts:
        out["notice"] = "还没有账号：可以点「从本机桌面端导入」，或用 OAuth 授权登录"
    return 200, out


def handle_account_add(plugin, body):
    """手工新增账号（粘贴 token）：body 字段 name / site / token / uid 全部可选（token 除外）。"""
    if not isinstance(body, dict):
        body = {}
    token = (body.get("token") or "").strip()
    if not token:
        return 400, {"error": "请提供 token"}
    uid = (body.get("uid") or "").strip() or jwt_uid(token)
    if not uid:
        return 400, {"error": "无法从 token 解析用户 id，请一并提供 uid"}
    site = (body.get("site") or "").strip()
    if site not in ("cn", "intl"):
        site = first_non_empty(realm_from_token(token, ""), "cn")

    acc = Account(
        id=uid,
        uid=uid,
        name=first_non_empty((body.get("name") or "").strip(), short_uid(uid)),
        site=site,
        domain=realm_config(plugin.eps, site).domain,
        access_token=token,
        token_expires_at=jwt_exp_millis(token),
        source="manual",
        added_at=now_millis(),
    )
    saved, created = plugin.store.upsert(acc)
    plugin.spawn_refresh(saved)

    verb = "已新增" if created else "已更新"
    return 200, {"message": "%s账号 %s" % (verb, saved.name), "account": account_view(saved)}


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_checkin(plugin, account_id):
    acc = plugin.store.get(account_id)
    if acc is None:
        return 404, {"error": "账号不存在"}
    try:
        ensure_token(plugin.eps, acc, plugin.store)
    except Exception as exc:
        return 409, {"error": str(exc)}

    try:
        result, msg = do_checkin(plugin.eps, acc)
    except Exception as exc:
        plugin.store.update(account_id, lambda a: setattr(a, "last_error", str(exc)))
        return 502, {"error": str(exc)}

    def mutate(a):
        a.checked_in_today = result.checked_in_today
        a.last_error = ""
        if result.streak_days > 0:
            a.streak_days = result.streak_days
        if result.today_credit > 0:
            a.today_credit = result.today_credit

    updated = plugin.store.update(account_id, mutate)
    plugin.spawn_refresh(updated)
    return 200, {"message": msg, "account": account_view(updated)}


def handle_quota(plugin, account_id):
    acc = plugin.store.get(account_id)
    if acc is None:
        return 404, {"error": "账号不存在"}
    try:
        ensure_token(plugin.eps, acc, plugin.store)
    except Exception as exc:
        return 409, {"error": str(exc)}

    try:
        updated, _packages, msg = fetch_quota(plugin.eps, acc)
    except Exception as exc:
        plugin.store.update(account_id, lambda a: setattr(a, "last_error", str(exc)))
        return 502, {"error": str(exc)}

    def mutate(a):
        a.credits = updated.credits
        a.credits_at = updated.credits_at
        a.plan = updated.plan
        a.last_error = ""

    saved = plugin.store.update(account_id, mutate)
    return 200, {"message": msg, "account": account_view(saved)}


# ---------- 登录 ----------


def handle_login_start(plugin, method, body):
    """发起登录：action 类（desktop）当场完成，link 类（oauth-*）返回授权链接。"""
    if method == "desktop":
        return login_from_desktop(plugin)
    realm = OAUTH_REALM_BY_METHOD.get(method)
    if realm is None:
        return 400, {"error": "未知的登录方式：" + method}
    if plugin.oauth is None:
        return 501, {"error": "本插件未启用 OAuth 登录"}
    if isinstance(body, dict):
        override = (body.get("realm") or "").strip()
        if override:
            realm = override

    try:
        session = plugin.oauth.start(realm)
    except Exception as exc:
        return 502, {"error": str(exc)}
    return 200, {
        "status": session.status,
        "message": session.message,
        "url": session.auth_url,
        "state": session.state,
    }


def handle_login_status(plugin, method, query):
    """轮询授权结果：GET /console/login/oauth-cn?state=xxx。"""
    if method not in OAUTH_REALM_BY_METHOD:
        return 400, {"error": "未知的登录方式：" + method}
    if plugin.oauth is None:
        return 501, {"error": "本插件未启用 OAuth 登录"}
    values = query.get("state") or [""]
    state = (values[0] or "").strip()
    if not state:
        return 400, {"error": "缺少 state"}
    status, message = plugin.oauth.status(state)
    return 200, {"status": status, "message": message}


def login_from_desktop(plugin):
    """从本机桌面端读取登录态导入账号（action 类登录）。"""
    import desktop

    accounts, notes, err = desktop.import_from_desktop()
    if err is not None:
        msg = "；".join(notes) if notes else str(err)
        return 502, {"error": msg}
    created = 0
    for acc in accounts:
        _saved, is_new = plugin.store.upsert(acc)
        if is_new:
            created += 1
        plugin.spawn_refresh(acc)
    msg = "已导入 %d 个账号" % len(accounts)
    if created != len(accounts):
        msg = "已更新 %d 个账号（其中 %d 个新增）" % (len(accounts), created)
    return 200, {"status": "ok", "message": msg}


# ---------- 后台刷新 ----------


def refresh_account(plugin, acc):
    """顺带刷新额度与签到状态；失败只记在账号上，不打扰用户操作。"""
    try:
        updated, _packages, _msg = fetch_quota(plugin.eps, acc)
    except Exception as exc:
        plugin.store.update(acc.id, lambda a: setattr(a, "last_error", str(exc)))
        return

    checked_in = updated.checked_in_today
    streak = updated.streak_days
    today_credit = updated.today_credit
    try:
        status = fetch_checkin_status(plugin.eps, acc)
        checked_in = status.checked_in_today
        streak = status.streak_days
        if status.today_credit > 0:
            today_credit = status.today_credit
    except Exception:
        pass

    def mutate(a):
        a.credits = updated.credits
        a.credits_at = updated.credits_at
        a.plan = updated.plan
        a.checked_in_today = checked_in
        a.streak_days = streak
        a.today_credit = today_credit
        a.last_error = ""

    plugin.store.update(acc.id, mutate)


# ---------- 模型 ----------


def handle_models(plugin, headers):
    """GET /v1/models：拉不到就回退空列表，绝不报错（选号失败也是 200）。"""
    return 200, model_list_payload(fetch_models(plugin.eps, plugin.store, headers))


# ---------- OAuth 会话 ----------


class OAuthSession:
    __slots__ = ("state", "auth_url", "realm", "status", "message", "created")

    def __init__(self, state, auth_url, realm):
        self.state = state
        self.auth_url = auth_url
        self.realm = realm
        self.status = "pending"
        self.message = "请在浏览器中完成授权"
        self.created = time.time()


class OAuthManager:
    """OAuth 免客户端登录：全程不需要桌面端在场。

    1. POST {chat_origin}/v2/plugin/auth/state?platform=CLI  → {state, authUrl}
    2. 用户在本机浏览器打开 authUrl，用自己的方式登录授权
    3. GET  {chat_origin}/v2/plugin/auth/token?state=xxx      → code=11217 表示还在等
    4. GET  {chat_origin}/v2/plugin/login/account?state=xxx   → 昵称（可选）
    """

    def __init__(self, plugin):
        self.plugin = plugin
        self.mu = threading.Lock()
        self.sessions = {}

    def start(self, realm):
        import upstream

        if realm not in ("cn", "intl"):
            realm = self._default_realm()
        state, auth_url = upstream.oauth_start(self.plugin.eps, realm, realm)
        session = OAuthSession(state, auth_url, realm)
        with self.mu:
            self.sessions[state] = session
        return session

    def _default_realm(self):
        """优先跟随已有账号的区域，否则按国内站处理。"""
        for acc in self.plugin.store.list():
            if acc.site in ("cn", "intl"):
                return acc.site
        return "cn"

    def status(self, state):
        with self.mu:
            session = self.sessions.get(state)
        if session is None:
            return "error", "授权会话不存在，请重新发起"
        if time.time() - session.created > LOGIN_TTL:
            session.status = "expired"
            session.message = "授权窗口已过期，请重新发起"
        if session.status == "pending":
            self._poll(session)
        return session.status, session.message

    def _poll(self, session):
        import upstream

        try:
            env = upstream.oauth_poll(self.plugin.eps, session.realm, session.state)
        except Exception:
            # 轮询失败不算错误，下一次再试
            session.message = "等待授权中…"
            return
        if env["code"] == upstream.OAUTH_PENDING_CODE:
            session.message = first_non_empty(env["msg"], "等待浏览器中完成授权")
            return
        if env["code"] != 0:
            session.status = "error"
            session.message = first_non_empty(env["msg"], "授权失败 code=%d" % env["code"])
            return

        data = env["data"] if isinstance(env["data"], dict) else {}
        token = as_str(data.get("accessToken"))
        if not token:
            session.message = "等待上游下发 token…"
            return
        uid = jwt_uid(token)
        if not uid:
            session.status = "error"
            session.message = "无法从 token 解析用户 id，请重试"
            return

        site = realm_from_token(token, as_str(data.get("domain"))) or session.realm
        expires = normalize_epoch_millis(data.get("expiresAt")) or jwt_exp_millis(token)
        cfg = realm_config(self.plugin.eps, site)

        acc = Account(
            id=uid,
            uid=uid,
            name=first_non_empty(
                upstream.oauth_nickname(self.plugin.eps, session.realm, session.state, token),
                short_uid(uid)),
            site=site,
            domain=first_non_empty(as_str(data.get("domain")), cfg.domain),
            access_token=token,
            refresh_token=as_str(data.get("refreshToken")),
            token_expires_at=expires,
            source="oauth",
            added_at=now_millis(),
        )
        saved, created = self.plugin.store.upsert(acc)
        verb = "已新增" if created else "已更新"
        session.status = "ok"
        session.message = "授权成功，账号 %s %s" % (verb, saved.name)

        # 入库后顺手刷一次额度（OAuth 账号可以自己续期，所以这里也顺带验证连通性）
        self.plugin.spawn_refresh(saved)


# ---------- 错误响应 ----------


def write_error(status, msg):
    """错误响应体：文案压到 240 字节内（OpenAI 入口只透传前 300 字节）。"""
    if status < 400:
        status = 400
    return status, {"error": clamp_bytes(msg, 240)}


def read_json_body(raw):
    """容错解析请求体 JSON（失败返回空字典）。"""
    if not raw:
        return {}
    try:
        doc = json.loads(raw.decode("utf-8"))
    except Exception:
        return {}
    return doc if isinstance(doc, dict) else {}
