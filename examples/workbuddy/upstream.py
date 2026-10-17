# -*- coding: utf-8 -*-
"""上游访问层（对应 Go 版 upstream.go）。

包含站点区域配置、请求头拼装、签到、额度、续期与通用解析工具。
"""

import json
import secrets
import socket
import time
import urllib.error
import urllib.parse
import urllib.request

from fingerprint import request_id, derive_id
from util import (
    as_str, first_non_empty, format_credits, now_millis, to_float, truncate_runes,
)

# OAuth 登录相关的固定语义值
OAUTH_PENDING_CODE = 11217  # 上游表示「还在等浏览器授权」

HTTP_TIMEOUT = 30          # 账单 / 签到类接口
MAX_RESPONSE_BYTES = 4 << 20


class RealmConfig:
    __slots__ = ("chat_origin", "billing_origin", "domain", "chat_ua",
                 "billing_ua", "intl", "has_checkin")

    def __init__(self, chat_origin, billing_origin, domain, chat_ua, billing_ua, intl, has_checkin):
        self.chat_origin = chat_origin
        self.billing_origin = billing_origin
        self.domain = domain
        self.chat_ua = chat_ua
        self.billing_ua = billing_ua
        self.intl = intl
        self.has_checkin = has_checkin


def realm_config(eps, site):
    """按站点区域返回出口配置（intl / cn）。"""
    if site == "intl":
        return RealmConfig(
            chat_origin=eps.intl_chat_origin.rstrip("/"),
            billing_origin=eps.intl_billing_origin.rstrip("/"),
            domain="www.workbuddy.ai",
            chat_ua="WorkBuddy/5.5.2 WorkBuddy AI/5.5.2 CLI/5.5.2",
            billing_ua="WorkBuddy/5.5.2",
            intl=True,
            has_checkin=False,
        )
    return RealmConfig(
        chat_origin=eps.cn_chat_origin.rstrip("/"),
        billing_origin=eps.cn_billing_origin.rstrip("/"),
        domain="copilot.tencent.com",
        chat_ua="WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1",
        billing_ua="WorkBuddy/5.5.6",
        intl=False,
        has_checkin=True,
    )


# ---------- 请求头 ----------


def billing_headers(acc, cfg):
    """账单/签到这类接口用的轻量身份头。

    这些端点只认「官网行为」的标头组合：Origin/Referer/X-CodeBuddy-Request，
    外加按 uid 稳定派生的设备指纹，缺一样就可能被风控挡掉。
    """
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/plain, */*",
        "X-Requested-With": "XMLHttpRequest",
        "User-Agent": cfg.billing_ua,
        "Origin": cfg.billing_origin,
        "Referer": cfg.billing_origin + "/",
        "Authorization": "Bearer " + acc.access_token,
        "X-User-Id": acc.uid,
        "X-Domain": first_non_empty(acc.domain, cfg.domain),
        "X-CodeBuddy-Request": "1",
        "X-Request-ID": request_id(acc.uid),
        "X-Machine-ID": derive_id(acc.uid, "machine"),
        "X-Session-ID": derive_id(acc.uid, "session"),
    }
    if cfg.intl:
        headers["Accept-Language"] = "en-US"
    else:
        headers["Accept-Language"] = "zh-CN"
        headers["X-Product"] = "SaaS"
    if acc.enterprise_id:
        headers["X-Enterprise-Id"] = acc.enterprise_id
        headers["X-Tenant-Id"] = acc.enterprise_id
    else:
        headers["X-No-Enterprise-Id"] = "1"
    if acc.device_token:
        headers["X-Device-Token"] = acc.device_token
    return headers


def chat_headers(acc, cfg):
    """对话转发用的「官方客户端身份」头。

    上游认这套头，缺了会被当成非官方客户端拒掉：
    X-IDE-Type / X-IDE-Name / X-Product = WorkBuddy、X-Agent-Purpose: conversation。
    """
    ide_version = "5.5.2" if cfg.intl else "5.5.6"
    return {
        "Content-Type": "application/json",
        "Accept": "application/json, text/plain, */*",
        "X-Agent-Purpose": "conversation",
        "X-IDE-Name": "WorkBuddy",
        "X-IDE-Type": "WorkBuddy",
        "X-IDE-Version": ide_version,
        "X-Product": "WorkBuddy",
        "X-Domain": first_non_empty(acc.domain, cfg.domain),
        "User-Agent": cfg.chat_ua,
        "X-User-Id": acc.uid,
        "Authorization": "Bearer " + acc.access_token,
        # 对话头用 32 位随机 hex，与账单头的稳定 request_id 不可混用
        "X-Request-ID": secrets.token_hex(16),
    }


