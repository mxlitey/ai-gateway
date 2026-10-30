"""GitHub 搜索 MCP 服务器（仅用 Python 标准库，无第三方依赖）。

按 docs/local-mcp-spec.md 实现 MCP Streamable HTTP：POST /mcp，无状态、无鉴权，
监听 127.0.0.1 上由网关分配的 AIGW_MCP_PORT 端口。

鉴权：可选读取环境变量 GITHUB_TOKEN，其次 GITHUB_PAT。
  未提供时以匿名身份调用 GitHub 公开搜索接口（搜索 10 次/分钟；代码搜索匿名不可用）。
  令牌可在 mcp.json 的 env 里配置。

提供的工具：
  search_repositories     按关键词/语言/star 数搜索仓库
  search_code             在仓库代码里搜索（必须带令牌，10 次/分钟）
  search_issues           搜索 issue（GitHub 的 issue 检索本就包含 PR）
  search_pull_requests    只搜 PR（自动追加 is:pr）
  search_commits          搜索提交记录
  search_users            搜索用户或组织账号
"""

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

API_BASE = "https://api.github.com"
USER_AGENT = "ai-gateway-github-search/1.0"
API_VERSION = "2022-11-28"
HTTP_TIMEOUT = 20

SUPPORTED_VERSIONS = ["2025-06-18", "2025-03-26", "2024-11-05"]
MCP_PATH = "/mcp"

# 单条结果正文字符上限，避免把整个 README 灌进模型上下文
ITEM_TEXT_LIMIT = 500
# 每次返回的最大条数上限（GitHub 搜索接口上限为 100，这里限制到更实用的范围）
MAX_PER_PAGE = 30

SORT_REPO = ["", "stars", "forks", "help-wanted-issues", "updated"]
SORT_ISSUE = ["", "comments", "reactions", "created", "updated"]
SORT_COMMIT = ["", "author-date", "committer-date"]
SORT_USER = ["", "followers", "repositories", "joined"]
ORDER = ["", "desc", "asc"]

SEARCH_TOOLS = [
    {
        "name": "search_repositories",
        "description": "搜索 GitHub 仓库。支持 language:、stars:>1000、pushed:>2026-01-01 等限定符，可按 star / fork / 更新时间排序。",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "搜索关键词与限定符，例如：llm gateway language:go stars:>500"},
                "sort": {"type": "string", "enum": SORT_REPO, "description": "排序字段，留空按最佳匹配"},
                "order": {"type": "string", "enum": ORDER, "description": "排序方向，默认 desc"},
                "per_page": {"type": "integer", "description": "返回条数，1-30，默认 10"},
                "page": {"type": "integer", "description": "页码，从 1 开始"},
            },
            "required": ["query"],
        },
    },
    {
        "name": "search_code",
        "description": "在 GitHub 仓库代码中搜索。支持 language:、repo:、path:、org: 等限定符。只检索默认分支，且必须携带有效令牌。",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "代码检索语句，例如：\"func NewServer\" language:go repo:gin-gonic/gin"},
                "per_page": {"type": "integer", "description": "返回条数，1-30，默认 10"},
                "page": {"type": "integer", "description": "页码，从 1 开始"},
            },
            "required": ["query"],
        },
    },
    {
        "name": "search_issues",
        "description": "搜索 issue（GitHub 的 issue 检索同时包含 PR，可用 is:pr / is:issue 区分）。支持 state:、label:、author:、repo: 等限定符。",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "检索语句，例如：stream timeout label:bug state:open repo:ollama/ollama"},
                "sort": {"type": "string", "enum": SORT_ISSUE, "description": "排序字段，留空按最佳匹配"},
                "order": {"type": "string", "enum": ORDER, "description": "排序方向"},
                "per_page": {"type": "integer", "description": "返回条数，1-30，默认 10"},
                "page": {"type": "integer", "description": "页码，从 1 开始"},
            },
            "required": ["query"],
        },
    },
    {
        "name": "search_pull_requests",
        "description": "只搜索 Pull Request（自动追加 is:pr 限定符）。",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "检索语句，例如：支持 流式 repo:BerriAI/litellm"},
                "sort": {"type": "string", "enum": SORT_ISSUE, "description": "排序字段，留空按最佳匹配"},
                "order": {"type": "string", "enum": ORDER, "description": "排序方向"},
                "per_page": {"type": "integer", "description": "返回条数，1-30，默认 10"},
                "page": {"type": "integer", "description": "页码，从 1 开始"},
            },
            "required": ["query"],
        },
    },
    {
        "name": "search_commits",
        "description": "搜索提交记录。支持 author:、repo:、committer-date: 等限定符。",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "检索语句，例如：fix streaming repo:songquanpeng/one-api"},
                "sort": {"type": "string", "enum": SORT_COMMIT, "description": "排序字段，留空按最佳匹配"},
                "order": {"type": "string", "enum": ORDER, "description": "排序方向"},
                "per_page": {"type": "integer", "description": "返回条数，1-30，默认 10"},
                "page": {"type": "integer", "description": "页码，从 1 开始"},
            },
            "required": ["query"],
        },
    },
    {
        "name": "search_users",
        "description": "搜索用户或组织账号。支持 type:user、type:org、location:、followers: 等限定符。",
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "检索语句，例如：location:china type:org followers:>500"},
                "sort": {"type": "string", "enum": SORT_USER, "description": "排序字段，留空按最佳匹配"},
                "order": {"type": "string", "enum": ORDER, "description": "排序方向"},
                "per_page": {"type": "integer", "description": "返回条数，1-30，默认 10"},
                "page": {"type": "integer", "description": "页码，从 1 开始"},
            },
            "required": ["query"],
        },
    },
]


