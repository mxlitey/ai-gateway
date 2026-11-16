"""Qoder 插件入口：OpenAI 兼容转发 + 账号控制台。

插件契约见 docs/plugin-spec.md：
  - GET  /health                 进程存活探针（不探测上游）
  - GET  /v1/models              按账号所在站点返回可用模型
  - POST /v1/chat/completions    OpenAI 兼容对话（含 SSE 流式）
  - GET  /console/accounts       账号列表 / 登录方式
  - DELETE /console/accounts/{id}
  - POST /console/accounts       手工新增（仅脚本使用，支持 pt- PAT）
  - POST /console/accounts/{id}/checkin
  - POST /console/accounts/{id}/quota
  - POST /console/login/{method}
  - GET  /console/login/{method}?state=...
"""

import json
import os
import re
import sys
import threading
import time
import traceback
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
if BASE_DIR not in sys.path:
    sys.path.insert(0, BASE_DIR)

import qoder_accounts  # noqa: E402
import upstream  # noqa: E402


def _env(name):
    value = os.environ.get(name) or ""
    return value.strip()


PORT = int(_env("AI_GATEWAY_PLUGIN_PORT") or 0)
if PORT <= 0:
    sys.exit("AI_GATEWAY_PLUGIN_PORT 未设置，插件无法启动")

DATA_DIR = _env("AI_GATEWAY_PLUGIN_DATA")
if not DATA_DIR:
    sys.exit("AI_GATEWAY_PLUGIN_DATA 未设置，插件无法启动")

ACCOUNTS_DIR = os.path.join(DATA_DIR, "accounts")
os.makedirs(ACCOUNTS_DIR, exist_ok=True)

POOL = qoder_accounts.AccountPool(
    ACCOUNTS_DIR,
    log=lambda msg: print("[qoder] " + str(msg), flush=True))
POOL.load()

LOGIN_METHODS = [
    {"id": "oauth-cn", "label": "国内站设备登录",
     "hint": "在浏览器完成 Qoder 授权，插件自动轮询结果"},
    {"id": "oauth-intl", "label": "国际站设备登录",
     "hint": "在浏览器完成 Qoder 授权，插件自动轮询结果"},
    {"id": "pat-cn", "label": "国内站 PAT 导入",
     "hint": "粘贴 pt- 开头的个人访问令牌",
     "fields": [
         {"name": "pat", "label": "个人访问令牌", "kind": "secret",
          "placeholder": "pt-..."},
         {"name": "name", "label": "备注名", "required": False},
     ]},
    {"id": "pat-intl", "label": "国际站 PAT 导入",
     "hint": "粘贴 pt- 开头的个人访问令牌",
     "fields": [
         {"name": "pat", "label": "个人访问令牌", "kind": "secret",
          "placeholder": "pt-..."},
         {"name": "name", "label": "备注名", "required": False},
     ]},
]


# ---------------------------------------------------------------------------
# 账号视图
# ---------------------------------------------------------------------------
def account_view(acc):
    view = {
        "id": acc.uid,
        "name": acc.nickname or acc.note or acc.uid[:8],
        "site": acc.realm,
        "enabled": bool(acc.enabled),
        "plan": acc.plan or "",
        "last_error": _redact(acc.last_error or "")[:200],
    }
    exp = getattr(acc, "expires_at", 0) or 0
    view["expires_at"] = int(float(exp) * 1000) if exp else 0
    credits = None
    try:
        credits = qoder_accounts.effective_remain(acc.credits)
    except Exception:
        credits = None
    if credits is not None:
        try:
            view["credits"] = float(credits)
        except (TypeError, ValueError):
            pass
    try:
        view["checked_in_today"] = bool(acc._checkin_stamped_today())
    except Exception:
        view["checked_in_today"] = False
    streak = getattr(acc, "_plugin_streak", None)
    if streak is not None:
        view["streak_days"] = int(streak)
    return view


def find_account(acc_id):
    if not acc_id:
        return None
    return POOL.get(acc_id)


def pick_account(acc_id):
    """按 X-Plugin-Account 选号；缺失时回退到第一个可用账号（直连调试）。"""
    acc = find_account(acc_id)
    if acc is not None:
        return acc
    for candidate in list(POOL.accounts):
        if candidate.enabled:
            return candidate
    return None


def _truncate_bytes(text, limit=240):
    raw = (text or "").encode("utf-8")
    if len(raw) <= limit:
        return text
    return raw[:limit].decode("utf-8", "ignore")


_SECRET_PATTERNS = (
    re.compile(r"(?i)bearer\s+[A-Za-z0-9._\-]+"),
    re.compile(r"\bpt-[A-Za-z0-9._\-]{6,}"),
    re.compile(r"\beyJ[A-Za-z0-9._\-]{20,}"),
)


