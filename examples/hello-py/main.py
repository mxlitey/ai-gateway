#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""最小 Python 插件示例：只用标准库，同一份文件可在 Windows / macOS / Linux 上运行。

约定（见 docs/plugin-spec.md）：
  - 监听 127.0.0.1:<AI_GATEWAY_PLUGIN_PORT>
  - GET  /health                 探活：<500 即视为存活，只反映插件进程自身
  - POST /v1/chat/completions    转发目标（这里回显输入，演示协议形状）
  - GET  /v1/models              可选：供面板「获取上游模型」

用法：
  python main.py                # 读环境变量 AI_GATEWAY_PLUGIN_PORT
  python main.py --port 20801   # 直连调试
"""

import argparse
import json
import os
import signal
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PLUGIN_VERSION = "1.0.0"


def log(msg: str) -> None:
    # 宿主会把 stdout/stderr 汇入网关日志区（带 [plugin:<id>] 前缀）
    print(msg, flush=True)


def port_from_env(default: int = 20801) -> int:
    try:
        n = int(os.environ.get("AI_GATEWAY_PLUGIN_PORT", "") or default)
        return n if 0 < n < 65536 else default
    except ValueError:
        return default


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args) -> None:  # noqa: A003
        log("%s - %s" % (self.address_string(), fmt % args))

    # ---- 工具 ----
    def send_json(self, status: int, body: dict) -> None:
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def read_json(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return {}
        return json.loads(self.rfile.read(length).decode("utf-8"))

    # ---- 路由 ----
    def do_GET(self) -> None:  # noqa: N802
        if self.path.startswith("/health"):
            self.send_json(200, {"status": "ok", "version": PLUGIN_VERSION})
        elif self.path.startswith("/v1/models"):
            self.send_json(200, {"data": [{"id": "hello-py"}]})
        else:
            self.send_json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if not self.path.startswith("/v1/chat/completions"):
            self.send_json(404, {"error": "not found"})
            return
        try:
            req = self.read_json()
        except ValueError:
            self.send_json(400, {"error": "请求体不是合法 JSON"})
            return

        # 网关按账号调度：X-Plugin-Account = 渠道密钥 = 账号 id（无控制台时为 gw-local）
        account = self.headers.get("X-Plugin-Account", "gw-local")
        messages = req.get("messages") or []
        last = messages[-1].get("content", "") if messages else ""
        if isinstance(last, list):
            last = "".join(part.get("text", "") for part in last if isinstance(part, dict))

        body = {
            "id": "chatcmpl-hello-py",
            "object": "chat.completion",
            "created": 0,
            "model": req.get("model", "hello-py"),
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": "Hello from %s: %s" % (account, last)},
                    "finish_reason": "stop",
                }
            ],
            "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
        }
        self.send_json(200, body)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=0)
    args = parser.parse_args()

    port = args.port or port_from_env()
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)

    def stop(_signum, _frame):
        log("收到停止信号，正在退出")
        server.shutdown()

    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            signal.signal(sig, stop)
        except (AttributeError, ValueError):
            pass
    if hasattr(signal, "SIGBREAK"):
        signal.signal(signal.SIGBREAK, stop)

    log("监听 http://127.0.0.1:%d（Python %s）" % (port, sys.version.split()[0]))
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
