# -*- coding: utf-8 -*-
"""MiniMax Code 上游访问层：OAuth 设备码、令牌续期、模型目录、积分与推理。

协议要点（实测口径，来自 dsh-our-free-model 的 minimax-* 实现）：
  - 设备码轮询的「还没授权」有两种形态：**HTTP 200 + status=pending** 与
    OAuth 标准的 **400 + error=authorization_pending** —— 两种都必须认；
  - access_token 前缀 `mmoat_`、**不是 JWT**，过期时间必须由 expires_in 自算；
  - 推理走 **Anthropic Messages**（`/mavis/api/v1/llm/v1/messages`），不是 OpenAI；
  - 业务码在 `base_resp.status_code`（签到 / 积分），HTTP 200 也可能是失败。
"""

import base64
import hashlib
import json
import secrets
import time
import urllib.parse

import net
import util


class UpstreamError(Exception):
    def __init__(self, status, msg, disable=False):
        super().__init__(msg)
        self.status = int(status)
        self.msg = msg
        self.disable = bool(disable)


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


class Upstream:
    def __init__(self, eps):
        self.eps = eps

    # ---------- OAuth 设备码 ----------

    def start_device(self):
        verifier = secrets.token_urlsafe(48)
        challenge = base64.urlsafe_b64encode(
            hashlib.sha256(verifier.encode("ascii")).digest()).rstrip(b"=").decode("ascii")
        status, doc, raw = net.form_request(
            "POST", self.eps.account_host + self.eps.device_code,
            {
                "client_id": self.eps.client_id,
                "scope": self.eps.scope,
                "audience": self.eps.audience,
                "code_challenge": challenge,
                "code_challenge_method": "S256",
            },
            {"Accept": "application/json"}, timeout=30)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(
                502, "MiniMax 设备码申请失败（HTTP %s）：%s" % (
                    status, util.truncate_runes(error_text(raw), 100)))
        device_code = util.as_str(doc.get("device_code")).strip()
        user_code = util.as_str(doc.get("user_code")).strip()
        uri = (util.as_str(doc.get("verification_uri")).strip()
               or util.as_str(doc.get("verification_url")).strip())
        expires = util.number_field(doc, "expires_in")
        if not device_code or not user_code or not uri or expires <= 0:
            raise UpstreamError(502, "MiniMax 设备码响应无法解析")
        interval = util.number_field(doc, "interval")
        interval = interval if interval > 0 else int(self.eps.poll_interval_seconds)
        return {
            "device_code": device_code,
            "code_verifier": verifier,
            "user_code": user_code,
            "url": util.as_str(doc.get("verification_uri_complete")).strip() or uri,
            "expires_in": expires,
            "interval": max(1, interval),
        }

    def poll_device_token(self, device_code, code_verifier):
        """轮询一次。返回 (kind, payload)：kind ∈ done / pending / slow_down / error。"""
        status, doc, raw = net.form_request(
            "POST", self.eps.account_host + self.eps.token,
            {
                "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
                "device_code": device_code,
                "client_id": self.eps.client_id,
                "code_verifier": code_verifier,
            },
            {"Accept": "application/json"}, timeout=30)
        record = doc if isinstance(doc, dict) else {}
        state = util.as_str(record.get("status")).strip().lower()
        error = util.as_str(record.get("error")).strip().lower()
        if status == 200 and state == "pending":
            return "pending", ""
        if status == 200 and state == "slow_down":
            return "slow_down", ""
        if status == 200 and state in ("denied", "access_denied"):
            return "error", "用户拒绝了 MiniMax 授权"
        if status == 200 and state in ("expired", "expired_token"):
            return "error", "设备码已过期，请重新发起登录"
        if error == "authorization_pending":
            return "pending", ""
        if error == "slow_down":
            return "slow_down", ""
        if status == 200:
            try:
                return "done", parse_token_grant(record, self.eps)
            except UpstreamError as err:
                return "error", err.msg
        return "error", "MiniMax 授权失败：%s" % (
            util.as_str(record.get("error")) or util.truncate_runes(error_text(raw), 100))

    def refresh_token(self, refresh_token):
        status, doc, raw = net.form_request(
            "POST", self.eps.account_host + self.eps.token,
            {
                "grant_type": "refresh_token",
                "refresh_token": refresh_token,
                "client_id": self.eps.client_id,
                "scope": self.eps.scope,
                "audience": self.eps.audience,
            },
            {"Accept": "application/json"}, timeout=30)
        if status in (400, 401, 403):
            raise UpstreamError(401, "MiniMax 续期被拒绝（HTTP %s），请重新登录" % status,
                                disable=True)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(502, "MiniMax 续期失败（HTTP %s）：%s" % (
                status, util.truncate_runes(error_text(raw), 100)))
        return parse_token_grant(doc, self.eps, refresh_token)

    # ---------- 目录 ----------

    def fetch_models(self, token):
        query = urllib.parse.urlencode({
            "region": self.eps.region, "buildEnv": self.eps.build_env})
        status, doc, _raw = net.json_request(
            "GET", self.eps.api_host + self.eps.models + "?" + query, None,
            {"Authorization": "Bearer " + token, "Accept": "application/json"}, timeout=30)
        if status != 200 or not isinstance(doc, dict):
            return []
        return parse_models(doc)

    # ---------- 签到 / 额度 ----------

    def _biz_request(self, token, path, method, body=None, timezone_id="Asia/Shanghai"):
        url = "%s%s?%s" % (self.eps.api_host, path,
                           urllib.parse.urlencode({"timezone_id": timezone_id}))
        headers = {"Authorization": "Bearer " + token, "Accept": "application/json"}
        status, doc, raw = net.json_request(method, url, body, headers, timeout=30)
        if status in (401, 403):
            raise UpstreamError(401, "账号凭据已失效（HTTP %s），请重新登录" % status, disable=True)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(502, "上游返回异常（HTTP %s）：%s" % (
                status, util.truncate_runes(error_text(raw), 100)))
        base = doc.get("base_resp") if isinstance(doc.get("base_resp"), dict) else {}
        code = base.get("status_code")
        if isinstance(code, (int, float)) and not isinstance(code, bool) and code != 0:
            message = util.as_str(base.get("status_msg")) or ("业务错误 %s" % int(code))
            raise UpstreamError(502, "MiniMax：%s" % util.truncate_runes(message, 100))
        data = doc.get("data")
        return data if isinstance(data, dict) else doc

    def fetch_checkin(self, token):
        data = self._biz_request(token, self.eps.signin_status, "GET")
        return parse_checkin(data)

    def claim_checkin(self, token):
        data = self._biz_request(token, self.eps.signin_claim, "POST", {})
        return data

    def fetch_credits(self, token):
        data = self._biz_request(token, self.eps.credit_details, "GET")
        total = data.get("total_count")
        if isinstance(total, bool) or not isinstance(total, (int, float)):
            return 0.0
        return float(total)

    # ---------- 推理（Anthropic Messages） ----------

    def messages(self, token, body):
        """发一次流式 Messages 请求，返回 StreamResponse。"""
        headers = {
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
        }
        try:
            resp = net.request(
                "POST", self.eps.api_host + self.eps.messages, headers,
                json.dumps(body, ensure_ascii=False).encode("utf-8"), timeout=600)
        except net.TransportError as exc:
            raise as_upstream_error(exc)
        if resp.status >= 400:
            try:
                text = net.read_all(resp).decode("utf-8", "replace")
            finally:
                resp.close()
            raise classify_status(resp.status, text)
        return resp


