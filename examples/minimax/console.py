# -*- coding: utf-8 -*-
"""控制台接口（docs/plugin-spec.md §6 约定）。"""

from chat import account_for_request, ensure_token, models_payload
from upstream import UpstreamError, as_upstream_error
from util import now_millis, sanitize_error, truncate_runes

LOGIN_METHODS = [
    {
        "id": "device",
        "label": "MiniMax Code 设备码登录",
        "hint": "点一下会打开 MiniMax 授权页，确认用户码后在面板里等结果（无需本机客户端）",
    },
]


def account_view(account):
    return {
        "id": account.uid,
        "name": account.name(),
        "site": "cn",
        "enabled": account.usable(),
        "plan": account.plan or "MiniMax Code",
        "credits": account.credits,
        "checked_in_today": account.checked_in_today,
        "streak_days": account.streak_days,
        "expires_at": int(account.expires_at or 0),
        "last_error": sanitize_error(account.last_error),
    }


def handle_accounts_list(plugin):
    accounts = plugin.store.list()
    out = {"accounts": [account_view(a) for a in accounts], "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = "还没有账号：点「MiniMax Code 设备码登录」在浏览器里完成授权"
    return 200, out


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_login_start(plugin, method, payload, headers):
    if method != "device":
        return 400, {"error": "未知的登录方式：" + str(method)}
    try:
        return 200, plugin.login.start()
    except UpstreamError as err:
        return err.status, {"error": truncate_runes(err.msg, 120)}
    except Exception as exc:
        return 502, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}


def handle_login_status(plugin, method, query):
    if method != "device":
        return 400, {"error": "该登录方式不需要轮询"}
    state = (query.get("state") or [""])[0].strip()
    if not state:
        return 400, {"error": "缺少 state"}
    status, message = plugin.login.status(state)
    return 200, {"status": status, "message": message}


def handle_checkin(plugin, account_id):
    """签到（每日积分）。必须幂等：今天已签返回成功语义 + 说明。"""
    account = plugin.store.get(account_id)
    if account is None:
        return 404, {"error": "账号不存在"}
    try:
        ensure_token(plugin, account)
        state = plugin.upstream.fetch_checkin(account.token)
    except Exception as exc:
        ue = as_upstream_error(exc)
        plugin.note_error(ue, account)
        return ue.status, {"error": truncate_runes(ue.msg, 120)}

    if state.get("checked_in_today"):
        def mutate_skip(st):
            st.checked_in_today = True
            st.streak_days = int(state.get("streak_days") or st.streak_days)
            st.last_error = ""

        updated = plugin.store.update_state(account_id, mutate_skip) or account
        return 200, {"message": "今日已签到", "account": account_view(updated)}

    credit = 0
    streak = int(state.get("streak_days") or 0)
    try:
        data = plugin.upstream.claim_checkin(account.token)
        value = data.get("claim_result")
        # 2 = 已领过（幂等成功）
        if isinstance(value, (int, float)) and not isinstance(value, bool) and value == 1:
            points = data.get("points")
            if isinstance(points, (int, float)) and not isinstance(points, bool):
                credit = int(points)
    except Exception as exc:
        ue = as_upstream_error(exc)
        plugin.note_error(ue, account)
        return ue.status, {"error": truncate_runes(ue.msg, 120)}

    def mutate(st):
        st.checked_in_today = True
        if streak:
            st.streak_days = streak
        if credit:
            st.credits += credit
        st.credits_at = now_millis()
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate) or account
    message = "签到成功" + ("，+%d 积分" % credit if credit else "")
    return 200, {"message": message, "account": account_view(updated)}


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
    try:
        credits = plugin.upstream.fetch_credits(account.token)
    except UpstreamError as err:
        notes.append("积分查询失败：" + truncate_runes(err.msg, 60))

    checked_in, streak = account.checked_in_today, account.streak_days
    try:
        state = plugin.upstream.fetch_checkin(account.token)
        checked_in = bool(state.get("checked_in_today"))
        streak = int(state.get("streak_days") or streak)
    except UpstreamError as err:
        notes.append("签到状态查询失败：" + truncate_runes(err.msg, 60))

    def mutate(st):
        st.credits = credits
        st.credits_at = now_millis()
        st.checked_in_today = checked_in
        st.streak_days = streak
        st.plan = "MiniMax Code"
        st.last_error = "；".join(notes)[:100] if notes else ""

    updated = plugin.store.update_state(account_id, mutate) or account
    message = "额度已刷新"
    if notes:
        message += "（%s）" % "；".join(notes)
    return 200, {"message": message, "account": account_view(updated)}


def handle_models(plugin, headers):
    entries = []
    try:
        account = account_for_request(plugin, headers)
        entries = plugin.upstream.fetch_models(account.token)
    except Exception:
        entries = []
    if not entries:
        entries = list(plugin.eps.fallback_models)
    return 200, models_payload(entries)


__all__ = [
    "handle_accounts_list", "handle_account_delete", "handle_checkin", "handle_quota",
    "handle_models", "handle_login_start", "handle_login_status", "account_view",
    "LOGIN_METHODS",
]
