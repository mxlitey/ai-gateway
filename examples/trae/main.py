#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""TRAE 插件：把 TRAE SOLO 通道包装成 OpenAI 兼容上游，供 AI Gateway 按「账号即密钥」调度。

能力：
  - 实现 GET /health、POST /v1/chat/completions、GET /v1/models
  - 控制台：账号列表 / 网页登录（回调走网关统一入口）/ 手工导入 / 删除 / 签到 / 额度刷新
  - 按 X-Plugin-Account 精确选号（选择哪个账号全部归网关，插件不做任何调度）

用法：
    python main.py serve --port <AI_GATEWAY_PLUGIN_PORT> --data <插件数据目录>
    python main.py version

上游主机与接口路径都可在插件目录下的 config.json 里覆盖（见 config.py）。
"""

import argparse
import json
import os
import signal
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import chat  # noqa: E402
import console  # noqa: E402
import importer  # noqa: E402
import login  # noqa: E402
import util  # noqa: E402
from accounts import AccountStore  # noqa: E402
from config import (  # noqa: E402
    default_config_path, default_data_dir, default_model, env_int, load_endpoints,
)
from upstream import Upstream, UpstreamError, as_upstream_error  # noqa: E402

PLUGIN_VERSION = "1.0.0"

# 后台任务参数：token 到期前 24 小时预刷新，每 30 分钟复查一次。
BG_INTERVAL = 30 * 60
REFRESH_SKEW = 24 * 60 * 60

_STOP = threading.Event()


class Plugin:
    def __init__(self, eps, store, data_dir, dump_dir=""):
        self.eps = eps
        self.store = store
        self.data_dir = data_dir
        self.dump_dir = dump_dir
        self.upstream = Upstream(eps)
        self.login = login.LoginBroker(self)
        self._log_lock = threading.Lock()

    def log(self, msg):
        """带 [trae] 前缀输出到 stdout（宿主按行收集为插件日志）。"""
        with self._log_lock:
            sys.stdout.write("[trae] %s\n" % msg)
            sys.stdout.flush()

    # ---------- 状态上报（不是调度） ----------

    def note_upstream_error(self, account, ue):
        """记录上游错误：登录态失效时把账号硬禁用，并写脱敏后的 last_error。"""
        if account is None or ue is None:
            return

        def mutate(st):
            if ue.disable:
                st.enabled = False
                st.hard_disabled = True
            st.last_error = util.sanitize_error(ue.msg)

        self.store.update_state(account.uid, mutate)

    def note_success(self, uid):
        """清掉上次的错误提示。"""

        def mutate(st):
            st.last_error = ""

        self.store.update_state(uid, mutate)

    # ---------- 手工导入 ----------

    def import_credential(self, token, name, uid_hint):
        return importer.import_credential(self, token, name, uid_hint)

    # ---------- 后台任务 ----------

    def start_background(self):
        thread = threading.Thread(target=self._background_loop, daemon=True)
        thread.start()

    def _background_loop(self):
        # 启动时先补一次临期 token 的预刷新
        self.run_token_pre_refresh()
        while not _STOP.wait(BG_INTERVAL):
            self.run_token_pre_refresh()

    def run_token_pre_refresh(self):
        """对 24 小时内即将过期的账号做 token 预刷新并原子落盘。"""
        now = int(time.time())
        for account in self.store.list():
            if not (account.refresh_token or "").strip():
                continue
            if account.expires_at <= 0 or account.expires_at - now > REFRESH_SKEW:
                continue
            work = account.copy()
            try:
                self.upstream.refresh_token(work)
            except Exception as exc:
                self.log("[token 预刷新] %s 失败：%s" % (account.name(), exc))
                continue
            try:
                self.store.save_auth(work.to_auth())
            except Exception as exc:
                self.log("[token 预刷新] %s 落盘失败：%s" % (account.name(), exc))
                continue
            self.log("[token 预刷新] %s 完成" % account.name())


# ---------- 对话转发 ----------


def forward_chat(plugin, handler, headers):
    """把网关转来的 OpenAI 请求落到 TRAE SOLO 上游。

    返回 (kind, payload)：kind 为 "json"（非流式，payload 是 (status, body)）
    或 "stream"（流式，payload 是上游响应对象，由调用方边收边写）。
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
    model = body.get("model")
    model = model.strip() if isinstance(model, str) else ""

    # 上游只接受流式；把 model 归一化为 config_name / model
    body["stream"] = True
    if not model or model.lower() == "auto":
        model = default_model()
    body["function"] = plugin.eps.function
    body["config_name"] = model
    body["model"] = model
    chat.prepare_messages(body)
    chat.normalize_tool_choice(body)
    chat.normalize_tools(body)

    try:
        account = chat.account_for_request(plugin, headers)
    except Exception as exc:
        ue = as_upstream_error(exc)
        return "json", console.write_error(ue.status, ue.msg)

    # 账号级必填字段必须在序列化之前注入
    chat.inject_solo_fields(plugin, body, account)

    try:
        prepared = json.dumps(body, ensure_ascii=False).encode("utf-8")
    except Exception:
        return "json", console.write_error(500, "构造上游请求失败")

    # 官方域优先，连接层失败或 404 时回退镜像域
    candidates = [plugin.eps.agent_host + plugin.eps.ep_chat]
    mirror = (plugin.eps.chat_mirror or "").strip()
    if mirror:
        candidates.append(mirror)

    response = None
    last_error = None
    for index, target in enumerate(candidates):
        request_headers = plugin.upstream.solo_headers(account, True)
        request_headers["Referer"] = target  # 与请求 URL 同源（伪装同源请求）
        try:
            response = util_http_stream(target, request_headers, prepared)
        except Exception as exc:
            last_error = exc
            if index == len(candidates) - 1:
                return "json", console.write_error(
                    502, "请求上游失败：" + util.truncate_runes(str(exc), 80))
            continue
        if response.status == 404 and index < len(candidates) - 1:
            response.close()
            continue
        break

    if response is None:
        return "json", console.write_error(
            502, "请求上游失败：" + util.truncate_runes(str(last_error or "上游无响应"), 80))

    if response.status != 200:
        upstream_body = response.read(1 << 20)
        ue = _classify(response.status, upstream_body)
        plugin.note_upstream_error(account, ue)
        response.close()
        return "json", console.write_error(ue.status, ue.msg)

    if want_stream:
        return "stream", (response, account, model)

    # 非流式：读完整个 SSE 再聚合（成功响应必须带非空 choices）
    import sse

    out, _, serr, agg_err = sse.aggregate_solo(response, model)
    response.close()
    if serr is not None:
        plugin.note_upstream_error(account, serr)
        return "json", console.write_error(serr.status, serr.msg)
    if agg_err is not None:
        return "json", console.write_error(502, "聚合上游响应失败")
    if not isinstance(out, dict) or not out.get("choices"):
        return "json", console.write_error(502, "上游未返回任何内容")
    plugin.note_success(account.uid)
    return "json", (200, out)


