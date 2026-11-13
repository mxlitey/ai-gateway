"""生图 MCP 服务器（仅用 Python 标准库，无第三方依赖）。

按 docs/local-mcp-spec.md 实现 MCP Streamable HTTP：POST /mcp，无状态、无鉴权，
监听 127.0.0.1 上由网关分配的 AIGW_MCP_PORT 端口。

它本身不直连 ModelScope：生图统一走网关的 OpenAI 兼容接口
  POST {AIGW_IMAGE_BASE_URL}/v1/images/generations
异步渠道的「提交任务 + 轮询」由网关内部完成，本服务只需一次调用并等待结果。

为什么用作业模型：
  网关调用 MCP 工具受两层限制：单次调用超时（服务器未配置 timeout_ms 时默认 120 秒），
  以及底层 HTTP 连接等待响应头的上限（同样 120 秒，调大 timeout_ms 也突破不了）。
  而生图实测可达 120 秒以上。因此 generate_image 最多等待 wait_seconds（默认 20 秒）：
  完成就直接返回图片，没完成就返回 job_id，由客户端每 5 秒调用 get_image_job 轮询。
  这样每次 MCP 调用都远小于网关超时，无需改网关配置。

环境变量（在 mcp.json 的 env 里配置，均可留空）：
  AIGW_IMAGE_BASE_URL        网关地址，默认 http://127.0.0.1:8787
  AIGW_IMAGE_API_KEY         网关 sk- 密钥；留空时用调用方带来的 Authorization，
                             即面板「MCP 服务器 → 生图 → 密钥」里配的那把
  AIGW_IMAGE_MODEL           默认生图模型（generate_image 未传 model 时使用）
  AIGW_IMAGE_TIMEOUT_SECONDS 单次生图整体超时，默认 600（应大于渠道轮询超时）
  AIGW_IMAGE_SAVE_DIR        图片保存目录，默认 $AIGW_MCP_DATA/images

提供的工具：
  generate_image  生成图片（支持参考图 image_url、尺寸、数量等）
  get_image_job   查询生图作业状态与结果
"""

import base64
import json
import os
import re
import threading
import time
import uuid
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SUPPORTED_VERSIONS = ["2025-06-18", "2025-03-26", "2024-11-05"]
MCP_PATH = "/mcp"

DEFAULT_WAIT_SECONDS = 20
MAX_WAIT_SECONDS = 600
JOB_TTL_SECONDS = 3600
MAX_JOBS = 100
MAX_DOWNLOAD_BYTES = 32 << 20
DOWNLOAD_TIMEOUT = 60
# 参考图上限：网关 MCP 入口整体请求体上限 8 MB（internal/mcp/handler.go 的 maxRequestBody），
# base64 会膨胀约 1/3 且不再额外转义，故 base64 字符数上限取 7 M（解码后约 5.5 MB），
# 留约 0.6 MB 给提示词与 JSON 结构。单张解码后 3 MB 是上游更常见的容忍上限。
MAX_REFERENCE_BYTES = 3 << 20
MAX_REFERENCE_TOTAL_CHARS = 7 << 20
B64_RE = re.compile(r"^[A-Za-z0-9+/]+={0,2}$")
REFERENCE_HINT = (
    "参考图格式无法识别。可用的三种形式：公网 http(s) 图片地址、"
    "data:image/...;base64,... 或纯 base64；如果图片已经是本机文件，直接传 image_path 文件路径即可。"
    "对话里粘贴的图片不会自动进入 MCP 工具参数，需要传文件路径或链接。"
)

