"""本地 MCP 示例服务器（仅用标准库，无需安装依赖）。

网关会把它登记为「本地」服务器，自动分配 127.0.0.1 的随机端口并通过环境变量传入：
  AIGW_MCP_PORT / PORT  实际监听端口
也可以自己在 mcp.json 的 args / env 里用 {{port}} 占位符引用该端口。

提供的工具：
  echo  回显输入文本
  now   返回服务器当前时间
"""

import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOOLS = [
    {
        "name": "echo",
        "description": "回显传入的文本",
        "inputSchema": {
            "type": "object",
            "properties": {"text": {"type": "string", "description": "要回显的文本"}},
            "required": ["text"],
        },
    },
    {
        "name": "now",
        "description": "返回服务器当前时间",
        "inputSchema": {"type": "object", "properties": {}},
    },
]


def call_tool(name, arguments):
    if name == "echo":
        return {"content": [{"type": "text", "text": str(arguments.get("text", ""))}], "isError": False}
    if name == "now":
        text = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime())
        return {"content": [{"type": "text", "text": text}], "isError": False}
    return {"content": [{"type": "text", "text": f"未知工具: {name}"}], "isError": True}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # 避免刷屏
        pass

    def _json(self, status, payload):
        data = b"" if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        if data:
            self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if data:
            self.wfile.write(data)

    def do_GET(self):
        self._json(405, {"error": "use POST"})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
            msg = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            self._json(400, {"jsonrpc": "2.0", "error": {"code": -32700, "message": "parse error"}})
            return

        method = msg.get("method")
        rid = msg.get("id")

        # 通知不需要响应
        if method == "notifications/initialized":
            self._json(202, None)
            return

        if method == "initialize":
            result = {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "local-demo", "version": "1.0.0"},
            }
        elif method == "tools/list":
            result = {"tools": TOOLS}
        elif method == "tools/call":
            params = msg.get("params") or {}
            result = call_tool(params.get("name"), params.get("arguments") or {})
        elif method == "ping":
            result = {}
        else:
            if rid is not None:
                self._json(200, {"jsonrpc": "2.0", "id": rid,
                                 "error": {"code": -32601, "message": f"method not found: {method}"}})
            else:
                self._json(202, None)
            return

        if rid is None:
            self._json(202, None)
            return
        self._json(200, {"jsonrpc": "2.0", "id": rid, "result": result})


def main():
    port = int(os.environ.get("AIGW_MCP_PORT") or os.environ.get("PORT") or "9100")
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    print(f"local-demo MCP listening on 127.0.0.1:{port}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