def classify_status(status, text):
    """错误语义（plugin-spec §5.2）：401/403/429/5xx 可切号；400 等不切号。"""
    lower = (text or "").lower()
    if status == 402:
        return UpstreamError(429, "MiniMax 余额不足：%s" % util.truncate_runes(text, 100))
    if status in (429,) or status >= 500:
        return UpstreamError(429 if status == 429 else 502, util.truncate_runes(text, 160))
    if status in (401, 403):
        return UpstreamError(status, util.truncate_runes(text, 160))
    if status == 404:
        return UpstreamError(404, util.truncate_runes(text, 160))
    if "quota" in lower or "balance" in lower or "insufficient" in lower:
        return UpstreamError(429, util.truncate_runes(text, 160))
    return UpstreamError(status if 400 <= status < 500 else 502,
                         util.truncate_runes(text, 160))


def parse_token_grant(doc, eps, previous_refresh=""):
    access = util.as_str(doc.get("access_token")).strip()
    refresh = util.as_str(doc.get("refresh_token")).strip() or previous_refresh
    token_type = util.as_str(doc.get("token_type")).strip()
    expires_in = util.number_field(doc, "expires_in")
    scope = util.as_str(doc.get("scope")).strip()
    if not access:
        raise UpstreamError(502, "MiniMax 令牌响应缺少 access_token")
    if not refresh:
        raise UpstreamError(502, "MiniMax 令牌响应缺少 refresh_token")
    if token_type and token_type.lower() != "bearer":
        raise UpstreamError(502, "MiniMax 令牌响应的 token_type 不是 Bearer")
    if expires_in <= 0:
        raise UpstreamError(502, "MiniMax 令牌响应缺少 expires_in")
    if scope and eps.scope not in scope.split():
        raise UpstreamError(502, "MiniMax 令牌响应的 scope 不含 %s" % eps.scope)
    return {
        "access_token": access,
        "refresh_token": refresh,
        "expires_at": int(time.time() * 1000) + expires_in * 1000,
        "scope": scope,
    }


