# -*- coding: utf-8 -*-
"""控制台接口（docs/plugin-spec.md §6 约定）。

    GET    /console/accounts               账号列表（含 notice / login_methods）
    POST   /console/accounts               手工导入（面板不调用，供脚本使用）
    DELETE /console/accounts/{id}          删除账号
    POST   /console/accounts/{id}/checkin  签到（宿主自动签到也走这里，必须幂等）
    POST   /console/accounts/{id}/quota    刷新额度
    POST   /console/login/{method}         发起登录
    GET    /console/login/{method}?state=  轮询登录结果（link 类）
    GET    /console/callback/oauth/{state} 统一回调入口转发到这里

账号响应里绝不含任何凭据（token / refresh_token / cookie）。
"""

import random
import time

from chat import account_for_request, ensure_token, models_payload, static_models
from login import LOGIN_METHODS
from upstream import UpstreamError, as_upstream_error
from util import clamp_bytes, now_millis, sanitize_error, truncate_runes


def account_view(account):
    view = {
        "id": account.uid,
        "name": account.name(),
        "site": "cn",
        "enabled": account.usable(),
        "plan": account.plan or ("OAuth" if account.is_oauth() else "访问令牌"),
        "credits": account.credits,
        "checked_in_today": account.checked_in_today,
        "expires_at": int(account.expires_at or 0),
        "last_error": sanitize_error(account.last_error),
    }
    if account.streak_days:
        view["streak_days"] = account.streak_days
    return view


def handle_accounts_list(plugin):
    accounts = plugin.store.list()
    out = {"accounts": [account_view(a) for a in accounts], "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = (
            "还没有账号：点「OAuth 授权登录」用魔搭账号授权，"
            "或在「粘贴令牌登录」里填入访问令牌（可附浏览器 Cookie 以解锁每日魔粒任务）"
        )
    return 200, out


def handle_account_add(plugin, body):
    """手工导入（面板不调用）：body 可含 token / cookie / name。"""
    if not isinstance(body, dict):
        body = {}
    try:
        saved, created = plugin.login.paste_token(body)
    except UpstreamError as err:
        return err.status, {"error": truncate_runes(err.msg, 120)}
    except Exception as exc:
        return 400, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}
    return 200, {
        "message": "%s账号 %s" % ("已新增" if created else "已更新", saved.name()),
        "account": account_view(saved),
    }


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_login_start(plugin, method, payload, headers):
    """POST /console/login/{method}。返回体同时含 url 与 state 时面板按 link 处理。"""
    if method == "oauth":
        base = _callback_base(plugin, headers)
        if not base:
            return 500, {"error": "缺少网关回调基址（X-Gateway-Callback-Base），请从网关面板发起登录"}
        try:
            return 200, plugin.login.start(base)
        except UpstreamError as err:
            return err.status, {"error": truncate_runes(err.msg, 120)}
        except Exception as exc:
            return 502, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}
    if method == "paste":
        try:
            saved, created = plugin.login.paste_token(payload or {})
        except UpstreamError as err:
            return err.status, {"error": truncate_runes(err.msg, 120)}
        except Exception as exc:
            return 400, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}
        return 200, {
            "status": "ok",
            "message": "%s账号 %s" % ("已新增" if created else "已更新", saved.name()),
            "account": account_view(saved),
        }
    if method == "cookie":
        try:
            saved, _ = plugin.login.attach_cookie(payload or {})
        except UpstreamError as err:
            return err.status, {"error": truncate_runes(err.msg, 120)}
        return 200, {
            "status": "ok",
            "message": "已更新 %s 的 Web 会话 Cookie" % saved.name(),
            "account": account_view(saved),
        }
    return 400, {"error": "未知的登录方式：" + str(method)}


def handle_login_status(plugin, method, query):
    if method != "oauth":
        return 400, {"error": "该登录方式不需要轮询"}
    state = (query.get("state") or [""])[0].strip()
    if not state:
        return 400, {"error": "缺少 state"}
    status, message = plugin.login.status(state)
    return 200, {"status": status, "message": message}


