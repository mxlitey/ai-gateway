# -*- coding: utf-8 -*-
"""控制台接口（docs/plugin-spec.md §6 约定）。

    GET    /console/accounts               账号列表（含 notice / login_methods）
    DELETE /console/accounts/{id}          删除账号
    POST   /console/accounts/{id}/quota    刷新额度
    POST   /console/login/paste            粘贴 refresh_token（action 类）

账号响应里绝不含任何凭据（access_token / refresh_token）。

⚠️ 登录方式：Google 原生客户端走 **loopback 回调**，而插件规范禁止插件自行监听
额外端口、网关统一回调地址也不在 Google 的 loopback 白名单里，故这里只支持
粘贴 refresh_token（换取令牌那一步由用户在 Google 侧或其它工具完成）。
"""

import hashlib

from chat import SUPPORTED_MODEL, ensure_token, models_payload
from upstream import UpstreamError, as_upstream_error
from util import now_millis, sanitize_error, truncate_runes

LOGIN_METHODS = [
    {
        "id": "paste",
        "label": "粘贴 Google refresh_token",
        "hint": ("从已登录的 Gemini CLI / Code Assist 凭据里取出 refresh_token 粘贴进来；"
                 "插件会自动续期 access_token"),
        "fields": [
            {"name": "refresh_token", "label": "refresh_token", "kind": "secret",
             "placeholder": "1//0g..."},
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
        "plan": account.plan or "Code Assist",
        "credits": account.credits,
        "expires_at": int(account.expires_at or 0),
        "last_error": sanitize_error(account.last_error),
    }


def handle_accounts_list(plugin):
    accounts = plugin.store.list()
    out = {"accounts": [account_view(a) for a in accounts], "login_methods": LOGIN_METHODS}
    if not accounts:
        out["notice"] = "还没有账号：粘贴 Google refresh_token 后即可转发对话"
    return 200, out


def handle_account_delete(plugin, account_id):
    if not plugin.store.remove(account_id):
        return 404, {"error": "账号不存在"}
    return 200, {"success": True}


def handle_login_start(plugin, method, payload, headers):
    if method != "paste":
        return 400, {"error": "未知的登录方式：" + str(method)}
    return _paste_refresh_token(plugin, payload or {})


def _paste_refresh_token(plugin, body):
    refresh_token = (body.get("refresh_token") or body.get("token") or "").strip()
    if not refresh_token:
        return 400, {"error": "请提供 refresh_token"}

    try:
        grant = plugin.upstream.refresh(refresh_token)
    except UpstreamError as err:
        return err.status, {"error": truncate_runes(err.msg, 120)}
    except Exception as exc:
        return 502, {"error": truncate_runes(as_upstream_error(exc).msg, 120)}

    info = plugin.upstream.userinfo(grant["access_token"])
    email = (info.get("email") or "").strip() if isinstance(info, dict) else ""
    uid = email or _digest(refresh_token)
    nickname = (body.get("name") or "").strip() or email or uid

    from accounts import Credential

    saved, created = plugin.store.upsert(Credential(
        uid=_safe_uid(uid),
        nickname=nickname,
        email=email,
        token=grant["access_token"],
        refresh_token=grant["refresh_token"],
        expires_at=grant["expires_at"],
        scope=grant.get("scope", ""),
        source="paste",
    ))
    return 200, {
        "status": "ok",
        "message": "%s账号 %s" % ("已新增" if created else "已更新", saved.name()),
        "account": account_view(saved),
    }


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
        quota = plugin.upstream.fetch_quota(account.token)
    except UpstreamError as err:
        plugin.note_error(err, account)
        return err.status, {"error": truncate_runes(err.msg, 120)}

    remaining = float(quota.get("remaining") or 0)

    def mutate(st):
        st.credits = remaining
        st.credits_at = now_millis()
        if quota.get("plan"):
            st.plan = quota["plan"]
        st.last_error = ""

    updated = plugin.store.update_state(account_id, mutate) or account
    message = "额度已刷新：剩余 %d tokens" % int(remaining)
    if quota.get("total"):
        message += "（共 %d）" % int(quota["total"])
    return 200, {"message": message, "account": account_view(updated)}


def handle_models(plugin, headers):
    """模型目录：模型名带档位后缀（low/medium/high/tiered）时即为思考预算。"""
    ids = [SUPPORTED_MODEL] + ["%s-%s" % (SUPPORTED_MODEL, tier)
                               for tier in ("low", "medium", "high", "tiered")]
    return 200, models_payload(ids)


def _digest(value):
    return "rt-" + hashlib.sha256((value or "").encode("utf-8")).hexdigest()[:16]


def _safe_uid(value):
    text = (value or "").strip()
    for ch in ("/", "\\", "?", "#", " "):
        text = text.replace(ch, "_")
    return "".join(ch for ch in text if ch.isprintable()) or "gemini-account"


__all__ = [
    "handle_accounts_list", "handle_account_delete", "handle_quota", "handle_models",
    "handle_login_start", "account_view", "LOGIN_METHODS",
]
