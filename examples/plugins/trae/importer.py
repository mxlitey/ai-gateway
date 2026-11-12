# -*- coding: utf-8 -*-
"""手工导入（对应 Go 版 importer.go）。

POST /console/accounts 的 token 兼容两种形态：
  1. TRAE 登录回调链接：走 ExchangeToken（轮换 refreshToken）+ GetUserInfo 补 uid/昵称；
  2. 完整凭证 JSON：嵌套形 {"auth":{...},"account":{...}} 或扁平形，直接校验落盘。

两种都失败时返回中文错误；成功后以 uid 为准 upsert。
"""

from accounts import Auth, parse_auth
from upstream import as_upstream_error, parse_callback
from util import new_checkin_device_id, random_hex, uid_from_token


def import_credential(plugin, token, name, uid_hint):
    """返回 (Account, created)。失败时抛异常。"""
    token = (token or "").strip()
    if not token:
        raise RuntimeError("token 为空")

    auth = Auth()
    if token.startswith("http://") or token.startswith("https://"):
        info = parse_callback(token)
        auth = Auth(
            access_token=info.access_token,
            refresh_token=info.refresh_token,
            uid=info.uid,
            nickname=info.nickname,
            enterprise_id=info.enterprise_id,
            expires_at=info.expires_at,
            domain="trae.cn",
            api_host=plugin.eps.oauth_host,
            machine_id=random_hex(16),
            # device_id 故意留空：由下方补齐 16 位纯数字签到 deviceId
        )
        if (auth.refresh_token or "").strip():
            account = _auth_to_account(auth)
            try:
                plugin.upstream.refresh_token(account)
            except Exception as exc:
                raise RuntimeError("换取 access token 失败：%s" % as_upstream_error(exc).msg)
            auth = account.to_auth()
        try:
            probe = Auth(access_token=auth.access_token, api_host=auth.api_host)
            uid, nick, ent = plugin.upstream.get_user_info(_auth_to_account(probe))
            if uid.strip():
                auth.uid = uid
            if nick.strip():
                auth.nickname = nick
            if ent.strip():
                auth.enterprise_id = ent
        except Exception:
            pass
        if not (auth.uid or "").strip():
            raise RuntimeError("无法从回调链接确定用户 id，请确认链接完整")
        if not (auth.access_token or "").strip():
            raise RuntimeError("回调链接换取 access token 失败")
    else:
        try:
            parsed = parse_auth(token.encode("utf-8"))
        except Exception:
            raise RuntimeError("token 既不是回调链接也不是合法凭证 JSON")
        auth = parsed
        if not (auth.uid or "").strip():
            auth.uid = (uid_hint or "").strip()
        if not (auth.uid or "").strip():
            auth.uid = uid_from_token(auth.access_token)
        if not (auth.access_token or "").strip():
            raise RuntimeError("凭证 JSON 缺少 accessToken")
        if not (auth.uid or "").strip():
            raise RuntimeError("凭证 JSON 缺少 uid，且无法从 accessToken 解析")
        if not (auth.domain or "").strip():
            auth.domain = "trae.cn"
        if not (auth.api_host or "").strip():
            auth.api_host = plugin.eps.oauth_host

    if (name or "").strip():
        auth.nickname = name.strip()
    if not (auth.device_id or "").strip():
        auth.device_id = new_checkin_device_id()
    if not (auth.domain or "").strip():
        auth.domain = "trae.cn"
    if not (auth.api_host or "").strip():
        auth.api_host = plugin.eps.oauth_host

    return plugin.store.upsert(auth)


def _auth_to_account(auth):
    from accounts import Account

    return Account(
        uid=auth.uid, nickname=auth.nickname, enterprise_id=auth.enterprise_id,
        access_token=auth.access_token, refresh_token=auth.refresh_token,
        expires_at=auth.expires_at, domain=auth.domain, api_host=auth.api_host,
        machine_id=auth.machine_id, device_id=auth.device_id, enabled=True,
    )
