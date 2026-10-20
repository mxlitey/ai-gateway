# -*- coding: utf-8 -*-
"""控制台接口（docs/plugin-spec.md §6 约定）。"""

from chat import account_for_request, ensure_token, models_payload
from login import render_page
from upstream import UpstreamError, as_upstream_error
from util import as_str, now_millis, sanitize_error, to_float_ok, truncate_runes

LOGIN_METHODS = [
    {
        "id": "oauth",
        "label": "LobsterAI 授权登录",
        "hint": "点一下会打开有道龙虾的登录页，完成后账号自动入库",
    },
]


def account_view(account):
    return {
        "id": account.uid,
        "name": account.name(),
        "site": "cn",
        "enabled": account.usable(),
        "plan": account.plan or "LobsterAI",
        "credits": account.credits,
        "expires_at": int(account.expires_at or 0),
        "last_error": sanitize_error(account.last_error),
    }


def handle_accounts_list(plugin):
    accounts = plugin.store.list()
    out = {"accounts": [account_view(a) for a in accounts], "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = "还没有账号：点「LobsterAI 授权登录」在浏览器里完成授权"
    return 200, out


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_login_start(plugin, method, payload, headers):
    if method != "oauth":
        return 400, {"error": "未知的登录方式：" + str(method)}
    if not plugin.port:
        return 500, {"error": "插件端口未知，无法生成回环回调地址"}
    try:
        return 200, plugin.login.start()
    except UpstreamError as err:
        return err.status, {"error": truncate_runes(err.msg, 120)}
    except Exception as exc:
        return 502, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}


def handle_login_status(plugin, method, query):
    if method != "oauth":
        return 400, {"error": "该登录方式不需要轮询"}
    state = (query.get("state") or [""])[0].strip()
    if not state:
        return 400, {"error": "缺少 state"}
    status, message = plugin.login.status(state)
    return 200, {"status": status, "message": message}


def handle_callback(plugin, state, query):
    """GET /auth/callback?code=...&state=...：返回给人看的 HTML。"""
    ok, message = plugin.login.handle_callback(state, query)
    return render_page(ok, message)


def handle_quota(plugin, account_id):
    account = plugin.store.get(account_id)
    if account is None:
        return 404, {"error": "账号不存在"}
    try:
        ensure_token(plugin, account)
    except Exception as exc:
        ue = as_upstream_error(exc)
        plugin.note_error(ue, account)
        return ue.status, {"error": truncate_runes(ue.msg, 120)}

    notes = []
    credits = account.credits
    plan = account.plan
    try:
        data = plugin.upstream.fetch_profile(account.token)
        value, ok = to_float_ok(data.get("balance"))
        if ok:
            credits = value
        name = as_str(data.get("planName")) or as_str(data.get("plan"))
        if name:
            plan = name
    except UpstreamError as err:
        notes.append("额度查询失败：" + truncate_runes(err.msg, 60))

    def mutate(st):
        st.credits = credits
        st.credits_at = now_millis()
        if plan:
            st.plan = plan
        st.last_error = "；".join(notes)[:100] if notes else ""

    updated = plugin.store.update_state(account_id, mutate) or account
    message = "额度已刷新"
    if notes:
        message += "（%s）" % "；".join(notes)
    return 200, {"message": message, "account": account_view(updated)}


def handle_models(plugin, headers):
    ids = []
    try:
        account = account_for_request(plugin, headers)
        ids = plugin.upstream.fetch_models(account.token)
    except Exception:
        ids = []
    if not ids:
        ids = list(plugin.eps.fallback_models)
    return 200, models_payload(ids)


__all__ = [
    "handle_accounts_list", "handle_account_delete", "handle_quota", "handle_models",
    "handle_login_start", "handle_login_status", "handle_callback", "account_view",
    "LOGIN_METHODS", "render_page",
]
