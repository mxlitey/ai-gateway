# -*- coding: utf-8 -*-
"""Cline 上游接口模板：端点、客户端标识与兜底模型表。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。
覆盖规则：只覆盖用户显式写过（非空字符串 / 非空数组）的字段，其余保持默认。
"""

import json
import os
from dataclasses import dataclass, field, fields

# 免费模型是**独立 id**：`cline-free/...`（免费）与同名不计前缀的付费条目是两个模型。
# 远端 `recommended-models` 的 free 数组才是权威来源；本表只在远端不可用时顶替。
_FALLBACK_MODELS = [
    "stealth/space-bunny-alpha",
    "cline-free/mimo-v2.6-flash",
    "cline-free/muse-spark-1.3-contributor",
]


@dataclass
class Endpoints:
    # 站点
    api_base: str = "https://api.cline.bot"
    app_base: str = "https://app.cline.bot"
    workos_base: str = "https://api.workos.com"

    # WorkOS 设备码
    device_authorize: str = "/user_management/authorize/device"
    device_authenticate: str = "/user_management/authenticate"
    workos_client_id: str = "client_01K3A541FN8TA3EPPHTD2325AR"

    # Cline 自有令牌
    register: str = "/api/v1/auth/register"
    refresh: str = "/api/v1/auth/refresh"
    me: str = "/api/v1/users/me"
    balance: str = "/api/v1/users/%s/balance"  # %s = 账号 id（usr-...）

    # 推理与目录
    chat_completions: str = "/api/v1/chat/completions"
    models: str = "/api/v1/models"
    recommended_models: str = "/api/v1/ai/cline/recommended-models"

    # 访问令牌前缀（不可剥：剥掉即 401）
    token_prefix: str = "workos:"

    # 官方客户端标识头
    http_referer: str = "https://cline.bot"
    x_title: str = "Cline"
    x_is_multiroot: str = "false"
    x_client_type: str = "cline-sdk"

    # 设备码轮询
    poll_interval_seconds: float = 5.0
    login_timeout_seconds: int = 300

    # 余额折算系数（上游返回 micro-USD）
    balance_scale: float = 100000.0

    # 远端目录不可用时的兜底清单
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
