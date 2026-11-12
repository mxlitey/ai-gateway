# -*- coding: utf-8 -*-
"""Cline 上游访问层：登录、续期、推理、模型目录与余额。

错误语义（docs/plugin-spec.md §5.2）：
  401 / 403 / 404 / 429 / 5xx  → 交回原状态码，让网关切号
  其余 4xx（400 等）          → 不切号
  403 但文案是「地域限制」    → 归 400（换号也没用，如实说明原因）
  402 / 额度耗尽文案          → 归 429（账号级额度用尽，换个号可能还能用）
"""

import net
import util

# 上游表示「用户还没在浏览器里完成授权」的错误码（不是失败）。
AUTH_PENDING = "authorization_pending"
SLOW_DOWN = "slow_down"

# 额度/限流文案标记（中英双通道）。
_CREDIT_MARKERS = (
    "insufficient", "quota", "rate limit", "too many requests", "balance",
    "credit", "payment required", "exceeded",
    "积分不足", "额度不足", "余额不足", "频率限制", "超出限制",
)

# 与凭据无关的访问限制（地域封锁 / 模型未开通）。
_REGION_MARKERS = (
    "not available in your region",
    "access forbidden",
    "region not supported",
    "not available in your country",
)


class UpstreamError(Exception):
    def __init__(self, status, msg, disable=False, code=""):
        super().__init__(msg)
        self.status = int(status)
        self.msg = msg
        self.disable = bool(disable)
        self.code = code


def as_upstream_error(exc):
    if isinstance(exc, UpstreamError):
        return exc
    if isinstance(exc, net.TransportError):
        return UpstreamError(502, "连接上游失败：%s" % util.truncate_runes(str(exc), 80))
    return UpstreamError(502, util.truncate_runes(str(exc) or "上游请求失败", 120))


def error_text(raw):
    doc = net.parse_json(raw)
    if isinstance(doc, dict):
        err = doc.get("error")
        if isinstance(err, dict):
            for key in ("message", "msg", "type", "code"):
                value = err.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        if isinstance(err, str) and err.strip():
            return err.strip()
        for key in ("message", "msg", "error_description", "detail"):
            value = doc.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    text = (raw or b"").decode("utf-8", "replace").strip()
    return util.truncate_runes(text, 200) or "上游返回错误"


def classify_status(status, text, context="chat"):
    """把上游 HTTP 状态映射成插件对外语义。"""
    lower = (text or "").lower()
    if context == "chat" and status == 403 and any(m in lower for m in _REGION_MARKERS):
        # 地域限制与账号无关：归 400，避免网关白白切号。
        return UpstreamError(400, "模型在当前地区不可用：%s" % util.truncate_runes(text, 120))
    if status in (402,) or (status == 429 and any(m in lower for m in _CREDIT_MARKERS)):
        return UpstreamError(429, "Cline 额度或频率受限：%s" % util.truncate_runes(text, 100))
    if status in (429,) or status >= 500:
        return UpstreamError(429 if status == 429 else 502,
                             util.truncate_runes(text, 160))
    if status in (401, 403):
        return UpstreamError(status, util.truncate_runes(text, 160))
    if status == 404:
        return UpstreamError(404, util.truncate_runes(text, 160))
    return UpstreamError(status if 400 <= status < 500 else 502,
                         util.truncate_runes(text, 160))


