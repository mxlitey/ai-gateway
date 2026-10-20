# -*- coding: utf-8 -*-
"""Loomy（讯飞）上游访问层：HMAC-SHA1 签名、短信登录、模型目录、积分与推理。

三条必须记住的坑（来自 dsh-our-free-model 的 loomy-* 实现）：
  1. **两套认证头**：`/chat/completions` 只认 `Authorization: Bearer`，
     而 `/models`、`/points/*` 只认小写 `token` 头；带错的那个回 HTTP 200 +
     `{"code":"100002","desc":"缺少 token"}`。
  2. **业务失败恒返回 HTTP 200**，成败只能读 body 的 `code`（成功是 `000000`）。
  3. **没有 refresh 端点**：session 过期只能重新短信登录。
"""

import base64
import hashlib
import hmac
import json
import time
import urllib.parse
import uuid

import net
import util

OK_CODE = "000000"
AUTH_ERROR_CODE = "100002"


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


def parse_envelope(doc, allow_list=False):
    """`{code, desc, data}`：code 必须为 `000000`，且 data 为对象（或按需允许数组）。"""
    if not isinstance(doc, dict):
        return None, "响应不是 JSON 对象"
    code = util.as_str(doc.get("code"))
    message = util.as_str(doc.get("desc")) or util.as_str(doc.get("message"))
    if code != OK_CODE:
        text = message or ("业务错误 %s" % (code or "(缺少 code)"))
        return None, text
    data = doc.get("data")
    if isinstance(data, dict):
        return data, ""
    if allow_list and isinstance(data, list):
        return {"data": data}, ""
    return None, message or "data 为空（session 可能已失效）"


def split_rate(raw_name):
    """从展示名末尾拆出倍率：`MiniMax M3 （x4.0）` → (`MiniMax M3`, `x4.0`)。"""
    import re

    text = (raw_name or "").strip()
    if not text:
        return "", ""
    match = re.match(r"^(.*?)\s*[（(]\s*(x\s*[\d.]+)\s*[)）]\s*$", text, re.I)
    if match and match.group(1).strip():
        return match.group(1).strip(), match.group(2).replace(" ", "").lower()
    match = re.match(r"^(.*?)\s*·\s*(x\s*[\d.]+)\s*$", text, re.I)
    if match and match.group(1).strip():
        return match.group(1).strip(), match.group(2).replace(" ", "").lower()
    return text, ""


def display_name(raw_name):
    name, rate = split_rate(raw_name)
    return "%s · %s" % (name, rate) if rate else name


def build_account_body(eps, param):
    return {
        "base": {
            "appid": eps.app_id,
            "modelid": "Web",
            "version": "1.0.0",
            "devid": "web",
            "ua": eps.client_ua,
            "traceid": uuid.uuid4().hex,
        },
        "param": param,
    }


def signing_string(method, path, query, body, content_type, date, nonce):
    """讯飞 CAccount 的 9 段签名串：后两段恒为空（不发 x-* 头）。"""
    escaped_path = _escape_path(path)
    escaped_query = "&".join(
        "%s=%s" % (_escape(qk), _escape(qv)) for qk, qv in (query or {}).items())
    md5 = base64.b64encode(hashlib.md5(body.encode("utf-8")).digest()).decode("ascii") if body else ""
    return "\n".join([
        method.upper(), escaped_path, escaped_query, md5, content_type,
        date, nonce, "", "",
    ])


def _escape(value):
    return urllib.parse.quote(str(value), safe="-_.~")


def _escape_path(path):
    text = path if path.startswith("/") else "/" + path
    if len(text) > 1 and text.endswith("/"):
        text = text[:-1]
    return "/".join(_escape(seg) if seg else "" for seg in text.split("/"))


