#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""LobsterAI（有道龙虾）插件：把 LobsterAI 账号接进 AI Gateway。

能力：
  - OpenAI 兼容上游：GET /health、POST /v1/chat/completions、GET /v1/models
  - 控制台：授权登录（回调落在插件同一端口的 /auth/callback）/ 删除账号 / 额度刷新
  - 按 X-Plugin-Account 精确选号（选哪个账号完全由网关决定，插件不做调度）

用法：
    python main.py serve --port <AI_GATEWAY_PLUGIN_PORT> --data <插件数据目录>
    python main.py version
"""

import argparse
import json
import os
import signal
import sys
import threading
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import chat  # noqa: E402
import console  # noqa: E402
import login  # noqa: E402
import util  # noqa: E402
from accounts import AccountStore  # noqa: E402
from config import (  # noqa: E402
    default_config_path, default_data_dir, env_int, load_endpoints,
)
from upstream import Upstream, as_upstream_error  # noqa: E402

PLUGIN_VERSION = "1.0.0"

_STOP = threading.Event()


class Plugin:
    def __init__(self, eps, store, data_dir, port=0, config_path=""):
        self.eps = eps
        self.store = store
        self.data_dir = data_dir
        self.port = port
        self.config_path = config_path
        self.upstream = Upstream(eps)
        self.login = login.LoginBroker(self)
        self._log_lock = threading.Lock()

    def log(self, msg):
        with self._log_lock:
            sys.stdout.write("[lobsterai] %s\n" % msg)
            sys.stdout.flush()

    def note_error(self, err, account=None):
        if account is None:
            return
        message = util.sanitize_error(getattr(err, "msg", str(err)))

        def mutate(st):
            st.last_error = message
            if getattr(err, "disable", False):
                st.hard_disabled = True

        self.store.update_state(account.uid, mutate)

    def note_success(self, uid):
        def mutate(st):
            if st.last_error:
                st.last_error = ""

        self.store.update_state(uid, mutate)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "lobsterai-plugin/" + PLUGIN_VERSION

    plugin = None

    def log_message(self, fmt, *args):  # noqa: A003
        self.plugin.log("%s - %s" % (self.address_string(), fmt % args))

    def read_body(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        if length <= 0:
            return b""
        return self.rfile.read(length)

    def read_json(self):
        raw = self.read_body()
        if not raw:
            return {}
        try:
            doc = json.loads(raw.decode("utf-8"))
        except Exception:
            return None
        return doc if isinstance(doc, dict) else None

    def send_json(self, status, body):
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def send_html(self, status, body):
        raw = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def headers_map(self):
        return {k: v for k, v in self.headers.items()}

    def path_only(self):
        return self.path.split("?", 1)[0]

    def path_parts(self):
        return [urllib.parse.unquote(p) for p in self.path_only().split("/") if p]

    def query(self):
        return urllib.parse.parse_qs(
            urllib.parse.urlparse(self.path).query, keep_blank_values=True)

    def do_GET(self):  # noqa: N802
        plugin = self.plugin
        parts = self.path_parts()
        path = self.path_only()

        if path.startswith("/health"):
            return self.send_json(200, {
                "status": "ok", "version": PLUGIN_VERSION,
                "accounts": len(plugin.store.list()),
            })
        if path.startswith("/v1/models"):
            status, body = console.handle_models(plugin, self.headers_map())
            return self.send_json(status, body)
        # 上游要求的固定回环回调路径：只挂在插件自身的监听端口上
        if path == login.CALLBACK_PATH:
            page = console.handle_callback(
                plugin, (self.query().get("state") or [""])[0], self.query())
            return self.send_html(200, page)
        if parts[:2] == ["console", "accounts"] and len(parts) == 2:
            status, body = console.handle_accounts_list(plugin)
            return self.send_json(status, body)
        if parts[:2] == ["console", "login"] and len(parts) == 3:
            status, body = console.handle_login_status(plugin, parts[2], self.query())
            return self.send_json(status, body)
        return self.send_json(404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        plugin = self.plugin
        parts = self.path_parts()
        path = self.path_only()

        if path.startswith("/v1/chat/completions"):
            return self.handle_chat()
        if parts[:2] == ["console", "accounts"] and len(parts) == 4:
            account_id, action = parts[2], parts[3]
            if action == "quota":
                status, payload = console.handle_quota(plugin, account_id)
                return self.send_json(status, payload)
        if parts[:2] == ["console", "login"] and len(parts) == 3:
            body = self.read_json()
            if body is None:
                return self.send_json(400, {"error": "请求体不是合法 JSON"})
            status, payload = console.handle_login_start(
                plugin, parts[2], body, self.headers_map())
            return self.send_json(status, payload)
        return self.send_json(404, {"error": "not found"})

    def do_DELETE(self):  # noqa: N802
        parts = self.path_parts()
        if parts[:2] == ["console", "accounts"] and len(parts) == 3:
            status, payload = console.handle_account_delete(self.plugin, parts[2])
            return self.send_json(status, payload)
        return self.send_json(404, {"error": "not found"})

    def handle_chat(self):
        try:
            kind, payload = chat.forward_chat(
                self.plugin, self.read_body(), self.headers_map())
        except Exception as exc:
            ue = as_upstream_error(exc)
            return self.send_json(ue.status, {"error": util.clamp_bytes(ue.msg, 240)})

        if kind == "json":
            status, body = payload
            return self.send_json(status, body)

        response, account = payload
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        wrote_any = False
        try:
            while True:
                chunk = response.read(8192)
                if not chunk:
                    break
                self.wfile.write(chunk)
                self.wfile.flush()
                wrote_any = True
        except Exception as exc:
            self.plugin.log("[chat] 流转发中断：%s" % util.truncate_runes(str(exc), 80))
        finally:
            response.close()
        if wrote_any:
            self.plugin.note_success(account.uid)


class PluginServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def run_serve(args):
    parser = argparse.ArgumentParser(prog="serve", add_help=True)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--data", default="")
    parser.add_argument("--config", default="")
    opts = parser.parse_args(args)

    port = opts.port or env_int("AI_GATEWAY_PLUGIN_PORT", 20818)
    data_dir = opts.data or default_data_dir()
    config_path = opts.config or default_config_path()

    eps = load_endpoints(config_path)
    store = AccountStore(data_dir)
    plugin = Plugin(eps, store, data_dir, port=port, config_path=config_path)

    Handler.plugin = plugin
    server = PluginServer(("127.0.0.1", port), Handler)
    plugin.log("监听 http://127.0.0.1:%d（数据目录 %s，账号 %d 个）" % (
        port, data_dir, len(store.list())))

    def on_signal(_signum, _frame):
        plugin.log("收到停止信号，正在退出")
        _STOP.set()
        plugin.login.close_all()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, on_signal)
    signal.signal(signal.SIGTERM, on_signal)
    sigbreak = getattr(signal, "SIGBREAK", None)
    if sigbreak is not None:
        signal.signal(sigbreak, on_signal)

    try:
        server.serve_forever()
    finally:
        server.server_close()
    return 0


def main():
    args = sys.argv[1:]
    cmd = "serve"
    if args and not args[0].startswith("-"):
        cmd = args[0]
        args = args[1:]

    try:
        if cmd in ("serve", "run"):
            return run_serve(args)
        if cmd in ("version", "-v", "--version"):
            print("lobsterai plugin " + PLUGIN_VERSION)
            return 0
        raise RuntimeError("未知命令 %r，可用：serve | version" % cmd)
    except Exception as exc:
        sys.stderr.write("[lobsterai] 退出：%s\n" % exc)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
