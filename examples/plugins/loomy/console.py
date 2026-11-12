# -*- coding: utf-8 -*-
"""控制台接口（docs/plugin-spec.md §6 约定）。

Loomy 的登录是**两步短信**，故声明两个带 `fields` 的登录方法：
  sms-code  —— 填手机号，发验证码
  sms-login —— 填手机号 + 验证码，完成登录
"""

from chat import account_for_request, ensure_token, models_payload
from upstream import UpstreamError, as_upstream_error
from util import now_millis, sanitize_error, truncate_runes

LOGIN_METHODS = [
    {
        "id": "sms-code",
        "label": "① 发送短信验证码",
        "hint": "填写手机号，讯飞会给它发一条验证码短信",
        "fields": [
            {"name": "phone", "label": "手机号", "placeholder": "13800138000"},
        ],
    },
    {
        "id": "sms-login",
        "label": "② 提交短信验证码",
        "hint": "填同一个手机号与收到的验证码，完成后账号自动入库",
        "fields": [
            {"name": "phone", "label": "手机号", "placeholder": "13800138000"},
            {"name": "code", "label": "验证码", "kind": "secret", "placeholder": "6 位数字"},
        ],
    },
]


def account_view(account):
    return {
        "id": account.uid,
        "name": account.name(),
        "site": "cn",
        "enabled": account.usable(),
        "plan": account.plan or "Loomy",
        "credits": account.credits,
        "checked_in_today": account.checked_in_today,
        "expires_at": int(account.expires_at or 0),
        "last_error": sanitize_error(account.last_error),
    }


def handle_accounts_list(plugin):
    accounts = plugin.store.list()
    out = {"accounts": [account_view(a) for a in accounts], "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = ("还没有账号：先点「① 发送短信验证码」，收到短信后点"
                         "「② 提交短信验证码」完成登录（两步用同一个手机号）")
    return 200, out


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_login_start(plugin, method, payload, headers):
    if method == "sms-code":
        return plugin.login.send_code(payload or {})
    if method == "sms-login":
        return plugin.login.login(payload or {})
    return 400, {"error": "未知的登录方式：" + str(method)}


def handle_checkin(plugin, account_id):
    """签到 = 触发每日额度初始化（幂等：`alreadyProcessed` 为真即“今天已处理”）。"""
    account = plugin.store.get(account_id)
    if account is None:
        return 404, {"error": "账号不存在"}
    try:
        ensure_token(plugin, account)
        _granted, message = plugin.upstream.claim_daily(account.token)
    except Exception as exc:
        ue = as_upstream_error(exc)
        plugin.note_error(ue, account)
        return ue.status, {"error": truncate_runes(ue.msg, 120)}

    points = None
    try:
        points = plugin.upstream.fetch_points(account.token)
    except Exception:
        points = None

    def mutate(st):
        st.checked_in_today = True
        if points is not None:
            st.credits = points["total"]
            st.credits_at = now_millis()
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate) or account
    return 200, {"message": message, "account": account_view(updated)}


def handle_quota(plugin, account_id):
    account = plugin.store.get(account_id)
    if account is None:
        return 404, {"error": "账号不存在"}
    notes = []
    total, permanent, daily = account.credits, None, None
    try:
        ensure_token(plugin, account)
        points = plugin.upstream.fetch_points(account.token)
        total = points["total"]
        permanent = points["permanent"]
        daily = points["daily"]
    except UpstreamError as err:
        notes.append("积分查询失败：" + truncate_runes(err.msg, 60))
        plugin.note_error(err, account)
    except Exception as exc:
        notes.append("积分查询失败：" + truncate_runes(as_upstream_error(exc).msg, 60))

    def mutate(st):
        st.credits = total
        st.credits_at = now_millis()
        st.last_error = "；".join(notes)[:100] if notes else ""

    updated = plugin.store.update_state(account_id, mutate) or account
    message = "积分已刷新"
    if permanent is not None and daily is not None:
        message += "：永久 %s + 每日 %s" % (_fmt(permanent), _fmt(daily))
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


def _fmt(value):
    value = float(value)
    return ("%d" % value) if value.is_integer() else ("%.2f" % value)


__all__ = [
    "handle_accounts_list", "handle_account_delete", "handle_checkin", "handle_quota",
    "handle_models", "handle_login_start", "account_view", "LOGIN_METHODS",
]
