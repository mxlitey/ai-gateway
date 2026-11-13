#!/usr/bin/env python3
"""可复用的生图命令行工具（直连 image-gen MCP，支持本地参考图）。

为什么需要它：image-gen MCP 部署在远端时读不到本机路径（image_path 会被拼成
/mcp/image-generation/C:\\...），本地图片必须转成 base64 data URI；而 base64
有几十万字符，不能塞进对话里，所以在本地脚本里完成编码最省事。

启动时自动完成两件事：
  1. 寻找 MCP 配置（--config / 环境变量 / 常见客户端路径）；
  2. 调 tools/list 自动识别 generate_image / get_image_job 的实际工具名，
     聚合入口（/mcp）与单服务器入口（/mcp/image-gen）都能用。

模型不在脚本里查：--model 必填。请先按 SKILL.md「先选模型」一节的流程
查 GET /v1/models?type=image，把候选交给用户选定后再传进来。

用法示例：
  # 带本地参考图（图 1、图 2 对应提示词里的 Picture 1/2）
  python image-gen-cli.py --model MiniMax/MiniMax-H3-Image \
      --prompt "Picture 1 作背景，Picture 2 的猫走进画面" \
      --images "C:/path/a.jpg" "C:/path/b.png" --out "output/result.png"

  # 文生图
  python image-gen-cli.py --model MiniMax/MiniMax-H3-Image --prompt "一只橘猫在雪地里"

  # 显式指定 MCP 地址与密钥（不读配置文件）
  python image-gen-cli.py --model MiniMax/MiniMax-H3-Image --prompt "..." \
      --url "https://host/mcp/image-gen" --token "sk-xxx"

  # 参考图很大时自动传到 Flyimg 图床改传公网直链（默认已启用，无需额外参数）
  python image-gen-cli.py --model MiniMax/MiniMax-H3-Image --prompt "..." --images "big.jpg"

  # 换用其它图床，或传 --worker "" 完全禁用上传
  python image-gen-cli.py --model MiniMax/MiniMax-H3-Image --prompt "..." \
      --images "big.jpg" --worker "https://your-worker.workers.dev"
"""

import argparse
import base64
import io
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime

# 参考图上限与 image-generation MCP 服务端保持一致：
# base64 合计不超过 7 M 字符（解码后约 5.5 MB），留足网关 8 MB 请求体余量。
MAX_TOTAL_B64_CHARS = 7 << 20
MAX_SINGLE_BYTES = 3 << 20
MAX_IMAGES = 4

SUBMIT_RETRIES = 3

CONFIG_ENV = "IMAGE_GEN_MCP_CONFIG"

# 默认图床：参考图超出 base64 上限时自动上传，改用公网直链
DEFAULT_WORKER = "https://flyimg.lii.qzz.io"

# 固定 User-Agent：urllib 默认 UA 会被 Cloudflare 以 Error 1010 拦掉
USER_AGENT = "image-gen-cli/1.1"


def log(msg):
    print(msg, file=sys.stderr, flush=True)


# ---------- 配置探测 ----------

def config_candidates():
    """按优先级列出可能的 MCP 配置文件路径。"""
    home = os.path.expanduser("~")
    appdata = os.environ.get("APPDATA") or os.path.join(home, "AppData", "Roaming")
    out = []
    env_path = os.environ.get(CONFIG_ENV)
    if env_path:
        out.append(env_path)
    out += [
        os.path.join(home, ".workbuddy", "mcp.json"),
        os.path.join(home, ".trae-cn", "mcp.json"),
        os.path.join(home, ".trae", "mcp.json"),
        os.path.join(home, ".cursor", "mcp.json"),
        os.path.join(home, ".codeium", "windsurf", "mcp_config.json"),
        os.path.join(appdata, "Claude", "claude_desktop_config.json"),
        os.path.join(os.getcwd(), "mcp.json"),
        os.path.join(os.getcwd(), ".mcp.json"),
    ]
    return out


def _server_entries(cfg):
    """兼容 mcpServers 与 servers 两种写法。"""
    for field in ("mcpServers", "servers", "mcp"):
        servers = cfg.get(field)
        if isinstance(servers, dict):
            for name, srv in servers.items():
                if isinstance(srv, dict):
                    yield name, srv


