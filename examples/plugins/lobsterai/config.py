# -*- coding: utf-8 -*-
"""LobsterAI（有道龙虾）上游接口模板。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。
"""

import json
import os
from dataclasses import dataclass, field, fields

_FALLBACK_MODELS = [
    "deepseek-v4-flash", "deepseek-v4-pro", "MiniMax-M3", "MiniMax-M2.7",
    "qwen3.7-max", "qwen3.7-plus", "qwen3.6-plus", "qwen3.5-plus-2026-04-20",
    "kimi-k2.7-code", "kimi-k2.7-code-highspeed", "kimi-k2.6", "kimi-k2.5",
    "doubao-seed-2-1-pro-260628", "doubao-seed-2-1-turbo-260628",
    "doubao-seed-2-0-code-preview-260215",
    "glm-5.2", "glm-5.1", "glm-5v-turbo", "glm-5",
]


@dataclass
class Endpoints:
    # 站点
    api_base: str = "https://lobsterai-server.youdao.com"
    portal_base: str = "https://lobsterai.youdao.com"
    client_version_api: str = (
        "https://api-overmind.youdao.com/openapi/get/luna/hardware/lobsterai/prod/update")
    fallback_client_version: str = "2026.9.4"

    # 端点
    exchange: str = "/api/auth/exchange"
    refresh: str = "/api/auth/refresh"
    chat_completions: str = "/api/proxy/v1/chat/completions"
    models: str = "/api/models/available"
    profile_summary: str = "/api/user/profile-summary"
    callback_path: str = "/auth/callback"

    # 客户端身份
    user_agent: str = "LobsterAI/0.1.0"
    # 两个能力都必须声明：少了它模型清单会少 kimi-k3，且「关闭思考」会 500
    client_capabilities: str = "kimi-k3-agentic-v1,thinking-level-control-v1"

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