class Upstream:
    def __init__(self, eps):
        self.eps = eps

    # ---------- 头 ----------

    def base_headers(self, extra=None):
        headers = {
            "Accept": "application/json",
            "HTTP-Referer": self.eps.http_referer,
            "X-Title": self.eps.x_title,
            "X-IS-MULTIROOT": self.eps.x_is_multiroot,
            "X-CLIENT-TYPE": self.eps.x_client_type,
        }
        if extra:
            headers.update(extra)
        return headers

    def auth_headers(self, token, extra=None):
        """访问令牌必须带 `workos:` 前缀（剥掉即 401）。"""
        value = (token or "").strip()
        prefix = self.eps.token_prefix
        if value and not value.startswith(prefix):
            value = prefix + value
        headers = self.base_headers(extra)
        headers["Authorization"] = "Bearer " + value
        return headers

    # ---------- 登录：WorkOS 设备码 ----------

    def device_authorization(self):
        status, doc, raw = net.form_request(
            "POST", self.eps.workos_base + self.eps.device_authorize,
            {"client_id": self.eps.workos_client_id},
            {"Accept": "application/json"}, timeout=30)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(
                502, "Cline 设备码授权失败（HTTP %s）：%s" % (status, util.truncate_runes(error_text(raw), 100)))
        device_code = util.as_str(doc.get("device_code"))
        user_code = util.as_str(doc.get("user_code"))
        uri = util.as_str(doc.get("verification_uri"))
        if not device_code or not user_code or not uri:
            raise UpstreamError(502, "Cline 设备码响应缺少必要字段")
        expires = util.number_field(doc, "expires_in") or self.eps.login_timeout_seconds
        interval = util.number_field(doc, "interval")
        interval = interval if interval > 0 else int(self.eps.poll_interval_seconds)
        return {
            "device_code": device_code,
            "user_code": user_code,
            "url": util.as_str(doc.get("verification_uri_complete")) or uri,
            "expires_in": max(60, expires),
            "interval": max(1, interval),
        }

    def poll_device_token(self, device_code):
        """轮询一次。返回 (kind, payload)：kind ∈ done / pending / slow_down / error。"""
        status, doc, raw = net.form_request(
            "POST", self.eps.workos_base + self.eps.device_authenticate,
            {
                "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
                "device_code": device_code,
                "client_id": self.eps.workos_client_id,
            },
            {"Accept": "application/json"}, timeout=30)
        if status == 200 and isinstance(doc, dict):
            access = util.as_str(doc.get("access_token"))
            refresh = util.as_str(doc.get("refresh_token"))
            if not access:
                return "error", "WorkOS token 响应缺少 access_token"
            return "done", {"access_token": access, "refresh_token": refresh}
        code = util.as_str(doc.get("error")) if isinstance(doc, dict) else ""
        description = util.as_str(doc.get("error_description")) if isinstance(doc, dict) else ""
        if code == AUTH_PENDING:
            return "pending", description
        if code == SLOW_DOWN:
            return "slow_down", description
        if code in ("access_denied", "expired_token", "invalid_grant"):
            return "error", description or "WorkOS 授权失败：%s" % code
        return "error", "WorkOS 轮询失败（HTTP %s）：%s" % (
            status, util.truncate_runes(error_text(raw), 100))

    def register_tokens(self, access_token, refresh_token):
        """用 WorkOS token 换 Cline 自己的 token（`/api/v1/auth/register`）。"""
        status, doc, raw = net.json_request(
            "POST", self.eps.api_base + self.eps.register,
            {"accessToken": access_token, "refreshToken": refresh_token},
            self.base_headers(), timeout=30)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(
                502, "Cline 令牌注册失败（HTTP %s）：%s" % (status, util.truncate_runes(error_text(raw), 100)))
        return parse_token_payload(doc)

    def refresh_tokens(self, refresh_token):
        """续期：字段是驼峰 `refreshToken` / `grantType`（不是 OAuth 标准形态）。"""
        status, doc, raw = net.json_request(
            "POST", self.eps.api_base + self.eps.refresh,
            {"refreshToken": refresh_token, "grantType": "refresh_token"},
            self.base_headers(), timeout=30)
        if status in (401, 403):
            raise UpstreamError(401, "Cline 续期被拒绝（HTTP %s），请重新登录" % status, disable=True)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(
                502, "Cline 续期失败（HTTP %s）：%s" % (status, util.truncate_runes(error_text(raw), 100)))
        return parse_token_payload(doc)

    # ---------- 身份 / 目录 / 余额 ----------

    def fetch_me(self, token):
        status, doc, raw = net.json_request(
            "GET", self.eps.api_base + self.eps.me, None, self.auth_headers(token), timeout=30)
        if status != 200 or not isinstance(doc, dict):
            return None
        data = doc.get("data") if isinstance(doc.get("data"), dict) else doc
        return data

    def fetch_models(self, token):
        """远端模型清单：`recommended-models` 的 free 数组是免费模型的唯一权威来源。"""
        ids = []
        free_ids = []
        try:
            status, doc, _raw = net.json_request(
                "GET", self.eps.api_base + self.eps.recommended_models, None,
                self.base_headers(), timeout=30)
            if status == 200 and isinstance(doc, dict):
                for key in ("free", "recommended", "clinePass", "cline_pass"):
                    for item in doc.get(key) or []:
                        if isinstance(item, dict):
                            mid = util.as_str(item.get("id")).strip()
                            if mid:
                                ids.append(mid)
                                if key == "free":
                                    free_ids.append(mid)
        except net.TransportError:
            pass

        all_ids = []
        if (token or "").strip():
            try:
                status, doc, _raw = net.json_request(
                    "GET", self.eps.api_base + self.eps.models, None,
                    self.auth_headers(token), timeout=30)
                if status == 200:
                    data = doc.get("data") if isinstance(doc, dict) else None
                    for item in data or []:
                        if isinstance(item, dict):
                            mid = util.as_str(item.get("id")).strip()
                            if mid:
                                all_ids.append(mid)
            except net.TransportError:
                pass

        out = []
        seen = set()
        for mid in list(ids) + list(all_ids):
            if mid and mid not in seen:
                seen.add(mid)
                out.append(mid)
        return {
            "models": out,
            "free": free_ids,
            # 远端全部失败时用兜底表，保证用户至少能看到免费模型
            "fallback": [] if out else list(self.eps.fallback_models),
        }

    def fetch_balance(self, token, account_id):
        """余额查询：必须用 Cline 的 `usr-...`，不是 JWT 的 sub。"""
        if not (account_id or "").strip():
            raise UpstreamError(400, "该账号缺少 Cline 账号 id，无法查询余额")
        path = self.eps.balance % account_id.strip()
        status, doc, raw = net.json_request(
            "GET", self.eps.api_base + path, None, self.auth_headers(token), timeout=30)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(
                status if status in (401, 403, 404, 429) else 502,
                "余额查询失败（HTTP %s）：%s" % (status, util.truncate_runes(error_text(raw), 100)))
        data = doc.get("data") if isinstance(doc.get("data"), dict) else doc
        raw_balance = data.get("balance")
        if isinstance(raw_balance, bool) or not isinstance(raw_balance, (int, float)):
            raise UpstreamError(502, "余额响应缺少 balance 字段")
        return float(raw_balance) / float(self.eps.balance_scale or 1.0)

    # ---------- 推理 ----------

    def chat(self, token, body, want_stream):
        """转发一次 chat。返回 StreamResponse（调用方负责 close）。"""
        payload = dict(body)
        # 上游只支持 SSE；非流式由调用方聚合。
        payload["stream"] = True
        raw = net.json_dumps(payload)
        headers = self.auth_headers(token, {
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
        })
        try:
            resp = net.request(
                "POST", self.eps.api_base + self.eps.chat_completions,
                headers, raw, timeout=600)
        except net.TransportError as exc:
            raise as_upstream_error(exc)
        if resp.status >= 400:
            try:
                text = net.read_all(resp).decode("utf-8", "replace")
            finally:
                resp.close()
            raise classify_status(resp.status, text, "chat")
        return resp


def parse_token_payload(doc):
    """解析注册/续期响应（套 `{success, data}` 信封，字段是驼峰）。"""
    inner = doc.get("data") if isinstance(doc.get("data"), dict) else doc
    access = util.as_str(inner.get("accessToken") or inner.get("access_token")).strip()
    refresh = util.as_str(inner.get("refreshToken") or inner.get("refresh_token")).strip()
    expires = util.parse_timestamp_ms(
        inner.get("expiresAt") or inner.get("expires_at") or inner.get("expire_time"))
    user = inner.get("userInfo") if isinstance(inner.get("userInfo"), dict) else {}
    account_id = util.as_str(
        user.get("clineUserId") or user.get("accountId")
        or inner.get("accountId") or inner.get("account_id")).strip()
    email = util.as_str(user.get("email") or inner.get("email")).strip()
    first = util.as_str(user.get("firstName")).strip()
    last = util.as_str(user.get("lastName")).strip()
    display = (first + " " + last).strip()
    return {
        "access_token": access,
        "refresh_token": refresh,
        "expires_at": expires,
        "account_id": account_id,
        "email": email,
        "display_name": display,
    }


def free_marker(model_id):
    return "cline-free/" in (model_id or "") or (model_id or "").endswith(":free")