def _header_value(srv, name):
    headers = srv.get("headers")
    if not isinstance(headers, dict):
        return ""
    for k, v in headers.items():
        if k.lower() == name.lower() and isinstance(v, str):
            return v
    return ""


def _pick_server(cfg):
    """优先选名字或地址里含 image 的服务器，否则选第一个有 url 的。"""
    best, fallback = None, None
    for name, srv in _server_entries(cfg):
        url = srv.get("url")
        if not isinstance(url, str) or not url.strip():
            continue  # command 型（stdio）服务器用不了
        if fallback is None:
            fallback = (name, srv)
        haystack = (name + " " + url).lower()
        if "image" in haystack and best is None:
            best = (name, srv)
    return best or fallback


def load_endpoint(explicit_path="", explicit_url="", explicit_token=""):
    """返回 (mcp_url, authorization, 配置来源描述)。"""
    if explicit_url:
        auth = explicit_token.strip()
        if auth and not auth.lower().startswith("bearer "):
            auth = "Bearer " + auth
        if not auth:
            raise RuntimeError("使用 --url 时必须同时提供 --token")
        return explicit_url.strip(), auth, "--url 参数"

    paths = [explicit_path] if explicit_path else config_candidates()
    for path in paths:
        if not os.path.isfile(path):
            continue
        try:
            with open(path, "r", encoding="utf-8") as f:
                cfg = json.load(f)
        except (OSError, ValueError):
            continue
        picked = _pick_server(cfg)
        if not picked:
            continue
        name, srv = picked
        auth = _header_value(srv, "Authorization") or _header_value(srv, "X-Api-Key")
        if auth.lower().startswith("x-api-key:"):
            auth = auth.split(":", 1)[1].strip()
        if not auth:
            raise RuntimeError("%s 里的 %s 缺少 Authorization" % (path, name))
        if not auth.lower().startswith("bearer "):
            auth = "Bearer " + auth
        return srv["url"].strip(), auth, "%s → %s" % (path, name)

    if explicit_path:
        raise RuntimeError("指定的配置文件不存在或没有可用的 url 型 MCP 服务器：%s" % explicit_path)
    raise RuntimeError(
        "没有找到可用的 MCP 配置，已尝试：\n  " + "\n  ".join(paths) +
        "\n请用 --config 指定文件，或用 --url/--token 直接给出地址与密钥，"
        "也可设置环境变量 %s。" % CONFIG_ENV
    )


def gateway_base(mcp_url):
    """从 MCP 地址推导网关根地址，便于在报错里提示 /v1/models 的完整地址。"""
    parsed = urllib.parse.urlparse(mcp_url)
    if not parsed.scheme or not parsed.netloc:
        raise RuntimeError("MCP 地址不是合法的 http(s) URL：%s" % mcp_url)
    return "%s://%s" % (parsed.scheme, parsed.netloc)


# ---------- MCP 客户端 ----------

