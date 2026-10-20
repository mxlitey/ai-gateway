# -*- coding: utf-8 -*-
"""上游主机与接口路径（可在插件目录下的 config.json 里覆盖）。

魔搭没有「客户端」，对话面是官方 OpenAI 兼容 API，控制面是 OAuth 2.0 + OIDC
与 www.modelscope.cn 的 openapi。所有路径集中在这里：上游一改，只改配置、不动代码。
覆盖规则：只覆盖用户显式写过（非空字符串 / 非空数组）的字段，其余保持默认。
"""

import json
import os
from dataclasses import dataclass, fields


@dataclass
class Endpoints:
    # ===== 对话面（官方 OpenAI 兼容）=====
    api_base: str = "https://api-inference.modelscope.cn"
    chat_path: str = "/v1/chat/completions"
    models_path: str = "/v1/models"

    # ===== 站点面（OAuth / openapi / 魔粒）=====
    web_base: str = "https://www.modelscope.cn"
    oauth_authorize: str = "/oauth/authorize"
    oauth_token: str = "/oauth/token"
    oauth_register: str = "/oauth/register"
    oauth_userinfo: str = "/oauth/userinfo"
    users_me: str = "/openapi/v1/users/me"
    balance: str = "/openapi/v1/magicubes/balance"
    earn_rules: str = "/openapi/v1/magicubes/earn/rules"
    login_info: str = "/api/v1/users/login/info"
    mcp_servers: str = "/api/v1/dolphin/mcpServers"
    star_prefix: str = "/api/v1/mcpServers"

    # ===== OAuth 参数 =====
    # api-inference 是调用推理的授权；openid 必选，profile 用于取昵称
    oauth_scopes: str = "openid profile api-inference"

    # ===== 魔粒每日任务 =====
    # 规则键为上游实测值，不要臆造
    rule_daily: str = "daily_active"
    rule_aliyun_bind: str = "aliyun_bindlogin"
    rule_like: str = "interaction_like"
    like_hard_cap: int = 25          # 单日点赞安全阀（上游 daily_cap 异常放大时兜底）
    like_run_cap: int = 20           # 单次签到最多点多少个（其余留给下一轮）
    like_budget_seconds: float = 12  # 单次签到的点赞时间预算，保证宿主 30s 超时不被打穿
    like_delay_min_ms: int = 400
    like_delay_max_ms: int = 900

    # ===== 请求头 / 超时 =====
    # 控制面必须带浏览器上下文，否则风控中间件会静默忽略（AgentHub 实测）
    user_agent: str = (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
    )
    referer_path: str = "/my/overview"
    # Web 会话触碰（触发 daily_active 的登录事件）
    touch_paths: tuple = ("/my/overview", "/", "/models", "/my/tasks")
    json_timeout: int = 60
    chat_timeout: int = 300
    # Cookie 失效判定（上游对未登录返回的业务码 / 文案）
    cookie_dead_re: str = "InvalidAuthentication|not logged in|登录已过期|禁止访问|authentication required"

    # ===== 其它 =====
    # 拉不到模型目录时的兜底清单（只用于展示，不用于可用性判定）
    fallback_models: tuple = (
        "deepseek-ai/DeepSeek-V4.1-Flash",
        "deepseek-ai/DeepSeek-V4-Pro",
        "Qwen/Qwen3.8-Flash-Next",
        "Qwen/Qwen3.5-397B-A17B",
        "Qwen/Qwen3.5-122B-A10B",
        "ZhipuAI/GLM-5.2",
        "ZhipuAI/GLM-4.7-Flash",
        "MiniMax/MiniMax-M3",
        "stepfun-ai/Step-3.7-Flash",
        "stepfun-ai/Step-3.5-Flash",
    )


_TEXT_FIELDS = {f.name for f in fields(Endpoints)}


def plugin_dir():
    return os.path.dirname(os.path.abspath(__file__))


def default_config_path():
    return os.path.join(plugin_dir(), "config.json")


def default_data_dir():
    value = (os.environ.get("AI_GATEWAY_PLUGIN_DATA") or "").strip()
    if value:
        return value
    return os.path.join(plugin_dir(), "data")


def load_endpoints(path):
    """读 config.json，只覆盖用户显式写过的字段；文件不存在时直接用默认值。"""
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
    for name in _TEXT_FIELDS:
        value = user.get(name)
        if isinstance(value, str) and value.strip():
            setattr(eps, name, value.strip())
        elif isinstance(value, list) and value:
            setattr(eps, name, tuple(str(v) for v in value if str(v).strip()))
        elif isinstance(value, int) and not isinstance(value, bool) and value > 0:
            setattr(eps, name, value)
        elif isinstance(value, float) and value > 0:
            setattr(eps, name, value)
    return eps


def env_int(name, fallback):
    value = (os.environ.get(name) or "").strip()
    if not value:
        return fallback
    try:
        num = int(value)
    except ValueError:
        return fallback
    return num if num > 0 else fallback