def _redact(text):
    text = str(text or "")
    for pattern in _SECRET_PATTERNS:
        text = pattern.sub("[已隐藏]", text)
    return text


# ---------------------------------------------------------------------------
# HTTP 处理
# ---------------------------------------------------------------------------
class Handler(BaseHTTPRequestHandler):
    server_version = "QoderPlugin/1.0"
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print("[qoder] %s - %s" % (self.address_string(), fmt % args), flush=True)

    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, BrokenPipeError):
            pass

    # -- 基础设施 ---------------------------------------------------------
    def _read_json(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except (TypeError, ValueError):
            length = 0
        if length <= 0:
            return {}
        if length > 1024 * 1024:
            raise ValueError("请求体超过 1MB")
        raw = self.rfile.read(length)
        try:
            data = json.loads(raw.decode("utf-8"))
        except Exception:
            raise ValueError("请求体不是合法 JSON")
        if not isinstance(data, dict):
            raise ValueError("请求体必须是 JSON 对象")
        return data

    def _send_json(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _send_error(self, status, message, err_type=None):
        types = {400: "invalid_request", 401: "auth_failed", 403: "forbidden",
                 404: "not_found", 429: "rate_limited", 500: "plugin_error",
                 502: "upstream_error", 503: "upstream_error"}
        self._send_json(status, {
            "error": _truncate_bytes(_redact(message)),
            "type": err_type or types.get(status, "plugin_error"),
        })

    def _route(self):
        parsed = urllib.parse.urlsplit(self.path)
        return parsed.path.rstrip("/") or "/", urllib.parse.parse_qs(parsed.query)

    # -- GET --------------------------------------------------------------
    def do_GET(self):
        path, query = self._route()
        try:
            if path == "/health":
                return self._send_json(200, {"status": "ok"})
            if path == "/v1/models":
                return self._handle_models()
            if path == "/console/accounts":
                return self._handle_accounts()
            if path.startswith("/console/login/"):
                method = path[len("/console/login/"):]
                state = (query.get("state") or [""])[0]
                return self._handle_login_poll(method, state)
            return self._send_error(400, "未知路径：%s" % path)
        except Exception as exc:
            traceback.print_exc()
            return self._send_error(500, "插件内部错误：%s" % exc)

    # -- POST -------------------------------------------------------------
    def do_POST(self):
        path, _ = self._route()
        try:
            if path == "/v1/chat/completions":
                return self._handle_chat()
            if path == "/console/accounts":
                return self._handle_account_create()
            if path.startswith("/console/accounts/") and path.endswith("/checkin"):
                acc_id = urllib.parse.unquote(
                    path[len("/console/accounts/"):-len("/checkin")].strip("/"))
                return self._handle_checkin(acc_id)
            if path.startswith("/console/accounts/") and path.endswith("/quota"):
                acc_id = urllib.parse.unquote(
                    path[len("/console/accounts/"):-len("/quota")].strip("/"))
                return self._handle_quota(acc_id)
            if path.startswith("/console/login/"):
                method = path[len("/console/login/"):]
                return self._handle_login_start(method)
            return self._send_error(400, "未知路径：%s" % path)
        except Exception as exc:
            traceback.print_exc()
            return self._send_error(500, "插件内部错误：%s" % exc)

    # -- DELETE -----------------------------------------------------------
    def do_DELETE(self):
        path, _ = self._route()
        try:
            if path.startswith("/console/accounts/"):
                acc_id = urllib.parse.unquote(
                    path[len("/console/accounts/"):].strip("/"))
                if not acc_id:
                    return self._send_error(400, "缺少账号 id")
                acc = find_account(acc_id)
                if acc is None:
                    return self._send_error(404, "账号不存在：%s" % acc_id)
                POOL.remove(acc_id)
                return self._send_json(200, {"success": True})
            return self._send_error(400, "未知路径：%s" % path)
        except Exception as exc:
            traceback.print_exc()
            return self._send_error(500, "插件内部错误：%s" % exc)

    # -- 模型 -------------------------------------------------------------
    def _handle_models(self):
        acc_id = (self.headers.get("X-Plugin-Account") or "").strip()
        acc = find_account(acc_id) if acc_id else None
        models = upstream.list_models(acc)
        return self._send_json(200, {"object": "list", "data": models})

    # -- 对话 -------------------------------------------------------------
    def _handle_chat(self):
        try:
            payload = self._read_json()
        except ValueError as exc:
            return self._send_error(400, str(exc))

        model = str(payload.get("model") or "").strip()
        if not model:
            return self._send_error(400, "缺少 model 参数")
        if not payload.get("messages"):
            return self._send_error(400, "缺少 messages 参数")

        acc_id = (self.headers.get("X-Plugin-Account") or "").strip()
        acc = pick_account(acc_id)
        if acc is None:
            return self._send_error(503, "插件内没有可用账号，请先在控制台登录",
                                    "upstream_error")
        if not acc.enabled:
            return self._send_error(403, "账号 %s 已在插件侧停用" % acc.uid)

        cred_before = (acc.access_token, acc.refresh_token, acc.expires_at)
        try:
            if not acc.ready():
                now = time.time()
                if acc.cooldown_until and acc.cooldown_until > now:
                    return self._send_error(429, "账号 %s 正在冷却中" % acc.uid)
                if acc.balance_until and acc.balance_until > now:
                    return self._send_error(403, "账号 %s 额度已用尽，暂停调用" % acc.uid)
                if acc.breaker_until and acc.breaker_until > now:
                    return self._send_error(503, "账号 %s 处于上游故障熔断窗口" % acc.uid)
                return self._send_error(
                    401, "账号 %s 凭据不可用：%s" % (acc.uid, acc.last_error or "需要重新登录"))
        except Exception as exc:
            return self._send_error(401, "账号 %s 刷新凭据失败：%s" % (acc.uid, exc))

        if (acc.access_token, acc.refresh_token, acc.expires_at) != cred_before:
            try:
                acc.save(POOL.dir)
            except Exception:
                pass

        model_key = upstream.resolve_model(model, acc.realm)
        try:
            body_obj = upstream.build_body(payload, acc, model_key)
            resp = upstream.open_chat(acc, model_key, body_obj)
        except upstream.UpstreamError as exc:
            status, err_type, message = upstream.map_error(exc)
            return self._send_error(status, message, err_type)
        except Exception as exc:
            traceback.print_exc()
            return self._send_error(502, "请求 Qoder 上游失败：%s" % exc,
                                    "upstream_error")

        if not payload.get("stream"):
            try:
                result = upstream.aggregate(resp, model_key)
            except upstream.UpstreamError as exc:
                status, err_type, message = upstream.map_error(exc)
                return self._send_error(status, message, err_type)
            except Exception as exc:
                traceback.print_exc()
                return self._send_error(502, "解析上游响应失败：%s" % exc,
                                        "upstream_error")
            finally:
                try:
                    resp.close()
                except Exception:
                    pass
            return self._send_json(200, result)

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        try:
            for chunk in upstream.stream_sse(resp):
                self.wfile.write(chunk)
                self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass
        except upstream.UpstreamError as exc:
            _, _, message = upstream.map_error(exc)
            event = {"error": {"message": message, "type": "upstream_error"}}
            try:
                self.wfile.write(("data: " + json.dumps(event, ensure_ascii=False)
                                  + "\n\n").encode("utf-8"))
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()
            except Exception:
                pass
        except Exception as exc:
            traceback.print_exc()
            print("[qoder] stream aborted: %s" % exc, flush=True)
        finally:
            try:
                resp.close()
            except Exception:
                pass

    # -- 控制台：账号 -----------------------------------------------------
    def _handle_accounts(self):
        accounts = [account_view(a) for a in list(POOL.accounts)]
        payload = {"accounts": accounts, "login_methods": LOGIN_METHODS}
        if not accounts:
            payload["notice"] = "还没有账号，点击上方按钮登录 Qoder"
        return self._send_json(200, payload)

    def _handle_account_create(self):
        try:
            body = self._read_json()
        except ValueError as exc:
            return self._send_error(400, str(exc))
        token = str(body.get("token") or body.get("pat") or "").strip()
        site = str(body.get("site") or "cn").strip() or "cn"
        name = str(body.get("name") or "").strip()
        if site not in ("cn", "intl"):
            return self._send_error(400, "site 只能是 cn 或 intl")
        if not token.startswith("pt-"):
            return self._send_error(400, "仅支持 pt- 开头的个人访问令牌")
        try:
            acc = POOL.import_pat(token, realm=site)
        except ValueError as exc:
            return self._send_error(400, "PAT 导入失败：%s" % exc)
        except Exception as exc:
            return self._send_error(400, "PAT 校验失败：%s" % exc)
        if name:
            acc.note = name
            acc.save(POOL.dir)
        return self._send_json(200, {"message": "账号已加入",
                                     "account": account_view(acc)})

    def _handle_checkin(self, acc_id):
        acc = find_account(acc_id)
        if acc is None:
            return self._send_error(404, "账号不存在：%s" % acc_id)
        try:
            acc.ready()
            result = acc.checkin()
        except Exception as exc:
            return self._send_error(400, "签到失败：%s" % exc)
        if not result.get("ok"):
            try:
                acc.save(POOL.dir)
            except Exception:
                pass
            return self._send_error(400, "签到失败：%s" % (result.get("error") or result))
        try:
            acc.save(POOL.dir)
        except Exception:
            pass
        if result.get("streak_days") is not None:
            acc._plugin_streak = result["streak_days"]
        message = result.get("msg") or result.get("message") or "签到成功"
        if result.get("reward_credits"):
            message += "，+%s 积分" % result["reward_credits"]
        return self._send_json(200, {"message": message,
                                     "account": account_view(acc)})

    def _handle_quota(self, acc_id):
        acc = find_account(acc_id)
        if acc is None:
            return self._send_error(404, "账号不存在：%s" % acc_id)
        try:
            acc.ready()
            result = acc.fetch_credits()
        except Exception as exc:
            return self._send_error(400, "额度查询失败：%s" % exc)
        if isinstance(result, dict) and result.get("ok") is False:
            try:
                acc.save(POOL.dir)
            except Exception:
                pass
            reason = result.get("error") or "上游未返回额度数据"
            return self._send_error(400, "额度查询失败：%s" % reason)
        try:
            acc.fetch_plan()
        except Exception:
            pass
        try:
            acc.save(POOL.dir)
        except Exception:
            pass
        return self._send_json(200, {"message": "额度已刷新",
                                     "account": account_view(acc)})

    # -- 控制台：登录 -----------------------------------------------------
    def _handle_login_start(self, method):
        try:
            body = self._read_json()
        except ValueError as exc:
            return self._send_error(400, str(exc))
        if method in ("oauth-cn", "oauth-intl"):
            realm = "intl" if method.endswith("intl") else "cn"
            try:
                result = POOL.start_login(realm=realm, platform="CLI")
            except Exception as exc:
                return self._send_error(400, "发起登录失败：%s" % exc)
            return self._send_json(200, {
                "status": "pending",
                "message": "请在浏览器中完成 Qoder 授权",
                "url": result["authUrl"],
                "state": result["state"],
            })
        if method in ("pat-cn", "pat-intl"):
            realm = "intl" if method.endswith("intl") else "cn"
            pat = str(body.get("pat") or "").strip()
            name = str(body.get("name") or "").strip()
            if not pat:
                return self._send_json(200, {"status": "error",
                                             "message": "请填写个人访问令牌"})
            if not pat.startswith("pt-"):
                return self._send_json(200, {"status": "error",
                                             "message": "令牌必须以 pt- 开头"})
            try:
                acc = POOL.import_pat(pat, realm=realm)
            except ValueError as exc:
                return self._send_json(200, {"status": "error",
                                             "message": "令牌校验失败：%s" % exc})
            except Exception as exc:
                text = str(exc)
                if any(code in text for code in ("400", "401", "403")):
                    return self._send_json(200, {
                        "status": "error",
                        "message": "令牌无效或已过期，请重新生成个人访问令牌"})
                return self._send_json(200, {"status": "error",
                                             "message": "导入失败：%s" % exc})
            if name:
                acc.note = name
                try:
                    acc.save(POOL.dir)
                except Exception:
                    pass
            return self._send_json(200, {"status": "ok",
                                         "message": "账号已加入"})
        return self._send_error(400, "未知登录方式：%s" % method)

    def _handle_login_poll(self, method, state):
        if method not in ("oauth-cn", "oauth-intl"):
            return self._send_json(200, {"status": "error",
                                         "message": "该登录方式不需要轮询"})
        if not state:
            return self._send_json(200, {"status": "expired",
                                         "message": "登录会话已失效，请重新发起"})
        try:
            result = POOL.poll_login(state)
        except Exception as exc:
            return self._send_json(200, {"status": "error",
                                         "message": "查询登录状态失败：%s" % exc})
        status = result.get("status")
        if status == "ok":
            return self._send_json(200, {"status": "ok", "message": "登录成功"})
        if status == "pending":
            return self._send_json(200, {"status": "pending",
                                         "message": result.get("message") or "等待授权"})
        if status == "expired":
            return self._send_json(200, {"status": "expired",
                                         "message": result.get("message") or "授权已过期"})
        return self._send_json(200, {"status": "expired",
                                     "message": result.get("message") or "登录会话已失效"})


def main():
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("[qoder] listening on 127.0.0.1:%d, accounts=%d"
          % (PORT, len(POOL.accounts)), flush=True)

    def stop(*_):
        threading.Thread(target=server.shutdown, daemon=True).start()

    for sig_name in ("SIGINT", "SIGTERM", "SIGBREAK"):
        sig = getattr(__import__("signal"), sig_name, None)
        if sig is not None:
            try:
                __import__("signal").signal(sig, stop)
            except Exception:
                pass

    try:
        server.serve_forever()
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
