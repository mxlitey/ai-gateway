# -*- coding: utf-8 -*-
"""上游调用：请求头构造、错误分类、token 刷新、签到与额度（对应 Go 版 upstream.go）。

关键约定（网关切号依赖这些，必须逐字对应）：
  - 先看上游业务码，再看 HTTP 状态码；**从不使用 404**。
  - 1001 在对话语境是「登录态失效（硬禁用）」，在签到/额度语境是「服务未开放」。
"""

import json
import re
import time
from urllib.parse import urlencode

import util
from util import (
    as_str, dig_bool, dig_number, dig_value, first_non_empty, first_positive,
    number_field, random_hex, to_float,
)

# 上游在「今天已签到」时也用成功码返回；按幂等语义当成功处理。
_CHECKED_IN_RE = re.compile(r"已签到|已经签到|already", re.IGNORECASE)

# 设备维度「今日已签到」的业务码：语义等同幂等成功，不作为失败展示。
_CODE_ALREADY_CHECKED_IN = 9095

# 签到类接口（status / claim）的请求体，与官方客户端逐字对齐。
_REQ_SOURCE_BODY = b'{"req_source":1}'

# 权益包 product_type 官方枚举（见 www.trae.cn 前端 ProductType）：
#   0 Free / 1 Pro / 2 Package / 3 PromoCode / 4 ProPlus / 5 ProPlusPack /
#   6 Ultra / 7 PayGo / 8 Lite / 9 SoloInvite / 100 CNExpress
# 其中 0/1/4/6/8/100 是「套餐身份」，2/3/5/7/9 是活动、兑换码等临时包。
PACK_PLAN_NAMES = {
    0: "免费版",
    1: "Pro",
    4: "Pro+",
    6: "Ultra",
    8: "Lite",
    100: "Express",
}

# 套餐档位从低到高（仅用于在一堆包中挑出当前生效的最高档）。
PACK_PLAN_RANK = [0, 8, 1, 4, 6, 100]

# 不属于任何套餐的临时包：它们不参与套餐识别。
PACK_NON_PLAN_TYPES = {2, 3, 5, 7, 9}


def pack_rank(product_type):
    """套餐档位序号；未知/非套餐类型返回 -1（永远排在已知套餐之后）。"""
    try:
        ptype = int(product_type)
    except (TypeError, ValueError):
        return -1
    if ptype in PACK_NON_PLAN_TYPES:
        return -1
    try:
        return PACK_PLAN_RANK.index(ptype)
    except ValueError:
        return -1



class UpstreamError(Exception):
    """映射到网关切号语义后的错误。"""

    def __init__(self, status, msg, disable=False, code=0):
        super().__init__(msg)
        self.status = status
        self.msg = msg
        self.disable = disable
        self.code = int(code or 0)


def classify_code(code):
    """按上游业务码映射（trae2api 实测码表）。"""
    if code == 1001:
        # 登录态失效：硬禁用该账号
        return UpstreamError(401, "账号登录态失效，请重新登录", disable=True, code=1001)
    if code == 1005:
        # 积分不足属账号级权益问题：用 403，让网关换一个还有额度的账号继续
        return UpstreamError(403, "该账号未开通此权益", disable=False, code=1005)
    if code == 4008:
        return UpstreamError(429, "免费额度已用尽，等待重置或签到", disable=False, code=4008)
    if code == 4011:
        return UpstreamError(429, "触发上游限流，请稍后重试", disable=False, code=4011)
    if code == 9074:
        # 签到高峰的容量限制：上游临时拒绝，直接如实返回，不自动重试
        return UpstreamError(502, "上游繁忙（当前参与用户太多），请稍后手动重试", disable=False, code=9074)
    if code == 4001:
        # 参数无效：模型不存在 / 版本不匹配 —— 换谁都一样，不切号
        return UpstreamError(400, "模型不可用，请更换模型", disable=False, code=4001)
    return None


