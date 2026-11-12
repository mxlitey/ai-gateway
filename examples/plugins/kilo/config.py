# -*- coding: utf-8 -*-
"""上游主机与可调参数（可在插件目录下的 config.json 里覆盖）。"""

import json
import os
from dataclasses import dataclass, fields


@dataclass
class Endpoints:
    base: str = "https://api.kilo.ai/api/gateway"
    chat_path: str = "/chat/completions"
    models_path: str = "/models"

    # 只把免费池的模型广播给面板：isFree=true 的条目 + 自动路由 kilo-auto/free
    free_only: bool = True
    auto_free_id: str = "kilo-auto/free"

    user_agent: str = "ai-gateway-kilo-plugin/1.0"
    listing_timeout: int = 15
    chat_timeout: int = 300

    # 清单缓存：模型集合与能力跟随上游刷新，不在插件里保存静态快照
    listing_ttl: int = 600
    listing_fail_ttl: int = 60
    listing_fail_models: tuple = ("kilo-auto/free",)


def plugin_dir():
    return os.path.dirname(os.path.abspath(__file__))


def default_config_path():
    return os.path.join(plugin_dir(), "config.json")


def load_endpoints(path):
    eps = Endpoints()
    if not (path or "").strip():
        return eps
    try:
        with open(path, "rb") as fh:
            user = json.loads(fh.read().decode("utf-8"))
    except FileNotFoundError:
        return eps
    except Exception as exc:
        raise RuntimeError("解析接口配置 %s 失败: %s" % (path, exc))
    if not isinstance(user, dict):
        raise RuntimeError("接口配置顶层必须是 JSON 对象")
    for f in fields(Endpoints):
        value = user.get(f.name)
        if isinstance(value, bool):
            setattr(eps, f.name, value)
        elif isinstance(value, str) and value.strip():
            setattr(eps, f.name, value.strip())
        elif isinstance(value, int) and value > 0:
            setattr(eps, f.name, value)
        elif isinstance(value, list) and value:
            setattr(eps, f.name, tuple(str(v) for v in value))
    return eps


def env_int(name, fallback):
    try:
        value = int((os.environ.get(name) or "").strip())
    except ValueError:
        return fallback
    return value if value > 0 else fallback