IMAGE_TOOLS = [
    {
        "name": "generate_image",
        "description": (
            "调用网关的 /v1/images/generations 生成图片，支持文生图与参考图编辑（image_url）。"
            "单次生图通常需要 1-3 分钟：本工具最多阻塞 wait_seconds 秒（默认 20）；"
            "若返回 status=running，请每 5 秒调用 get_image_job(job_id) 直到 succeeded 或 failed。"
            "参考图需是公网 URL、data URI、纯 base64，或本机文件路径（image_path）；"
            "对话里粘贴的图片不会自动变成工具参数。"
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "prompt": {"type": "string", "description": "提示词"},
                "model": {"type": "string", "description": "生图模型名；留空使用环境变量 AIGW_IMAGE_MODEL"},
                "image_url": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": (
                        "参考图数组，支持公网 http(s) URL、data:image/...;base64,... 或纯 base64；"
                        "传单个字符串也可以。建议单张 ≤2 MB、最多 4 张，"
                        "所有参考图 base64 合计不超过 7 M 字符（解码后约 5.5 MB）。"
                        "对话里粘贴的图片不会被 MCP 工具自动读取，需要链接、base64 或改用 image_path。"
                    ),
                },
                "image_path": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": (
                        "参考图的本机文件路径数组（传单个字符串也可以）。"
                        "服务会读取文件转成 data URI 再发给网关，适合对话附件已落盘成本地文件的场景。"
                        "仅当本服务与文件在同一台机器上时可用；服务部署在远端时路径读不到，"
                        "请改用 image_url 传公网链接或 base64。"
                        "支持 PNG/JPEG/WebP/GIF，单张 ≤3 MB；相对路径按本服务目录解析。"
                    ),
                },
                "size": {"type": "string", "description": "图片尺寸，例如 1024x1024、1328x1328"},
                "n": {"type": "integer", "description": "生成数量，1-4，默认 1"},
                "negative_prompt": {"type": "string", "description": "反向提示词（上游支持时生效）"},
                "seed": {"type": "integer", "description": "随机种子（上游支持时生效）"},
                "loras": {"type": "object", "description": "LoRA 配置对象，原样透传给上游"},
                "extra": {"type": "object", "description": "其它上游参数，原样透传（不覆盖已显式传入的字段）"},
                "response_format": {
                    "type": "string",
                    "enum": ["url", "b64_json"],
                    "description": "默认 url；b64_json 会返回很大的 base64 文本，一般不建议",
                },
                "download": {"type": "boolean", "description": "为 true 时把图片下载到本地并返回路径"},
                "wait_seconds": {"type": "integer", "description": "本次调用最多等待秒数，默认 20，上限 600"},
            },
            "required": ["prompt"],
        },
    },
    {
        "name": "get_image_job",
        "description": "查询 generate_image 创建的作业；status 为 running / succeeded / failed。",
        "inputSchema": {
            "type": "object",
            "properties": {"job_id": {"type": "string", "description": "generate_image 返回的作业 ID"}},
            "required": ["job_id"],
        },
    },
]


class ToolError(Exception):
    """给模型看的可读错误。"""


# ---------- 配置 ----------

def _env(name, default=""):
    return (os.environ.get(name) or default).strip()


def _base_url():
    return (_env("AIGW_IMAGE_BASE_URL", "http://127.0.0.1:8787")).rstrip("/")


def _api_key():
    return _env("AIGW_IMAGE_API_KEY")


def _default_model():
    return _env("AIGW_IMAGE_MODEL")


def _timeout_seconds():
    try:
        value = int(_env("AIGW_IMAGE_TIMEOUT_SECONDS", "600"))
    except ValueError:
        value = 600
    return max(30, min(3600, value))


def _save_dir():
    configured = _env("AIGW_IMAGE_SAVE_DIR")
    if configured:
        return configured
    data_dir = _env("AIGW_MCP_DATA")
    if data_dir:
        return os.path.join(data_dir, "images")
    return os.path.join(os.path.dirname(os.path.abspath(__file__)), ".images")


# 访问本机网关不走系统代理；下载上游图片则沿用默认 opener（可能要走代理）
_LOCAL_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


# ---------- 作业存储 ----------

_JOBS = {}
_JOBS_LOCK = threading.Lock()


def _prune_jobs_locked(now):
    finished = [j for j in _JOBS.values() if j["status"] != "running"]
    for job in finished:
        if now - job["updated"] > JOB_TTL_SECONDS:
            _JOBS.pop(job["id"], None)
    if len(_JOBS) <= MAX_JOBS:
        return
    # 超出上限时按更新时间从旧到新清理已完成的作业
    for job in sorted(finished, key=lambda j: j["updated"]):
        _JOBS.pop(job["id"], None)
        if len(_JOBS) <= MAX_JOBS:
            break