def oauth_base_headers(cfg):
    return {
        "Content-Type": "application/json",
        "Accept": "application/json, text/plain, */*",
        "X-Requested-With": "XMLHttpRequest",
        "User-Agent": cfg.billing_ua,
        "Origin": cfg.billing_origin,
        "Referer": cfg.billing_origin + "/",
    }


# ---------- 底层请求 ----------


class UpstreamError(Exception):
    def __init__(self, message):
        super().__init__(message)
        self.message = message


def http_json(method, url, body, headers, timeout=HTTP_TIMEOUT):
    """发一个 JSON 请求，返回 (status, raw_bytes)。网络失败抛 UpstreamError。"""
    payload = json.dumps(body if body is not None else {}, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(url, data=payload, method=method)
    for key, value in headers.items():
        if value:
            request.add_header(key, value)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as resp:
            raw = resp.read(MAX_RESPONSE_BYTES)
            return resp.status, raw
    except urllib.error.HTTPError as exc:
        try:
            raw = exc.read(MAX_RESPONSE_BYTES)
        except Exception:
            raw = b""
        return exc.code, raw
    except (urllib.error.URLError, socket.timeout, OSError) as exc:
        raise UpstreamError("请求上游失败: %s" % truncate_runes(str(exc), 120))


def parse_envelope(raw, status):
    """解析上游统一响应外壳 {code,msg,data}；非 JSON / 401 时给出可读错误。"""
    try:
        doc = json.loads(raw.decode("utf-8"))
    except Exception:
        snippet = truncate_runes(raw.decode("utf-8", "replace"), 200)
        if status == 401:
            raise UpstreamError("登录态无效或已过期（401）")
        raise UpstreamError("上游返回了非 JSON 响应（HTTP %d）：%s" % (status, snippet))
    if not isinstance(doc, dict):
        raise UpstreamError("上游返回了非 JSON 响应（HTTP %d）" % status)
    code = doc.get("code")
    if isinstance(code, bool) or not isinstance(code, (int, float)):
        code = 0
    env = {
        "code": int(code),
        "msg": as_str(doc.get("msg")),
        "data": doc.get("data") if isinstance(doc.get("data"), (dict, list)) else {},
    }
    if status == 401:
        raise UpstreamError("登录态无效或已过期（401）")
    return env


def do_json(method, url, body, headers, timeout=HTTP_TIMEOUT):
    """返回 (env, status)；非 200 与上游错误码都如实带出，由调用方判断。"""
    status, raw = http_json(method, url, body, headers, timeout=timeout)
    try:
        env = parse_envelope(raw, status)
    except UpstreamError as exc:
        raise exc
    return env, status


def call_billing(eps, acc, origin, path, body, timeout=HTTP_TIMEOUT):
    """用账单身份头调一个接口；origin 为空时用该区域的账单域。"""
    cfg = realm_config(eps, acc.site)
    if not origin:
        origin = cfg.billing_origin
    env, _ = do_json("POST", origin + path, body, billing_headers(acc, cfg), timeout=timeout)
    return env


# ---------- 签到 ----------


class CheckinResult:
    __slots__ = ("checked_in_today", "streak_days", "today_credit", "daily_credit")

    def __init__(self, checked_in_today=False, streak_days=0, today_credit=0.0, daily_credit=0.0):
        self.checked_in_today = checked_in_today
        self.streak_days = streak_days
        self.today_credit = today_credit
        self.daily_credit = daily_credit


def fetch_checkin_status(eps, acc):
    """查签到状态（只读）。注意它挂在对话域上，但用的是账单身份头。"""
    cfg = realm_config(eps, acc.site)
    if not cfg.has_checkin:
        return CheckinResult()
    env = call_billing(eps, acc, cfg.chat_origin, eps.checkin_status, None)
    data = env["data"] if isinstance(env["data"], dict) else {}

    result = CheckinResult()
    if data.get("today_checked_in") is True:
        result.checked_in_today = True
    result.streak_days = int(to_float(data.get("streak_days")))
    result.today_credit = to_float(data.get("today_credit"))
    result.daily_credit = to_float(data.get("daily_credit"))
    return result


def do_checkin(eps, acc):
    """执行签到；已签到按成功语义返回。返回 (CheckinResult, message)。"""
    cfg = realm_config(eps, acc.site)
    if not cfg.has_checkin:
        raise UpstreamError("该站点没有签到功能")
    env = call_billing(eps, acc, "", eps.checkin, None)
    if env["code"] == 0:
        return CheckinResult(checked_in_today=True), "签到成功"
    if env["code"] == 10001:
        return CheckinResult(checked_in_today=True), "今天已经签到过了"
    msg = env["msg"] or ("上游返回 code=%d" % env["code"])
    raise UpstreamError("签到失败：%s" % msg)


# ---------- 额度 ----------


def parse_packages(raw):
    """把资源包响应解析成可读视图列表。"""
    data = raw if isinstance(raw, dict) else {}
    accounts = data.get("Accounts")
    out = []
    if not isinstance(accounts, list):
        return out
    for item in accounts:
        if not isinstance(item, dict):
            continue
        size = _pick_float(item, "CycleCapacitySizePrecise", "CycleCapacitySize",
                           "CapacitySizePrecise", "CapacitySize")
        remain = _pick_float(item, "CycleCapacityRemainPrecise", "CycleCapacityRemain",
                             "CapacityRemainPrecise", "CapacityRemain")
        used = _pick_float(item, "CycleCapacityUsedPrecise", "CycleCapacityUsed",
                           "CapacityUsedPrecise", "CapacityUsed")
        # 上游有时只给其中两个字段，按总量补齐第三个
        if size > 0 and used <= 0 and remain <= size:
            used = max(0.0, size - remain)
        elif size > 0 and remain <= 0 and used < size:
            remain = max(0.0, size - used)
        out.append({
            "name": first_non_empty(as_str(item.get("PackageName")), "资源包"),
            "package_code": as_str(item.get("PackageCode")),
            "product_name": as_str(item.get("ProductName")),
            "unit": first_non_empty(as_str(item.get("CapacityUnit")), "credits"),
            "remain": remain,
            "used": used,
            "size": size,
            "expire_time": first_non_empty(as_str(item.get("CycleEndTime")),
                                           as_str(item.get("ExpiredTime"))),
        })
    return out


def _pick_float(m, *keys):
    for key in keys:
        if key in m and m[key] not in (None, ""):
            value = to_float(m[key])
            if value != 0:
                return value
    return 0.0


def fetch_quota(eps, acc):
    """查额度：先取套餐概览拿到 PackageCode，再分别拉免费包与付费包。

    返回 (更新后的 Account, packages, message)。
    """
    env = call_billing(eps, acc, "", eps.resource_summary, None)
    if env["code"] != 0:
        raise UpstreamError("查询额度失败：%s" % first_non_empty(
            env["msg"], "code=%d" % env["code"]))

    summary = env["data"] if isinstance(env["data"], dict) else {}
    codes = []
    packages_raw = summary.get("Packages")
    if isinstance(packages_raw, list):
        for item in packages_raw:
            if isinstance(item, dict):
                code = as_str(item.get("PackageCode"))
                if code:
                    codes.append(code)

    updated = acc.copy()
    packages = []
    if codes:
        free_body = {
            "PackageCodes": codes,
            "PageNumber": 1,
            "PageSize": 200,
            "Status": [0],
        }
        try:
            env = call_billing(eps, acc, "", eps.resource_free, free_body)
            if env["code"] == 0:
                packages.extend(parse_packages(env["data"]))
        except UpstreamError:
            pass

        paid_body = {
            "PackageCodes": codes,
            "PageNumber": 1,
            "PageSize": 200,
            "Status": [0, 3],
            "NeedRenewInfo": True,
        }
        try:
            env = call_billing(eps, acc, "", eps.resource_paid, paid_body)
            if env["code"] == 0:
                packages.extend(parse_packages(env["data"]))
        except UpstreamError:
            pass

    # 剩余额度 = 各资源包剩余之和；套餐名取第一个有名字的包
    total = 0.0
    for pkg in packages:
        total += pkg["remain"]
        if not updated.plan and pkg["name"]:
            updated.plan = pkg["name"]
    if packages:
        updated.credits = total
    updated.credits_at = now_millis()

    msg = "额度已刷新：%s" % format_credits(updated.credits)
    if updated.plan:
        msg = "额度已刷新：%s（%s）" % (format_credits(updated.credits), updated.plan)
    return updated, packages, msg


# ---------- 续期 ----------


def ensure_token(eps, acc, store):
    """保证账号有可用的 accessToken。

    关键约束：从桌面端导入的账号**不主动刷新**——refreshToken 是独占的，
    我们一刷，桌面端就被踢下线；OAuth 登录的账号自己刷。
    """
    if acc.token_alive():
        return
    if acc.source == "desktop":
        raise UpstreamError("登录态已过期：请打开一次桌面端让它自动刷新，然后重新导入")
    if not acc.refresh_token:
        raise UpstreamError("登录态已过期且没有 refreshToken，请重新授权登录")
    refresh_token(eps, acc, store)


def refresh_token(eps, acc, store):
    """用 refreshToken 续期，并把新凭据写回存储。"""
    cfg = realm_config(eps, acc.site)
    headers = billing_headers(acc, cfg)
    headers["X-Refresh-Token"] = acc.refresh_token
    headers["X-Auth-Refresh-Source"] = "plugin"

    env, _ = do_json("POST", cfg.chat_origin + eps.token_refresh, None, headers)
    if env["code"] != 0:
        raise UpstreamError("续期失败：%s" % first_non_empty(env["msg"], "code=%d" % env["code"]))

    data = env["data"] if isinstance(env["data"], dict) else {}
    access = as_str(data.get("accessToken"))
    if not access:
        raise UpstreamError("续期响应里没有 accessToken")

    now = now_millis()
    acc.access_token = access
    refresh = as_str(data.get("refreshToken"))
    if refresh:
        acc.refresh_token = refresh
    expires_in = to_float(data.get("expiresIn"))
    if expires_in > 0:
        acc.token_expires_at = now + int(expires_in) * 1000
    refresh_expires_in = to_float(data.get("refreshExpiresIn"))
    if refresh_expires_in > 0:
        acc.refresh_expires_at = now + int(refresh_expires_in) * 1000

    if store is not None:
        def mutate(stored):
            stored.access_token = acc.access_token
            stored.refresh_token = acc.refresh_token
            stored.token_expires_at = acc.token_expires_at
            stored.refresh_expires_at = acc.refresh_expires_at

        store.update(acc.id, mutate)


# ---------- OAuth ----------


def oauth_start(eps, realm, default_realm):
    """发起一次授权，返回 (state, auth_url)。"""
    cfg = realm_config(eps, realm)
    target = cfg.chat_origin + eps.auth_state + "?platform=" + urllib.parse.quote("CLI")
    env, _ = do_json("POST", target, None, oauth_base_headers(cfg))
    if env["code"] != 0:
        raise UpstreamError("发起授权失败：%s" % first_non_empty(
            env["msg"], "code=%d" % env["code"]))
    data = env["data"] if isinstance(env["data"], dict) else {}
    state = as_str(data.get("state"))
    auth_url = as_str(data.get("authUrl"))
    if not state or not auth_url:
        raise UpstreamError("授权响应里缺少 state/authUrl")
    return state, auth_url


def oauth_poll(eps, realm, state):
    """轮询一次授权结果，返回上游响应外壳。"""
    cfg = realm_config(eps, realm)
    target = cfg.chat_origin + eps.auth_token + "?state=" + urllib.parse.quote(state)
    env, _ = do_json("GET", target, None, oauth_base_headers(cfg))
    return env


def oauth_nickname(eps, realm, state, token):
    """取授权账号的昵称（失败返回空串）。"""
    cfg = realm_config(eps, realm)
    headers = oauth_base_headers(cfg)
    headers["Authorization"] = "Bearer " + token
    target = cfg.chat_origin + eps.login_account + "?state=" + urllib.parse.quote(state)
    try:
        env, _ = do_json("GET", target, None, headers)
    except UpstreamError:
        return ""
    data = env["data"] if isinstance(env["data"], dict) else {}
    return as_str(data.get("nickname"))


# ---------- 模型 ----------


def extract_models(data):
    """从产品配置里取可用模型 id（多个 agent 各带一组，合并去重）。"""
    if not isinstance(data, dict):
        return []
    ids = []
    seen = set()

    def add(value):
        if not isinstance(value, str):
            return
        value = value.strip()
        if not value or value in seen:
            return
        seen.add(value)
        ids.append(value)

    def add_list(value):
        if isinstance(value, list):
            for item in value:
                add(item)

    add_list(data.get("models"))
    add_list(data.get("availableModels"))
    agents = data.get("agents")
    if isinstance(agents, list):
        for item in agents:
            if isinstance(item, dict):
                add_list(item.get("models"))
                add_list(item.get("availableModels"))
    ids.sort()
    return ids


# ---------- 通用解析工具 ----------


def decode_data(raw):
    """上游响应外壳里的 data 段（解析失败按空对象处理）。"""
    return raw if isinstance(raw, dict) else {}


def key_matches(key, *names):
    """键名归一化比较（忽略大小写与 _ / -）。"""
    norm = key.lower().replace("_", "").replace("-", "")
    return any(norm == name.lower() for name in names)


def deep_find(m, predicate):
    """按 key 名在嵌套 JSON 里找第一个匹配值；遍历顺序固定，结果可复现。"""
    if not isinstance(m, dict) or not m:
        return None, False
    for key in sorted(m.keys()):
        if predicate(key, m[key]):
            return m[key], True
    for key in sorted(m.keys()):
        value = m[key]
        if isinstance(value, dict):
            found, ok = deep_find(value, predicate)
            if ok:
                return found, True
        elif isinstance(value, list):
            for item in value:
                if isinstance(item, dict):
                    found, ok = deep_find(item, predicate)
                    if ok:
                        return found, True
    return None, False
