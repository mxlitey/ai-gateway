#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""WorkBuddy (CodeBuddy) 插件：把本机桌面端账号接进网关面板。

能力：
  - 从本机桌面端导入账号（解密其 at-rest 加密的登录态）
  - OAuth 授权登录（浏览器授权，完成后账号自动入库）
  - 多账号、签到、额度查询
  - 对话转发（上游只接受流式：客户端要流式就透传，要非流式就在本地聚合）

用法：
    python main.py serve --port <AI_GATEWAY_PLUGIN_PORT> --data <插件数据目录>
    python main.py import --data <插件数据目录>     # 命令行导入一次，便于排查
    python main.py probe                            # 诊断内存扫描
    python main.py ask --prompt hi                  # 最小实测
    python main.py version

上游接口路径都可在插件目录下的 config.json 里覆盖（见 config.py）。
"""

import argparse
import json
import os
import signal
import sys
import threading
import time
import urllib.parse
from http.client import HTTPConnection, HTTPSConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import chat  # noqa: E402
import console  # noqa: E402
import desktop  # noqa: E402
import procscan  # noqa: E402
import reasoning  # noqa: E402
import upstream  # noqa: E402
from accounts import AccountStore  # noqa: E402
from config import (  # noqa: E402
    default_config_path, default_data_dir, env_int, load_endpoints, plugin_dir,
)
from util import first_non_empty, format_time, mask_token, site_label, short_uid  # noqa: E402

PLUGIN_VERSION = "0.4.0"

_STOP = threading.Event()


class Plugin:
    def __init__(self, eps, store, data_dir, config_path="", dump_dir=""):
        self.eps = eps
        self.store = store
        self.data_dir = data_dir
        self.config_path = config_path
        self.dump_dir = dump_dir
        self.oauth = console.OAuthManager(self)
        self._log_lock = threading.Lock()

    def log(self, msg):
        """带 [workbuddy] 前缀输出到 stdout（宿主按行收集为插件日志）。"""
        with self._log_lock:
            sys.stdout.write("[workbuddy] %s\n" % msg)
            sys.stdout.flush()

    def spawn_refresh(self, acc):
        """后台刷新一个账号的额度与签到状态（不阻塞当前请求）。"""
        if acc is None:
            return
        threading.Thread(target=console.refresh_account, args=(self, acc), daemon=True).start()


# ---------- 对话转发 ----------


def forward_chat(plugin, handler, headers):
    """把网关转来的 OpenAI 请求落到上游 chat 出口。

    返回 (kind, payload)：kind 为 "json"（payload 是 (status, body)）
    或 "stream"（payload 是上游响应对象，由调用方边收边写）。
    """
    raw = handler.read_body()
    chat.dump_request(plugin, headers, raw)
    try:
        body = json.loads(raw.decode("utf-8"))
    except Exception:
        return "json", console.write_error(400, "请求体不是合法 JSON")
    if not isinstance(body, dict):
        return "json", console.write_error(400, "请求体不是合法 JSON")

    want_stream = body.get("stream") is True
    # 上游只接受流式（非流式会返回 code=11101），这里统一改写成流式
    body["stream"] = True
    # 把所有 user 消息统一改写成 WorkBuddy 的 <user_query> 形态（含历史）
    chat.normalize_user_messages(body)

    try:
        acc = chat.account_for_request(plugin.eps, plugin.store, headers)
    except Exception as exc:
        return "json", console.write_error(503, str(exc))

    # 思考强度映射（对齐参考实现 thinking.go）：按模型目录收敛 reasoning_effort，
    # 并按 deepseek 语义注入 thinking 开关 / 回填 reasoning_content。取不到目录时仅保留注入。
    try:
        catalog = chat.reasoning_catalog(plugin.eps, acc)
    except Exception:
        catalog = {}
    reasoning.apply(body, catalog.get(str(body.get("model") or "").strip().lower()))

    try:
        payload = json.dumps(body, ensure_ascii=False).encode("utf-8")
    except Exception:
        return "json", console.write_error(500, "构造上游请求失败")

    cfg = upstream.realm_config(plugin.eps, acc.site)
    # 会话头族以客户端发来的为准（会话中途换到本插件时才能与官方归组一致）
    meta = chat.conversation_meta(body, headers)
    try:
        response = http_stream(cfg.chat_origin + plugin.eps.chat_completions,
                               upstream.chat_headers(acc, cfg, meta),
                               payload)
    except Exception as exc:
        return "json", console.write_error(502, "请求上游失败: " + str(exc))

    if want_stream and response.status == 200:
        return "stream", response

    # 非流式：读完整个 SSE 再聚合；上游报错时原样返回它的错误体
    full = response.read(32 << 20)
    status = response.status
    response.close()
    if status != 200:
        return "json", (status, _decode_json(full))
    merged, err = chat.aggregate_sse(full)
    if err is not None:
        return "json", console.write_error(502, "聚合上游流式响应失败: " + err)
    return "json", (200, merged)


def _decode_json(raw):
    try:
        return json.loads(raw.decode("utf-8"))
    except Exception:
        return {"error": raw.decode("utf-8", "replace")[:300]}


def http_stream(url, headers, payload, timeout=120):
    """发起流式上游请求，返回带 .status/.read()/.close() 的响应对象。"""
    parts = urllib.parse.urlsplit(url)
    cls = HTTPSConnection if parts.scheme == "https" else HTTPConnection
    conn = cls(parts.hostname, parts.port or (443 if parts.scheme == "https" else 80), timeout=timeout)
    path = parts.path or "/"
    if parts.query:
        path += "?" + parts.query
    conn.request("POST", path, body=payload, headers=headers)
    return _StreamResponse(conn, conn.getresponse())


class _StreamResponse:
    __slots__ = ("_conn", "_resp", "status")

    def __init__(self, conn, resp):
        self._conn = conn
        self._resp = resp
        self.status = resp.status

    def read(self, limit=None):
        if limit is None:
            return self._resp.read()
        return self._resp.read(limit)

    def close(self):
        for target in (self._resp, self._conn):
            try:
                target.close()
            except Exception:
                pass


# ---------- HTTP 路由 ----------


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "workbuddy-plugin/" + PLUGIN_VERSION

    # 由服务器注入
    plugin = None

    def log_message(self, fmt, *args):  # noqa: A003
        self.plugin.log("%s - %s" % (self.address_string(), fmt % args))

    # ---------- 工具 ----------

    def read_body(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return b""
        return self.rfile.read(length)

    def send_json(self, status, body):
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def headers_map(self):
        return {k: v for k, v in self.headers.items()}

    def path_parts(self):
        path = self.path.split("?", 1)[0]
        return [p for p in path.split("/") if p]

    def query(self):
        return urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query, keep_blank_values=True)

    # ---------- 路由 ----------

    def do_GET(self):  # noqa: N802
        plugin = self.plugin
        parts = self.path_parts()

        if self.path.startswith("/health") or self.path.startswith("/healthz"):
            return self.send_json(200, {
                "status": "ok", "version": PLUGIN_VERSION, "accounts": len(plugin.store.list()),
            })
        if self.path.startswith("/v1/models"):
            status, body = console.handle_models(plugin, self.headers_map())
            return self.send_json(status, body)
        if len(parts) == 2 and parts[0] == "console" and parts[1] == "accounts":
            status, body = console.handle_accounts_list(plugin)
            return self.send_json(status, body)
        if len(parts) == 3 and parts[0] == "console" and parts[1] == "login":
            status, body = console.handle_login_status(plugin, parts[2], self.query())
            return self.send_json(status, body)
        return self.send_json(404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        plugin = self.plugin
        parts = self.path_parts()

        if self.path.startswith("/v1/chat/completions"):
            return self.handle_chat()
        if len(parts) == 2 and parts[0] == "console" and parts[1] == "accounts":
            body = console.read_json_body(self.read_body())
            status, payload = console.handle_account_add(plugin, body)
            return self.send_json(status, payload)
        if len(parts) == 4 and parts[0] == "console" and parts[1] == "accounts" and parts[3] == "checkin":
            status, payload = console.handle_checkin(plugin, parts[2])
            return self.send_json(status, payload)
        if len(parts) == 4 and parts[0] == "console" and parts[1] == "accounts" and parts[3] == "quota":
            status, payload = console.handle_quota(plugin, parts[2])
            return self.send_json(status, payload)
        if len(parts) == 3 and parts[0] == "console" and parts[1] == "login":
            body = console.read_json_body(self.read_body())
            status, payload = console.handle_login_start(plugin, parts[2], body)
            return self.send_json(status, payload)
        return self.send_json(404, {"error": "not found"})

    def do_DELETE(self):  # noqa: N802
        plugin = self.plugin
        parts = self.path_parts()
        if len(parts) == 3 and parts[0] == "console" and parts[1] == "accounts":
            status, payload = console.handle_account_delete(plugin, parts[2])
            return self.send_json(status, payload)
        return self.send_json(404, {"error": "not found"})

    # ---------- 对话 ----------

    def handle_chat(self):
        try:
            kind, payload = forward_chat(self.plugin, self, self.headers_map())
        except Exception as exc:
            return self.send_json(502, {"error": str(exc)})

        if kind == "json":
            status, body = payload
            return self.send_json(status, body)

        response = payload
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            # 照原样透传上游 SSE（上游已是 OpenAI 形态，无需转换）
            while True:
                chunk = response.read(8192)
                if not chunk:
                    break
                self.wfile.write(chunk)
                self.wfile.flush()
        except Exception:
            pass
        finally:
            response.close()


class PluginServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


# ---------- 命令行 ----------


def run_serve(args):
    parser = argparse.ArgumentParser(prog="serve", add_help=True)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--data", default="")
    parser.add_argument("--config", default="")
    parser.add_argument("--dump", default="")
    opts = parser.parse_args(args)

    port = opts.port or env_int("AI_GATEWAY_PLUGIN_PORT", 8399)
    data_dir = opts.data or default_data_dir()
    config_path = opts.config or default_config_path()
    dump_dir = (opts.dump or os.environ.get("WORKBUDDY_DUMP_DIR") or "").strip()
    if dump_dir:
        os.makedirs(dump_dir, mode=0o755, exist_ok=True)

    eps = load_endpoints(config_path)
    store = AccountStore(data_dir)
    plugin = Plugin(eps, store, data_dir, config_path, dump_dir)
    if dump_dir:
        plugin.log("已开启请求体 dump：%s" % dump_dir)

    Handler.plugin = plugin
    server = PluginServer(("127.0.0.1", port), Handler)
    plugin.log("监听 http://127.0.0.1:%d（数据目录 %s，账号 %d 个）" % (
        port, data_dir, len(store.list())))

    def on_signal(_signum, _frame):
        plugin.log("收到停止信号，正在退出")
        _STOP.set()
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


def run_import(args):
    parser = argparse.ArgumentParser(prog="import", add_help=True)
    parser.add_argument("--data", default="")
    opts = parser.parse_args(args)

    data_dir = opts.data or default_data_dir()
    accounts, notes, err = desktop.import_from_desktop()
    for note in notes:
        sys.stderr.write("[workbuddy] 提示：%s\n" % note)
    if err is not None:
        sys.stderr.write("[workbuddy] 退出：%s\n" % err)
        return 1

    store = AccountStore(data_dir)
    for acc in accounts:
        _saved, created = store.upsert(acc)
        action = "新增" if created else "更新"
        sys.stdout.write("[workbuddy] %s账号 %s（%s，uid=%s）token=%s 到期=%s\n" % (
            action, acc.name, site_label(acc.site), short_uid(acc.uid),
            mask_token(acc.access_token), format_time(acc.token_expires_at)))
    return 0


def run_probe(args):
    parser = argparse.ArgumentParser(prog="probe", add_help=True)
    parser.parse_args(args)
    try:
        blob = desktop.find_key_blob()
        key_id = blob.protector_key_id()
        sys.stdout.write("[workbuddy] 目标 protectorKeyId=%s（来自 %s）\n" % (key_id, blob.path))
        for line in procscan.probe_processes(key_id):
            sys.stdout.write("[workbuddy] %s\n" % line)
    except Exception as exc:
        sys.stderr.write("[workbuddy] 退出：%s\n" % exc)
        return 1
    return 0


def run_ask(args):
    """排错用的最小实测：拿第一个账号向上游发一次对话，打印原始响应。"""
    parser = argparse.ArgumentParser(prog="ask", add_help=True)
    parser.add_argument("--data", default="")
    parser.add_argument("--prompt", default="hi")
    parser.add_argument("--stream", dest="stream", action="store_true", default=True)
    opts = parser.parse_args(args)

    data_dir = opts.data or default_data_dir()
    store = AccountStore(data_dir)
    eps = load_endpoints(default_config_path())

    try:
        acc = chat.pick_account(eps, store)
    except Exception as exc:
        sys.stderr.write("[workbuddy] 退出：%s\n" % exc)
        return 1
    cfg = upstream.realm_config(eps, acc.site)

    body = {
        "model": "auto",
        "stream": opts.stream,
        "messages": [{"role": "user", "content": opts.prompt}],
    }
    payload = json.dumps(body, ensure_ascii=False).encode("utf-8")
    try:
        response = http_stream(cfg.chat_origin + eps.chat_completions,
                               upstream.chat_headers(acc, cfg), payload, timeout=60)
    except Exception as exc:
        sys.stderr.write("[workbuddy] 退出：%s\n" % exc)
        return 1

    raw = response.read(64 << 10)
    status = response.status
    response.close()
    sys.stdout.write("[workbuddy] HTTP %d  %s%s\n" % (status, cfg.chat_origin, eps.chat_completions))
    sys.stdout.write("[workbuddy] 响应：%s\n" % raw.decode("utf-8", "replace")[:1200])
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
        if cmd == "import":
            return run_import(args)
        if cmd == "probe":
            return run_probe(args)
        if cmd == "ask":
            return run_ask(args)
        if cmd in ("version", "-v", "--version"):
            print("workbuddy plugin " + PLUGIN_VERSION)
            return 0
        raise RuntimeError("未知命令 %r，可用：serve | import | probe | ask | version" % cmd)
    except Exception as exc:
        sys.stderr.write("[workbuddy] 退出：%s\n" % exc)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
