# -*- coding: utf-8 -*-
"""Gemini（Google Code Assist）上游接口模板。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。

⚠️ 登录方式说明：Google 的原生客户端走 **loopback 回调**
（`http://localhost:<端口>/oauth-callback`，任意端口合法）。插件规范禁止插件
自行监听额外端口，而网关统一回调地址是 https 域名、不匹配 Google 的
「原生应用 loopback」白名单，因此本插件采用**粘贴 refresh_token** 的登录方式
（OAuth 换取令牌那一步由用户在 Google 侧或其它工具完成）。
"""

import json
import os
from dataclasses import dataclass, field, fields

_FALLBACK_MODELS = ["gemini-3.8-flash"]


@dataclass
class Endpoints:
    # 授权与令牌（仅用于续期；换取令牌由用户完成）
    token_host: str = "https://oauth2.googleapis.com"
    userinfo: str = "https://www.googleapis.com/oauth2/v2/userinfo"
    client_id: str = (
        "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com")
    client_secret: str = "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"

    # Cloud Code Assist
    endpoint_daily: str = "https://daily-cloudcode-pa.googleapis.com"
    endpoint_sandbox: str = "https://daily-cloudcode-pa.sandbox.googleapis.com"
    stream_path: str = "/v1internal:streamGenerateContent?alt=sse"
    generate_path: str = "/v1internal:generateContent"
    load_code_assist: str = "/v1internal:loadCodeAssist"
    quota_path: str = "/v1internal:retrieveUserQuotaSummary"

    # 身份头（逐字写死，不许随机）
    user_agent: str = "antigravity/4.3.0 (cmdc-pak)"
    client_name: str = "antigravity"
    client_version: str = "4.3.0"
    machine_id: str = "cmdc-pak"
    vscode_sessionid: str = "proxy"
    upstream_user_agent: str = "antigravity"

    # 项目号兜底（免费档 loadCodeAssist 返回空 project 时用）
    default_project: str = "aicode-consumers"

    # 默认输出上限
    max_output_tokens: int = 64000

    fallback_models: list = field(default_factory=lambda: list(_FALLBACK_MODELS))


def load_endpoints(path):
    eps = Endpoints()
    if not (path or "").strip():
        return eps
    try:
        with open(path, "rb") as fh:
            raw = fh.read()
    except FileNotFoundError:
        return eps
    except OSError as exc:
        raise RuntimeError("读取接口配置失败: %s" % exc)
    try:
        user = json.loads(raw.decode("utf-8"))
    except Exception as exc:
        raise RuntimeError("解析接口配置 %s 失败: %s" % (path, exc))
    if not isinstance(user, dict):
        raise RuntimeError("解析接口配置 %s 失败：顶层不是 JSON 对象" % path)
    for f in fields(Endpoints):
        value = user.get(f.name)
        if isinstance(value, str) and value.strip():
            setattr(eps, f.name, value)
        elif isinstance(value, list) and value:
            setattr(eps, f.name, [v for v in value if isinstance(v, str) and v.strip()])
        elif isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0:
            setattr(eps, f.name, value)
    return eps


def plugin_dir():
    return os.path.dirname(os.path.abspath(__file__))


def default_data_dir():
    value = (os.environ.get("AI_GATEWAY_PLUGIN_DATA") or "").strip()
    if value:
        return value
    return os.path.join(plugin_dir(), "data")


def default_config_path():
    return os.path.join(plugin_dir(), "config.json")


def env_int(name, fallback):
    value = (os.environ.get(name) or "").strip()
    if value:
        try:
            num = int(value)
        except ValueError:
            return fallback
        if num > 0:
            return num
    return fallback
