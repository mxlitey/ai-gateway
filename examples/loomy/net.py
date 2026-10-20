# -*- coding: utf-8 -*-
"""极简 HTTP 传输层（只用标准库）。"""

import gzip
import http.client
import json
import socket
import ssl
import zlib
from urllib.parse import urlsplit

DEFAULT_TIMEOUT = 60


class TransportError(Exception):
    def __init__(self, message, timeout=False):
        super().__init__(message)
        self.timeout = timeout


class StreamResponse:
    __slots__ = ("status", "headers", "reason", "_conn", "_resp", "_closed")

    def __init__(self, conn, resp):
        self._conn = conn
        self._resp = resp
        self.status = resp.status
        self.reason = resp.reason
        self.headers = {k.lower(): v for k, v in resp.getheaders()}
        self._closed = False

    def header(self, name, default=""):
        return self.headers.get(name.lower(), default)

    def content_type(self):
        return self.header("content-type")

    def is_event_stream(self):
        return "text/event-stream" in self.content_type().lower()

    def read(self, limit=None):
        try:
            return self._resp.read() if limit is None else self._resp.read(limit)
        except (socket.timeout, TimeoutError) as exc:
            raise TransportError("读取上游响应超时", timeout=True) from exc
        except (http.client.HTTPException, OSError) as exc:
            raise TransportError("读取上游响应失败：%s" % exc) from exc

    def close(self):
        if self._closed:
            return
        self._closed = True
        for target in (self._resp, self._conn):
            try:
                target.close()
            except Exception:
                pass


def _connection(url, timeout):
    parts = urlsplit(url)
    if parts.scheme not in ("http", "https"):
        raise TransportError("不支持的 URL 协议：%s" % parts.scheme)
    host = parts.hostname or ""
    port = parts.port or (443 if parts.scheme == "https" else 80)
    path = parts.path or "/"
    if parts.query:
        path += "?" + parts.query
    if parts.scheme == "https":
        context = ssl.create_default_context()
        conn = http.client.HTTPSConnection(host, port, timeout=timeout, context=context)
    else:
        conn = http.client.HTTPConnection(host, port, timeout=timeout)
    return conn, path


def request(method, url, headers=None, body=None, timeout=DEFAULT_TIMEOUT):
    conn, path = _connection(url, timeout)
    payload = body
    if isinstance(payload, str):
        payload = payload.encode("utf-8")
    try:
        conn.request(method, path, body=payload, headers=headers or {})
        resp = conn.getresponse()
    except (socket.timeout, TimeoutError) as exc:
        try:
            conn.close()
        except Exception:
            pass
        raise TransportError("连接上游超时（%ss）" % timeout, timeout=True) from exc
    except (http.client.HTTPException, OSError) as exc:
        try:
            conn.close()
        except Exception:
            pass
        raise TransportError("连接上游失败：%s" % exc) from exc
    return StreamResponse(conn, resp)


def read_all(resp, limit=None):
    raw = resp.read(limit) if limit else resp.read()
    encoding = resp.header("content-encoding").lower()
    if not raw:
        return b""
    try:
        if "gzip" in encoding:
            return gzip.decompress(raw)
        if "deflate" in encoding:
            return zlib.decompress(raw)
    except Exception:
        return raw
    return raw


def parse_json(raw):
    if not raw:
        return None
    try:
        return json.loads(raw.decode("utf-8", "replace"))
    except Exception:
        return None


def json_dumps(doc):
    return json.dumps(doc, ensure_ascii=False).encode("utf-8")


def json_request(method, url, doc, headers=None, timeout=DEFAULT_TIMEOUT, raw_body=None):
    """发一次 JSON 请求。`raw_body` 用于「签名与发送必须共用同一串字节」的场景。"""
    merged = dict(headers or {})
    if raw_body is not None:
        body = raw_body
        merged.setdefault("Content-Type", "application/json")
    elif doc is not None:
        body = json_dumps(doc)
        merged.setdefault("Content-Type", "application/json")
    else:
        body = None
    resp = request(method, url, merged, body, timeout)
    try:
        raw = read_all(resp)
        status = resp.status
    finally:
        resp.close()
    return status, parse_json(raw), raw