class ToolError(Exception):
    """工具执行失败：由调用方转成 isError 的文本结果。"""


# ---------- GitHub 调用 ----------

def _token():
    """读取访问令牌；优先 GITHUB_TOKEN，其次 GITHUB_PAT。"""
    for name in ("GITHUB_TOKEN", "GITHUB_PAT"):
        value = (os.environ.get(name) or "").strip()
        if value:
            return value
    return ""


def _error_message(payload):
    """从 GitHub 错误响应里取出 message（响应体可能是字符串或对象）。"""
    if isinstance(payload, dict):
        return str(payload.get("message") or "").strip()
    if isinstance(payload, str):
        try:
            obj = json.loads(payload)
            if isinstance(obj, dict):
                return str(obj.get("message") or "").strip()
        except ValueError:
            pass
        return payload.strip()[:200]
    return ""


def _rate_line(headers):
    """把配额信息压成一行，方便模型判断还能调几次。"""
    if not headers:
        return ""
    remaining = headers.get("X-RateLimit-Remaining")
    if not remaining:
        return ""
    text = "剩余配额 %s 次" % remaining
    try:
        reset = headers.get("X-RateLimit-Reset")
        if reset:
            text += "（%s 重置）" % time.strftime("%H:%M", time.localtime(int(reset)))
    except (TypeError, ValueError):
        pass
    return text


def _get(path, params):
    """发起一次 GitHub REST GET，返回 (payload, headers)；错误统一抛 ToolError。"""
    url = API_BASE + path
    if params:
        url += "?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url, method="GET")
    req.add_header("Accept", "application/vnd.github+json")
    req.add_header("X-GitHub-Api-Version", API_VERSION)
    req.add_header("User-Agent", USER_AGENT)
    tok = _token()
    if tok:
        req.add_header("Authorization", "Bearer " + tok)

    try:
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT) as resp:
            raw = resp.read().decode("utf-8", "replace")
            return json.loads(raw), resp.headers
    except urllib.error.HTTPError as exc:
        body = ""
        try:
            body = exc.read().decode("utf-8", "replace")
        except Exception:
            pass
        payload = body
        try:
            payload = json.loads(body)
        except ValueError:
            pass
        detail = _error_message(payload)
        if exc.code == 401:
            raise ToolError("GitHub 令牌无效或已过期（检查 GITHUB_TOKEN / GITHUB_PAT）")
        if exc.code in (403, 429):
            msg = detail or "触发速率限制或权限不足"
            hint = _rate_line(exc.headers)
            if not tok:
                msg += "；当前为匿名调用，配置 GITHUB_TOKEN 可获得更高配额"
            raise ToolError("GitHub 拒绝请求：%s%s" % (msg, ("；" + hint) if hint else ""))
        if exc.code == 422:
            raise ToolError("检索语句无法解析：%s" % (detail or "请检查限定符与长度"))
        raise ToolError("GitHub 返回 HTTP %d：%s" % (exc.code, detail or "未知错误"))
    except urllib.error.URLError as exc:
        raise ToolError("连接 GitHub 失败：%s" % (exc.reason or exc))
    except Exception as exc:
        raise ToolError("请求 GitHub 失败：%s" % exc)


# ---------- 参数与文本处理 ----------

def _clip(text, limit=ITEM_TEXT_LIMIT):
    """压缩空白并截断，控制单条结果长度。"""
    if not text:
        return ""
    flat = " ".join(str(text).split())
    return flat if len(flat) <= limit else flat[:limit] + "…"


