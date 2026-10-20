# -*- coding: utf-8 -*-
"""LobsterAI（有道）上游访问层：授权码换令牌、续期、模型目录、额度与推理。

协议速览（来自 dsh-our-free-model 的 lobsterai-* 实现）：
    POST /api/auth/exchange               无认证，authCode + 身份载荷 → token
    POST /api/auth/refresh                无认证，refreshToken + 身份载荷 → token
    POST /api/proxy/v1/chat/completions   Bearer，标准 OpenAI + 标准 SSE
    GET  /api/models/available            Bearer + 两个 X-LobsterAI-Client-* 头
    GET  /api/user/profile-summary        Bearer
统一信封 `{code, msg, data}`：`code !== 0` 即失败；chat 端点例外（裸 SSE）。
"""

import hashlib
import json

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


def parse_envelope(doc):
    """`{code, msg, data}`：code 必须为 0，且 data 是对象。"""
    if not isinstance(doc, dict):
        return None, "响应不是 JSON 对象"
    code = util.number_field(doc, "code")
    message = util.as_str(doc.get("msg")) or util.as_str(doc.get("message"))
    if code != 0:
        return None, message or ("code=%s" % code)
    data = doc.get("data")
    if not isinstance(data, dict):
        return None, message or "data 为空（登录态可能已失效）"
    return data, ""


class Upstream:
    def __init__(self, eps):
        self.eps = eps
        self._version_cache = (0.0, "")

    # ---------- 客户端版本号（换 token 与续期都要带） ----------

    def client_version(self):
        import time

        cached_at, cached = self._version_cache
        if cached and time.time() - cached_at < 12 * 3600:
            return cached
        try:
            status, doc, _raw = net.json_request(
                "GET", self.eps.client_version_api, None,
                {"Accept": "application/json", "User-Agent": self.eps.user_agent},
                timeout=15)
            version = ""
            if status == 200 and isinstance(doc, dict):
                outer = doc.get("data")
                value = outer.get("value") if isinstance(outer, dict) else None
                version = util.as_str(value.get("version") if isinstance(value, dict) else "")
            version = version.strip()
            if version and version[0].isdigit():
                self._version_cache = (time.time(), version)
                return version
        except net.TransportError:
            pass
        return self.eps.fallback_client_version

    # ---------- 头 ----------

    def anonymous_headers(self):
        return {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": self.eps.user_agent,
        }

    def auth_headers(self, token, accept="application/json"):
        return {
            "Authorization": "Bearer " + token,
            "Accept": accept,
            "Content-Type": "application/json",
            "User-Agent": self.eps.user_agent,
        }

    def client_headers(self, token, accept="application/json"):
        """模型列表 / 对话都要这两个能力头（影响模型集合与思考开关）。"""
        headers = self.auth_headers(token, accept)
        headers["X-LobsterAI-Client-Capabilities"] = self.eps.client_capabilities
        headers["X-LobsterAI-Client-Version"] = self.client_version()
        return headers

    # ---------- 登录 / 续期 ----------

    def exchange_code(self, code, uuid, first_keyfrom):
        body = {
            "authCode": code,
            "firstKeyfrom": first_keyfrom,
            "latestKeyfrom": str(util.now_millis()),
            "uuid": uuid,
            "version": self.client_version(),
        }
        status, doc, raw = net.json_request(
            "POST", self.eps.api_base + self.eps.exchange, body,
            self.anonymous_headers(), timeout=30)
        if status != 200:
            raise UpstreamError(
                502, "LobsterAI 换取凭据失败（HTTP %s）：%s" % (
                    status, util.truncate_runes(_error_text(raw), 100)))
        data, message = parse_envelope(doc)
        if data is None:
            raise UpstreamError(400, "LobsterAI 换取凭据失败：%s" % message)
        payload = parse_token_payload(data)
        if not payload["access_token"]:
            raise UpstreamError(502, "LobsterAI 换取凭据响应缺少 accessToken")
        return payload

    def refresh(self, refresh_token, uuid, first_keyfrom, latest_keyfrom, user_id=""):
        body = {
            "firstKeyfrom": first_keyfrom or "",
            "latestKeyfrom": latest_keyfrom or "",
            "version": self.client_version(),
            "refreshToken": refresh_token,
        }
        if uuid:
            body["uuid"] = uuid
        if user_id:
            body["userId"] = user_id
        status, doc, raw = net.json_request(
            "POST", self.eps.api_base + self.eps.refresh, body,
            self.anonymous_headers(), timeout=30)
        if status in (401, 403):
            raise UpstreamError(401, "LobsterAI 续期被拒绝（HTTP %s），请重新登录" % status,
                                disable=True)
        if status != 200:
            raise UpstreamError(
                502, "LobsterAI 续期失败（HTTP %s）：%s" % (
                    status, util.truncate_runes(_error_text(raw), 100)))
        data, message = parse_envelope(doc)
        if data is None:
            raise UpstreamError(400, "LobsterAI 续期失败：%s" % message)
        payload = parse_token_payload(data)
        if not payload["access_token"]:
            raise UpstreamError(502, "LobsterAI 续期响应缺少 accessToken")
        return payload

    # ---------- 目录 / 额度 ----------

    def fetch_models(self, token):
        status, doc, raw = net.json_request(
            "GET", self.eps.api_base + self.eps.models, None,
            self.client_headers(token), timeout=30)
        if status != 200:
            raise UpstreamError(
                status if status in (401, 403) else 502,
                "模型列表查询失败（HTTP %s）：%s" % (status, util.truncate_runes(_error_text(raw), 100)))
        data, message = parse_envelope(doc)
        if data is None:
            raise UpstreamError(400, message)
        raw_list = data.get("models") if isinstance(data.get("models"), list) else data.get("data")
        if not isinstance(raw_list, list):
            return []
        out = []
        for item in raw_list:
            if not isinstance(item, dict):
                continue
            mid = util.as_str(item.get("id")).strip()
            if mid:
                out.append(mid)
        return out

    def fetch_profile(self, token):
        status, doc, raw = net.json_request(
            "GET", self.eps.api_base + self.eps.profile_summary, None,
            self.auth_headers(token), timeout=30)
        if status != 200:
            raise UpstreamError(
                status if status in (401, 403) else 502,
                "额度查询失败（HTTP %s）：%s" % (status, util.truncate_runes(_error_text(raw), 100)))
        data, message = parse_envelope(doc)
        if data is None:
            raise UpstreamError(400, message)
        return data

    # ---------- 推理 ----------

    def chat(self, token, body):
        payload = dict(body)
        payload["stream"] = True
        headers = self.client_headers(token, "text/event-stream, application/json")
        try:
            resp = net.request(
                "POST", self.eps.api_base + self.eps.chat_completions, headers,
                json.dumps(payload, ensure_ascii=False).encode("utf-8"), timeout=600)
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
    lower = (text or "").lower()
    if status in (401, 403):
        return UpstreamError(status, util.truncate_runes(text, 160))
    if status == 429 or status >= 500:
        return UpstreamError(429 if status == 429 else 502, util.truncate_runes(text, 160))
    if status == 402 or "insufficient" in lower or "balance" in lower or "quota" in lower:
        return UpstreamError(429, util.truncate_runes(text, 160))
    if status == 404:
        return UpstreamError(404, util.truncate_runes(text, 160))
    return UpstreamError(status if 400 <= status < 500 else 502,
                         util.truncate_runes(text, 160))


