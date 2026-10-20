# -*- coding: utf-8 -*-
"""上游调用：请求头、错误分类、对话、模型目录、魔粒额度与每日任务、令牌续期。

错误语义严格对齐 docs/plugin-spec.md §5.2（网关只看状态码决定要不要换号）：

    401 / 403 / 404 / 429 / 5xx  -> 「换个账号可能就好」，网关切号
    400 / 其它 4xx               -> 「换谁来都一样」，不切号

魔搭是官方公开 API，错误体形如 {"error":{"message":...}}；配额类错误我们用 403
（账号级、可切号），模型 id 不存在用 400（全局、不切号）。
"""

import json
import re
import time

import net
import util

# 配额 / 余额类文案（上游对魔粒耗尽的口径不稳定，按文案识别）
_QUOTA_RE = re.compile(r"insufficient|quota|balance|魔粒|余额|欠费|exceeded|limit reached", re.I)
_REALNAME_RE = re.compile(r"real-name|realname|实名", re.I)
_BIND_RE = re.compile(r"bind your alibaba cloud|绑定阿里云", re.I)


class UpstreamError(Exception):
    """映射到网关切号语义后的错误。"""

    def __init__(self, status, msg, disable=False, code=""):
        super().__init__(msg)
        self.status = int(status)
        self.msg = msg
        self.disable = bool(disable)
        self.code = code


def as_upstream_error(err):
    if isinstance(err, UpstreamError):
        return err
    if isinstance(err, net.TransportError):
        return UpstreamError(502, "网络错误：%s" % util.truncate_runes(str(err), 80))
    return UpstreamError(502, util.truncate_runes(str(err or "上游错误"), 80))


def _error_message(raw):
    """从任意上游错误体里提取给人看的文案。"""
    doc = net.parse_json(raw)
    if isinstance(doc, dict):
        err = doc.get("error")
        if isinstance(err, dict):
            for key in ("message", "msg", "code", "type"):
                value = err.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        if isinstance(err, str) and err.strip():
            return err.strip()
        for key in ("message", "msg", "error_description"):
            value = doc.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    text = (raw or b"").decode("utf-8", "replace").strip()
    return util.truncate_runes(text, 200) or "上游未返回错误详情"


def classify_status(status, raw, context="chat"):
    """把上游 HTTP 状态映射成网关能理解的切号语义。"""
    message = _error_message(raw)
    lowered = message.lower()

    if context == "identity":
        # 身份端点：401/403 说明令牌本身无效（粘贴错误或已被吊销）→ 硬禁用并切号
        if status in (401, 403):
            return UpstreamError(401, "令牌无效或已被吊销，请到魔搭「访问令牌」页重新生成", disable=True)
        return UpstreamError(502, "身份查询失败：%s" % util.truncate_runes(message, 80))

    if context == "magicubes":
        # 魔粒控制面：401/403 基本都是令牌无效或未授权 → 切号
        if status in (401, 403):
            return UpstreamError(401, "魔粒接口拒绝该账号的令牌，请重新登录", disable=True)
        return UpstreamError(502, "魔粒接口错误：%s" % util.truncate_runes(message, 80))

    if _REALNAME_RE.search(message):
        return UpstreamError(
            403,
            "该魔搭账号绑定的阿里云账号未完成实名认证，无法推理；请到 "
            "https://www.modelscope.cn/my/accountsettings 完成实名",
        )
    if _BIND_RE.search(message):
        return UpstreamError(
            403,
            "该魔搭账号尚未绑定阿里云账号，无法推理；请到 "
            "https://www.modelscope.cn/my/accountsettings 绑定后再试",
        )
    if status == 401 or "authentication" in lowered or "token is invalid" in lowered:
        return UpstreamError(401, "魔搭令牌无效或已过期，请重新登录", disable=True)
    if status == 403:
        if _QUOTA_RE.search(message):
            return UpstreamError(403, "该账号额度不足：%s" % util.truncate_runes(message, 60))
        return UpstreamError(403, "该账号被上游拒绝（权限或地区限制）")
    if status == 404:
        # 魔搭的模型清单对所有账号一致，缺失属于「请求本身的问题」——不切号，
        # 否则会把一个坏模型在渠道内所有账号上轮流试一遍（plugin-spec §5.2 的坑）。
        return UpstreamError(400, "上游没有该模型：%s" % util.truncate_runes(message, 60))
    if status == 429 or _QUOTA_RE.search(message):
        return UpstreamError(429, "上游限流或额度耗尽：%s" % util.truncate_runes(message, 60))
    if status >= 500:
        return UpstreamError(502, "上游服务异常：%s" % util.truncate_runes(message, 60))
    if status >= 400:
        return UpstreamError(400, "上游拒绝了该请求：%s" % util.truncate_runes(message, 90))
    return UpstreamError(502, "上游返回未知状态 %d" % status)