def classify_upstream(status, body):
    """先看上游业务码，再看 HTTP 状态码。本插件只支持国内站，模型缺失对谁都一样，因此从不使用 404。"""
    code = extract_code(body)
    if code != 0:
        mapped = classify_code(code)
        if mapped is not None:
            return mapped
    if status == 401:
        return UpstreamError(401, "账号登录态失效，请重新登录", disable=True)
    if status == 403:
        return UpstreamError(403, "该账号无权使用（权益不足或地区限制）", disable=False)
    if status == 429:
        return UpstreamError(429, "触发上游限流，请稍后重试", disable=False)
    if status >= 500:
        return UpstreamError(502, "上游服务异常，请稍后重试", disable=False)
    if status >= 400:
        # 其余 4xx 归为请求本身的问题 —— 不切号
        return UpstreamError(400, "上游拒绝了该请求（参数无效）", disable=False)
    return UpstreamError(502, "上游返回了未知错误", disable=False)


def extract_code(body):
    """从上游响应体里提取业务码（形如 {"code":1001} 或 {"Result":{"code":...}}）。"""
    body = (body or "").strip()
    if not body:
        return 0
    try:
        obj = json.loads(body)
    except Exception:
        obj = None
    if isinstance(obj, dict):
        value = number_field(obj, "code")
        if value != 0:
            return value
        result = obj.get("Result")
        if isinstance(result, dict):
            value = number_field(result, "code")
            if value != 0:
                return value
        data = obj.get("data")
        if isinstance(data, dict):
            value = number_field(data, "code")
            if value != 0:
                return value
    # 兜底：直接扫已知码（这些是具体四位数，误判概率极低）
    for code in (1001, 1005, 4001, 4008, 4011, 9074):
        if ('"code":%d' % code) in body:
            return code
    return 0


def is_code(err, code):
    return isinstance(err, UpstreamError) and err.code == code


def as_upstream_error(err):
    """把任意错误规整为 UpstreamError（非本类型时按 502 处理）。"""
    if err is None:
        return UpstreamError(502, "上游错误")
    if isinstance(err, UpstreamError):
        return err
    return UpstreamError(502, util.truncate_runes(str(err), 80))