def _classify(status, body):
    from upstream import classify_upstream

    return classify_upstream(status, body.decode("utf-8", "replace"))


def util_http_stream(url, headers, payload, timeout=120):
    """发起流式上游请求，返回一个带 .status/.read()/.readline()/.close() 的响应对象。"""
    import http.client
    from urllib.parse import urlsplit

    parts = urlsplit(url)
    host = parts.hostname or ""
    port = parts.port or (443 if parts.scheme == "https" else 80)
    path = parts.path or "/"
    if parts.query:
        path += "?" + parts.query
    cls = http.client.HTTPSConnection if parts.scheme == "https" else http.client.HTTPConnection
    conn = cls(host, port, timeout=timeout)
    conn.request("POST", path, body=payload, headers=headers)
    resp = conn.getresponse()
    return _StreamResponse(conn, resp)


class _StreamResponse:
    """包装 http.client 响应，提供流式读取。"""

    def __init__(self, conn, resp):
        self._conn = conn
        self._resp = resp
        self.status = resp.status

    def read(self, limit=None):
        if limit is None:
            return self._resp.read()
        return self._resp.read(limit)

    def readline(self):
        return self._resp.readline()

    def close(self):
        try:
            self._resp.close()
        except Exception:
            pass
        try:
            self._conn.close()
        except Exception:
            pass


