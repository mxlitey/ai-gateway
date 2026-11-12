# -*- coding: utf-8 -*-
"""控制台接口（docs/plugin-spec.md §6 约定）。

    GET    /console/accounts               账号列表（含 notice / login_methods）
    DELETE /console/accounts/{id}          删除账号
    POST   /console/accounts/{id}/quota    刷新余额
    POST   /console/login/{method}         发起登录（device = link 类，paste = action 类）
    GET    /console/login/{method}?state=  轮询登录结果（仅 link 类）

账号响应里绝不含任何凭据（access_token / refresh_token）。
"""

from chat import account_for_request, ensure_token, models_payload
from upstream import UpstreamError, as_upstream_error
from util import now_millis, sanitize_error, truncate_runes

LOGIN_METHODS = [
    {
        "id": "device",
        "label": "Cline 设备码登录",
        "hint": "点一下会打开 Cline 授权页，输入/确认用户码后回来等结果（无需本机客户端）",
    },
    {
        "id": "paste",
        "label": "粘贴访问令牌",
        "hint": "已有 Cline 账号时可直接粘贴访问令牌（需带 workos: 前缀）",
        "fields": [
            {"name": "token", "label": "访问令牌", "kind": "secret",
             "placeholder": "workos:eyJ..."},
            {"name": "name", "label": "备注名", "required": False},
        ],
    },
]


def account_view(account):
    return {
        "id": account.uid,
        "name": account.name(),
        "site": "intl",
        "enabled": account.usable(),
        "plan": account.plan or ("设备码登录" if account.source == "oauth" else "访问令牌"),
        "credits": account.credits,
        "expires_at": int(account.expires_at or 0),
        "last_error": sanitize_error(account.last_error),
    }


def handle_accounts_list(plugin):
    accounts = plugin.store.list()
    out = {"accounts": [account_view(a) for a in accounts], "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = ("还没有账号：点「Cline 设备码登录」在浏览器里完成授权，"
                         "或粘贴已有的访问令牌")
    return 200, out


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_login_start(plugin, method, payload, headers):
    """返回体同时含 url 与 state 时面板按 link 处理。"""
    if method == "device":
        try:
            return 200, plugin.login.start()
        except UpstreamError as err:
            return err.status, {"error": truncate_runes(err.msg, 120)}
        except Exception as exc:
            return 502, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}
    if method == "paste":
        return _paste_token(plugin, payload or {})
    return 400, {"error": "未知的登录方式：" + str(method)}


def _paste_token(plugin, body):
    token = (body.get("token") or "").strip()
    if not token:
        return 400, {"error": "请提供访问令牌"}
    # 校验一次：能读到 /users/me 才算有效
    me = plugin.upstream.fetch_me(token)
    if me is None:
        return 401, {"error": "令牌无效或已过期（/users/me 未通过）"}
    account_id = _first(me, ("clineUserId", "accountId", "id"))
    email = _first(me, ("email",))
    uid = account_id or email or util_digest(token)
    from accounts import Credential

    saved, created = plugin.store.upsert(Credential(
        uid=_safe_uid(uid),
        nickname=(body.get("name") or "").strip() or email or uid,
        email=email,
        token=token,
        expires_at=0,
        account_id=account_id,
        source="paste",
    ))
    return 200, {
        "status": "ok",
        "message": "%s账号 %s" % ("已新增" if created else "已更新", saved.name()),
        "account": account_view(saved),
    }


def handle_login_status(plugin, method, query):
    if method != "device":
        return 400, {"error": "该登录方式不需要轮询"}
    state = (query.get("state") or [""])[0].strip()
    if not state:
        return 400, {"error": "缺少 state"}
    status, message = plugin.login.status(state)
    return 200, {"status": status, "message": message}


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

    try:
        credits = plugin.upstream.fetch_balance(account.token, account.account_id)
    except UpstreamError as err:
        plugin.note_error(err, account)
        return err.status, {"error": truncate_runes(err.msg, 120)}

    def mutate(st):
        st.credits = credits
        st.credits_at = now_millis()
        st.plan = "按量计费"
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate) or account
    return 200, {"message": "余额已刷新：$%.2f" % credits, "account": account_view(updated)}


def handle_models(plugin, headers):
    """GET /v1/models：先按请求账号拉目录，拉不到回退兜底清单。"""
    token = ""
    try:
        account = account_for_request(plugin, headers)
        token = account.token
    except Exception:
        account = None

    result = plugin.upstream.fetch_models(token)
    ids = result["models"] or result["fallback"]
    if account is not None:
        free = set(result["free"])
        ids = sorted(ids, key=lambda mid: (0 if mid in free else 1, mid))
    return 200, models_payload(ids)


def _first(doc, keys):
    for key in keys:
        value = doc.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def _safe_uid(value):
    text = (value or "").strip()
    for ch in ("/", "\\", "?", "#", " "):
        text = text.replace(ch, "_")
    return "".join(ch for ch in text if ch.isprintable()) or "cline-account"


def util_digest(token):
    from accounts import digest_uid

    return digest_uid(token)


__all__ = [
    "handle_accounts_list", "handle_account_delete", "handle_quota", "handle_models",
    "handle_login_start", "handle_login_status", "account_view", "LOGIN_METHODS",
]
