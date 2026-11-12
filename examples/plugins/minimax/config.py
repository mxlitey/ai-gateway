# -*- coding: utf-8 -*-
"""MiniMax Code 上游接口模板：端点、OAuth 客户端与兜底模型表。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。
"""

import json
import os
from dataclasses import dataclass, field, fields

# 兜底目录（远端不可用时顶替）。⚠️ 上下文窗口取**档位表最大档**：
# M3.1-Flash-Preview 的 limit.context 是 512K，但可选窗口到 1M。
_FALLBACK_MODELS = [
    "MiniMax-M3.1-Flash-Preview",
    "MiniMax-M3",
    "MiniMax-M2.7-highspeed",
    "MiniMax-M2.7",
]


@dataclass
class Endpoints:
    # 主机
    account_host: str = "https://account.minimax.cn"
    api_host: str = "https://agent.minimax.cn"

    # OAuth
    device_code: str = "/oauth2/device/code"
    token: str = "/oauth2/token"
    client_id: str = "mcode-public"
    audience: str = "agent-backend"
    scope: str = "agent.default"

    # 目录 / 积分
    models: str = "/mavis/api/v1/models"
    region: str = "cn"
    build_env: str = "prod"
    signin_status: str = "/minimax-cloud/api/v1/signin/status"
    signin_claim: str = "/minimax-cloud/api/v1/signin/claim"
    credit_details: str = "/minimax-cloud/api/v1/credit/details"

    # 推理（Anthropic Messages）
    messages: str = "/mavis/api/v1/llm/v1/messages"

    # 轮询
    poll_interval_seconds: float = 5.0
    login_timeout_seconds: int = 300

    # 默认输出上限（Anthropic 协议必填 max_tokens）
    default_max_tokens: int = 8192

    fallback_models: list = field(default_factory=lambda: list(_FALLBACK_MODELS))


def load_endpoints(path):
    """读 config.json，只覆盖用户显式写过的字段。文件不存在时直接用默认值。"""
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