def handle_callback(plugin, state, query):
    """GET /console/callback/oauth/{state}：返回 HTML（成功 / 失败都给人看）。"""
    return plugin.login.handle_callback(state, query)


# ---------- 签到 ----------


def handle_checkin(plugin, account_id):
    """签到 = 领取每日魔粒任务（Web 会话触碰 + 按余量点赞）。

    必须幂等：今天已经完成时返回成功语义 + 说明，而不是报错——宿主每小时都会调用。
    """
    account = plugin.store.get(account_id)
    if account is None:
        return 404, {"error": "账号不存在"}
    try:
        ensure_token(plugin, account)
    except Exception as exc:
        ue = as_upstream_error(exc)
        plugin.note_error(ue, account)
        return ue.status, {"error": truncate_runes(ue.msg, 120)}

    if not (account.cookie or "").strip():
        # 没有 Web 会话：登录奖励与点赞都做不了。如实说明，不伪造成功。
        message = ("该账号没有 Web 会话 Cookie，无法领取每日登录奖励与点赞；"
                   "请在「补充 Web 会话 Cookie」里粘贴浏览器 Cookie，或改用 OAuth 授权登录")
        plugin.store.update_state(account_id, lambda st: setattr(st, "last_error", message[:100]))
        return 200, {"message": message, "account": account_view(plugin.store.get(account_id))}

    try:
        state = plugin.upstream.checkin_state(account)
    except UpstreamError as err:
        plugin.note_error(err, account)
        return err.status, {"error": truncate_runes(err.msg, 120)}

    gained_before = _balance_or_none(plugin, account)
    touched, dead, note = plugin.upstream.touch_web_session(account)
    if dead:
        message = ("Web 会话 Cookie 已失效，无法领取每日登录奖励与点赞；"
                   "请在「补充 Web 会话 Cookie」里更新，或重新 OAuth 授权")
        plugin.store.update_state(account_id, lambda st: setattr(st, "last_error", message[:100]))
        return 200, {"message": message, "account": account_view(plugin.store.get(account_id))}

    # 点赞：先重读规则拿最新余量（上面的触碰可能刚解锁登录奖励）
    liked = 0
    like_error = ""
    try:
        fresh = plugin.upstream.checkin_state(account)
        remain = min(int(fresh.get("like_remain") or 0),
                     max(1, int(plugin.eps.like_run_cap)), plugin.eps.like_hard_cap)
        if remain > 0:
            targets = plugin.upstream.like_targets(account, remain + 4)
            if not targets:
                like_error = "没有可点赞的 MCP 服务（上游列表为空）"
            deadline = time.time() + max(3.0, float(plugin.eps.like_budget_seconds))
            for owner, name in targets:
                if liked >= remain or time.time() > deadline:
                    break
                if plugin.upstream.like_one(account, owner, name):
                    liked += 1
                time.sleep(random.uniform(
                    plugin.eps.like_delay_min_ms / 1000.0,
                    max(plugin.eps.like_delay_min_ms, plugin.eps.like_delay_max_ms) / 1000.0))
            if liked == 0 and not like_error:
                like_error = "点赞未成功（Cookie 可能无权调用点赞接口）"
    except UpstreamError as err:
        like_error = truncate_runes(err.msg, 80)

    after_state = None
    gained = 0.0
    try:
        after_state = plugin.upstream.checkin_state(account)
        remaining = int(after_state.get("like_remain") or 0)
    except UpstreamError:
        remaining = max(int(state.get("like_remain") or 0) - liked, 0)

    balance_after = _balance_or_none(plugin, account)
    if gained_before is not None and balance_after is not None:
        gained = max(balance_after - gained_before, 0.0)

    def mutate(st):
        st.checked_in_today = bool(after_state.get("checked_in_today")) if after_state else st.checked_in_today
        st.streak_days = int(after_state.get("streak_days") or st.streak_days) if after_state else st.streak_days
        st.credits = balance_after if balance_after is not None else st.credits
        st.credits_at = now_millis()
        st.last_error = like_error[:100]

    updated = plugin.store.update_state(account_id, mutate) or account

    final = after_state or state
    parts = []
    if final.get("daily_done"):
        parts.append("登录奖励已领")
    if final.get("bind_done"):
        parts.append("绑云奖励已领")
    if liked:
        parts.append("点赞 %d 次 +%d 魔粒" % (liked, liked * int(final.get("like_amount") or 2)))
    elif like_error:
        parts.append(like_error)
    if gained > 0:
        parts.append("魔粒 +%s" % _fmt(gained))
    if touched:
        parts.append("已刷新 Web 会话")
    if note:
        parts.append(note)
    if remaining > 0 and not like_error:
        parts.append("仍有 %d 次点赞未做（下轮继续）" % remaining)
    if not parts:
        parts.append(final.get("message") or "今日任务已全部完成")

    return 200, {"message": " · ".join(parts), "account": account_view(updated)}


