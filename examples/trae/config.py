# -*- coding: utf-8 -*-
"""TRAE 上游的主机与接口路径模板（对应 Go 版 config.go）。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。
覆盖规则：只覆盖用户显式写过（非空字符串）的字段，其余保持默认。
"""

import json
import os
from dataclasses import dataclass, fields


@dataclass
class Endpoints:
    # 主机
    agent_host: str = "https://trae-api-cn.mchost.guru"  # 对话 / 模型表
    ug_host: str = "https://api.trae.cn"                 # 签到 / 积分
    oauth_host: str = "https://api.trae.com.cn"          # ExchangeToken / GetUserInfo
    console_host: str = "https://www.trae.cn"            # 网页登录授权页

    # 客户端指纹（复刻官方客户端）
    client_id: str = "en1oxy7wnw8j9n"
    app_id: str = "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8"
    # user_agent 是对话/模型请求的官方客户端指纹；旧值 "Trae/<IdeVersion>" 会被风控识别为非官方客户端。
    user_agent: str = "TraeClient/TTNet"
    # auth_app_version 是登录授权页（x_app_version）与 GetUserInfo 用的版本号。
    # 注意：与 ide_version 不是一套，别混用。
    auth_app_version: str = "3.5.66"
    ide_version: str = "0.1.50"
    ide_version_code: str = "20260811"
    device_brand: str = "CREFG-XX"
    os_version: str = "Windows 11 Home China"
    function: str = "solo_work_lite"
    # chat_mirror 是可选的对话镜像域**完整 URL**；留空则只用主域。
    chat_mirror: str = ""

    # 签到 / 积分（ug 族）的专属画像。这一族与对话那套**不是同一套头**：
    # 上游认的是 VSCode 插件进程的 UA 与 Package-Type，照搬对话那套会回
    # 401 + code 1001「not able to authenticate you」，看着像登录态失效。
    ug_user_agent: str = "VSCode 1.107.1 (TRAE CN)"
    ug_market_client_id: str = "VSCode 1.107.1"
    ug_package_type: str = "stable_cn"

    # 接口路径
    ep_chat: str = "/api/agent/v3/llm_utils_chat"
    ep_models: str = "/api/ide/v1/get_detail_param"
    ep_exchange: str = "/cloudide/api/v3/trae/oauth/ExchangeToken"
    ep_user_info: str = "/cloudide/api/v3/trae/GetUserInfo"
    ep_checkin_status: str = "/trae/api/v2/ug/checkin_credits/status"
    ep_checkin_claim: str = "/trae/api/v2/ug/checkin_credits/claim"
    ep_ent_usage: str = "/trae/api/v2/pay/ide_user_ent_usage"


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
        raise RuntimeError("解析接口配置 %s 失败: 顶层不是 JSON 对象" % path)
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


def default_model():
    """默认模型：TRAE_DEFAULT_MODEL 优先，否则 glm-5.2。"""
    value = (os.environ.get("TRAE_DEFAULT_MODEL") or "").strip()
    if value:
        return value
    return "glm-5.2"


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