def parse_token_payload(data):
    user = data.get("user") if isinstance(data.get("user"), dict) else {}
    expires_in = util.number_field(data, "expiresIn")
    access = util.as_str(data.get("accessToken")).strip()
    expires_at = 0
    if expires_in > 0:
        expires_at = util.now_millis() + expires_in * 1000
    elif access:
        expires_at = util.jwt_exp_millis(access)
    return {
        "access_token": access,
        "refresh_token": util.as_str(data.get("refreshToken")).strip(),
        "expires_at": expires_at,
        "user_id": (util.as_str(user.get("id")).strip()
                    or util.as_str(user.get("userId")).strip()
                    or util.as_str(user.get("yid")).strip()),
        "nickname": util.as_str(user.get("nickname")).strip(),
    }


def resolve_uid(payload):
    for key in ("user_id",):
        value = util.as_str(payload.get(key)).strip()
        if value:
            return value
    return hashlib.sha256((payload.get("access_token") or "").encode("utf-8")).hexdigest()[:16]


def _error_text(raw):
    doc = net.parse_json(raw)
    if isinstance(doc, dict):
        message = util.as_str(doc.get("msg")) or util.as_str(doc.get("message"))
        if message:
            return message
    return util.truncate_runes((raw or b"").decode("utf-8", "replace"), 160) or "上游返回错误"
