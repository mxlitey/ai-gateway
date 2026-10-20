# -*- coding: utf-8 -*-
"""极简 HTTP 传输层（只用标准库）。

为什么不用 urllib：对话需要「拿到响应头后边收边转」，http.client 能精确控制
连接超时与逐块读取；urllib 只能整体读。所有请求都显式关闭连接，避免残留。
"""

import gzip
import http.client
import json
import socket
import ssl
import zlib
from urllib.parse import urlsplit

DEFAULT_TIMEOUT = 60


class TransportError(Exception):
    """网络层错误（连不上 / 超时 / 读断流）。"""

    def __init__(self, message, timeout=False):
        super().__init__(message)
        self.timeout = timeout


class StreamResponse:
    """带状态的响应包装：响应头已到，正文可逐块读。"""

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

    def readline(self):
        try:
            return self._resp.readline()
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
    """发一次请求，返回 StreamResponse（正文未读）。"""
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
    """读完正文并按 Content-Encoding 解压（部分网关会 gzip）。"""
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
