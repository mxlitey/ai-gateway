# -*- coding: utf-8 -*-
"""控制台接口（对应 Go 版 console.go）。

按 docs/plugin-spec.md §6 的约定实现，界面由网关面板统一渲染：

    GET    /console/accounts              账号列表（含 notice 与 login_methods）
    POST   /console/accounts              手工导入（面板不调用，供脚本使用）
    DELETE /console/accounts/{id}         删除账号
    POST   /console/accounts/{id}/checkin 签到
    POST   /console/accounts/{id}/quota   刷新额度
    POST   /console/login/web             发起网页登录
    GET    /console/login/web?state=xxx   轮询登录结果
"""

from chat import STATIC_MODELS, account_for_request
from upstream import UpstreamError, as_upstream_error
from util import clamp_bytes, now_millis, sanitize_error, truncate_runes

LOGIN_METHODS = [{
    "id": "web",
    "label": "登录 TRAE 账号",
    "hint": "在浏览器完成授权，回调自动捕获",
}]


def account_view(account):
    """账号对外的安全视图：只暴露面板需要的字段，绝不含任何凭据。"""
    expires = account.expires_at * 1000 if account.expires_at > 0 else 0
    view = {
        "id": account.uid,
        "name": account.name(),
        "site": "cn",
        "enabled": account.usable(),
        "plan": account.plan or "免费版",
        "credits": account.credits,
        "checked_in_today": account.checked_in_today,
        "expires_at": expires,
        "last_error": sanitize_error(account.last_error),
    }
    if account.streak_days:
        view["streak_days"] = account.streak_days
    return view


def account_view_list(accounts):
    return [account_view(a) for a in accounts]


def handle_accounts_list(plugin):
    accounts = account_view_list(plugin.store.list())
    out = {"accounts": accounts, "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = "还没有账号：点「登录 TRAE 账号」，或调用 POST /console/accounts 手工导入"
    return 200, out


def handle_account_add(plugin, body):
    """手工导入（面板不调用）。以 uid 为准 upsert。"""
    if not isinstance(body, dict):
        body = {}
    token = (body.get("token") or "").strip()
    if not token:
        return 400, {"error": "请提供 token（TRAE 登录回调链接或凭证 JSON）"}
    try:
        account, created = plugin.import_credential(
            token, (body.get("name") or "").strip(), (body.get("uid") or "").strip())
    except Exception as exc:
        return 400, {"error": truncate_runes(str(exc), 90)}
    verb = "已新增" if created else "已更新"
    return 200, {"message": "%s账号 %s" % (verb, account.name()), "account": account_view(account)}


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_checkin(plugin, account_id):
    """签到：今日已签按成功语义返回（幂等友好）。"""
    if plugin.store.get(account_id) is None:
        return 404, {"error": "账号不存在"}
    try:
        updated, msg = perform_checkin(plugin, account_id)
    except Exception as exc:
        ue = as_upstream_error(exc)
        return ue.status, {"error": ue.msg}
    return 200, {"message": msg, "account": account_view(updated)}


def perform_checkin(plugin, account_id):
    """对一个账号执行签到（幂等：已签则只回报状态）并刷新额度快照。"""
    account = plugin.store.get(account_id)
    if account is None:
        raise UpstreamError(404, "账号不存在")
    work = account.copy()
    try:
        plugin.upstream.ensure_token(work, plugin.store)
    except Exception as exc:
        plugin.note_upstream_error(work, as_upstream_error(exc))
        raise

    state, err = plugin.upstream.checkin_status(work)
    if err is not None:
        plugin.note_upstream_error(work, as_upstream_error(err))
        raise err
    if state.unavailable:
        raise UpstreamError(403, "该账号未开放签到服务（上游返回 code 1001）", code=1001)

    checked_in = state.checked_in
    msg = "今天已经签到过了"
    if not checked_in:
        if not state.enable:
            raise UpstreamError(502, "该账号当前不可签到（上游未开放签到）")
        # 签到失败直接如实返回错误，不做任何自动重试
        result, cerr = plugin.upstream.checkin_claim(work)
        if cerr is not None:
            plugin.note_upstream_error(work, as_upstream_error(cerr))
            raise cerr
        if result.unavailable:
            raise UpstreamError(403, "该账号未开放签到服务（上游返回 code 1001）", code=1001)
        checked_in = True
        # 上游对「今天已签到」也用成功码/9095 返回 → 按幂等成功处理
        if result.already:
            msg = "今日已签到"
        elif result.message:
            msg = "签到成功：" + result.message
        else:
            msg = "签到成功"

    def mutate(st):
        st.checked_in_today = checked_in
        st.streak_days = state.streak_days
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate)
    # 顺带刷新额度与套餐快照（只在签到/额度刷新时查询，不做定时轮询）
    try:
        credits, plan = plugin.upstream.ent_usage_info(work)
    except Exception:
        credits, plan = None, ""
    if credits is not None:
        def mutate_credits(st):
            st.credits = credits
            if plan:
                st.plan = plan
            st.credits_at = now_millis()

        updated = plugin.store.update_state(account_id, mutate_credits)
    return updated, msg