class Upstream:
    """上游 HTTP 客户端。api 用于短 JSON 请求（带总超时），stream 用于流式对话。"""

    def __init__(self, eps):
        self.eps = eps

    # ---------- 请求头 ----------

    def client_ua(self):
        ua = (self.eps.user_agent or "").strip()
        if ua:
            return ua
        return "Trae/" + self.eps.ide_version

    def solo_headers(self, account, stream):
        """llm_utils_chat / get_detail_param 所需的 SOLO 专属头。

        指纹逐字段对齐官方客户端：「缺任何一项都可能被上游风控识别为非官方客户端」。
        referer 由调用方按实际请求 URL 设置（同源伪装）。
        """
        headers = {
            "Content-Type": "application/json",
            "Accept": "text/event-stream" if stream else "application/json",
            "Accept-Language": "zh-CN,zh;q=0.9",
            "User-Agent": self.client_ua(),
            "Authorization": "Cloud-IDE-JWT " + account.access_token,
            "X-Cloudide-Token": account.access_token,
            "X-Ide-Token": account.access_token,
            "X-App-Id": self.eps.app_id,
            "X-App-Version": "default",
            "App-Version": self.eps.ide_version,
            "X-Ide-Version": self.eps.ide_version,
            "X-Ide-Version-Code": self.eps.ide_version_code,
            "X-App-Version-Code": self.eps.ide_version_code,
            "X-Ide-Version-Type": "stable",
            "X-Device-Type": "windows",
            "X-Device-Cpu": "Intel",
            "X-OS-Version": self.eps.os_version,
            "X-Device-Brand": self.eps.device_brand,
            "Request-Traffic-Type": "prod",
            # X-Ss-Dp 由 TTNet 原生层注入（sscronet.dll），客户端 JS 与 ai-agent 都不发，
            # 这里保留以模拟原生层的出站形态。
            "X-Ss-Dp": "787976",
        }
        if account.uid:
            headers["X-Uid"] = account.uid
        if account.machine_id:
            headers["X-Machine-Id"] = account.machine_id
        if account.device_id:
            headers["X-Device-Id"] = account.device_id
        headers.update(self._trace_headers())
        return headers

    def _trace_headers(self):
        """AgentHub 同款的四个链路追踪头。"""
        tid = "00-" + random_hex(16) + "-" + random_hex(16) + "-01"
        return {
            "X-Tt-Trace-Id": tid,
            "X-Flow-Traceparent": "04-" + tid[3:35] + "-" + random_hex(16) + "-01",
            "X-Custom-Trace-Id": tid[3:19],
            "X-Request-Id": "req_" + random_hex(16),
        }

    def ug_headers(self, account):
        """签到 / 积分（api.trae.cn）所需的头。

        与官方客户端一致：统一用 Cloud-IDE-JWT 凭证。
        """
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": self.client_ua(),
            "Authorization": "Cloud-IDE-JWT " + account.access_token,
            "X-User-Region": "CN",
            "X-App-Type": "trae",
            "Origin": self.eps.console_host,
            "Referer": self.eps.console_host + "/",
        }
        if account.device_id:
            headers["X-Device-Id"] = account.device_id
        return headers

    def oauth_headers(self):
        """ExchangeToken / GetUserInfo 所需的头（无签名，仅 UA）。"""
        return {
            "Content-Type": "application/json",
            "Accept": "application/json",
            "User-Agent": self.client_ua(),
        }

    # ---------- 基础请求 ----------

    def do_json(self, method, url, headers, body=None, timeout=60):
        """发请求并返回 2xx 响应体（bytes）；非 2xx 时抛归类后的 UpstreamError。"""
        payload = body if body is not None else b"{}"
        status, raw = util_http_request(method, url, headers, payload, timeout=timeout)
        if status >= 400:
            raise classify_upstream(status, raw.decode("utf-8", "replace"))
        return raw

    # ---------- token ----------

    def refresh_token(self, account):
        """通过 ExchangeToken 强制刷新 access token（refreshToken 会轮换）。

        成功后更新 account 的字段；调用方负责落盘。
        """
        if not (account.refresh_token or "").strip():
            raise UpstreamError(401, "账号缺少 refreshToken，请重新登录", disable=True)
        host = (account.api_host or "").strip() or self.eps.oauth_host
        body = json.dumps({
            "ClientID": self.eps.client_id,
            "RefreshToken": account.refresh_token,
            "ClientSecret": "-",
            "UserID": "",
        }, ensure_ascii=False).encode("utf-8")
        data = self.do_json("POST", host + self.eps.ep_exchange, self.oauth_headers(), body)
        try:
            resp = json.loads(data.decode("utf-8"))
        except Exception:
            raise RuntimeError("解析 ExchangeToken 响应失败")
        result = resp.get("Result") if isinstance(resp, dict) else None
        result = result if isinstance(result, dict) else {}
        token = as_str(result.get("Token"))
        if not token.strip():
            raise UpstreamError(401, "账号登录态已失效，请重新登录", disable=True)
        account.access_token = token
        new_refresh = as_str(result.get("RefreshToken"))
        if new_refresh.strip():
            account.refresh_token = new_refresh
        expire_at = number_field(result, "TokenExpireAt")
        expire_duration = number_field(result, "TokenExpireDuration")
        if expire_at > 0:
            account.expires_at = util.normalize_expire_seconds(expire_at)
        elif expire_duration > 0:
            account.expires_at = int(time.time()) + expire_duration

    def ensure_token(self, account, store):
        """保证账号有可用的 accessToken：快过期（含已过期）时用 refreshToken 续期并落盘。"""
        if account.token_alive():
            return
        if not (account.refresh_token or "").strip():
            raise UpstreamError(401, "账号登录态已过期，请重新登录", disable=True)
        self.refresh_token(account)
        try:
            store.save_auth(account.to_auth())
        except Exception:
            pass

    def get_user_info(self, account):
        """查询账号资料（登录后补 uid / 昵称 / 企业 id）。"""
        host = (account.api_host or "").strip() or self.eps.oauth_host
        # GetUserInfo 与 ExchangeToken 同在授权域，用授权版本号
        body = json.dumps({"ReqSource": "IDE", "IDEVersion": self.eps.auth_app_version},
                          ensure_ascii=False).encode("utf-8")
        headers = self.oauth_headers()
        headers["X-Cloudide-Token"] = account.access_token
        data = self.do_json("POST", host + self.eps.ep_user_info, headers, body)
        try:
            resp = json.loads(data.decode("utf-8"))
        except Exception:
            raise RuntimeError("解析 GetUserInfo 响应失败")
        result = resp.get("Result") if isinstance(resp, dict) else None
        result = result if isinstance(result, dict) else {}
        return (as_str(result.get("UserID")), as_str(result.get("ScreenName")),
                as_str(result.get("EnterpriseID")))

    # ---------- 模型表 ----------

    def fetch_models(self, account):
        """拉 SOLO 模型表（get_detail_param）。返回 [(id, name)]。"""
        body = json.dumps({
            "function": self.eps.function,
            "config_names": None,
            "need_prompt": False,
            "current_config_info": None,
            "poly_prompt": True,
            "mode_type": None,
            "agent_type": None,
        }, ensure_ascii=False).encode("utf-8")
        url = self.eps.agent_host + self.eps.ep_models
        headers = self.solo_headers(account, False)
        headers["Referer"] = url
        data = self.do_json("POST", url, headers, body)
        try:
            resp = json.loads(data.decode("utf-8"))
        except Exception:
            raise RuntimeError("解析模型表失败")
        entries = resp.get("config_info_list") if isinstance(resp, dict) else None
        entries = entries if isinstance(entries, list) else []
        out = []
        for cfg in entries:
            if not isinstance(cfg, dict):
                continue
            name = as_str(cfg.get("config_name")).strip()
            if not name:
                continue
            display = cfg.get("display_config") if isinstance(cfg.get("display_config"), dict) else {}
            out.append((name, as_str(display.get("display_name"))))
        if not out:
            raise RuntimeError("上游模型表为空")
        return out

    # ---------- 签到 ----------

    def checkin_status(self, account):
        """查询签到状态。

        对齐官方客户端：POST + Cloud-IDE-JWT + {"req_source":1}。
        （GET ?did=<deviceId> + Bearer 的旧口径实测恒定返回 code 1001，已移除。）
        注意：签到语境下的 code 1001 表示「签到服务未对该账号开放」，不是登录态失效。
        """
        url = self.eps.ug_host + self.eps.ep_checkin_status
        return self._parse_checkin_status(url, "POST", self.ug_headers(account), _REQ_SOURCE_BODY)

    def _parse_checkin_status(self, url, method, headers, body):
        state = CheckinState()
        try:
            data = self.do_json(method, url, headers, body)
        except UpstreamError as err:
            if err.code == 1001:
                state.unavailable = True
                return state, None
            return state, err
        try:
            resp = json.loads(data.decode("utf-8"))
        except Exception:
            resp = {}
        if not isinstance(resp, dict):
            resp = {}
        code = number_field(resp, "code")
        # 上游既用 code 0 也用 code 200 表示成功
        if code not in (0, 200):
            if code == 1001:
                state.unavailable = True
                return state, None
            mapped = classify_code(code)
            if mapped is not None:
                return state, mapped
            return state, UpstreamError(502, "签到状态查询失败", code=code)
        state.checked_in = bool(resp.get("checked_in")) or bool(resp.get("checkedIn"))
        state.enable = bool(resp.get("enable"))
        state.streak_days = number_field(resp, "consecutive_days")
        state.credits_earned_today = to_float(resp.get("credits_earned_today"))
        return state, None

    def checkin_claim(self, account):
        """执行签到。请求体与官方客户端一致（{"req_source":1}），不做任何重试。"""
        out = CheckinClaimResult()
        url = self.eps.ug_host + self.eps.ep_checkin_claim
        try:
            data = self.do_json("POST", url, self.ug_headers(account), _REQ_SOURCE_BODY)
        except UpstreamError as err:
            if err.code == 1001:
                out.unavailable = True
                return out, None
            return out, err
        try:
            resp = json.loads(data.decode("utf-8"))
        except Exception:
            resp = {}
        if not isinstance(resp, dict):
            resp = {}
        code = number_field(resp, "code")
        out.message = (as_str(resp.get("message")) or "").strip()
        if not out.message:
            out.message = (as_str(resp.get("msg")) or "").strip()
        if code == 1001:
            out.unavailable = True
            return out, None
        # 9095：该设备今日已签到，属幂等成功
        if code == _CODE_ALREADY_CHECKED_IN:
            out.already = True
            return out, None
        if code in (0, 200):
            out.already = bool(_CHECKED_IN_RE.search(out.message))
            return out, None
        mapped = classify_code(code)
        if mapped is not None:
            return out, mapped
        msg = out.message or "签到失败"
        return out, UpstreamError(502, util.truncate_runes(msg, 100), code=code)

    # ---------- 额度 ----------

    def ent_usage_info(self, account):
        """查账号额度明细，返回 (未过期剩余积分之和, 套餐名)。

        逐包规则：隐藏包 / 已取消（status=3）/ 兑换码包（product_type=3）跳过；
        credits_limit 缺失或为 0 的包不属于该账号；credits_limit < 0 是不限量哨兵（整体返回 -1）。
        套餐名按 product_type 取「当前生效包」里的最高档位（含免费包，它没有 credits_limit）。
        """
        url = self.eps.ug_host + self.eps.ep_ent_usage
        try:
            data = self.do_json("POST", url, self.ug_headers(account), b"{}")
        except UpstreamError as err:
            # pay/ug 域对部分账号（scope 限制）整体拒绝并返回 code 1001：
            # 这是「积分服务不开放」，不是凭证失效。
            if err.code == 1001:
                raise UpstreamError(403, "该账号未开放积分服务（上游 scope 限制）", code=1001)
            raise
        try:
            doc = json.loads(data.decode("utf-8"))
        except Exception:
            raise UpstreamError(502, "额度查询响应无法解析")
        packs, _ = dig_value(doc, "user_entitlement_pack_list")
        if not isinstance(packs, list) or not packs:
            raise UpstreamError(502, "上游未返回额度信息")

        now = int(time.time())
        total = 0.0
        unlimited = False
        valid = 0
        plan_type = -1
        for item in packs:
            if not isinstance(item, dict):
                continue
            if dig_bool(item, "is_hide"):
                continue
            status, ok = dig_number(item, "status")
            if ok and int(status) == 3:
                continue
            product_type, ok = dig_number(item, "product_type")
            ptype = int(product_type) if ok else 0
            if ptype == 3:
                continue
            # 到期时间可能以秒或毫秒给出；取不到视为长期有效
            expired = False
            for key in ("end_time", "expire_time", "expire_at", "deadline", "valid_until"):
                ms, ok = dig_number(item, key)
                if ok and ms > 0:
                    if ms < 1e12:  # 秒 → 毫秒
                        ms *= 1000
                    expired = ms / 1000 <= now
                    break
            if expired:
                continue
            # 套餐档位与额度分开判定：免费包只给功能开关，没有 credits_limit
            if pack_rank(ptype) > pack_rank(plan_type):
                plan_type = ptype
            limit, ok = dig_number(item, "credits_limit")
            if not ok or limit == 0:
                continue
            valid += 1
            if limit < 0:
                unlimited = True
                continue
            used, _ = dig_number(item, "credits_amount")
            remain = limit - used
            if remain > 0:
                total += remain
        if valid == 0 and plan_type < 0:
            raise UpstreamError(502, "上游未返回有效额度包")
        plan = PACK_PLAN_NAMES.get(plan_type, "")
        if unlimited:
            return -1.0, plan
        return total, plan

    def ent_usage_remain(self, account):
        """兼容旧调用：只取剩余积分。"""
        return self.ent_usage_info(account)[0]

    def extract_ide_credits(self, doc):
        """从对话流的 notify_usage 里提取 SOLO 通道专属的 ide_credits。

        刻意**不**兜底到通用 credits 字段（历史上正是这个兜底让面板把积分显示成 100）。
        """
        if not isinstance(doc, dict) or not doc:
            return 0.0, ""
        value, ok = util.deep_find_number(doc, "ide_credits")
        if ok:
            return value, "ide_credits"
        value, ok = util.deep_find_number(doc, "ideCredits")
        if ok:
            return value, "ide_credits"
        return 0.0, ""