def _balance_or_none(plugin, account):
    try:
        value, _ = plugin.upstream.fetch_balance(account.token)
        return value
    except Exception:
        return None


def _fmt(value):
    value = float(value)
    return ("%d" % value) if value.is_integer() else ("%.2f" % value)


# ---------- 额度 ----------


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
    checked_in, streak = account.checked_in_today, account.streak_days
    try:
        state = plugin.upstream.checkin_state(account)
        checked_in = bool(state.get("checked_in_today"))
        streak = int(state.get("streak_days") or 0)
    except UpstreamError as err:
        notes.append("签到状态查询失败：" + truncate_runes(err.msg, 60))

    credits, plan = account.credits, account.plan
    try:
        credits, data = plugin.upstream.fetch_balance(account.token)
        plan = _plan_label(data) or plan
    except UpstreamError as err:
        notes.append("余额查询失败：" + truncate_runes(err.msg, 60))

    def mutate(st):
        st.credits = credits
        st.credits_at = now_millis()
        st.checked_in_today = checked_in
        st.streak_days = streak
        if plan:
            st.plan = plan
        # 查询失败时保留原因（面板账号行会显示红色小字），不要静默清空
        st.last_error = "；".join(notes)[:100] if notes else ""

    updated = plugin.store.update_state(account_id, mutate) or account
    message = "额度已刷新"
    if notes:
        message += "（%s）" % "；".join(notes)
    return 200, {"message": message, "account": account_view(updated)}


def _plan_label(data):
    """魔粒余额里的套餐标识（上游字段不稳定，取不到就不显示）。"""
    if not isinstance(data, dict):
        return ""
    for key in ("plan", "plan_name", "subscription", "tier"):
        value = data.get(key)
        if isinstance(value, str) and value.strip():
            return truncate_runes(value.strip(), 20)
    return ""


# ---------- 模型 ----------


def handle_models(plugin, headers):
    """GET /v1/models：按 X-Plugin-Account 的账号令牌拉；拉不到就回退静态清单。"""
    ids = static_models(plugin.eps)
    account = None
    try:
        account = account_for_request(plugin, headers)
    except Exception:
        account = None
    if account is not None:
        fetched = plugin.upstream.fetch_models(account.token)
        if fetched:
            ids = fetched
    return 200, models_payload(ids)


def write_error(status, msg):
    if status < 400:
        status = 400
    return status, {"error": clamp_bytes(msg, 240)}


# ---------- 回调基址 ----------


def _callback_base(plugin, headers):
    """回调基址：优先读本次登录请求的注入头，其次读进程启动时注入的环境变量。

    两者都没有说明插件不是由网关拉起的（直连调试）：此时不回退自建监听，
    直接在面板上报错，避免生成一个永远回调不通的授权地址（plugin-spec §6.7）。
    """
    import os

    for name in ("X-Gateway-Callback-Base", "x-gateway-callback-base"):
        value = (headers.get(name) or "").strip()
        if value:
            return value.rstrip("/")
    value = (os.environ.get("AI_GATEWAY_CALLBACK_BASE") or "").strip()
    return value.rstrip("/") if value else ""


__all__ = [
    "handle_accounts_list", "handle_account_add", "handle_account_delete",
    "handle_checkin", "handle_quota", "handle_models", "handle_login_start",
    "handle_login_status", "handle_callback", "write_error", "account_view",
]