def handle_quota(plugin, account_id):
    """刷新额度：只在被调用时向上游查询并更新快照（不做定时轮询）。"""
    account = plugin.store.get(account_id)
    if account is None:
        return 404, {"error": "账号不存在"}
    work = account.copy()
    try:
        plugin.upstream.ensure_token(work, plugin.store)
    except Exception as exc:
        ue = as_upstream_error(exc)
        return ue.status, {"error": ue.msg}
    _, err = plugin.upstream.checkin_status(work)
    if err is not None:
        plugin.note_upstream_error(work, as_upstream_error(err))
        ue = as_upstream_error(err)
        return ue.status, {"error": ue.msg}
    updated, note = refresh_quota_snapshot(plugin, account_id)
    return 200, {"message": "额度已刷新" + note, "account": account_view(updated)}


def refresh_quota_snapshot(plugin, account_id):
    """刷新一个账号的额度与签到状态快照。返回 (Account, note)。"""
    account = plugin.store.get(account_id)
    if account is None:
        return None, "账号不存在"
    state, err = plugin.upstream.checkin_status(account)
    unavailable = ""
    if err is not None:
        plugin.note_upstream_error(account, as_upstream_error(err))
        return plugin.store.get(account_id), ""
    if state.unavailable:
        unavailable = "（签到服务未对该账号开放）"

    try:
        credits, plan = plugin.upstream.ent_usage_info(account)
    except Exception as exc:
        # 额度查询失败：保留既有快照，只更新签到状态，并如实说明
        def mutate(st):
            st.checked_in_today = state.checked_in
            st.streak_days = state.streak_days

        updated = plugin.store.update_state(account_id, mutate)
        return updated, unavailable + "（额度查询失败：" + as_upstream_error(exc).msg + "）"

    def mutate(st):
        st.checked_in_today = state.checked_in
        st.streak_days = state.streak_days
        st.credits = credits
        if plan:
            st.plan = plan
        st.credits_at = now_millis()
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate)
    return updated, unavailable


def handle_login_start(plugin, method, headers):
    """发起登录：POST /console/login/web → {"status","message","url","state"}。

    响应体里**同时有 url 与 state**，面板据此判定为 link 类并轮询。
    回调地址固定为 http://127.0.0.1:<插件端口>/authorize（官方授权页硬校验该形态）。
    """
    from login import loopback_base

    if method != "web":
        return 400, {"error": "未知的登录方式：" + method}
    base = loopback_base(plugin.port)
    try:
        session = plugin.login.start(base)
    except Exception as exc:
        return 502, {"error": str(exc)}
    return 200, {
        "status": session.status,
        "message": session.message,
        "url": session.auth_url,
        "state": session.state,
    }


def handle_login_status(plugin, method, query):
    """轮询登录结果：GET /console/login/web?state=xxx → {"status","message"}。"""
    if method != "web":
        return 400, {"error": "未知的登录方式：" + method}
    state = (query.get("state") or [""])[0].strip()
    if not state:
        return 400, {"error": "缺少 state"}
    status, message = plugin.login.status(state)
    return 200, {"status": status, "message": message}


def handle_models(plugin, headers):
    """GET /v1/models：拉不到就回退静态清单，绝不报错。"""
    ids = list(STATIC_MODELS)
    try:
        account = account_for_request(plugin, headers)
    except Exception:
        account = None
    if account is not None:
        try:
            models = plugin.upstream.fetch_models(account)
            ids = [m[0] for m in models]
            plugin.note_success(account.uid)
        except Exception:
            pass
    seen = set()
    data = []
    for mid in ids:
        mid = (mid or "").strip()
        if not mid or mid in seen:
            continue
        seen.add(mid)
        data.append({"id": mid, "object": "model", "owned_by": "trae-solo"})
    return 200, {"object": "list", "data": data}


def write_error(status, msg):
    """错误响应体：文案压到 240 字节内（OpenAI 入口只透传前 300 字节）。"""
    if status < 400:
        status = 400
    return status, {"error": clamp_bytes(msg, 240)}
