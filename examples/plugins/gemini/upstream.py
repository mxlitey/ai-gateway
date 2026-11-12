# -*- coding: utf-8 -*-
"""Gemini（Google Code Assist）上游访问层：续期、项目探测、额度与推理。

三条上游对齐细节（来自 dsh-our-free-model 的 gemini-* 实现）：
  1. **身份五头逐字写死、不许随机**，且**不带** `x-goog-api-key`；
  2. 流式请求**刻意不带 `Accept` 头**（与官方客户端抓包一致）；
  3. 信封**逐层字母序序列化** —— 上游看到的是字节。
"""

import random

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
            for key in ("message", "status", "code"):
                value = err.get(key)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        if isinstance(err, str) and err.strip():
            return err.strip()
    text = (raw or b"").decode("utf-8", "replace").strip()
    return util.truncate_runes(text, 200) or "上游返回错误"


class Upstream:
    def __init__(self, eps):
        self.eps = eps

    # ---------- 身份头 ----------

    def identity_headers(self, token, include_accept=False):
        headers = {
            "Authorization": "Bearer " + token,
            "User-Agent": self.eps.user_agent,
            "x-client-name": self.eps.client_name,
            "x-client-version": self.eps.client_version,
            "x-machine-id": self.eps.machine_id,
            "x-vscode-sessionid": self.eps.vscode_sessionid,
            "Content-Type": "application/json",
        }
        if include_accept:
            headers["Accept"] = "application/json"
        return headers

    # ---------- OAuth ----------

    def refresh(self, refresh_token):
        status, doc, raw = net.form_request(
            "POST", self.eps.token_host + "/token",
            {
                "client_id": self.eps.client_id,
                "client_secret": self.eps.client_secret,
                "refresh_token": refresh_token,
                "grant_type": "refresh_token",
            },
            {"Accept": "application/json"}, timeout=30)
        if status in (400, 401, 403) and isinstance(doc, dict) and doc.get("error"):
            raise UpstreamError(
                401, "Google 续期被拒绝：%s" % error_text(raw), disable=True)
        if status != 200 or not isinstance(doc, dict):
            raise UpstreamError(
                502, "Google 续期失败（HTTP %s）：%s" % (
                    status, util.truncate_runes(error_text(raw), 100)))
        access = util.as_str(doc.get("access_token")).strip()
        if not access:
            raise UpstreamError(502, "Google 令牌响应缺少 access_token")
        expires_in = util.number_field(doc, "expires_in") or 3600
        return {
            "access_token": access,
            # ⚠️ Google 偶尔轮换 refresh_token：给了新的就必须回写
            "refresh_token": util.as_str(doc.get("refresh_token")).strip() or refresh_token,
            "expires_at": util.now_millis() + expires_in * 1000,
            "scope": util.as_str(doc.get("scope")),
            "token_type": util.as_str(doc.get("token_type")) or "Bearer",
        }

    def userinfo(self, token):
        status, doc, _raw = net.json_request(
            "GET", self.eps.userinfo, None,
            {"Authorization": "Bearer " + token, "Accept": "application/json"}, timeout=20)
        if status != 200 or not isinstance(doc, dict):
            return {}
        return doc

    # ---------- 项目号探测 ----------

    def probe_project(self, token):
        """`loadCodeAssist` 探项目号。

        成功但为空（免费档常态）→ 回落兜底串；探测**失败** → 抛错（不发推理）。
        """
        body = b'{"metadata":{"ideType":"ANTIGRAVITY"}}'
        last = ""
        for endpoint in (self.eps.endpoint_sandbox, self.eps.endpoint_daily):
            try:
                resp = net.request(
                    "POST", endpoint + self.eps.load_code_assist,
                    self.identity_headers(token), body, timeout=30)
                try:
                    raw = net.read_all(resp)
                    status = resp.status
                finally:
                    resp.close()
            except net.TransportError as exc:
                last = str(exc)
                continue
            if status != 200:
                last = "loadCodeAssist HTTP %s" % status
                continue
            doc = net.parse_json(raw)
            project = _parse_project(doc)
            return project or self.eps.default_project
        raise UpstreamError(502, "无法探测 Google Cloud Code 项目号：%s" % (
            util.truncate_runes(last, 100) or "未知原因"))

    # ---------- 额度 ----------

    def fetch_quota(self, token):
        """额度查询：daily 失败时回落 sandbox（两个环境都可能承接）。"""
        last_status, last_raw = 0, b""
        for endpoint in (self.eps.endpoint_daily, self.eps.endpoint_sandbox):
            try:
                status, doc, raw = net.json_request(
                    "POST", endpoint + self.eps.quota_path, {},
                    self.identity_headers(token, include_accept=True), timeout=30)
            except net.TransportError:
                continue
            if status == 200 and isinstance(doc, dict):
                return _parse_quota(doc)
            if status in (401, 403):
                raise UpstreamError(
                    status, "额度查询失败（HTTP %s）：%s" % (
                        status, util.truncate_runes(error_text(raw), 100)))
            last_status, last_raw = status, raw
        raise UpstreamError(
            502, "额度查询失败（HTTP %s）：%s" % (
                last_status, util.truncate_runes(error_text(last_raw), 100)))

    # ---------- 推理 ----------

    def stream(self, token, envelope):
        """发一次流式请求。信封必须字母序序列化。"""
        body = util.sorted_dumps_bytes(envelope)
        try:
            resp = net.request(
                "POST", self.eps.endpoint_daily + self.eps.stream_path,
                self.identity_headers(token), body, timeout=600)
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
    if status == 404:
        return UpstreamError(404, util.truncate_runes(text, 160))
    if "quota" in lower or "resource_exhausted" in lower or "rate limit" in lower:
        return UpstreamError(429, util.truncate_runes(text, 160))
    return UpstreamError(status if 400 <= status < 500 else 502,
                         util.truncate_runes(text, 160))