class CheckinState:
    __slots__ = ("checked_in", "enable", "unavailable", "streak_days", "credits_earned_today")

    def __init__(self):
        self.checked_in = False
        self.enable = False
        self.unavailable = False
        self.streak_days = 0
        self.credits_earned_today = 0.0


class CheckinClaimResult:
    __slots__ = ("already", "unavailable", "message")

    def __init__(self):
        self.already = False
        self.unavailable = False
        self.message = ""


# ---------- 登录 URL 与回调解析 ----------


def build_login_url(eps, machine_id, device_id, callback_url):
    """构造 TRAE 登录 URL。machine_id / device_id 必须与落盘凭证共用同一对。"""
    from urllib.parse import quote

    base_params = {
        "login_version": "1",
        "auth_from": "solo",
        "login_channel": "native_ide",
        "plugin_version": "2.3.62834",
        "auth_type": "local",
        "client_id": eps.client_id,
        "redirect": "0",
    }
    parts = [eps.console_host + "/authorization?" + urlencode(base_params)]
    parts.append("&login_trace_id=" + quote(machine_trace_id(machine_id, device_id), safe=""))
    parts.append("&auth_callback_url=" + quote(callback_url, safe=""))
    parts.append("&machine_id=" + quote(machine_id, safe=""))
    parts.append("&device_id=" + quote(device_id, safe=""))
    parts.append("&x_device_id=" + quote(device_id, safe=""))
    parts.append("&x_machine_id=" + quote(machine_id, safe=""))
    parts.append("&x_device_brand=PC")
    parts.append("&x_device_type=PC")
    parts.append("&x_os_version=1.0")
    parts.append("&x_app_version=" + quote(eps.auth_app_version, safe=""))
    parts.append("&x_app_type=stable")
    return "".join(parts)


