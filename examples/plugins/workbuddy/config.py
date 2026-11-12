# -*- coding: utf-8 -*-
"""WorkBuddy 上游接口模板（对应 Go 版 upstream.go 里的 Endpoints 与 main.go 的加载逻辑）。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。
覆盖规则：只覆盖用户显式写过（非空字符串）的字段，其余保持默认。
"""

import json
import os
from dataclasses import dataclass, fields


@dataclass
class Endpoints:
    # 站点出口
    intl_chat_origin: str = "https://www.workbuddy.ai"
    intl_billing_origin: str = "https://www.workbuddy.ai"
    cn_chat_origin: str = "https://copilot.tencent.com"
    cn_billing_origin: str = "https://www.codebuddy.cn"

    # OAuth
    auth_state: str = "/v2/plugin/auth/state"
    auth_token: str = "/v2/plugin/auth/token"
    login_account: str = "/v2/plugin/login/account"
    token_refresh: str = "/v2/plugin/auth/token/refresh"

    # 账单 / 签到
    checkin: str = "/v2/billing/meter/daily-checkin"
    checkin_status: str = "/v2/billing/meter/checkin-activity-status"
    resource_summary: str = "/billing/meter/get-user-resource-summary"
    resource_free: str = "/billing/meter/get-user-resource-free-packages"
    resource_paid: str = "/billing/meter/get-user-resource-paid-packages"
    enterprise_usage: str = "/billing/meter/get-enterprise-user-usage"

    # 对话
    chat_completions: str = "/v2/chat/completions"
    # 可用模型清单在产品配置里：data.agents[].models（实测 /v2/models 不存在）
    models: str = "/v3/config"


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
    return eps


def plugin_dir():
    """插件目录（等价 Go 版的 filepath.Dir(os.Executable())）。"""
    return os.path.dirname(os.path.abspath(__file__))


def default_data_dir():
    """插件数据目录：环境变量优先，否则 <插件目录>/data。"""
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