def _new_job(payload):
    now = time.time()
    job = {
        "id": uuid.uuid4().hex,
        "status": "running",
        "model": payload.get("model", ""),
        "created": now,
        "updated": now,
        "result": None,
        "error": "",
    }
    with _JOBS_LOCK:
        _prune_jobs_locked(now)
        _JOBS[job["id"]] = job
    return job


def _job_snapshot(job):
    with _JOBS_LOCK:
        return {
            "status": job["status"],
            "job_id": job["id"],
            "elapsed_ms": int((time.time() - job["created"]) * 1000),
            "result": job["result"],
            "error": job["error"],
        }


def _run_job(job, payload, download, key):
    try:
        result = _request_images(payload, key)
        if download:
            for image in result.get("images", []):
                if image.get("url") and not image.get("path"):
                    try:
                        path, size = _download_image(image["url"])
                        image["path"] = path
                        image["bytes"] = size
                    except ToolError as exc:
                        # 下载失败不影响已生成的图片地址
                        image["download_error"] = str(exc)
        with _JOBS_LOCK:
            job["result"] = result
            job["status"] = "succeeded"
            job["updated"] = time.time()
    except ToolError as exc:
        with _JOBS_LOCK:
            job["error"] = str(exc)
            job["status"] = "failed"
            job["updated"] = time.time()
    except Exception as exc:  # 兜底，避免线程静默退出
        with _JOBS_LOCK:
            job["error"] = "生图失败: %s" % exc
            job["status"] = "failed"
            job["updated"] = time.time()


# ---------- 网关调用 ----------

