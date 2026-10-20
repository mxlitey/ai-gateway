#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Kilo 免费池插件：把 Kilo AI 公共网关的免密免费模型接进 AI Gateway。

特点：
  - 不需要账号、不需要 Key：上游免费池本身免鉴权（AgentHub 的 Kilo 车道同款来源）
  - 模型清单实时跟随上游：只广播 isFree=true 的条目 + 自动路由 kilo-auto/free
  - 无控制台：没有账号概念，渠道密钥固定为占位值 gw-local

上游端点（实测 2026-10）：
    GET  https://api.kilo.ai/api/gateway/models
    POST https://api.kilo.ai/api/gateway/chat/completions
付费模型在不带凭据时返回 401 PAID_MODEL_AUTH_REQUIRED —— 那是「请求里点错了模型」，
按 plugin-spec §5.2 映射为 400（换号也没用），不是账号故障。

用法：
    python main.py serve --port <AI_GATEWAY_PLUGIN_PORT>
    python main.py models          # 命令行打印当前免费清单
    python main.py version
"""

import argparse
import codecs
import json
import os
import signal
import sys
import threading
import time
from http.client import HTTPConnection, HTTPSConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from config import default_config_path, env_int, load_endpoints  # noqa: E402

PLUGIN_VERSION = "1.0.0"
_STOP = threading.Event()

# 清单缓存：(过期时间, 模型 id 列表)
_listing_lock = threading.Lock()
_listing_cache = (0.0, [])


def log(msg):
    sys.stdout.write("[kilo] %s\n" % msg)
    sys.stdout.flush()


# ---------- HTTP ----------


class UpstreamError(Exception):
    def __init__(self, status, msg):
        super().__init__(msg)
        self.status = int(status)
        self.msg = msg


def _connect(url, timeout):
    parts = urlsplit(url)
    cls = HTTPSConnection if parts.scheme == "https" else HTTPConnection
    conn = cls(parts.hostname, parts.port or (443 if parts.scheme == "https" else 80),
               timeout=timeout)
    path = parts.path or "/"
    if parts.query:
        path += "?" + parts.query
    return conn, path


def _error_message(raw):
    try:
        doc = json.loads(raw.decode("utf-8", "replace"))
    except Exception:
        doc = None
    if isinstance(doc, dict):
        err = doc.get("error")
        if isinstance(err, dict):
            for key in ("message", "code"):
                if isinstance(err.get(key), str) and err[key].strip():
                    return err[key].strip()
        if isinstance(err, str) and err.strip():
            return err.strip()
        for key in ("message", "error_description"):
            if isinstance(doc.get(key), str) and doc[key].strip():
                return doc[key].strip()
    text = (raw or b"").decode("utf-8", "replace").strip()
    return text[:200] or "上游未返回错误详情"


def classify(status, raw):
    """按 plugin-spec §5.2 映射：付费模型用 400（请求问题），限流 429，上游故障 502。"""
    message = _error_message(raw)
    lowered = message.lower()
    if status == 401 and ("paid_model_auth_required" in lowered or "sign in" in lowered):
        return UpstreamError(
            400, "该模型不在 Kilo 免费池里（需要登录 Kilo 账号）：%s" % message[:80])
    if status in (401, 403):
        return UpstreamError(
            400, "Kilo 免费池拒绝了该请求（公开网关不需要凭据，通常是模型不在免费池）：%s"
            % message[:80])
    if status == 404:
        return UpstreamError(400, "Kilo 网关没有该模型：%s" % message[:80])
    if status == 429:
        return UpstreamError(429, "Kilo 免费池限流：%s" % message[:80])
    if status >= 500:
        return UpstreamError(502, "Kilo 网关异常：%s" % message[:80])
    if status >= 400:
        return UpstreamError(400, "Kilo 网关拒绝了该请求：%s" % message[:100])
    return UpstreamError(502, "Kilo 网关返回未知状态 %d" % status)


def request(eps, method, path, payload=None, timeout=60, accept="application/json"):
    url = eps.base.rstrip("/") + path
    headers = {
        "content-type": "application/json",
        "accept": accept,
        "user-agent": eps.user_agent,
    }
    conn, target = _connect(url, timeout)
    try:
        conn.request(method, target, body=payload, headers=headers)
        return conn, conn.getresponse()
    except Exception as exc:
        try:
            conn.close()
        except Exception:
            pass
        raise UpstreamError(502, "连接 Kilo 网关失败：%s" % str(exc)[:80])


def fetch_free_models(eps):
    """拉免费清单（带 TTL 缓存）。失败时回退到内置的自动路由 id。"""
    global _listing_cache
    now = time.time()
    with _listing_lock:
        expires, cached = _listing_cache
        if expires > now and cached:
            return cached

    models = []
    try:
        conn, resp = request(eps, "GET", eps.models_path, timeout=eps.listing_timeout)
        try:
            raw = resp.read(8 << 20)
            status = resp.status
        finally:
            conn.close()
        if status >= 400:
            raise classify(status, raw)
        doc = json.loads(raw.decode("utf-8", "replace"))
        for item in (doc.get("data") if isinstance(doc, dict) else None) or []:
            if not isinstance(item, dict):
                continue
            mid = item.get("id")
            if not isinstance(mid, str) or not mid.strip():
                continue
            if eps.free_only and item.get("isFree") is not True:
                continue
            models.append(mid.strip())
    except Exception as exc:
        log("拉取免费模型清单失败：%s" % str(exc)[:120])
        models = list(eps.listing_fail_models)
        ttl = eps.listing_fail_ttl
    else:
        ttl = eps.listing_ttl
    if eps.auto_free_id and eps.auto_free_id not in models:
        models.insert(0, eps.auto_free_id)
    with _listing_lock:
        _listing_cache = (now + ttl, models)
    return models


# ---------- 对话 ----------


def rewrite_body(body):
    """请求体整理：只保留上游可接受的字段语义，其它原样透传。"""
    out = dict(body or {})
    if out.get("max_completion_tokens") is not None and out.get("max_tokens") is None:
        out["max_tokens"] = out.pop("max_completion_tokens")
    else:
        out.pop("max_completion_tokens", None)
    if out.get("stream") is True:
        options = dict(out.get("stream_options") or {})
        options["include_usage"] = True
        out["stream_options"] = options
    return out


def forward_chat(eps, raw, want_stream):
    """返回 (kind, payload)。kind=json -> (status, doc)；kind=stream -> (conn, resp)。"""
    try:
        body = json.loads(raw.decode("utf-8"))
    except Exception:
        return "json", (400, {"error": "请求体不是合法 JSON"})
    if not isinstance(body, dict):
        return "json", (400, {"error": "请求体不是合法 JSON"})
    if not str(body.get("model") or "").strip():
        return "json", (400, {"error": "缺少 model"})

    payload = json.dumps(rewrite_body(body), ensure_ascii=False).encode("utf-8")
    accept = "text/event-stream, application/json"
    try:
        conn, resp = request(eps, "POST", eps.chat_path, payload,
                             timeout=eps.chat_timeout, accept=accept)
    except UpstreamError as err:
        return "json", (err.status, {"error": err.msg[:240]})

    if resp.status == 200 and want_stream:
        ctype = (resp.getheader("content-type") or "").lower()
        if "text/event-stream" in ctype:
            return "stream", (conn, resp)
        # 上游用整包 JSON 回了流式请求：读完再回给客户端（网关会兜成非流式）
        raw_body = resp.read()
        conn.close()
        return "json", _json_result(resp.status, raw_body)

    raw_body = resp.read(32 << 20)
    status = resp.status
    conn.close()
    return "json", _json_result(status, raw_body)


def _json_result(status, raw):
    if status >= 400:
        err = classify(status, raw)
        return err.status, {"error": err.msg[:240]}
    try:
        doc = json.loads(raw.decode("utf-8", "replace"))
    except Exception:
        return 502, {"error": "上游返回了无法解析的响应体"}
    if not isinstance(doc, dict):
        return 502, {"error": "上游返回了无法解析的响应体"}
    if doc.get("error"):
        return 502, {"error": _error_message(raw)[:240]}
    if not doc.get("choices"):
        return 502, {"error": "上游未返回任何内容"}
    return 200, normalize_doc(doc)


def normalize_doc(doc):
    """把 Kilo 的 `reasoning` 归一成 OpenAI 客户端认得的 `reasoning_content`。

    实测（2026-10）：思考型免费模型把思考链放在 message.reasoning / delta.reasoning，
    正文为 null；多数 OpenAI 兼容客户端只认 reasoning_content，直接透传会显示空回复。
    """
    if not isinstance(doc, dict):
        return doc
    for choice in doc.get("choices") or []:
        if not isinstance(choice, dict):
            continue
        for key in ("message", "delta"):
            part = choice.get(key)
            if not isinstance(part, dict):
                continue
            if "reasoning" in part and not part.get("reasoning_content"):
                part["reasoning_content"] = part.pop("reasoning")
            if part.get("content") is None and part.get("reasoning_content"):
                part["content"] = ""
    return doc


def normalize_sse_line(text):
    """按行改写 SSE：只处理 data: 行，且只在出现 reasoning 字段时重新序列化。"""
    line = text.strip()
    if not line.startswith("data:") or '"reasoning"' not in line:
        return text
    payload = line[len("data:"):].strip()
    if not payload or payload == "[DONE]":
        return text
    try:
        doc = json.loads(payload)
    except Exception:
        return text
    if not isinstance(doc, dict):
        return text
    normalize_doc(doc)
    return "data: " + json.dumps(doc, ensure_ascii=False)


# ---------- HTTP 服务 ----------


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "kilo-plugin/" + PLUGIN_VERSION

    eps = None

    def log_message(self, fmt, *args):  # noqa: A003
        log("%s - %s" % (self.address_string(), fmt % args))

    def read_body(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        return self.rfile.read(length) if length > 0 else b""

    def send_json(self, status, body):
        raw = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):  # noqa: N802
        path = self.path.split("?", 1)[0]
        if path.startswith("/health"):
            return self.send_json(200, {
                "status": "ok", "version": PLUGIN_VERSION,
                "free_models": len(fetch_free_models(self.eps)),
            })
        if path.startswith("/v1/models"):
            data = [{"id": mid, "object": "model", "owned_by": "kilo"}
                    for mid in fetch_free_models(self.eps)]
            return self.send_json(200, {"object": "list", "data": data})
        return self.send_json(404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        if not self.path.split("?", 1)[0].startswith("/v1/chat/completions"):
            return self.send_json(404, {"error": "not found"})
        try:
            want_stream = False
            raw = self.read_body()
            try:
                want_stream = json.loads(raw.decode("utf-8")).get("stream") is True
            except Exception:
                pass
            kind, payload = forward_chat(self.eps, raw, want_stream)
        except Exception as exc:
            return self.send_json(502, {"error": str(exc)[:240]})

        if kind == "json":
            status, body = payload
            return self.send_json(status, body)

        conn, resp = payload
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
        carry = ""
        try:
            # 上游已是 OpenAI SSE；逐行改写 reasoning 字段后立即转发（不攒整包）
            while True:
                chunk = resp.read(8192)
                if not chunk:
                    break
                carry += decoder.decode(chunk)
                while "\n" in carry:
                    line, carry = carry.split("\n", 1)
                    self.wfile.write((normalize_sse_line(line) + "\n").encode("utf-8"))
                self.wfile.flush()
            tail = carry + decoder.decode(b"", True)
            if tail:
                self.wfile.write((normalize_sse_line(tail) + "\n").encode("utf-8"))
                self.wfile.flush()
        except Exception as exc:
            log("流转发中断：%s" % str(exc)[:80])
        finally:
            try:
                conn.close()
            except Exception:
                pass


class PluginServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


# ---------- 入口 ----------


def run_serve(args):
    parser = argparse.ArgumentParser(prog="serve", add_help=True)
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--data", default="")
    parser.add_argument("--config", default="")
    opts = parser.parse_args(args)

    port = opts.port or env_int("AI_GATEWAY_PLUGIN_PORT", 20812)
    eps = load_endpoints(opts.config or default_config_path())
    Handler.eps = eps

    server = PluginServer(("127.0.0.1", port), Handler)
    log("监听 http://127.0.0.1:%d（上游 %s，免密免费池）" % (port, eps.base))

    def on_signal(_signum, _frame):
        log("收到停止信号，正在退出")
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


def run_models(args):
    parser = argparse.ArgumentParser(prog="models", add_help=True)
    parser.add_argument("--config", default="")
    opts = parser.parse_args(args)
    eps = load_endpoints(opts.config or default_config_path())
    for mid in fetch_free_models(eps):
        sys.stdout.write(mid + "\n")
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
        if cmd == "models":
            return run_models(args)
        if cmd in ("version", "-v", "--version"):
            print("kilo plugin " + PLUGIN_VERSION)
            return 0
        raise RuntimeError("未知命令 %r，可用：serve | models | version" % cmd)
    except Exception as exc:
        sys.stderr.write("[kilo] 退出：%s\n" % exc)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