# ---------- HTTP 路由 ----------


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "trae-plugin/" + PLUGIN_VERSION

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

    def send_html(self, body):
        raw = body.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def headers_map(self):
        return {k: v for k, v in self.headers.items()}

    def path_parts(self):
        path = self.path.split("?", 1)[0]
        return [p for p in path.split("/") if p]

    def query(self):
        from urllib.parse import parse_qs, urlparse

        return parse_qs(urlparse(self.path).query, keep_blank_values=True)

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
        if len(parts) == 4 and parts[0] == "console" and parts[1] == "callback" and parts[2] == "web":
            ok, message = plugin.login.handle_callback(parts[3], self.path, self.path.split("?", 1)[1] if "?" in self.path else "")
            return self.send_html(login.render_login_page(ok, message))
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
            body = self._decode_json(self.read_body())
            status, payload = console.handle_account_add(plugin, body)
            return self.send_json(status, payload)
        if len(parts) == 4 and parts[0] == "console" and parts[1] == "accounts" and parts[3] == "checkin":
            status, payload = console.handle_checkin(plugin, parts[2])
            return self.send_json(status, payload)
        if len(parts) == 4 and parts[0] == "console" and parts[1] == "accounts" and parts[3] == "quota":
            status, payload = console.handle_quota(plugin, parts[2])
            return self.send_json(status, payload)
        if len(parts) == 3 and parts[0] == "console" and parts[1] == "login":
            status, payload = console.handle_login_start(plugin, parts[2], self.headers_map())
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
        plugin = self.plugin
        try:
            kind, payload = forward_chat(plugin, self, self.headers_map())
        except Exception as exc:
            ue = as_upstream_error(exc)
            return self.send_json(ue.status, {"error": ue.msg})

        if kind == "json":
            if isinstance(payload, tuple) and len(payload) == 2 and isinstance(payload[0], int):
                status, body = payload
            else:
                status, body = payload  # type: ignore[misc]
            return self.send_json(status, body)

        response, account, model = payload
        import sse

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        try:
            wrote_any, _, serr, _ = sse.stream_solo(self.wfile, response, model)
            if serr is not None and not wrote_any:
                # 尚未写出任何 chunk（响应头已发出，这里只能补一个 SSE 错误事件）
                plugin.note_upstream_error(account, serr)
            elif serr is None:
                plugin.note_success(account.uid)
        finally:
            response.close()

    @staticmethod
    def _decode_json(raw):
        if not raw:
            return {}
        try:
            return json.loads(raw.decode("utf-8"))
        except Exception:
            return {}


class PluginServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


# ---------- 入口 ----------


def run_serve(args):
    parser = argparse.ArgumentParser(prog="serve", add_help=True)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--data", default="")
    parser.add_argument("--config", default="")
    parser.add_argument("--dump", default="")
    opts = parser.parse_args(args)

    port = opts.port or env_int("AI_GATEWAY_PLUGIN_PORT", 20727)
    data_dir = opts.data or default_data_dir()
    config_path = opts.config or default_config_path()
    dump_dir = (opts.dump or os.environ.get("TRAE_DUMP_DIR") or "").strip()
    if dump_dir:
        os.makedirs(dump_dir, mode=0o755, exist_ok=True)

    eps = load_endpoints(config_path)
    store = AccountStore(data_dir)
    plugin = Plugin(eps, store, data_dir, dump_dir)
    if dump_dir:
        plugin.log("已开启请求体 dump：%s" % dump_dir)

    Handler.plugin = plugin
    server = PluginServer(("127.0.0.1", port), Handler)
    plugin.log("监听 http://127.0.0.1:%d（数据目录 %s，账号 %d 个）" % (port, data_dir, len(store.list())))

    plugin.start_background()

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
            print("trae plugin " + PLUGIN_VERSION)
            return 0
        raise RuntimeError("未知命令 %r，可用：serve | version" % cmd)
    except Exception as exc:
        sys.stderr.write("[trae] 退出：%s\n" % exc)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
