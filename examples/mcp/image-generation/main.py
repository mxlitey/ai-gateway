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
  AIGW_IMAGE_MAX_CONCURRENCY 同时进行的生图任务上限，默认 4
  AIGW_IMAGE_FETCH_CA        下载内网 https 图片时使用的 CA 文件（自签证书场景）
  AIGW_IMAGE_FETCH_INSECURE  =1 时跳过内网图片的 TLS 校验（默认关闭，不推荐）

返回约定：
  - 图片统一返回网关地址（网关已把上游结果落盘，链接有效期由网关配置决定，默认约 1 小时）；
  - 失败时 isError=true，structuredContent.error 含 code / message / retryable / upstream_status；
  - generate_image 支持 request_id 幂等键：相同 request_id 重复调用复用同一作业。

提供的工具：
  generate_image     生成图片（支持参考图 image_url、尺寸、数量等）
  get_image_job      查询生图作业状态与结果
  list_image_models  列出可用生图模型（等价于网关 /v1/models?type=image）

自描述：initialize 会返回 instructions，说明模型查询、作业轮询、交付方式，
以及网关内置图床的 HTTP 直传能力（不经过 MCP，不受 8 MB 请求体限制），
客户端不依赖外部技能文档也能完成第一次调用。
"""

import base64
import ipaddress
import json
import os
import re
import socket
import ssl
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
# 单次 MCP 调用允许的请求体上限，与网关 MCP 入口保持一致
MAX_REQUEST_BYTES = 8 << 20
MAX_DOWNLOAD_BYTES = 32 << 20
DOWNLOAD_TIMEOUT = 60
MAX_REQUEST_ID_LENGTH = 128
REQUEST_ID_RE = re.compile(r"^[A-Za-z0-9._:-]+$")
# 参考图预算：默认单张 3 MB、所有参考图 base64 合计 7 M 字符。
# 这是上游服务商接收 base64 的上限，与 MCP 入口的 8 MB 请求体限制无关
# （入口那份限制只约束客户端传来的短字符串入参）。
def _env_int(name, default):
    try:
        return int((os.environ.get(name) or "").strip() or default)
    except ValueError:
        return default


def _env_flag(name):
    return (os.environ.get(name) or "").strip().lower() in ("1", "true", "yes", "on")


MAX_REFERENCE_MB = max(1, _env_int("AIGW_IMAGE_REF_MAX_MB", 3))
MAX_REFERENCE_BYTES = MAX_REFERENCE_MB << 20
MAX_REFERENCE_TOTAL_MB = max(MAX_REFERENCE_MB, _env_int("AIGW_IMAGE_REF_TOTAL_MB", 7))
MAX_REFERENCE_TOTAL_CHARS = MAX_REFERENCE_TOTAL_MB << 20
LINK_TTL_HINT = "链接有时效（默认约 1 小时，以网关配置为准）"
# 参考图下载（内网 http/https）的边界：只转发，不落盘持久保存
IMAGE_FETCH_MAX_BYTES = 32 << 20
IMAGE_FETCH_TIMEOUT = 30
IMAGE_FETCH_REDIRECTS = 3
# 链路本地 / 组播 / 保留地址禁止访问（云元数据服务在 169.254.0.0/16）
_BLOCKED_NETS = [
    ipaddress.ip_network(n)
    for n in ("169.254.0.0/16", "224.0.0.0/4", "0.0.0.0/8", "ff00::/8", "fe80::/10", "::/128")
]
REFERENCE_HINT = (
    "参考图只接受公网 http(s) 图片地址；本机文件请用 image_path 传路径。"
    "对话里粘贴的图片不会自动进入 MCP 工具参数。"
)

IMAGE_TOOLS = [
    {
        "name": "generate_image",
        "description": (
            "调用网关的 /v1/images/generations 生成图片，支持文生图与参考图编辑（image_url）。"
            "模型名不确定时先调用 list_image_models 查询，再显式传 model。"
            "单次生图通常需要 1-3 分钟：本工具最多阻塞 wait_seconds 秒（默认 20）；"
            "若返回 status=running，请每 5 秒调用 get_image_job(job_id) 直到 succeeded 或 failed。"
            "成功结果的 url 是网关自身地址（/v1/images/files/...），"
            + LINK_TTL_HINT + "，可直接下载或给用户；重要结果建议 download=true 落本地。"
            "参考图用公网 http(s) URL（image_url）或本机文件路径（image_path）；"
            "内网 URL 由本服务下载后转发。对话里粘贴的图片不会自动变成工具参数。"
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "prompt": {"type": "string", "description": "提示词"},
                "model": {
                    "type": "string",
                    "description": (
                        "生图模型名；不确定有哪些可用模型时先调用 list_image_models。"
                        "留空时使用环境变量 AIGW_IMAGE_MODEL；该变量未配置且未传本字段会直接报错。"
                    ),
                },
                "image_url": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": (
                        "参考图数组，只接受公网 http(s) 地址；传单个字符串也可以。"
                        "内网地址由本服务下载后转成 base64，或在上传图床后传链接，不经过客户端上下文。"
                        "建议最多 4 张。"
                        "对话里粘贴的图片不会被 MCP 工具自动读取，需要链接或改用 image_path。"
                    ),
                },
                "image_path": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": (
                        "参考图的本机文件路径数组（传单个字符串也可以）。"
                        "服务会读取文件转成 base64 再发给网关，适合对话附件已落盘成本地文件的场景。"
                        "仅当本服务与文件在同一台机器上时可用；服务部署在远端时路径读不到，"
                        "请改用 image_url 传内网或公网链接。"
                        "支持 PNG/JPEG/WebP/GIF；"
                        f"超过 {MAX_REFERENCE_MB} MB 时，若网关图床公网可达会自动上传换链接，否则报错。"
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
                "request_id": {
                    "type": "string",
                    "description": (
                        "幂等键（可选，1-128 位字母/数字/._:-）。相同 request_id 重复调用会复用同一个作业，"
                        "超时重试不会重复生图。"
                    ),
                },
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
    {
        "name": "list_image_models",
        "description": (
            "列出当前密钥可用的生图模型（来自网关 /v1/models?type=image）。"
            "首次调用 generate_image 前用它确定 model 取值；返回 default_model 表示服务端已配置默认模型。"
        ),
        "inputSchema": {"type": "object", "properties": {}, "required": []},
    },
]

# initialize 返回的说明书：客户端不依赖外部技能包也能完成第一次调用。
INSTRUCTIONS = (
    "本服务把生图请求转发到 AI Gateway。推荐流程："
    "1) 不确定模型时先调用 list_image_models，拿到 models 列表与 default_model；"
    "2) 调用 generate_image，至少传 prompt；没有 default_model 时必须显式传 model；"
    "3) 返回 status=running 时每 5 秒调用 get_image_job(job_id) 直到 succeeded/failed；"
    "4) 成功结果的 images[].url 是网关自身地址，" + LINK_TTL_HINT + "，可直接下载或交给用户；"
    "需要长期保存时用 download=true 落本地；"
    "5) 失败时 isError=true，structuredContent.error 含 code/message/retryable/upstream_status；"
    "6) 超时重试请带上同一个 request_id，服务端会复用已有作业，不会重复生图。"
)


def _instructions():
    """在基础说明书后追加网关图床能力（HTTP 直连，不经过 MCP 工具）。"""
    base = _base_url()
    return (
        INSTRUCTIONS
        + "\n\n网关内置图床（默认开启；直接 HTTP 调用，不经过本服务，因此不受 8 MB 请求体限制）："
        + "上传 POST " + base + "/v1/images/uploads，"
        + "Authorization: Bearer <与调用本服务相同的 sk- 密钥>，"
        + "Content-Type 为 image/png|jpeg|webp|gif，请求体是图片原始字节（不是 multipart/form-data），"
        + "单张默认上限 100 MB；返回 {id,url,bytes,content_type,expires_at}，"
        + LINK_TTL_HINT + "；"
        + "能力查询 GET " + base + "/v1/images/host（enabled/public 等，public=false 表示上游取不到该地址）；"
        + "删除 DELETE " + base + "/v1/uploads/<id>。"
        + "上面的地址取自本服务配置，若你在远端且它指向回环/内网地址，请换用你连接 MCP 时使用的网关地址。"
        + "适合把本机图片交给其它工具、或需要自己控制参考图链接的场景；"
        + "只是给 generate_image 传参考图时，优先用 image_path / image_url，由本服务处理。"
    )


class ToolError(Exception):
    """给模型看的可读错误，同时携带机器可读的错误码。

    code      稳定的错误分类，客户端可据此决定重试/换参/放弃
    retryable 同样的请求重试是否可能成功
    upstream_status 上游 HTTP 状态码（0 表示未拿到响应）
    hint      给模型的补充建议（可选）
    """

    def __init__(self, message, code="invalid_request", retryable=False,
                 upstream_status=0, hint=""):
        super().__init__(message)
        self.message = message
        self.code = code
        self.retryable = bool(retryable)
        self.upstream_status = int(upstream_status or 0)
        self.hint = hint

    def payload(self):
        out = {"code": self.code, "message": self.message, "retryable": self.retryable}
        if self.upstream_status:
            out["upstream_status"] = self.upstream_status
        if self.hint:
            out["hint"] = self.hint
        return out


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


# 访问本机网关不走系统代理；下载参考图同样直连（内网地址走代理只会失败）
_LOCAL_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


# ---------- 作业存储 ----------

_JOBS = {}
_JOB_BY_REQUEST_ID = {}
_JOBS_LOCK = threading.Lock()


def _max_concurrency():
    try:
        value = int(_env("AIGW_IMAGE_MAX_CONCURRENCY", "4"))
    except ValueError:
        value = 4
    return max(1, min(32, value))


# 同时在跑的生图任务上限：避免一次并发提交把上游与本机线程打爆
_JOB_SLOTS = threading.BoundedSemaphore(_max_concurrency())


def _drop_job_locked(job):
    _JOBS.pop(job["id"], None)
    request_id = job.get("request_id")
    if request_id and _JOB_BY_REQUEST_ID.get(request_id) is job:
        _JOB_BY_REQUEST_ID.pop(request_id, None)


def _prune_jobs_locked(now):
    running, finished = [], []
    for job in list(_JOBS.values()):
        if job["status"] == "running" and now - job["created"] > job["deadline_seconds"]:
            # 超时作业标记失败，避免永远占着并发槽、也避免客户端无限轮询
            job["status"] = "failed"
            job["error"] = {
                "code": "timeout",
                "message": "生图超时（%d 秒），请重试或换用更快的模型" % job["deadline_seconds"],
                "retryable": True,
            }
            job["updated"] = now
            finished.append(job)
        elif job["status"] == "running":
            running.append(job)
        else:
            finished.append(job)

    for job in finished:
        if now - job["updated"] > JOB_TTL_SECONDS:
            _drop_job_locked(job)
    if len(_JOBS) <= MAX_JOBS:
        return
    # 超出上限时按更新时间从旧到新清理已完成的作业
    for job in sorted(finished, key=lambda j: j["updated"]):
        _drop_job_locked(job)
        if len(_JOBS) <= MAX_JOBS:
            break


def _new_job(payload, request_id=""):
    now = time.time()
    job = {
        "id": uuid.uuid4().hex,
        "status": "running",
        "model": payload.get("model", ""),
        "request_id": request_id,
        "created": now,
        "updated": now,
        "deadline_seconds": _timeout_seconds(),
        "result": None,
        "error": None,
    }
    with _JOBS_LOCK:
        _prune_jobs_locked(now)
        if sum(1 for j in _JOBS.values() if j["status"] == "running") >= MAX_JOBS:
            raise ToolError(
                "当前生图任务过多（%d 个在跑），请稍后重试" % MAX_JOBS,
                code="server_busy",
                retryable=True,
            )
        _JOBS[job["id"]] = job
        if request_id:
            _JOB_BY_REQUEST_ID[request_id] = job
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


def _finish_job(job, status, result=None, error=None):
    with _JOBS_LOCK:
        if job["status"] != "running":
            # 已被判定超时，迟到的结果直接丢弃
            return
        job["status"] = status
        job["result"] = result
        job["error"] = error
        job["updated"] = time.time()


def _run_job(job, payload, download, key):
    try:
        # 排队等待并发槽：没有槽位时作业保持 running，客户端轮询即可
        with _JOB_SLOTS:
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
                            image["download_error"] = exc.message
    except ToolError as exc:
        _finish_job(job, "failed", error=exc.payload())
        return
    except Exception as exc:  # 兜底，避免线程静默退出
        _finish_job(job, "failed", error={
            "code": "internal_error",
            "message": "生图失败: %s" % exc,
            "retryable": False,
        })
        return
    _finish_job(job, "succeeded", result=result)


# ---------- 网关调用 ----------

def _request_images(payload, key):
    # 密钥优先取调用方带来的 Authorization（面板「MCP 服务器 → 密钥」），
    # 没有时回落到 env 里的 AIGW_IMAGE_API_KEY。
    key = (key or "").strip() or _api_key()
    if not key:
        raise ToolError(
            "没有可用的 API 密钥：请在「MCP 服务器 → 生图 → 编辑 → 密钥」里填入面板签发的 sk- 密钥"
            "（也可在 mcp.json 的 env 里设置 AIGW_IMAGE_API_KEY）",
            code="auth_missing",
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
        message, code, retryable = _gateway_error(exc)
        raise ToolError(message, code=code, retryable=retryable, upstream_status=exc.code)
    except urllib.error.URLError as exc:
        reason = getattr(exc, "reason", exc)
        if isinstance(reason, TimeoutError):
            raise ToolError(
                "生图请求超时（%d 秒）；可调大 AIGW_IMAGE_TIMEOUT_SECONDS 或渠道轮询超时" % _timeout_seconds(),
                code="timeout",
                retryable=True,
            )
        raise ToolError(
            "无法连接网关 %s：%s（确认网关已启动、端口正确）" % (_base_url(), reason),
            code="gateway_unreachable",
            retryable=True,
        )
    except TimeoutError:
        raise ToolError(
            "生图请求超时（%d 秒）；可调大 AIGW_IMAGE_TIMEOUT_SECONDS 或渠道轮询超时" % _timeout_seconds(),
            code="timeout",
            retryable=True,
        )
    except Exception as exc:
        raise ToolError("请求网关失败：%s" % exc, code="gateway_unreachable", retryable=True)

    try:
        data = json.loads(raw.decode("utf-8", "replace"))
    except ValueError:
        raise ToolError(
            "网关返回的不是 JSON：%s" % _clip(raw.decode("utf-8", "replace"), 300),
            code="invalid_response",
            retryable=True,
        )

    items = data.get("data") if isinstance(data, dict) else None
    if not isinstance(items, list) or not items:
        raise ToolError(
            "网关未返回图片：%s" % _clip(json.dumps(data, ensure_ascii=False), 300),
            code="empty_result",
            retryable=True,
        )

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
        raise ToolError("网关返回的图片为空", code="empty_result", retryable=True)

    return {
        "status": "succeeded",
        "model": payload.get("model", ""),
        "elapsed_ms": int((time.time() - started) * 1000),
        "count": len(images),
        "images": images,
    }


def _gateway_error(exc):
    """把网关 HTTP 错误转换为 (可读文案, 错误码, 是否可重试)。"""
    raw = ""
    try:
        raw = exc.read().decode("utf-8", "replace")
    except Exception:
        pass
    detail, upstream_code = _extract_error_detail(raw)
    suffix = ("：" + detail) if detail else "：响应体为空"
    if exc.code == 401:
        return ("网关鉴权失败：API 密钥无效或已停用（检查 AIGW_IMAGE_API_KEY）" + suffix,
                "auth_failed", False)
    if exc.code == 403:
        return ("网关拒绝：该密钥无权调用生图或此 MCP 服务器" + suffix, "forbidden", False)
    if exc.code == 404:
        return ("网关地址不对或接口不存在（检查 AIGW_IMAGE_BASE_URL）" + suffix, "not_found", False)
    if exc.code == 400:
        return ("网关拒绝了生图请求" + suffix, "invalid_request", False)
    if exc.code == 413:
        return ("请求体过大（参考图太多或太大）" + suffix, "request_too_large", False)
    if exc.code == 429:
        return ("网关限流或额度不足" + suffix, "rate_limited", True)
    if exc.code == 504:
        return ("生图上游超时（HTTP 504）" + suffix, "upstream_timeout", True)
    if exc.code >= 500:
        return ("生图上游失败（HTTP %d）%s" % (exc.code, suffix), upstream_code or "upstream_error", True)
    return ("网关返回 HTTP %d%s" % (exc.code, suffix), upstream_code or "unexpected_status", False)


def _extract_error_detail(text):
    """从网关错误响应中提取 (message, code)，尽量透传上游原文。"""
    text = (text or "").strip()
    if not text:
        return "", ""
    try:
        data = json.loads(text)
    except ValueError:
        return _clip(text, 400), ""
    if isinstance(data, dict):
        err = data.get("error")
        if isinstance(err, dict):
            message = err.get("message") or err.get("msg") or ""
            code = err.get("code") or err.get("type") or ""
            if message:
                return _clip(str(message), 400), str(code or "")
        if isinstance(err, str) and err:
            return _clip(err, 400), ""
        if data.get("message"):
            return _clip(str(data["message"]), 400), str(data.get("code") or "")
    return _clip(text, 400), ""


# ---------- 图片落盘 ----------

def _save_base64(b64_text):
    try:
        raw = base64.b64decode(b64_text)
    except Exception:
        raise ToolError("网关返回的 base64 图片无法解码", code="invalid_response", retryable=True)
    if len(raw) > MAX_DOWNLOAD_BYTES:
        raise ToolError("图片超过 %d MB，无法保存" % (MAX_DOWNLOAD_BYTES >> 20), code="too_large")
    return _write_image(raw, _sniff_ext(raw))


def _download_image(url):
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise ToolError(
            "图片地址不是 http/https，无法下载：%s" % _clip(url, 120),
            code="download_failed",
        )
    req = urllib.request.Request(url, method="GET")
    req.add_header("User-Agent", "ai-gateway-image-mcp/1.0")
    try:
        with urllib.request.urlopen(req, timeout=DOWNLOAD_TIMEOUT) as resp:
            raw = resp.read(MAX_DOWNLOAD_BYTES + 1)
            content_type = resp.headers.get("Content-Type", "")
    except urllib.error.HTTPError as exc:
        raise ToolError(
            "下载图片失败（HTTP %d）：%s" % (exc.code, _clip(url, 120)),
            code="download_failed",
            retryable=exc.code >= 500,
            upstream_status=exc.code,
        )
    except Exception as exc:
        raise ToolError("下载图片失败：%s" % exc, code="download_failed", retryable=True)
    if len(raw) > MAX_DOWNLOAD_BYTES:
        raise ToolError("图片超过 %d MB，拒绝下载" % (MAX_DOWNLOAD_BYTES >> 20), code="too_large")
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
        raise ToolError(
            "保存图片失败：%s（检查 AIGW_IMAGE_SAVE_DIR / AIGW_MCP_DATA 是否可写）" % exc,
            code="storage_error",
        )
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


_IMAGE_HOST_CACHE = {"at": 0.0, "value": None}
_IMAGE_HOST_LOCK = threading.Lock()
_FETCH_OPENER = None
_FETCH_OPENER_LOCK = threading.Lock()


class _GuardedRedirect(urllib.request.HTTPRedirectHandler):
    """限制重定向跳数，并对每一跳重新做地址校验。"""

    max_redirections = IMAGE_FETCH_REDIRECTS

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        _guard_fetch_url(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _fetch_opener():
    global _FETCH_OPENER
    with _FETCH_OPENER_LOCK:
        if _FETCH_OPENER is None:
            if _env_flag("AIGW_IMAGE_FETCH_INSECURE"):
                ctx = ssl._create_unverified_context()
            else:
                ca = (os.environ.get("AIGW_IMAGE_FETCH_CA") or "").strip()
                ctx = ssl.create_default_context(cafile=ca or None)
            _FETCH_OPENER = urllib.request.build_opener(
                urllib.request.ProxyHandler({}),
                _GuardedRedirect(),
                urllib.request.HTTPSHandler(context=ctx),
            )
        return _FETCH_OPENER


def _is_public_url(text):
    """按字面量判断链接是否公网可达；不解析 DNS、不发探测。"""
    host = (urllib.parse.urlsplit(text).hostname or "").strip().lower()
    if not host:
        return False
    if host == "localhost" or host.endswith(".local"):
        return False
    try:
        ip = ipaddress.ip_address(host)
    except ValueError:
        return "." in host
    return not (ip.is_private or ip.is_loopback or ip.is_link_local
                or ip.is_multicast or ip.is_reserved or ip.is_unspecified)


def _guard_fetch_url(url):
    parts = urllib.parse.urlsplit(url)
    if parts.scheme not in ("http", "https"):
        raise ToolError("参考图链接只支持 http/https", code="image_fetch_failed")
    host = parts.hostname
    if not host:
        raise ToolError("参考图链接缺少主机名", code="image_fetch_failed")
    try:
        ips = [ipaddress.ip_address(host)]
    except ValueError:
        try:
            infos = socket.getaddrinfo(
                host, parts.port or (443 if parts.scheme == "https" else 80),
                proto=socket.IPPROTO_TCP,
            )
        except OSError as exc:
            raise ToolError("无法解析参考图地址 %s: %s" % (host, exc),
                            code="image_fetch_failed") from exc
        ips = []
        for info in infos:
            try:
                ips.append(ipaddress.ip_address(str(info[4][0]).split("%", 1)[0]))
            except ValueError:
                continue
    for ip in ips:
        for net in _BLOCKED_NETS:
            if ip in net:
                raise ToolError("参考图地址 %s 指向受限网段，已拒绝访问" % host,
                                code="image_fetch_failed")


def _fetch_reference(url, index):
    _guard_fetch_url(url)
    req = urllib.request.Request(url, headers={"User-Agent": "ai-gateway-image-mcp"})
    try:
        with _fetch_opener().open(req, timeout=IMAGE_FETCH_TIMEOUT) as resp:
            raw = resp.read(IMAGE_FETCH_MAX_BYTES + 1)
    except ToolError:
        raise
    except urllib.error.HTTPError as exc:
        raise ToolError("第 %d 张参考图下载失败: HTTP %d" % (index, exc.code),
                        code="image_fetch_failed") from exc
    except Exception as exc:
        raise ToolError("第 %d 张参考图下载失败: %s" % (index, exc),
                        code="image_fetch_failed") from exc
    if len(raw) > IMAGE_FETCH_MAX_BYTES:
        raise ToolError("第 %d 张参考图超过 %d MB，无法转发"
                        % (index, IMAGE_FETCH_MAX_BYTES >> 20),
                        code="reference_too_large")
    mime = _mime_from_bytes(raw)
    if not mime:
        raise ToolError("第 %d 张参考图不是支持的图片格式（PNG/JPEG/WebP/GIF）" % index,
                        code="image_fetch_failed")
    return raw, mime


def _gateway_image_host(key):
    """查询网关图床是否开启、是否声明公网可达；失败按不可用处理。"""
    now = time.time()
    with _IMAGE_HOST_LOCK:
        value = _IMAGE_HOST_CACHE["value"]
        if value is not None and now - _IMAGE_HOST_CACHE["at"] < 300:
            return value
    key = (key or "").strip() or _api_key()
    req = urllib.request.Request(_base_url() + "/v1/images/host", method="GET")
    req.add_header("User-Agent", "ai-gateway-image-mcp")
    if key:
        req.add_header("Authorization", "Bearer " + key)
    try:
        with _LOCAL_OPENER.open(req, timeout=10) as resp:
            data = json.loads(resp.read(1 << 20).decode("utf-8", "replace"))
    except Exception:
        return {"enabled": False, "public": False}
    if not isinstance(data, dict):
        return {"enabled": False, "public": False}
    value = {
        "enabled": bool(data.get("enabled")),
        "public": bool(data.get("public")),
        "max_mb": int(data.get("max_mb") or 0),
    }
    with _IMAGE_HOST_LOCK:
        _IMAGE_HOST_CACHE["at"] = now
        _IMAGE_HOST_CACHE["value"] = value
    return value


def _upload_reference(raw, mime, index, key):
    key = (key or "").strip() or _api_key()
    if not key:
        raise ToolError("第 %d 张参考图超过 base64 上限，上传图床需要 AIGW_IMAGE_API_KEY" % index,
                        code="auth_missing")
    req = urllib.request.Request(_base_url() + "/v1/images/uploads", data=raw, method="POST")
    req.add_header("Content-Type", mime)
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("User-Agent", "ai-gateway-image-mcp")
    try:
        with _LOCAL_OPENER.open(req, timeout=60) as resp:
            data = json.loads(resp.read(1 << 20).decode("utf-8", "replace"))
    except urllib.error.HTTPError as exc:
        detail = ""
        try:
            detail = exc.read(4096).decode("utf-8", "replace")
        except Exception:
            pass
        hint = "网关图床可能未开启或不可用"
        if exc.code == 413:
            hint = "图片超过网关图床单张上限"
        raise ToolError(("第 %d 张参考图上传失败: HTTP %d %s" % (index, exc.code, detail)).strip(),
                        code="image_upload_failed", hint=hint) from exc
    except Exception as exc:
        raise ToolError("第 %d 张参考图上传失败: %s" % (index, exc),
                        code="image_upload_failed") from exc
    url = data.get("url") if isinstance(data, dict) else None
    if not url:
        raise ToolError("第 %d 张参考图上传后没有拿到链接" % index, code="image_upload_failed")
    return str(url)


def _reference_from_bytes(raw, mime, index, key):
    """预算内直接转 base64；超预算时只有图床公网可达才能换成链接。"""
    if len(raw) <= MAX_REFERENCE_BYTES:
        return "data:%s;base64,%s" % (mime, base64.b64encode(raw).decode("ascii"))
    size = "%.1f MB" % (len(raw) / (1 << 20))
    cap = _gateway_image_host(key)
    if not cap.get("enabled"):
        raise ToolError(
            "第 %d 张参考图 %s，超过单张上限 %d MB；网关图床未开启，无法换成公网链接"
            % (index, size, MAX_REFERENCE_MB),
            code="reference_exceeds_base64_limit",
            hint="请压缩图片后重试，或改用公网图片地址。",
        )
    if not cap.get("public"):
        raise ToolError(
            "第 %d 张参考图 %s，超过单张上限 %d MB；"
            "网关图床不可公网访问，上传后的链接上游取不到"
            % (index, size, MAX_REFERENCE_MB),
            code="reference_exceeds_base64_limit",
            hint="请压缩图片后重试，或改用公网图片地址。",
        )
    return _upload_reference(raw, mime, index, key)


def _normalize_reference(value, index, key):
    """公网链接原样透传；内网链接由本服务下载后再决定 base64 还是上传。"""
    text = str(value or "").strip()
    if not text:
        raise ToolError("第 %d 张参考图为空" % index, code="invalid_reference")
    if not text.lower().startswith(("http://", "https://")):
        raise ToolError("第 %d 张参考图无法识别。%s" % (index, REFERENCE_HINT),
                        code="invalid_reference")
    if _is_public_url(text):
        return text
    raw, mime = _fetch_reference(text, index)
    return _reference_from_bytes(raw, mime, index, key)


def _reference_from_path(value, index, key):
    """读取本机图片文件，按预算决定转 base64 还是上传图床。"""
    raw_path = str(value or "").strip()
    if not raw_path:
        raise ToolError("第 %d 张参考图路径为空" % index, code="invalid_reference")
    path = raw_path if os.path.isabs(raw_path) else os.path.join(
        os.path.dirname(os.path.abspath(__file__)), raw_path)
    if not os.path.isfile(path):
        raise ToolError(
            "第 %d 张参考图文件不存在：%s（请确认路径；本服务若部署在远端，本机路径一定读不到）"
            % (index, _clip(path, 200)),
            code="invalid_reference",
        )
    try:
        with open(path, "rb") as fh:
            raw = fh.read(IMAGE_FETCH_MAX_BYTES + 1)
    except OSError as exc:
        raise ToolError("第 %d 张参考图读取失败：%s" % (index, exc),
                        code="invalid_reference") from exc
    if len(raw) > IMAGE_FETCH_MAX_BYTES:
        raise ToolError("第 %d 张参考图超过 %d MB，无法转发：%s"
                        % (index, IMAGE_FETCH_MAX_BYTES >> 20, _clip(path, 160)),
                        code="reference_too_large")
    mime = _mime_from_bytes(raw)
    if not mime:
        raise ToolError("第 %d 张参考图不是可识别的图片（PNG/JPEG/WebP/GIF）：%s"
                        % (index, _clip(path, 160)),
                        code="invalid_reference")
    return _reference_from_bytes(raw, mime, index, key)


def _collect_references(args, key):
    refs = []
    image_url = args.get("image_url")
    if isinstance(image_url, str) and image_url.strip():
        refs.append(_normalize_reference(image_url, 1, key))
    elif isinstance(image_url, list):
        refs.extend(_normalize_reference(u, i, key)
                    for i, u in enumerate(image_url, 1) if str(u).strip())

    image_path = args.get("image_path")
    start = len(refs) + 1
    if isinstance(image_path, str) and image_path.strip():
        refs.append(_reference_from_path(image_path, start, key))
    elif isinstance(image_path, list):
        refs.extend(_reference_from_path(p, start + i, key)
                    for i, p in enumerate(image_path) if str(p).strip())
    return refs


def _build_payload(args, key=""):
    prompt = str(args.get("prompt") or "").strip()
    if not prompt:
        raise ToolError("缺少参数 prompt")

    model = str(args.get("model") or "").strip() or _default_model()
    if not model:
        raise ToolError(
            "缺少参数 model；可先调用 list_image_models 查看可用模型，"
            "或在 mcp.json 的 env 里设置 AIGW_IMAGE_MODEL 作为默认模型"
        )

    response_format = str(args.get("response_format") or "url").strip().lower()
    if response_format not in ("url", "b64_json"):
        raise ToolError("response_format 只能是 url 或 b64_json")

    payload = {
        "model": model,
        "prompt": prompt,
        "n": _int_arg(args, "n", 1, 1, 4),
        "response_format": response_format,
    }

    refs = _collect_references(args, key)
    if refs:
        total_chars = sum(len(r) for r in refs)
        if total_chars > MAX_REFERENCE_TOTAL_CHARS:
            raise ToolError(
                "参考图总量过大（base64 共 %.1f M 字符，上限 %.1f M）；"
                "请压缩图片、减少张数，或改用公网 URL"
                % (total_chars / 1048576.0, MAX_REFERENCE_TOTAL_CHARS / 1048576.0),
                code="too_large",
                hint="也可以把图片放到公网地址，用 image_url 直接透传。",
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


def _request_id(args):
    raw = str(args.get("request_id") or "").strip()
    if not raw:
        return ""
    if len(raw) > MAX_REQUEST_ID_LENGTH or not REQUEST_ID_RE.match(raw):
        raise ToolError(
            "request_id 只能是 1-%d 位字母、数字或 ._:-" % MAX_REQUEST_ID_LENGTH,
        )
    return raw


def _list_models(key=""):
    """列出当前密钥可用的生图模型，免去客户端自己拼 HTTP 请求。"""
    key = (key or "").strip() or _api_key()
    if not key:
        raise ToolError(
            "没有可用的 API 密钥：请在「MCP 服务器 → 生图 → 编辑 → 密钥」里填入面板签发的 sk- 密钥"
            "（也可在 mcp.json 的 env 里设置 AIGW_IMAGE_API_KEY）",
            code="auth_missing",
        )

    req = urllib.request.Request(_base_url() + "/v1/models?type=image", method="GET")
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("User-Agent", "ai-gateway-image-mcp/1.0")
    try:
        with _LOCAL_OPENER.open(req, timeout=15) as resp:
            raw = resp.read(4 << 20)
    except urllib.error.HTTPError as exc:
        message, code, retryable = _gateway_error(exc)
        raise ToolError(message, code=code, retryable=retryable, upstream_status=exc.code)
    except urllib.error.URLError as exc:
        reason = getattr(exc, "reason", exc)
        raise ToolError(
            "无法连接网关 %s：%s（确认网关已启动、端口正确）" % (_base_url(), reason),
            code="timeout" if isinstance(reason, TimeoutError) else "gateway_unreachable",
            retryable=True,
        )
    except TimeoutError:
        raise ToolError("查询生图模型超时（15 秒）", code="timeout", retryable=True)

    try:
        data = json.loads(raw.decode("utf-8", "replace"))
    except ValueError:
        raise ToolError(
            "网关返回的不是 JSON：%s" % _clip(raw.decode("utf-8", "replace"), 300),
            code="invalid_response",
            retryable=True,
        )

    items = data.get("data") if isinstance(data, dict) else None
    models = []
    if isinstance(items, list):
        for item in items:
            if isinstance(item, dict) and item.get("id"):
                models.append(str(item["id"]))
    if not models:
        raise ToolError(
            "当前没有可用的生图模型：请到面板「渠道」把模型加入「生图模型」并选择同步或异步",
            code="no_image_models",
        )

    payload = {"status": "succeeded", "count": len(models), "models": models}
    default = _default_model()
    if default:
        payload["default_model"] = default
    else:
        payload["hint"] = "generate_image 需要显式传 model（或在 mcp.json 的 env 里设置 AIGW_IMAGE_MODEL）"
    return payload


def _generate(args, key=""):
    payload = _build_payload(args, key)
    download = _bool_arg(args, "download", False)
    wait_seconds = _int_arg(args, "wait_seconds", DEFAULT_WAIT_SECONDS, 0, MAX_WAIT_SECONDS)
    request_id = _request_id(args)

    # 幂等：相同 request_id 复用已有作业，重试不会重复生图、重复计费
    if request_id:
        with _JOBS_LOCK:
            existing = _JOB_BY_REQUEST_ID.get(request_id)
        if existing is not None:
            snap = _job_snapshot(existing)
            if snap["status"] == "running":
                return {
                    "status": "running",
                    "job_id": snap["job_id"],
                    "elapsed_ms": snap["elapsed_ms"],
                    "reused": True,
                    "hint": "相同 request_id 的作业仍在进行，请每 5 秒调用 get_image_job(job_id=\"%s\")"
                            % snap["job_id"],
                }
            result = _finish_result(snap)
            result["reused"] = True
            return result

    job = _new_job(payload, request_id)
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
            "reused": False,
            "hint": "生图仍在进行，请每 5 秒调用 get_image_job(job_id=\"%s\")；整体超时 %d 秒"
                    % (snap["job_id"], _timeout_seconds()),
        }

    result = _finish_result(snap)
    result["reused"] = False
    return result


def _finish_result(snap):
    if snap["status"] == "failed":
        _raise_job_error(snap.get("error"))

    result = dict(snap["result"] or {})
    result["job_id"] = snap["job_id"]
    return result


def _raise_job_error(err):
    if isinstance(err, dict):
        raise ToolError(
            err.get("message") or "生图失败（上游未提供原因）",
            code=err.get("code") or "internal_error",
            retryable=bool(err.get("retryable")),
            upstream_status=err.get("upstream_status") or 0,
        )
    raise ToolError(
        str(err) if err else "生图失败（上游未提供原因）",
        code="upstream_error",
        retryable=True,
    )


def _job_status(args):
    job_id = str(args.get("job_id") or "").strip()
    if not job_id:
        raise ToolError("缺少参数 job_id")
    with _JOBS_LOCK:
        _prune_jobs_locked(time.time())
        job = _JOBS.get(job_id)
    if job is None:
        raise ToolError(
            "作业 %s 不存在或已过期（进程重启后作业会丢失）；请重新调用 generate_image" % job_id,
            code="job_not_found",
        )

    snap = _job_snapshot(job)
    if snap["status"] == "succeeded":
        return _finish_result(snap)
    if snap["status"] == "failed":
        return {
            "status": "failed",
            "job_id": job_id,
            "elapsed_ms": snap["elapsed_ms"],
            "error": snap["error"] or {
                "code": "upstream_error",
                "message": "生图失败（上游未提供原因）",
                "retryable": True,
            },
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
        elif name == "list_image_models":
            payload = _list_models(key)
        else:
            return _error_result("未知工具: %s" % name, code="unknown_tool")
    except ToolError as exc:
        return _error_result(exc.message, exc.code, exc.retryable, exc.upstream_status)
    except Exception as exc:
        return _error_result("内部错误: %s" % exc, code="internal_error")

    return {
        "content": [{"type": "text", "text": json.dumps(payload, ensure_ascii=False)}],
        "structuredContent": payload,
        "isError": False,
    }


def _error_result(message, code="internal_error", retryable=False, upstream_status=0):
    error = {"code": code, "message": message, "retryable": bool(retryable)}
    if upstream_status:
        error["upstream_status"] = int(upstream_status)
    text = "[%s] %s" % (code, message)
    if retryable:
        text += "（可以重试）"
    return {
        "content": [{"type": "text", "text": text}],
        "structuredContent": {"status": "failed", "error": error},
        "isError": True,
    }


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
        except ValueError:
            self._json(200, {"jsonrpc": "2.0", "id": None,
                             "error": {"code": -32700, "message": "invalid Content-Length"}})
            return
        if length > MAX_REQUEST_BYTES:
            # 不读取超大请求体，直接返回并断开连接，避免内存被撑爆
            self._json(413, {"jsonrpc": "2.0", "id": None,
                             "error": {"code": -32600,
                                       "message": "request too large: %d bytes (limit %d)"
                                                  % (length, MAX_REQUEST_BYTES)}})
            self.close_connection = True
            return
        try:
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
                "serverInfo": {"name": "image-generation", "version": "1.1.0"},
                "instructions": _instructions(),
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