class Upstream:
    def __init__(self, eps):
        self.eps = eps

    # ---------- 讯飞账号（短信） ----------

    def _account_request(self, path, param):
        body = build_account_body(self.eps, param)
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        date = time.strftime("%a, %d %b %Y %H:%M:%S GMT", time.gmtime())
        nonce = str(uuid.uuid4())
        signature = base64.b64encode(hmac.new(
            self.eps.access_key_secret.encode("utf-8"),
            signing_string("POST", path, {}, raw.decode("utf-8"),
                           "application/json", date, nonce).encode("utf-8"),
            hashlib.sha1).digest()).decode("ascii")
        headers = {
            "Authorization": "account %s:%s" % (self.eps.access_key_id, signature),
            "Date": date,
            "Nonce": nonce,
            "Content-Type": "application/json",
            "Content-MD5": base64.b64encode(hashlib.md5(raw).digest()).decode("ascii"),
            "Accept": "application/json",
        }
        status, doc, _raw = net.json_request(
            "POST", self.eps.account_base + path, None, headers, timeout=30, raw_body=raw)
        if status != 200:
            raise UpstreamError(502, "讯飞账号请求失败（HTTP %s）" % status)
        data, message = parse_envelope(doc)
        if data is None:
            raise UpstreamError(400, message)
        return data

    def send_sms(self, phone):
        data = self._account_request(
            self.eps.send_msg_code,
            {"ccode": "86", "phone": phone, "expire": 300})
        msgid = util.as_str(data.get("msgid"))
        if not msgid:
            raise UpstreamError(502, "短信验证码响应缺少 msgid")
        return msgid

    def login_sms(self, phone, code, msgid):
        data = self._account_request(
            self.eps.check_code,
            {"ccode": "86", "phone": phone, "mcode": code, "msgid": msgid,
             "expire": self.eps.session_ttl_seconds})
        session = util.as_str(data.get("session"))
        userid = util.as_str(data.get("userid"))
        if not session:
            raise UpstreamError(502, "登录响应缺少 session")
        if not userid:
            raise UpstreamError(502, "登录响应缺少 userid")
        return {"session": session, "userid": userid, "phone": phone}

    # ---------- 业务 ----------

    def _business(self, token, path, method="GET", body=None, allow_list=False):
        """业务端点只认小写 `token` 头（带 Bearer 会被判「缺少 token」）。"""
        headers = {"Accept": "application/json", "token": token}
        if body is not None:
            headers["Content-Type"] = "application/json"
        try:
            resp = net.request(method, self.eps.api_base + path, headers,
                               json.dumps(body).encode("utf-8") if body is not None else None,
                               timeout=30)
        except net.TransportError as exc:
            raise as_upstream_error(exc)
        try:
            raw = net.read_all(resp)
            status = resp.status
        finally:
            resp.close()
        if status in (401, 403):
            raise UpstreamError(401, "账号登录态已失效（HTTP %s），请重新登录" % status, disable=True)
        doc = net.parse_json(raw)
        data, message = parse_envelope(doc, allow_list=allow_list)
        if data is None:
            if util.as_str((doc or {}).get("code")) == AUTH_ERROR_CODE:
                raise UpstreamError(401, message or "登录态已失效，请重新登录", disable=True)
            raise UpstreamError(400 if status < 500 else 502, message)
        return data

    def fetch_models(self, token):
        data = self._business(token, self.eps.models, allow_list=True)
        raw = data.get("data") if isinstance(data.get("data"), list) else data.get("models")
        if not isinstance(raw, list) and isinstance(data.get("list"), list):
            raw = data["list"]
        if not isinstance(raw, list):
            return []
        out = []
        for item in raw:
            if not isinstance(item, dict):
                continue
            mid = util.as_str(item.get("id")).strip()
            if not mid or util.as_str(item.get("type")) != "chat":
                continue
            capabilities = item.get("capabilities") if isinstance(item.get("capabilities"), dict) else {}
            inputs = capabilities.get("input_modalities")
            efforts = [util.as_str(x) for x in item.get("reasoning_efforts") or []
                       if util.as_str(x)]
            out.append({
                "id": mid,
                "name": display_name(util.as_str(item.get("name")) or mid),
                "context_window": util.number_field(item, "context_length"),
                "supports_image": isinstance(inputs, list)
                and "image" in [util.as_str(x).lower() for x in inputs],
                "efforts": efforts,
                "default_effort": util.as_str(item.get("default_reasoning_effort")),
            })
        return out

    def fetch_points(self, token):
        """返回 (永久积分, 每日余额, 合计)。"""
        data = self._business(token, self.eps.points_records)
        permanent, ok1 = util.to_float_ok(data.get("balance"))
        if not ok1:
            raise UpstreamError(502, "积分响应缺少 balance 字段")
        daily, _ = util.to_float_ok(data.get("dailyBalance"))
        available, ok2 = util.to_float_ok(data.get("availableBalance"))
        total = available if ok2 else permanent + daily
        return {"permanent": permanent, "daily": daily, "total": total}

    def claim_daily(self, token):
        """触发每日额度初始化。返回 (是否本次发放, 说明)。"""
        data = self._business(token, self.eps.points_claim, "POST", {})
        already = data.get("alreadyProcessed") is True
        quota, _ = util.to_float_ok(data.get("dailyQuota"))
        balance, _ = util.to_float_ok(data.get("dailyBalance"))
        consumed, _ = util.to_float_ok(data.get("dailyConsumed"))
        if already:
            message = "今日额度已初始化"
            if quota:
                message += "（每日 %s/%s）" % (_fmt(balance), _fmt(quota))
            return False, message
        granted = max(quota - consumed, 0.0) if quota else 0.0
        return True, "每日额度已初始化" + ("，可用 %s" % _fmt(granted) if granted else "")

    def chat(self, token, body):
        headers = {
            "Accept": "text/event-stream",
            "Content-Type": "application/json",
            "Authorization": "Bearer " + token,
            "token": token,
        }
        payload = dict(body)
        payload["stream"] = True
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


def _fmt(value):
    value = float(value)
    return ("%d" % value) if value.is_integer() else ("%.2f" % value)