class Upstream:
    def __init__(self, eps):
        self.eps = eps

    # ================= 请求头 =================

    def chat_headers(self, token, accept="text/event-stream, application/json"):
        """对话面：官方网关只认 Bearer（其余头与站点一致，带上无害）。"""
        return {
            "authorization": "Bearer " + token,
            "content-type": "application/json",
            "accept": accept,
            "user-agent": self.eps.user_agent,
        }

    def site_headers(self, token, referer_path="", extra=None):
        """控制面：三头同发 + 浏览器上下文（缺 UA/Origin/Referer 会被风控静默忽略）。"""
        base = self.eps.web_base.rstrip("/")
        headers = {
            "authorization": "Bearer " + token,
            "OpenAPI-Token": token,
            "X-Modelfun-Token": token,
            "content-type": "application/json",
            "accept": "application/json, text/plain, */*",
            "accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
            "user-agent": self.eps.user_agent,
            "origin": base,
            "referer": base + (referer_path or self.eps.referer_path),
        }
        if extra:
            headers.update(extra)
        return headers

    def cookie_headers(self, cookie, referer_path="", accept="application/json, text/plain, */*"):
        """仅 Cookie 可用族（/api/v1 族）：点赞、Web 会话触碰。"""
        base = self.eps.web_base.rstrip("/")
        return {
            "cookie": cookie,
            "content-type": "application/json",
            "accept": accept,
            "accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
            "user-agent": self.eps.user_agent,
            "origin": base,
            "referer": base + (referer_path or self.eps.referer_path),
        }

    # ================= 基础请求 =================

    def _json(self, method, url, headers, body=None, timeout=60):
        try:
            resp = net.request(method, url, headers, body, timeout=timeout)
        except net.TransportError as err:
            raise UpstreamError(502, "连接上游失败：%s" % util.truncate_runes(str(err), 80))
        try:
            raw = net.read_all(resp)
            status = resp.status
        finally:
            resp.close()
        return status, raw

    def _site_json(self, method, path, headers, body=None, context="magicubes"):
        status, raw = self._json(method, self.eps.web_base.rstrip("/") + path, headers, body)
        if status >= 400:
            raise classify_status(status, raw, context=context)
        return net.parse_json(raw) or {}

    # ================= 对话 =================

    def rewrite_body(self, body):
        """OpenAI 兼容请求体整理（官方网关的限制都收敛在这里）。"""
        out = dict(body or {})
        # 官方网关只认 max_tokens；客户端常发 max_completion_tokens
        if out.get("max_completion_tokens") is not None and out.get("max_tokens") is None:
            out["max_tokens"] = out.pop("max_completion_tokens")
        else:
            out.pop("max_completion_tokens", None)
        # 流式必须显式要 usage：否则每个 delta 帧都带 usage 空壳且没有最终真实帧，
        # 网关侧 token 统计会恒为 0（AgentHub 2026-10 实测）。
        if out.get("stream") is True:
            options = dict(out.get("stream_options") or {})
            options["include_usage"] = True
            out["stream_options"] = options
        return out

    def chat(self, token, body, stream):
        """发起对话。返回 StreamResponse；非 2xx 时抛 UpstreamError。"""
        url = self.eps.api_base.rstrip("/") + self.eps.chat_path
        payload = json.dumps(self.rewrite_body(body), ensure_ascii=False).encode("utf-8")
        headers = self.chat_headers(
            token, "text/event-stream, application/json" if stream else "application/json")
        try:
            resp = net.request("POST", url, headers, payload, timeout=self.eps.chat_timeout)
        except net.TransportError as err:
            raise UpstreamError(502, "请求上游失败：%s" % util.truncate_runes(str(err), 80))
        if resp.status >= 400:
            try:
                raw = resp.read(1 << 20)
            finally:
                resp.close()
            raise classify_status(resp.status, raw, context="chat")
        return resp

    def fetch_models(self, token):
        """GET /v1/models；失败时返回空列表（调用方回退静态清单）。"""
        url = self.eps.api_base.rstrip("/") + self.eps.models_path
        try:
            status, raw = self._json("GET", url, self.chat_headers(token, "application/json"), timeout=20)
        except net.TransportError:
            return []
        if status >= 400:
            return []
        doc = net.parse_json(raw) or {}
        items = doc.get("data") if isinstance(doc, dict) else None
        out = []
        for item in items if isinstance(items, list) else []:
            if isinstance(item, dict):
                mid = item.get("id") or item.get("modelId")
                if isinstance(mid, str) and mid.strip():
                    out.append(mid.strip())
        return out

    # ================= 身份 =================

    def fetch_identity(self, token):
        """用令牌换取账号身份（uid + 昵称）。返回 (uid, nickname)。"""
        headers = self.site_headers(token)
        doc = self._site_json("GET", self.eps.users_me, headers, context="identity")
        body = doc.get("data") if isinstance(doc, dict) else None
        data = body if isinstance(body, dict) else (doc if isinstance(doc, dict) else {})
        uid = util.first_non_empty(
            util.as_str(data.get("username")), util.as_str(data.get("userName")),
            util.as_str(data.get("sub")), util.as_str(data.get("user_id")))
        name = util.first_non_empty(
            util.as_str(data.get("nickname")), util.as_str(data.get("name")), uid)
        return uid.strip(), name.strip()

    # ================= OAuth =================

    def register_client(self, redirect_uri, client_name="AI Gateway"):
        """动态注册互联应用（RFC 7591）：无需鉴权，返回 client_id / client_secret。"""
        body = json.dumps({
            "client_name": util.truncate_runes(client_name, 60),
            "redirect_uris": [redirect_uri],
            "grant_types": ["authorization_code", "refresh_token"],
            "response_types": ["code"],
            "token_endpoint_auth_method": "client_secret_post",
            "scope": self.eps.oauth_scopes,
        }, ensure_ascii=False).encode("utf-8")
        headers = {
            "content-type": "application/json",
            "accept": "application/json",
            "user-agent": self.eps.user_agent,
        }
        url = self.eps.web_base.rstrip("/") + self.eps.oauth_register
        status, raw = self._json("POST", url, headers, body)
        doc = net.parse_json(raw) or {}
        if status >= 400 or not isinstance(doc, dict) or not doc.get("client_id"):
            raise UpstreamError(502, "互联应用动态注册失败：HTTP %d %s" % (
                status, util.truncate_runes(_error_message(raw), 80)))
        return util.as_str(doc.get("client_id")), util.as_str(doc.get("client_secret"))

    def authorize_url(self, client_id, redirect_uri, state):
        from urllib.parse import quote

        return (
            self.eps.web_base.rstrip("/") + self.eps.oauth_authorize
            + "?response_type=code"
            + "&client_id=" + quote(client_id, safe="")
            + "&redirect_uri=" + quote(redirect_uri, safe="")
            + "&scope=" + quote(self.eps.oauth_scopes, safe="")
            + "&state=" + quote(state, safe="")
        )

    def exchange_code(self, code, client_id, client_secret, redirect_uri):
        """授权码换令牌。⚠ 上游 OAuth 错误以 HTTP 200 + body.error 返回。"""
        from urllib.parse import urlencode

        form = urlencode({
            "grant_type": "authorization_code",
            "code": code,
            "redirect_uri": redirect_uri,
            "client_id": client_id,
            "client_secret": client_secret,
        }).encode("utf-8")
        url = self.eps.web_base.rstrip("/") + self.eps.oauth_token
        headers = {
            "content-type": "application/x-www-form-urlencoded",
            "accept": "application/json",
            "user-agent": self.eps.user_agent,
        }
        status, raw = self._json("POST", url, headers, form)
        return _token_response(status, raw)

    def refresh_token(self, client_id, client_secret, refresh_token):
        """refresh_token grant。⚠ 一次性轮换：成功返回新 refresh，必须立刻落盘。"""
        from urllib.parse import urlencode

        form = urlencode({
            "grant_type": "refresh_token",
            "refresh_token": refresh_token,
            "client_id": client_id,
            "client_secret": client_secret,
        }).encode("utf-8")
        url = self.eps.web_base.rstrip("/") + self.eps.oauth_token
        headers = {
            "content-type": "application/x-www-form-urlencoded",
            "accept": "application/json",
            "user-agent": self.eps.user_agent,
        }
        status, raw = self._json("POST", url, headers, form)
        return _token_response(status, raw, fallback_refresh=refresh_token)

    def userinfo(self, token):
        headers = {
            "authorization": "Bearer " + token,
            "accept": "application/json",
            "user-agent": self.eps.user_agent,
        }
        status, raw = self._json(
            "GET", self.eps.web_base.rstrip("/") + self.eps.oauth_userinfo, headers)
        doc = net.parse_json(raw) or {}
        if status >= 400:
            raise classify_status(status, raw)
        return doc if isinstance(doc, dict) else {}

    # ================= 魔粒：额度 =================

    def fetch_balance(self, token):
        """魔粒余额。价格单位是「魔粒」，允许小数。"""
        doc = self._site_json("GET", self.eps.balance, self.site_headers(token))
        data = doc.get("data") if isinstance(doc, dict) else None
        data = data if isinstance(data, dict) else {}
        value, ok = util.dig_number(data, "total_balance")
        if not ok:
            value, ok = util.dig_number(data, "available_balance")
        if not ok:
            value, ok = util.dig_number(data, "balance")
        if not ok:
            raise UpstreamError(502, "魔粒余额接口未返回可识别字段")
        return float(value), data

    # ================= 魔粒：每日任务 =================

    def fetch_earn_rules(self, token):
        doc = self._site_json("GET", self.eps.earn_rules, self.site_headers(token))
        rules = doc.get("data") if isinstance(doc, dict) else None
        return rules if isinstance(rules, list) else []

    @staticmethod
    def rule_of(rules, key):
        for item in rules or []:
            if isinstance(item, dict) and util.as_str(item.get("rule_key")) == key:
                return item
        return None

    def checkin_state(self, account):
        """只读：报告每日任务进度与点赞余量（绝不产生副作用）。"""
        rules = self.fetch_earn_rules(account.token)
        daily = self.rule_of(rules, self.eps.rule_daily)
        bind = self.rule_of(rules, self.eps.rule_aliyun_bind)
        like = self.rule_of(rules, self.eps.rule_like)
        daily_done = _rule_done(daily)
        bind_done = _rule_done(bind)
        like_cap = int(util.number_field(like or {}, "daily_cap"))
        like_used = int(util.number_field(like or {}, "today_used"))
        like_remain = max(like_cap - like_used, 0)
        like_amount = util.number_field(like or {}, "amount") or 2
        checked_in = bool(daily_done)
        streak = int(util.number_field(daily or {}, "consecutive_days"))
        if not streak:
            streak = int(util.number_field(daily or {}, "streak_days"))
        parts = [
            "登录奖励 %d %s" % (util.number_field(daily or {}, "amount") or 200,
                                "已领" if daily_done else "待领"),
            "绑云奖励 %d %s" % (util.number_field(bind or {}, "amount") or 50,
                                "已领" if bind_done else "待领"),
            ("点赞可领 %d×%d=%d 魔粒" % (like_remain, like_amount, like_remain * like_amount))
            if like_remain > 0 else "点赞今日已满",
        ]
        return {
            "checked_in": checked_in,
            "checked_in_today": checked_in,
            "streak_days": streak,
            "daily_done": daily_done,
            "bind_done": bind_done,
            "like_remain": like_remain,
            "like_amount": like_amount,
            "message": " · ".join(parts),
        }

    def touch_web_session(self, account, budget_seconds=6.0):
        """Web 会话触碰：daily_active（每日登录奖励）只认 Web 会话，不认 OpenAPI 调用。

        全程受 budget_seconds 约束：宿主单次签到请求 30s 超时，这里不能把时间耗光。
        返回 (touched, dead, note)。没有 Cookie 时 touched=0、note 说明原因。
        """
        if not (account.cookie or "").strip():
            return 0, False, "无 Web 会话 Cookie，登录奖励需在浏览器登录态下才能触发"
        deadline = time.time() + max(2.0, float(budget_seconds))
        touched = 0
        dead = False
        dead_re = re.compile(self.eps.cookie_dead_re, re.I)

        def visit(path, headers, html=False):
            nonlocal touched, dead
            if time.time() > deadline:
                return
            remaining = max(1.0, deadline - time.time())
            try:
                resp = net.request("GET", self.eps.web_base.rstrip("/") + path, headers,
                                   timeout=min(8, remaining))
            except net.TransportError:
                return
            try:
                raw = resp.read(256 << 10 if html else 64 << 10)
                status = resp.status
            except net.TransportError:
                return
            finally:
                resp.close()
            if status in (401, 403):
                dead = True
            elif status < 400:
                touched += 1
                if dead_re.search((raw or b"").decode("utf-8", "replace")):
                    dead = True

        for path in self.eps.touch_paths:
            visit(path, self.cookie_headers(
                account.cookie, path, "text/html,application/xhtml+xml,*/*;q=0.8"), html=True)
        visit(self.eps.login_info, self.cookie_headers(account.cookie, self.eps.login_info))
        return touched, dead, ""

    def like_targets(self, account, needed, timeout=8):
        """列可点赞的 MCP 服务（该端点只认 Cookie）。"""
        out, seen = [], set()
        path = self.eps.mcp_servers
        for page in range(1, 4):
            if len(out) >= needed:
                break
            body = json.dumps({"PageSize": 30, "PageNumber": page, "Query": "", "Criterion": []})
            try:
                status, raw = self._json(
                    "PUT", self.eps.web_base.rstrip("/") + path,
                    self.cookie_headers(account.cookie, "/mcp/servers"), body, timeout=timeout)
            except UpstreamError:
                break
            if status >= 400:
                break
            doc = net.parse_json(raw) or {}
            if not isinstance(doc, dict):
                break
            data = doc.get("Data") if isinstance(doc.get("Data"), dict) else {}
            servers = ((data.get("McpServer") or {}).get("McpServers")
                       if isinstance(data.get("McpServer"), dict) else None)
            servers = servers if isinstance(servers, list) else []
            if not servers:
                break
            for item in servers:
                if len(out) >= needed:
                    break
                if not isinstance(item, dict) or item.get("AlreadyStar"):
                    continue
                owner = util.first_non_empty(
                    util.as_str(item.get("Path")), util.as_str(item.get("FromSitePath")),
                    util.as_str(item.get("Namespace")))
                name = util.as_str(item.get("Name"))
                if not owner or not name:
                    continue
                key = owner + "/" + name
                if key in seen:
                    continue
                seen.add(key)
                out.append((owner, name))
        return out

    def like_one(self, account, owner, name, timeout=8):
        url = "%s%s/%s/%s/stars" % (
            self.eps.web_base.rstrip("/"), self.eps.star_prefix, owner, name)
        headers = self.cookie_headers(account.cookie, "/mcp/servers/%s/%s" % (owner, name))
        try:
            status, raw = self._json("PUT", url, headers, "{}", timeout=timeout)
        except UpstreamError:
            return False
        if status >= 400:
            return False
        doc = net.parse_json(raw) or {}
        if isinstance(doc, dict):
            return bool(doc.get("Success")) or doc.get("Code") in (200, "200")
        return True