def _int_arg(args, name, default, low, high):
    try:
        value = int(args.get(name) or default)
    except (TypeError, ValueError):
        value = default
    return max(low, min(high, value))


def _search_params(args, default_sort=""):
    """组装搜索接口的公共查询参数，空值不下发。"""
    query = (args.get("query") or "").strip()
    if not query:
        raise ToolError("缺少参数 query")
    if len(query) > 256:
        raise ToolError("query 超过 GitHub 上限 256 字符，请精简限定符")
    params = {
        "q": query,
        "per_page": _int_arg(args, "per_page", 10, 1, MAX_PER_PAGE),
        "page": _int_arg(args, "page", 1, 1, 100),
    }
    sort = (args.get("sort") or default_sort or "").strip()
    if sort:
        params["sort"] = sort
    order = (args.get("order") or "").strip()
    if order:
        params["order"] = order
    return params


# ---------- 结果格式化 ----------

def _fmt_repositories(items):
    if not items:
        return "没有匹配的仓库。"
    out = []
    for i, it in enumerate(items, 1):
        out.append("%d. %s  ★%d · %s · 更新于 %s\n   %s\n   %s" % (
            i,
            it.get("full_name", "?"),
            it.get("stargazers_count", 0),
            it.get("language") or "未知语言",
            (it.get("pushed_at") or "")[:10],
            _clip(it.get("description")) or "（无描述）",
            it.get("html_url", ""),
        ))
    return "\n".join(out)


def _fmt_code(items):
    if not items:
        return "没有匹配的代码。"
    out = []
    for i, it in enumerate(items, 1):
        repo = (it.get("repository") or {}).get("full_name", "?")
        out.append("%d. %s : %s\n   %s" % (i, repo, it.get("path", "?"), it.get("html_url", "")))
        for frag in (it.get("text_matches") or [])[:2]:
            body = _clip(frag.get("fragment"), 200)
            if body:
                out.append("   > %s" % body)
    return "\n".join(out)


def _fmt_issues(items):
    if not items:
        return "没有匹配的 issue。"
    out = []
    for i, it in enumerate(items, 1):
        url = it.get("html_url") or ""
        parts = url.split("/")
        repo = parts[3] + "/" + parts[4] if len(parts) >= 5 else "?"
        out.append("%d. [%s] %s #%d %s\n   %s · 评论 %d\n   %s" % (
            i,
            "PR" if it.get("pull_request") else "issue",
            repo,
            it.get("number", 0),
            _clip(it.get("title"), 160),
            it.get("state", "?"),
            it.get("comments", 0),
            url,
        ))
    return "\n".join(out)


def _fmt_commits(items):
    if not items:
        return "没有匹配的提交。"
    out = []
    for i, it in enumerate(items, 1):
        commit = it.get("commit") or {}
        author = commit.get("author") or {}
        repo = (it.get("repository") or {}).get("full_name", "?")
        out.append("%d. %s @ %s\n   %s\n   %s · %s" % (
            i,
            (it.get("sha") or "")[:8],
            repo,
            _clip(commit.get("message"), 160),
            author.get("name", "?"),
            (author.get("date") or "")[:10],
        ))
    return "\n".join(out)


def _fmt_users(items):
    if not items:
        return "没有匹配的用户。"
    out = []
    for i, it in enumerate(items, 1):
        # /search/users 的条目只有基础字段，不含 followers / public_repos
        out.append("%d. %s（%s）\n   %s" % (
            i, it.get("login", "?"), it.get("type", "User"), it.get("html_url", ""),
        ))
    return "\n".join(out)


def _render(body, payload, headers, shown):
    """给结果加总数前缀与配额后缀。"""
    total = payload.get("total_count")
    prefix = ""
    if isinstance(total, int):
        prefix = "共 %d 条匹配（本次展示 %d 条）\n\n" % (total, shown)
    text = prefix + body
    rate = _rate_line(headers)
    if rate:
        text += "\n\n---\n" + rate
    if not _token():
        text += "\n（未配置 GITHUB_TOKEN，当前为匿名调用：搜索 10 次/分钟，代码搜索不可用）"
    return text


# ---------- 工具分发 ----------