def _parse_project(doc):
    if not isinstance(doc, dict):
        return ""
    top = util.as_str(doc.get("cloudaicompanionProject"))
    if top:
        return top
    tier = doc.get("currentTier")
    if isinstance(tier, dict):
        inner = util.as_str(tier.get("cloudaicompanionProject"))
        if inner:
            return inner
    return ""


def _parse_quota(doc):
    """从 `retrieveUserQuotaSummary` 提取可读额度。字段不稳定，取不到就不编造。"""
    buckets = doc.get("buckets") if isinstance(doc.get("buckets"), list) else []
    total = 0.0
    remaining = 0.0
    for bucket in buckets:
        if not isinstance(bucket, dict):
            continue
        limit = bucket.get("tokenCount")
        left = bucket.get("remainingTokenCount")
        if isinstance(limit, (int, float)) and not isinstance(limit, bool):
            total += float(limit)
        if isinstance(left, (int, float)) and not isinstance(left, bool):
            remaining += float(left)
    plan = util.as_str(doc.get("tier")) or util.as_str(doc.get("paidTier"))
    return {"total": total, "remaining": remaining, "plan": plan}


def new_request_id():
    return "agent/%d/%s" % (util.now_millis(), "%08x" % random.getrandbits(32))


def derive_session_id(project, first_user_text, lane="infer", generation=0):
    """按 (project, 首条 user 文本, lane) 确定性派生 sessionId（FNV-1a 64 位）。

    ⚠️ 会话号**不是常量**：写死会让所有用户、所有对话共用一个会话。
    这里复刻的是**依赖维度与「同输入同输出」**，取值不与官方逐字相同。
    """
    base = "%s\u0000%s\u0000%s" % (project, first_user_text, lane)
    text = base if generation <= 0 else "%s\u0000%d" % (base, generation)
    hash_value = 0xCBF29CE484222325
    prime = 0x100000001B3
    mask = 0xFFFFFFFFFFFFFFFF
    for byte in text.encode("utf-8"):
        hash_value = (hash_value ^ byte) & mask
        hash_value = (hash_value * prime) & mask
    # 按有符号 64 位解释（官方取值有正有负）
    if hash_value >= 0x8000000000000000:
        hash_value -= 0x10000000000000000
    return str(hash_value)


def tier_for(effort):
    """思考档位 → 上游模型名后缀与思考预算。"""
    tier = effort if effort in ("low", "medium", "high", "tiered") else "medium"
    budget = {"low": 1000, "medium": 4000, "high": 10000, "tiered": -1}[tier]
    return tier, budget


def canonical_model_id(model_id):
    for tier in ("low", "medium", "high", "tiered"):
        suffix = "-" + tier
        if (model_id or "").endswith(suffix):
            return model_id[: -len(suffix)]
    return model_id or ""


__all__ = [
    "Upstream", "UpstreamError", "as_upstream_error", "classify_status",
    "new_request_id", "derive_session_id", "tier_for", "canonical_model_id",
]
