# -*- coding: utf-8 -*-
"""Loomy（讯飞）上游接口模板：端点、讯飞 CAccount 凭据与兜底模型表。

全部字段均可在插件目录下的 config.json 里覆盖：上游一改，只改配置、不动代码。

⚠️ `access_key_id` / `access_key_secret` / `app_id` 只是讯飞**账号端点**的签名材料
（短信验证码登录），随客户端分发、本质是公开值；业务与推理用的是登录后下发的
`session`，与它们无关。
"""

import json
import os
from dataclasses import dataclass, field, fields

# 兜底目录（远端 /models 不可用时顶替）。名称里的倍率会被展示函数拆出来。
_FALLBACK_MODELS = [
    "deepseek-v4-flash-0731",
    "MiniMax-M3",
    "Kimi-k2.6",
    "qwen-3.8-max",
    "GLM-5.3-Flash",
    "qwen3.8-flash",
    "spark-x",
    "mimo-v2.5",
]


@dataclass
class Endpoints:
    # 业务端点（推理 / 模型 / 积分）
    api_base: str = "https://loomyad.xunfei.cn/api/v1"
    # 讯飞账号（CAccount）
    account_base: str = "https://account.xfinfr.com"

    # 短信登录
    send_msg_code: str = "/login/phone/sendMsgCode"
    check_code: str = "/login/phone/checkCode"

    # 业务端点
    chat_completions: str = "/chat/completions"
    models: str = "/models"
    points_records: str = "/points/records?pageNo=1&pageSize=1&recordType=all"
    points_claim: str = "/points/first-login"

    # 讯飞账号签名材料
    access_key_id: str = "2thryby66wxi53sk"
    access_key_secret: str = "zsak6eadrbawz683wf5r3m2snrwj868r"
    app_id: str = "GM3LOOMY"
    client_ua: str = "Loomy|Desktop|Electron|macOS"

    # 会话有效期（服务端只接受登录时声明的 expire，响应不带到期时间）
    session_ttl_seconds: int = 1209600

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