def call_tool(name, args):
    """执行工具，返回 MCP 的 result 结构。"""
    if not isinstance(args, dict):
        args = {}
    args = dict(args)

    try:
        if name == "search_repositories":
            payload, headers = _get("/search/repositories", _search_params(args))
            items = payload.get("items") or []
            body = _fmt_repositories(items)
        elif name == "search_code":
            if not _token():
                raise ToolError("search_code 需要 GitHub 令牌：请配置环境变量 GITHUB_TOKEN 或 GITHUB_PAT")
            payload, headers = _get("/search/code", _search_params(args))
            items = payload.get("items") or []
            body = _fmt_code(items)
        elif name == "search_issues":
            payload, headers = _get("/search/issues", _search_params(args))
            items = payload.get("items") or []
            body = _fmt_issues(items)
        elif name == "search_pull_requests":
            args["query"] = (args.get("query") or "").strip() + " is:pr"
            payload, headers = _get("/search/issues", _search_params(args))
            items = payload.get("items") or []
            body = _fmt_issues(items)
        elif name == "search_commits":
            payload, headers = _get("/search/commits", _search_params(args))
            items = payload.get("items") or []
            body = _fmt_commits(items)
        elif name == "search_users":
            payload, headers = _get("/search/users", _search_params(args))
            items = payload.get("items") or []
            body = _fmt_users(items)
        else:
            return {"content": [{"type": "text", "text": "未知工具: %s" % name}], "isError": True}
    except ToolError as exc:
        return {"content": [{"type": "text", "text": str(exc)}], "isError": True}

    return {"content": [{"type": "text", "text": _render(body, payload, headers, len(items))}],
            "isError": False}


# ---------- MCP 传输层 ----------

def _negotiate(client_version):
    """客户端声明的版本受支持就用它，否则回退到本服务器实现的最新版本。"""
    if client_version in SUPPORTED_VERSIONS:
        return client_version
    return SUPPORTED_VERSIONS[0]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # 不刷屏；令牌也不会进日志
        pass

    def handle_one_request(self):
        # 客户端提前断开 keep-alive 连接时会抛 ConnectionResetError，
        # 属于正常现象；默认实现会打印整段 traceback 污染网关日志，这里静默。
        try:
            super().handle_one_request()
        except (ConnectionResetError, BrokenPipeError):
            self.close_connection = True

    def _json(self, status, payload, allow=None):
        data = b"" if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        if allow:
            self.send_header("Allow", allow)
        if data:
            self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if data:
            self.wfile.write(data)

    def _same_path(self):
        return self.path.split("?")[0].rstrip("/") == MCP_PATH.rstrip("/")

    def do_GET(self):
        # 本服务器不提供 SSE 推送流，按规范返回 405
        self._json(405, {"error": "use POST"}, allow="POST")

    def do_POST(self):
        if not self._same_path():
            self._json(404, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length") or 0)
            raw = self.rfile.read(length) if length > 0 else b"{}"
            msg = json.loads(raw.decode("utf-8", "replace"))
        except (ValueError, UnicodeDecodeError):
            self._json(200, {"jsonrpc": "2.0", "id": None,
                             "error": {"code": -32700, "message": "parse error"}})
            return

        if not isinstance(msg, dict):
            self._json(200, {"jsonrpc": "2.0", "id": None,
                             "error": {"code": -32600, "message": "invalid request"}})
            return

        method = msg.get("method") or ""
        rid = msg.get("id")

        # 通知（无 id，含 notifications/initialized）一律 202，无响应体
        if rid is None:
            self._json(202, None)
            return

        params = msg.get("params")
        if not isinstance(params, dict):
            params = {}

        if method == "initialize":
            result = {
                "protocolVersion": _negotiate(params.get("protocolVersion")),
                "capabilities": {"tools": {"listChanged": False}},
                "serverInfo": {"name": "github-search", "version": "1.0.0"},
            }
        elif method == "tools/list":
            result = {"tools": SEARCH_TOOLS}
        elif method == "tools/call":
            result = call_tool(params.get("name") or "", params.get("arguments") or {})
        elif method == "ping":
            result = {}
        else:
            self._json(200, {"jsonrpc": "2.0", "id": rid,
                             "error": {"code": -32601, "message": "method not found: %s" % method}})
            return

        self._json(200, {"jsonrpc": "2.0", "id": rid, "result": result})


def main():
    global MCP_PATH
    MCP_PATH = os.environ.get("AIGW_MCP_PATH") or "/mcp"

    port = int(os.environ.get("AIGW_MCP_PORT") or os.environ.get("PORT") or "9100")

    # 网关为每台本地服务器创建的私有可写目录；本服务器无状态，仅确保存在
    data_dir = os.environ.get("AIGW_MCP_DATA")
    if data_dir:
        try:
            os.makedirs(data_dir, exist_ok=True)
        except OSError:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    print("github-search MCP listening on 127.0.0.1:%d%s" % (port, MCP_PATH), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