class MCP:
    def __init__(self, url, auth):
        self.url, self.auth = url, auth
        self.session_id = None
        self.mid = 0
        self.tool_names = {}

    def post(self, payload, timeout=180):
        self.mid += 1
        if "id" in payload:
            payload = dict(payload, id=self.mid)
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "Authorization": self.auth,
        }
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        req = urllib.request.Request(
            self.url, data=json.dumps(payload).encode(), headers=headers, method="POST"
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                sid = r.headers.get("Mcp-Session-Id")
                if sid:
                    self.session_id = sid
                raw = r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", "replace")
            raise RuntimeError("HTTP %s: %s" % (e.code, body[:1500]))
        except urllib.error.URLError as e:
            raise RuntimeError("连接 MCP 失败：%s" % (e.reason or e))
        return self._parse(raw)

    @staticmethod
    def _parse(raw):
        raw = raw.strip()
        if not raw:
            return None
        if raw.startswith("{"):
            return json.loads(raw)
        events = [
            json.loads(line[5:].strip())
            for line in raw.splitlines()
            if line.startswith("data:") and line[5:].strip() not in ("", "[DONE]")
        ]
        return events[-1] if events else None

    def start(self):
        self.post({
            "jsonrpc": "2.0", "id": 0, "method": "initialize",
            "params": {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "image-gen-cli", "version": "1.1"},
            },
        })
        self.post({"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})
        self.detect_tools()

    def detect_tools(self):
        """从 tools/list 里认出 generate_image / get_image_job（含 alias__ 前缀）。"""
        resp = self.post({"jsonrpc": "2.0", "id": 0, "method": "tools/list", "params": {}})
        tools = ((resp or {}).get("result") or {}).get("tools") or []
        names = [t.get("name", "") for t in tools if isinstance(t, dict)]
        for base in ("generate_image", "get_image_job"):
            match = next((n for n in names if n == base or n.endswith("__" + base)), None)
            if not match:
                raise RuntimeError(
                    "MCP 服务里没有 %s 工具；可用工具：%s" % (base, ", ".join(names) or "（空）")
                )
            self.tool_names[base] = match
        log("工具：%s / %s" % (self.tool_names["generate_image"], self.tool_names["get_image_job"]))

    def call(self, base_name, args):
        name = self.tool_names.get(base_name, base_name)
        resp = self.post({
            "jsonrpc": "2.0", "id": 0, "method": "tools/call",
            "params": {"name": name, "arguments": args},
        })
        if not resp:
            return {}
        if "error" in resp:
            raise RuntimeError("MCP error: %s" % json.dumps(resp["error"], ensure_ascii=False))
        result = resp.get("result", {})
        if result.get("isError"):
            raise RuntimeError("tool error: %s" % json.dumps(result, ensure_ascii=False)[:1500])
        text = "\n".join(
            c.get("text", "") for c in result.get("content", []) if c.get("type") == "text"
        )
        try:
            return json.loads(text)
        except Exception:
            return {"raw": text}


def is_transient(err):
    """网络抖动、5xx、429 值得重试；业务错误（参数、模型未开通等）不重试。"""
    msg = str(err)
    if msg.startswith("tool error:"):
        return False
    for marker in ("HTTP 5", "HTTP 429", "timed out", "timeout", "Connection",
                   "Remote end closed", "连接 MCP 失败", "URLError"):
        if marker in msg:
            return True
    return False


def call_with_retry(mcp, base_name, args, attempts=SUBMIT_RETRIES):
    last = None
    for i in range(attempts):
        try:
            return mcp.call(base_name, args)
        except RuntimeError as e:
            last = e
            if not is_transient(e) or i == attempts - 1:
                raise
            delay = 2 * (2 ** i)
            log("调用失败（%s），%d 秒后重试（%d/%d）" % (str(e)[:120], delay, i + 1, attempts - 1))
            time.sleep(delay)
    raise last


# ---------- 参考图 ----------

def sniff_mime(raw):
    """按文件头判断真实图片类型，避免扩展名写错导致上游解码失败。"""
    if raw.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if raw.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if raw[:4] == b"RIFF" and raw[8:12] == b"WEBP":
        return "image/webp"
    if raw[:6] in (b"GIF87a", b"GIF89a"):
        return "image/gif"
    return ""


def build_image_urls(paths, worker_url=""):
    """把本地参考图转成工具参数。

    默认走 base64 data URI；当单张或总量超过 MCP 请求体预算时，
    若配置了 Flyimg Worker 地址则改为上传取公网 URL（请求体只有几百字节），
    否则报错提示压缩或配置图床。
    """
    if len(paths) > MAX_IMAGES:
        raise RuntimeError("参考图最多 %d 张，当前 %d 张" % (MAX_IMAGES, len(paths)))

    files = []
    total_bytes = 0
    for p in paths:
        if not os.path.isfile(p):
            raise RuntimeError("参考图不存在：%s" % p)
        with open(p, "rb") as f:
            raw = f.read()
        mime = sniff_mime(raw)
        if not mime:
            raise RuntimeError(
                "不是可识别的图片（仅支持 PNG/JPEG/WebP/GIF，按文件头判断）：%s" % p
            )
        files.append((p, raw, mime))
        total_bytes += len(raw)

    oversize = [f for f in files if len(f[1]) > MAX_SINGLE_BYTES]
    total_b64 = (total_bytes // 3 + 1) * 4  # 4/3 膨胀后的字符数（上界估算）
    if oversize or total_b64 > MAX_TOTAL_B64_CHARS:
        reason = (
            "单张 %d KB 超过 %d KB" % (len(oversize[0][1]) // 1024, MAX_SINGLE_BYTES // 1024)
            if oversize else
            "合计 %.1f M 字符超过 %.1f M" % (total_b64 / 1048576.0, MAX_TOTAL_B64_CHARS / 1048576.0)
        )
        if not worker_url:
            raise RuntimeError(
                "参考图超出 base64 上限（%s）。请压缩图片、减少张数，"
                "或用 --worker 指定 Flyimg Worker 地址改为传公网 URL" % reason
            )
        log("参考图超出 base64 上限（%s），改用图床直链" % reason)
        return upload_images(files, worker_url)

    urls = []
    for p, raw, mime in files:
        urls.append("data:%s;base64,%s" % (mime, base64.b64encode(raw).decode()))
        log("参考图 %s (%d KB, %s, base64)" % (os.path.basename(p), len(raw) // 1024, mime))
    return urls


def upload_images(files, worker_url, user_tag=""):
    """上传到 Flyimg Worker，返回公网直链列表。

    与官方 upload.sh 使用同一套接口：POST {worker}/upload，
    表单字段 file + user_tag；同一次调用的文件共用一个 user_tag。
    """
    tag = user_tag or "oc_%d_%s" % (int(time.time()), uuid.uuid4().hex[:8])
    urls = []
    for p, raw, mime in files:
        body, content_type = _multipart(
            {"user_tag": tag}, "file", os.path.basename(p), raw, mime
        )
        req = urllib.request.Request(worker_url.rstrip("/") + "/upload", data=body, method="POST")
        req.add_header("Content-Type", content_type)
        req.add_header("Accept", "application/json")
        # Cloudflare 前置的 Worker 会用 Error 1010 拦掉 urllib 默认的 User-Agent，
        # 必须显式给一个；与官方 upload.sh 用的 curl 保持同类标识
        req.add_header("User-Agent", USER_AGENT)
        try:
            with urllib.request.urlopen(req, timeout=180) as r:
                resp = json.loads(r.read().decode("utf-8", "replace"))
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", "replace")
            if e.code == 403 and "1010" in detail:
                raise RuntimeError(
                    "图床拒绝了请求（Cloudflare 1010）；请检查 Worker 是否允许当前 User-Agent"
                )
            raise RuntimeError("上传参考图失败（HTTP %s）：%s" % (e.code, detail[:400]))
        except Exception as e:
            raise RuntimeError("上传参考图失败：%s（检查 Worker 地址是否可访问）" % e)
        url = (resp or {}).get("url")
        if not url:
            raise RuntimeError("图床未返回 url：%s" % json.dumps(resp, ensure_ascii=False)[:400])
        urls.append(url)
        log("已上传 %s -> %s" % (os.path.basename(p), url))
    log("文件管理链接：%s/%s" % (worker_url.rstrip("/"), tag))
    return urls


def _multipart(fields, file_field, filename, data, mime):
    """构造 multipart/form-data 请求体（避免引入第三方库）。"""
    boundary = "----imagegen" + uuid.uuid4().hex
    buf = io.BytesIO()
    for k, v in fields.items():
        buf.write(("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n"
                   % (boundary, k, v)).encode())
    buf.write(("--%s\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n"
               "Content-Type: %s\r\n\r\n" % (boundary, file_field, filename, mime)).encode())
    buf.write(data)
    buf.write(("\r\n--%s--\r\n" % boundary).encode())
    return buf.getvalue(), "multipart/form-data; boundary=" + boundary


def download(url, out_path):
    os.makedirs(os.path.dirname(os.path.abspath(out_path)) or ".", exist_ok=True)
    last = None
    for i in range(SUBMIT_RETRIES):
        try:
            req = urllib.request.Request(url, method="GET")
            req.add_header("User-Agent", USER_AGENT)
            with urllib.request.urlopen(req, timeout=180) as r, open(out_path, "wb") as f:
                f.write(r.read())
            return out_path
        except Exception as e:
            last = e
            if i == SUBMIT_RETRIES - 1:
                raise RuntimeError("下载图片失败：%s" % e)
            time.sleep(2 * (2 ** i))
    raise RuntimeError("下载图片失败：%s" % last)


def main():
    ap = argparse.ArgumentParser(description="image-gen 生图（支持本地参考图）")
    ap.add_argument("--prompt", required=True, help="提示词；参考图按 --images 顺序为 Picture 1/2/...")
    ap.add_argument("--images", nargs="*", default=[], help="本地参考图路径，按顺序")
    ap.add_argument("--urls", nargs="*", default=[], help="公网参考图 URL，拼在 --images 之后")
    ap.add_argument("--model", required=True,
                    help="生图模型名。脚本不负责查询模型，请先按 SKILL 的流程取得候选、"
                         "让用户选好再传进来（例如 MiniMax/MiniMax-H3-Image）")
    ap.add_argument("--size", default="1328x1328")
    ap.add_argument("--n", type=int, default=1)
    ap.add_argument("--negative", default="")
    ap.add_argument("--seed", type=int)
    ap.add_argument("--out", default="", help="输出文件路径；留空则自动命名到 --outdir")
    ap.add_argument("--outdir", default="output")
    ap.add_argument("--wait", type=int, default=12,
                    help="单次阻塞秒数，建议 10-20；过大会触发 CDN 100 秒超时(524)")
    ap.add_argument("--timeout", type=int, default=540, help="整体轮询上限秒数")
    ap.add_argument("--config", default="", help="MCP 配置文件路径；留空自动探测")
    ap.add_argument("--url", default="", help="直接指定 MCP 地址，如 https://host/mcp/image-gen")
    ap.add_argument("--token", default="", help="配合 --url 使用的 sk- 密钥")
    ap.add_argument("--worker", default=os.environ.get("FLYIMG_WORKER_URL", DEFAULT_WORKER),
                    help="Flyimg Worker 地址；参考图超出 base64 上限时改走公网直链"
                         "（默认 %s，传空字符串可禁用）" % DEFAULT_WORKER)
    args = ap.parse_args()

    model = args.model.strip()
    if not model:
        raise RuntimeError(
            "缺少 --model。请先查询网关的可生图模型让用户选择，再把结果传进来：\n"
            "  curl -H \"Authorization: Bearer sk-xxx\" \"<网关地址>/v1/models?type=image\"\n"
            "不带 type=image 会把对话模型也列出来，不能直接照着传。"
        )

    url, auth, source = load_endpoint(args.config, args.url, args.token)
    log("配置：%s" % source)

    mcp = MCP(url, auth)
    mcp.start()

    ref = build_image_urls(args.images, args.worker.strip()) + list(args.urls)
    params = {
        "prompt": args.prompt,
        "model": model,
        "size": args.size,
        "n": args.n,
        "response_format": "url",
        "wait_seconds": args.wait,
    }
    if ref:
        params["image_url"] = ref
    if args.negative:
        params["negative_prompt"] = args.negative
    if args.seed is not None:
        params["seed"] = args.seed

    log("提交生图任务...")
    job = call_with_retry(mcp, "generate_image", params)
    job_id = job.get("job_id") or job.get("id")
    log("status=%s job_id=%s" % (job.get("status"), job_id))

    deadline = time.time() + args.timeout
    while job.get("status") == "running" and job_id and time.time() < deadline:
        time.sleep(5)
        job = call_with_retry(mcp, "get_image_job", {"job_id": job_id})
        log("status=%s elapsed=%sms" % (job.get("status"), job.get("elapsed_ms")))

    if job.get("status") != "succeeded":
        raise RuntimeError("生图未成功：%s" % json.dumps(job, ensure_ascii=False)[:1500])

    images = job.get("images", [])
    saved = []
    for i, img in enumerate(images):
        src = img.get("url")
        if not src:
            continue
        suffix = os.path.splitext(urllib.parse.urlparse(src).path)[1] or ".png"
        if args.out:
            path = args.out if len(images) == 1 else (
                os.path.splitext(args.out)[0] + "_%d%s" % (i + 1, os.path.splitext(args.out)[1] or suffix)
            )
        else:
            path = os.path.join(
                args.outdir, "image-%s-%d%s" % (datetime.now().strftime("%Y%m%d-%H%M%S"), i + 1, suffix)
            )
        download(src, path)
        saved.append(os.path.abspath(path))
        log("已保存 %s" % saved[-1])

    print(json.dumps({"status": "succeeded", "job_id": job_id, "model": model,
                      "files": saved,
                      "urls": [x.get("url") for x in images]}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        log("ERROR: %s" % e)
        sys.exit(1)
