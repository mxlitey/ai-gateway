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
from upstream import (
    _CODE_ALREADY_CHECKED_IN, _CODE_TOKEN_DEAD, CheckinState, UpstreamError,
    as_upstream_error,
)
from util import clamp_bytes, new_checkin_device_id, now_millis, sanitize_error, truncate_runes

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
    """对一个账号执行签到（幂等：已签则只回报状态）并刷新额度快照。

    一轮的节奏（这是本模块的安全边界，别顺手简化）：

        status 1 发 → （未签时）claim 1 发 → 同设备号回查 1 发 = 常态 3 发

    **device_id 每轮新生成、三发之间保持不变**：复用上一轮的号是风控画像里的
    可疑项；而回查必须用同一个号问「今天这个设备签过没有」，换号回查等于自证失败。

    两个判定都不能省：

      1. claim 回的 `code:0` 是**假成功**——当日已签过的账号对任何 device_id 都
         回 0。真到没到账只能看同设备号回查后 `checked_in` / `did_checked_in`
         有没有翻转；没翻转就如实报失败，绝不亮绿。
      2. 撞 `1001`（服务端作废了这张 JWT，本地到期时间看不出来）时**强制换发
         新令牌后重放一轮**，封顶两轮。不换发的话这个账号会天天签到失败。
    """
    account = plugin.store.get(account_id)
    if account is None:
        raise UpstreamError(404, "账号不存在")
    work = account.copy()
    try:
        plugin.upstream.ensure_token(work, plugin.store)
    except Exception as exc:
        plugin.note_upstream_error(work, as_upstream_error(exc))
        raise

    state, msg, reauth = _checkin_round(plugin, work)
    if reauth:
        try:
            plugin.upstream.force_refresh_token(work, plugin.store)
        except Exception as exc:
            # 连 refreshToken 都换不出新令牌：这才是真的「要重新登录」。
            raise UpstreamError(401, "登录态已被上游作废，换发新令牌也失败（%s），请重新登录"
                                % as_upstream_error(exc).msg, disable=True)
        state, msg, _ = _checkin_round(plugin, work)

    checked_in = state.done_today()

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


def _checkin_round(plugin, account):
    """跑一轮 status → claim → 回查，返回 (最终状态, 文案, 是否需要换发令牌)。

    「需要换发令牌重放」用第三个返回值往上传（不抛错，因为它是可恢复的）；
    其余失败照常抛 UpstreamError，由 handle_checkin 映射成面板上的错误 toast。
    """
    device_id = new_checkin_device_id()
    state, err = plugin.upstream.checkin_status(account, device_id)
    if err is not None:
        raise err
    if state.code == _CODE_TOKEN_DEAD:
        return CheckinState(), "", True
    if state.code not in (0, 200):
        # status 自己回了非零码：原样报成拒绝，不许退化成「活动没开」
        raise UpstreamError(502, "签到状态查询失败（上游返回 code %d%s）"
                            % (state.code, ("：" + state.message) if state.message else ""),
                            code=state.code)
    if state.done_today():
        return state, _already_msg(state), False
    if not state.enable:
        raise UpstreamError(403, "该账号当前没有可领取的签到活动（上游未开放这一档）")

    claim, cerr = plugin.upstream.checkin_claim(account, device_id)
    if cerr is not None:
        raise cerr
    if claim.needs_reauth:
        return CheckinState(), "", True
    if claim.code not in (0, 200) and claim.code != _CODE_ALREADY_CHECKED_IN:
        # 9095（该设备今日已签）在 claim 里已折成 already，走到这里都是真拒绝
        reason = claim.message or "上游未给文案"
        raise UpstreamError(502, "官方拒绝本次签到（%s）：%s" % (claim.code, reason),
                            code=claim.code)

    # 同设备号回查：claim 的 code:0 不能当成功，必须看到状态翻转
    after, aerr = plugin.upstream.checkin_status(account, device_id)
    if aerr is not None or not after.done_today():
        raise UpstreamError(
            502, "签到接口返回成功但同设备号回查未确认（服务端静默拒签），"
                 "当日不再自动重试，可稍后手动再签一次")
    return after, _claimed_msg(after, claim), False


def _already_msg(state):
    total = state.credits_earned_today
    if total > 0:
        return "今日已签到（官方报出当日奖励 %s 积分）" % _num(total)
    return "今日已签到"


def _claimed_msg(state, claim):
    total = state.credits_earned_today
    if total > 0:
        return "签到成功（官方报出当日奖励 %s 积分；到账看额度列）" % _num(total)
    return "签到成功（上游未给出奖励数额）" + (("：" + claim.message) if claim.message else "")


def _num(value):
    """奖励数额文案：整数不带小数点。"""
    if float(value) == int(value):
        return str(int(value))
    return "%.2f" % float(value)


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
    updated, note = refresh_quota_snapshot(plugin, account_id)
    return 200, {"message": "额度已刷新" + note, "account": account_view(updated)}


def refresh_quota_snapshot(plugin, account_id):
    """刷新一个账号的额度与签到状态快照。返回 (Account, note)。

    签到状态只是面板上的读数，因此这里**不换号、不重放**：换发令牌重放留给
    显式签到。回查用的设备号在这里无所谓（只读 checked_in 这类账号维度字段），
    但仍按规范发一个一次性的 16 位数字号，避免复用旧号落进风控画像。
    """
    account = plugin.store.get(account_id)
    if account is None:
        return None, "账号不存在"
    state, err = plugin.upstream.checkin_status(account, new_checkin_device_id())
    if err is not None:
        plugin.note_upstream_error(account, as_upstream_error(err))
        return plugin.store.get(account_id), ""

    try:
        credits, plan = plugin.upstream.ent_usage_info(account)
    except Exception as exc:
        # 额度查询失败：保留既有快照，只更新签到状态，并如实说明
        def mutate(st):
            st.checked_in_today = state.done_today()
            st.streak_days = state.streak_days

        updated = plugin.store.update_state(account_id, mutate)
        return updated, "（额度查询失败：" + as_upstream_error(exc).msg + "）"

    def mutate(st):
        st.checked_in_today = state.done_today()
        st.streak_days = state.streak_days
        st.credits = credits
        if plan:
            st.plan = plan
        st.credits_at = now_millis()
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate)
    return updated, ""


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