def machine_trace_id(machine_id, device_id):
    """取 (machineID+deviceID) 的后 16 个 hex 字符作为 login_trace_id。"""
    joined = machine_id + device_id
    if len(joined) >= 16:
        return joined[-16:]
    return "0" * (16 - len(joined)) + joined


class CallbackInfo:
    __slots__ = ("refresh_token", "access_token", "uid", "nickname", "enterprise_id", "expires_at")

    def __init__(self):
        self.refresh_token = ""
        self.access_token = ""
        self.uid = ""
        self.nickname = ""
        self.enterprise_id = ""
        self.expires_at = 0


def parse_callback(raw_url):
    """解析 TRAE 登录回调链接，提取凭证字段。"""
    from urllib.parse import parse_qs, urlparse

    raw_url = (raw_url or "").strip()
    if not raw_url:
        raise RuntimeError("回调链接为空")
    try:
        parsed = urlparse(raw_url)
    except Exception:
        raise RuntimeError("回调链接无法解析")
    query = parse_qs(parsed.query, keep_blank_values=True)

    def one(key):
        values = query.get(key)
        return values[0] if values else ""

    info = CallbackInfo()
    info.refresh_token = one("refreshToken")

    user_info = util.parse_json_param(one("userInfo")) or {}
    info.uid = as_str(user_info.get("UserID"))
    info.nickname = util.fix_mojibake(as_str(user_info.get("ScreenName")), info.uid)
    info.enterprise_id = as_str(user_info.get("TenantID"))

    user_jwt = util.parse_json_param(one("userJwt")) or {}
    jwt_token = as_str(user_jwt.get("Token"))
    if not info.refresh_token:
        info.refresh_token = as_str(user_jwt.get("RefreshToken"))
    if not info.refresh_token:
        info.access_token = jwt_token
        if not info.access_token:
            raise RuntimeError("回调链接缺少 refreshToken，且 userJwt 也没有 Token")
        exp = number_field(user_jwt, "TokenExpireAt")
        if exp > 0:
            info.expires_at = util.normalize_expire_seconds(exp)
    return info


# ---------- HTTP 传输（独立出来便于测试替换） ----------


def util_http_request(method, url, headers, payload, timeout=60):
    """发一次 HTTP 请求，返回 (status, body_bytes)。

    用 http.client 而非 urllib，便于流式读取与精确控制超时。
    """
    import http.client

    from urllib.parse import urlsplit

    parts = urlsplit(url)
    if parts.scheme not in ("http", "https"):
        raise RuntimeError("不支持的 URL: %s" % url)
    host = parts.hostname or ""
    port = parts.port or (443 if parts.scheme == "https" else 80)
    path = parts.path or "/"
    if parts.query:
        path += "?" + parts.query
    cls = http.client.HTTPSConnection if parts.scheme == "https" else http.client.HTTPConnection
    conn = cls(host, port, timeout=timeout)
    try:
        conn.request(method, path, body=payload, headers=headers)
        resp = conn.getresponse()
        body = resp.read()
        return resp.status, body
    finally:
        try:
            conn.close()
        except Exception:
            pass