def parse_models(doc):
    """从 `{providers:[...], config:{models:{}, model_order:[]}}` 提取对话模型。"""
    providers = doc.get("providers")
    config = doc.get("config")
    if isinstance(providers, list):
        for provider in providers:
            if isinstance(provider, dict) and util.as_str(provider.get("providerId")) == "minimax":
                if isinstance(provider.get("config"), dict):
                    config = provider.get("config")
                break
    if not isinstance(config, dict):
        return []
    models = config.get("models")
    if not isinstance(models, dict):
        return []
    order = config.get("model_order") if isinstance(config.get("model_order"), list) else []
    ids = [util.as_str(mid) for mid in order if util.as_str(mid)] or list(models.keys())
    out = []
    for mid in ids:
        entry = models.get(mid)
        if not isinstance(entry, dict):
            continue
        out.append({
            "id": mid,
            "name": util.as_str(entry.get("name")) or mid,
            "context_window": _positive(entry.get("context_window_options"))
            or util.number_field(entry.get("limit") if isinstance(entry.get("limit"), dict) else {},
                                 "context"),
            "max_tokens": util.number_field(
                entry.get("limit") if isinstance(entry.get("limit"), dict) else {}, "output"),
            "supports_image": "image" in _modalities(entry),
            "efforts": [util.as_str(x) for x in entry.get("effort_options") or []
                        if util.as_str(x)],
            "default_effort": util.as_str(entry.get("default_effort")),
            "thinking_mode": util.as_str(
                (entry.get("thinking_config") or {}).get("mode")
                if isinstance(entry.get("thinking_config"), dict) else ""),
        })
    return out


def _positive(values):
    if not isinstance(values, list):
        return 0
    numbers = [v for v in values if isinstance(v, (int, float)) and not isinstance(v, bool) and v > 0]
    return int(max(numbers)) if numbers else 0


def _modalities(entry):
    modalities = entry.get("modalities")
    if not isinstance(modalities, dict):
        return []
    raw = modalities.get("input")
    return [util.as_str(x).lower() for x in raw or [] if util.as_str(x)]


def parse_checkin(data):
    """签到面板 → (今日已签, 连续天数, 今日可得)。"""
    panel = data.get("panel") if isinstance(data.get("panel"), dict) else {}
    days = panel.get("days") if isinstance(panel.get("days"), list) else []
    checked = False
    streak = 0
    daily = 0
    for day in days:
        if not isinstance(day, dict):
            continue
        is_today = bool(day.get("isToday"))
        status = day.get("status")
        points = day.get("points")
        if is_today:
            checked = status == 1
            if isinstance(points, (int, float)) and not isinstance(points, bool):
                daily = int(points)
    # 连续天数：从今日往前数连续已签到
    ordered = sorted([d for d in days if isinstance(d, dict)],
                     key=lambda d: util.number_field(d, "dayNo"))
    index = next((i for i, d in enumerate(ordered) if d.get("isToday")), -1)
    if index >= 0 and ordered[index].get("status") == 1:
        while index >= 0 and ordered[index].get("status") == 1:
            streak += 1
            index -= 1
    elif index > 0:
        while index - 1 >= 0 and ordered[index - 1].get("status") == 1:
            streak += 1
            index -= 1
    return {"checked_in_today": checked, "streak_days": streak, "daily_credit": daily}