def _rule_done(rule):
    if not isinstance(rule, dict):
        return False
    used = util.number_field(rule, "today_used")
    remain = util.number_field(rule, "today_remain")
    return used > 0 and remain == 0


def _token_response(status, raw, fallback_refresh=""):
    doc = net.parse_json(raw) or {}
    if not isinstance(doc, dict):
        doc = {}
    # OAuth 规范：错误体带 error / error_description（魔搭也用 HTTP 200 返回）
    if doc.get("error"):
        raise UpstreamError(401, "令牌请求被拒：%s%s" % (
            util.as_str(doc.get("error")),
            ("（%s）" % util.as_str(doc.get("error_description"))) if doc.get("error_description") else ""),
            disable=True)
    if status >= 400 or not doc.get("access_token"):
        raise UpstreamError(401, "换取令牌失败：HTTP %d %s" % (
            status, util.truncate_runes(_error_message(raw), 80)), disable=True)
    refresh = util.as_str(doc.get("refresh_token")) or fallback_refresh
    return {
        "token": util.as_str(doc.get("access_token")),
        "refresh_token": refresh,
        "expires_at": _expires_ms(doc.get("expires_in")),
        "scope": util.as_str(doc.get("scope")),
        "rotated": bool(doc.get("refresh_token")) and util.as_str(doc.get("refresh_token")) != fallback_refresh,
    }


def _expires_ms(seconds_value):
    try:
        seconds = int(seconds_value or 0)
    except (TypeError, ValueError):
        return 0
    if seconds <= 0:
        return 0
    return int(time.time() * 1000) + seconds * 1000