def _request_images(payload, key):
    # 密钥优先取调用方带来的 Authorization（面板「MCP 服务器 → 密钥」），
    # 没有时回落到 env 里的 AIGW_IMAGE_API_KEY。
    key = (key or "").strip() or _api_key()
    if not key:
        raise ToolError(
            "没有可用的 API 密钥：请在「MCP 服务器 → 生图 → 编辑 → 密钥」里填入面板签发的 sk- 密钥"
            "（也可在 mcp.json 的 env 里设置 AIGW_IMAGE_API_KEY）"
        )

    url = _base_url() + "/v1/images/generations"
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(url, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Accept", "application/json")
    req.add_header("User-Agent", "ai-gateway-image-mcp/1.0")

    started = time.time()
    try:
        with _LOCAL_OPENER.open(req, timeout=_timeout_seconds()) as resp:
            raw = resp.read(16 << 20)
    except urllib.error.HTTPError as exc:
        raise ToolError(_http_error_message(exc))
    except urllib.error.URLError as exc:
        raise ToolError("无法连接网关 %s：%s（确认网关已启动、端口正确）" % (_base_url(), exc.reason or exc))
    except TimeoutError:
        raise ToolError("生图请求超时（%d 秒）；可调大 AIGW_IMAGE_TIMEOUT_SECONDS 或渠道轮询超时" % _timeout_seconds())
    except Exception as exc:
        raise ToolError("请求网关失败：%s" % exc)

    try:
        data = json.loads(raw.decode("utf-8", "replace"))
    except ValueError:
        raise ToolError("网关返回的不是 JSON：%s" % _clip(raw.decode("utf-8", "replace"), 300))

    items = data.get("data") if isinstance(data, dict) else None
    if not isinstance(items, list) or not items:
        raise ToolError("网关未返回图片：%s" % _clip(json.dumps(data, ensure_ascii=False), 300))

    images = []
    for item in items:
        if not isinstance(item, dict):
            continue
        image_url = item.get("url") or ""
        b64 = item.get("b64_json") or ""
        entry = {}
        if image_url:
            entry["url"] = image_url
        if b64:
            # b64 塞进 MCP 文本会非常大：一律落盘，返回本地路径
            path, size = _save_base64(b64)
            entry["path"] = path
            entry["bytes"] = size
        if not entry:
            continue
        images.append(entry)

    if not images:
        raise ToolError("网关返回的图片为空")

    return {
        "status": "succeeded",
        "model": payload.get("model", ""),
        "elapsed_ms": int((time.time() - started) * 1000),
        "count": len(images),
        "images": images,
    }


def _http_error_message(exc):
    detail = ""
    try:
        detail = exc.read().decode("utf-8", "replace")
    except Exception:
        pass
    detail = _extract_error(detail)
    suffix = ("；" + detail) if detail else ""
    if exc.code == 401:
        return "网关鉴权失败：API 密钥无效或已停用（检查 AIGW_IMAGE_API_KEY）" + suffix
    if exc.code == 403:
        return "网关拒绝：该密钥无权调用生图或此 MCP 服务器" + suffix
    if exc.code == 404:
        return "网关地址不对或接口不存在（检查 AIGW_IMAGE_BASE_URL）" + suffix
    if exc.code == 400:
        return "网关拒绝了生图请求" + suffix
    if exc.code == 429:
        return "网关限流或额度不足" + suffix
    if exc.code >= 500:
        return "生图上游失败（HTTP %d）%s" % (exc.code, suffix)
    return "网关返回 HTTP %d%s" % (exc.code, suffix)


def _extract_error(text):
    text = (text or "").strip()
    if not text:
        return ""
    try:
        data = json.loads(text)
    except ValueError:
        return _clip(text, 400)
    if isinstance(data, dict):
        err = data.get("error")
        if isinstance(err, dict) and err.get("message"):
            return _clip(str(err["message"]), 400)
        if isinstance(err, str) and err:
            return _clip(err, 400)
        if data.get("message"):
            return _clip(str(data["message"]), 400)
    return _clip(text, 400)


# ---------- 图片落盘 ----------

def _save_base64(b64_text):
    raw = base64.b64decode(b64_text)
    if len(raw) > MAX_DOWNLOAD_BYTES:
        raise ToolError("图片超过 %d MB，无法保存" % (MAX_DOWNLOAD_BYTES >> 20))
    return _write_image(raw, _sniff_ext(raw))


def _download_image(url):
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise ToolError("图片地址不是 http/https，无法下载：%s" % _clip(url, 120))
    req = urllib.request.Request(url, method="GET")
    req.add_header("User-Agent", "ai-gateway-image-mcp/1.0")
    try:
        with urllib.request.urlopen(req, timeout=DOWNLOAD_TIMEOUT) as resp:
            raw = resp.read(MAX_DOWNLOAD_BYTES + 1)
            content_type = resp.headers.get("Content-Type", "")
    except urllib.error.HTTPError as exc:
        raise ToolError("下载图片失败（HTTP %d）：%s" % (exc.code, _clip(url, 120)))
    except Exception as exc:
        raise ToolError("下载图片失败：%s" % exc)
    if len(raw) > MAX_DOWNLOAD_BYTES:
        raise ToolError("图片超过 %d MB，拒绝下载" % (MAX_DOWNLOAD_BYTES >> 20))
    ext = _ext_from_content_type(content_type) or _ext_from_url(parsed.path) or _sniff_ext(raw)
    return _write_image(raw, ext)


def _write_image(raw, ext):
    directory = _save_dir()
    try:
        os.makedirs(directory, exist_ok=True)
        name = "image-%s-%s.%s" % (
            time.strftime("%Y%m%d-%H%M%S"),
            uuid.uuid4().hex[:8],
            ext,
        )
        path = os.path.join(directory, name)
        with open(path, "wb") as fh:
            fh.write(raw)
    except OSError as exc:
        raise ToolError("保存图片失败：%s（检查 AIGW_IMAGE_SAVE_DIR / AIGW_MCP_DATA 是否可写）" % exc)
    return path, len(raw)


def _sniff_ext(raw):
    if raw.startswith(b"\x89PNG\r\n\x1a\n"):
        return "png"
    if raw.startswith(b"\xff\xd8\xff"):
        return "jpg"
    if raw[:4] == b"RIFF" and raw[8:12] == b"WEBP":
        return "webp"
    if raw[:6] in (b"GIF87a", b"GIF89a"):
        return "gif"
    return "bin"


def _ext_from_content_type(content_type):
    mapping = {
        "image/png": "png",
        "image/jpeg": "jpg",
        "image/jpg": "jpg",
        "image/webp": "webp",
        "image/gif": "gif",
    }
    return mapping.get((content_type or "").split(";")[0].strip().lower(), "")


def _ext_from_url(path):
    match = re.search(r"\.(png|jpe?g|webp|gif)$", (path or "").lower())
    if not match:
        return ""
    ext = match.group(1)
    return "jpg" if ext == "jpeg" else ext


def _clip(text, limit):
    text = " ".join(str(text or "").split())
    return text if len(text) <= limit else text[:limit] + "…"


# ---------- 工具实现 ----------

def _int_arg(args, name, default, low, high):
    try:
        value = int(args.get(name) if args.get(name) is not None else default)
    except (TypeError, ValueError):
        value = default
    return max(low, min(high, value))


def _bool_arg(args, name, default=False):
    value = args.get(name)
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    return str(value).strip().lower() in ("1", "true", "yes", "on")


def _mime_from_bytes(raw):
    ext = _sniff_ext(raw)
    return {
        "png": "image/png",
        "jpg": "image/jpeg",
        "webp": "image/webp",
        "gif": "image/gif",
    }.get(ext, "")


def _check_reference_size(raw, index):
    if len(raw) > MAX_REFERENCE_BYTES:
        raise ToolError(
            "第 %d 张参考图解码后 %d MB，超过单张上限 %d MB；请压缩图片或改用公网 URL"
            % (index, len(raw) >> 20, MAX_REFERENCE_BYTES >> 20)
        )


def _normalize_data_uri(text, index):
    header, sep, payload = text.partition(",")
    if not sep or "base64" not in header.lower():
        raise ToolError("第 %d 张参考图的 data URI 不是 base64 形式；%s" % (index, REFERENCE_HINT))
    compact = re.sub(r"\s+", "", payload)
    if not B64_RE.match(compact):
        raise ToolError("第 %d 张参考图的 base64 内容非法；%s" % (index, REFERENCE_HINT))
    try:
        raw = base64.b64decode(compact, validate=True)
    except Exception:
        raise ToolError("第 %d 张参考图的 base64 解码失败；%s" % (index, REFERENCE_HINT))
    _check_reference_size(raw, index)
    mime = header[5:].split(";")[0].strip().lower()
    if not mime.startswith("image/"):
        mime = _mime_from_bytes(raw)
    if not mime:
        raise ToolError("第 %d 张参考图不是可识别的图片（PNG/JPEG/WebP/GIF）" % index)
    return "data:%s;base64,%s" % (mime, compact)


def _normalize_reference(value, index):
    """把参考图统一成网关可直接透传的字符串。

    支持：公网 URL、data URI、纯 base64（自动补 data URI 前缀）。
    """
    text = str(value or "").strip()
    if not text:
        raise ToolError("第 %d 张参考图为空" % index)
    lower = text.lower()
    if lower.startswith(("http://", "https://")):
        return text
    if lower.startswith("data:"):
        return _normalize_data_uri(text, index)

    compact = re.sub(r"\s+", "", text)
    if B64_RE.match(compact):
        try:
            raw = base64.b64decode(compact, validate=True)
        except Exception:
            raise ToolError("第 %d 张参考图的 base64 解码失败；%s" % (index, REFERENCE_HINT))
        _check_reference_size(raw, index)
        mime = _mime_from_bytes(raw)
        if not mime:
            raise ToolError("第 %d 张参考图不是可识别的图片（PNG/JPEG/WebP/GIF）" % index)
        return "data:%s;base64,%s" % (mime, compact)

    raise ToolError("第 %d 张参考图无法识别。%s" % (index, REFERENCE_HINT))


def _reference_from_path(value, index):
    """读取本机图片文件并转成 data URI。"""
    raw_path = str(value or "").strip()
    if not raw_path:
        raise ToolError("第 %d 张参考图路径为空" % index)
    path = raw_path if os.path.isabs(raw_path) else os.path.join(os.path.dirname(os.path.abspath(__file__)), raw_path)
    if not os.path.isfile(path):
        raise ToolError(
            "第 %d 张参考图文件不存在：%s（请确认路径；本服务若部署在远端，本机路径一定读不到，"
            "改用 image_url 传公网链接或 base64）"
            % (index, _clip(path, 200))
        )
    try:
        if os.path.getsize(path) > MAX_REFERENCE_BYTES:
            raise ToolError(
                "第 %d 张参考图 %d MB，超过单张上限 %d MB；请压缩图片或改用公网 URL"
                % (index, os.path.getsize(path) >> 20, MAX_REFERENCE_BYTES >> 20)
            )
        with open(path, "rb") as fh:
            raw = fh.read(MAX_REFERENCE_BYTES + 1)
    except ToolError:
        raise
    except OSError as exc:
        raise ToolError("第 %d 张参考图读取失败：%s" % (index, exc))
    _check_reference_size(raw, index)
    mime = _mime_from_bytes(raw)
    if not mime:
        raise ToolError("第 %d 张参考图不是可识别的图片（PNG/JPEG/WebP/GIF）：%s" % (index, _clip(path, 160)))
    return "data:%s;base64,%s" % (mime, base64.b64encode(raw).decode("ascii"))


def _collect_references(args):
    refs = []
    image_url = args.get("image_url")
    if isinstance(image_url, str) and image_url.strip():
        refs.append(_normalize_reference(image_url, 1))
    elif isinstance(image_url, list):
        refs.extend(_normalize_reference(u, i) for i, u in enumerate(image_url, 1) if str(u).strip())

    image_path = args.get("image_path")
    start = len(refs) + 1
    if isinstance(image_path, str) and image_path.strip():
        refs.append(_reference_from_path(image_path, start))
    elif isinstance(image_path, list):
        refs.extend(_reference_from_path(p, start + i) for i, p in enumerate(image_path)
                    if str(p).strip())
    return refs


def _build_payload(args):
    prompt = str(args.get("prompt") or "").strip()
    if not prompt:
        raise ToolError("缺少参数 prompt")

    model = str(args.get("model") or "").strip() or _default_model()
    if not model:
        raise ToolError("缺少参数 model；也可以在 mcp.json 的 env 里设置 AIGW_IMAGE_MODEL 作为默认模型")

    response_format = str(args.get("response_format") or "url").strip().lower()
    if response_format not in ("url", "b64_json"):
        raise ToolError("response_format 只能是 url 或 b64_json")

    payload = {
        "model": model,
        "prompt": prompt,
        "n": _int_arg(args, "n", 1, 1, 4),
        "response_format": response_format,
    }

    refs = _collect_references(args)
    if refs:
        total_chars = sum(len(r) for r in refs)
        if total_chars > MAX_REFERENCE_TOTAL_CHARS:
            raise ToolError(
                "参考图总量过大（base64 共 %.1f M 字符，上限 %.1f M）；"
                "网关 MCP 入口请求体上限 8 MB，请压缩图片、减少张数，或改用公网 URL"
                % (total_chars / 1048576.0, MAX_REFERENCE_TOTAL_CHARS / 1048576.0)
            )
        # 只有一张参考图时按上游习惯发字符串，多张发数组
        payload["image_url"] = refs[0] if len(refs) == 1 else refs

    if args.get("size"):
        payload["size"] = str(args["size"]).strip()
    if args.get("negative_prompt"):
        payload["negative_prompt"] = str(args["negative_prompt"]).strip()
    if args.get("seed") is not None:
        payload["seed"] = _int_arg(args, "seed", 0, -1, 2 ** 31 - 1)
    if isinstance(args.get("loras"), (dict, list)):
        payload["loras"] = args["loras"]

    extra = args.get("extra")
    if isinstance(extra, dict):
        for key, value in extra.items():
            payload.setdefault(key, value)

    return payload


def _generate(args, key=""):
    payload = _build_payload(args)
    download = _bool_arg(args, "download", False)
    wait_seconds = _int_arg(args, "wait_seconds", DEFAULT_WAIT_SECONDS, 0, MAX_WAIT_SECONDS)

    job = _new_job(payload)
    thread = threading.Thread(target=_run_job, args=(job, payload, download, key), daemon=True)
    thread.start()

    deadline = time.time() + wait_seconds
    while time.time() < deadline:
        with _JOBS_LOCK:
            if job["status"] != "running":
                break
        time.sleep(0.2)

    snap = _job_snapshot(job)
    if snap["status"] == "running":
        return {
            "status": "running",
            "job_id": snap["job_id"],
            "elapsed_ms": snap["elapsed_ms"],
            "hint": "生图仍在进行，请每 5 秒调用 get_image_job(job_id=\"%s\")；整体超时 %d 秒"
                    % (snap["job_id"], _timeout_seconds()),
        }

    return _finish_result(snap)


def _finish_result(snap):
    if snap["status"] == "failed":
        raise ToolError(snap["error"] or "生图失败（上游未提供原因）")

    result = dict(snap["result"] or {})
    result["job_id"] = snap["job_id"]
    return result


def _job_status(args):
    job_id = str(args.get("job_id") or "").strip()
    if not job_id:
        raise ToolError("缺少参数 job_id")
    with _JOBS_LOCK:
        job = _JOBS.get(job_id)
    if job is None:
        raise ToolError("作业 %s 不存在或已过期（进程重启后作业会丢失）；请重新调用 generate_image" % job_id)

    snap = _job_snapshot(job)
    if snap["status"] == "succeeded":
        return _finish_result(snap)
    if snap["status"] == "failed":
        return {
            "status": "failed",
            "job_id": job_id,
            "elapsed_ms": snap["elapsed_ms"],
            "error": snap["error"] or "生图失败（上游未提供原因）",
        }
    return {
        "status": "running",
        "job_id": job_id,
        "elapsed_ms": snap["elapsed_ms"],
        "hint": "继续每 5 秒调用 get_image_job；整体超时 %d 秒" % _timeout_seconds(),
    }


def call_tool(name, arguments, key=""):
    if not isinstance(arguments, dict):
        arguments = {}
    try:
        if name == "generate_image":
            payload = _generate(arguments, key)
        elif name == "get_image_job":
            payload = _job_status(arguments)
        else:
            return _error_result("未知工具: %s" % name)
    except ToolError as exc:
        return _error_result(str(exc))
    except Exception as exc:
        return _error_result("内部错误: %s" % exc)

    return {
        "content": [{"type": "text", "text": json.dumps(payload, ensure_ascii=False)}],
        "structuredContent": payload,
        "isError": False,
    }


def _error_result(message):
    return {"content": [{"type": "text", "text": message}], "isError": True}


def _caller_key(headers):
    """取网关转发来的上游密钥。

    网关按「MCP 服务器 → 密钥」逐条尝试，并把 sk-... 写在 Authorization: Bearer 上；
    本地服务拿它去调 /v1/images/generations，意味着密钥只需在面板配一次。
    """
    auth = (headers.get("Authorization") or "").strip()
    if auth.lower().startswith("bearer "):
        return auth[7:].strip()
    return (headers.get("X-Api-Key") or "").strip()


# ---------- MCP 传输层 ----------

def _negotiate(client_version):
    if client_version in SUPPORTED_VERSIONS:
        return client_version
    return SUPPORTED_VERSIONS[0]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # 不刷屏；密钥不会进日志
        pass

    def handle_one_request(self):
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

        # 通知（无 id，含 notifications/initialized）一律 202
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
                "serverInfo": {"name": "image-generation", "version": "1.0.0"},
            }
        elif method == "tools/list":
            result = {"tools": IMAGE_TOOLS}
        elif method == "tools/call":
            result = call_tool(params.get("name") or "", params.get("arguments") or {},
                               _caller_key(self.headers))
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

    data_dir = _env("AIGW_MCP_DATA")
    if data_dir:
        try:
            os.makedirs(data_dir, exist_ok=True)
        except OSError:
            pass

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    print("image-generation MCP listening on 127.0.0.1:%d%s" % (port, MCP_PATH), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
