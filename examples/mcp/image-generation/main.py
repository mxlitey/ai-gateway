"""生图 MCP 服务器（仅用 Python 标准库，无第三方依赖）。

按 docs/local-mcp-spec.md 实现 MCP Streamable HTTP：POST /mcp，无状态、无鉴权，
监听 127.0.0.1 上由网关分配的 AIGW_MCP_PORT 端口。

它本身不直连 ModelScope：生图统一走网关的 OpenAI 兼容接口
  POST {AIGW_IMAGE_BASE_URL}/v1/images/generations
提交时带 async=true，网关立即返回作业号并在后台执行（异步渠道的「提交 + 轮询」
由网关负责），本服务只需轮询作业状态。

为什么用作业模型：
  生图实测可达数分钟，而 MCP 工具调用与 HTTP 等待响应头都有超时限制。
  因此 generate_image 只负责提交：立即返回 job_id 与 status，
  由客户端每 5 秒调用 get_image_job 轮询。
  作业记录保存在网关 SQLite 中（保留 24 小时），网关或本服务重启都不会丢：
  已提交到上游的异步任务会继续轮询，同步任务会标记为 gateway_restarted 提示重新提交。

环境变量（在 mcp.json 的 env 里配置，均可留空）：
  AIGW_IMAGE_BASE_URL        网关地址，默认 http://127.0.0.1:8787
  AIGW_IMAGE_API_KEY         网关 sk- 密钥；留空时用调用方带来的 Authorization，
                             即面板「MCP 服务器 → 生图 → 密钥」里配的那把
  AIGW_IMAGE_MODEL           默认生图模型（generate_image 未传 model 时使用）
  AIGW_IMAGE_INLINE_MB       单张可内联返回的上限（MB），默认 4；超过只回传直链
  AIGW_IMAGE_INLINE_TOTAL_MB 单次响应内联图片的总上限（MB），默认 10
  AIGW_IMAGE_FETCH_CA        下载内网 https 图片时使用的 CA 文件（自签证书场景）
  AIGW_IMAGE_FETCH_INSECURE  =1 时跳过内网图片的 TLS 校验（默认关闭，不推荐）

返回约定：
  - 网关严格透传、不落盘：作业结果里的 data / output_images / images 等字段是上游原始响应，
    images[].url 与 b64_json 都不经过转换，链接有效期由上游决定；
    网关只校验「必须有图」，取不到图会直接报错，不会返回一个空结果；
  - get_image_job 默认（inline=true）把图片字节以 MCP image 内容块一起返回，直链与图片数据
    在同一次响应里给全，客户端可直接保存/显示，不必再按链接下载一次；
  - 本服务不落盘：只做「取字节 + 内联」，重复的本地副本没有意义（服务常跑在网关容器里，
    其文件路径对客户端不可用）；
  - 作业对象带 expires_at（作业记录保留 24 小时，结果里叫 job_expires_at）与 poll_count / stage 进度；
  - 失败时 isError=true，structuredContent.error 含 code / message / retryable / upstream_status；
  - generate_image 支持 request_id 幂等键：相同 request_id 重复调用复用同一作业。

提供的工具：
  generate_image     生成图片（支持参考图 image_url、尺寸、数量等）
  get_image_job      查询生图作业状态与结果
  cancel_image_job   取消仍在排队或执行的作业
  list_image_models  列出可用生图模型（等价于网关 /v1/models?type=image）
  image_host_info    取对象存储预签名直传地址，图片走 HTTP 直传，不进 MCP 的 8 MB 限制

自描述：initialize 会返回 instructions，说明模型查询、作业轮询、交付方式，
客户端不依赖外部技能文档也能完成第一次调用。直传能力（HTTP 直传，不经过 MCP，
不受 8 MB 请求体限制）由 image_host_info 工具描述与返回值承载，不再写进 instructions，
避免地址写死或与工具说明重复。
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
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SUPPORTED_VERSIONS = ["2025-06-18", "2025-03-26", "2024-11-05"]
MCP_PATH = "/mcp"

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
# 单张可内联进 MCP 响应的上限：图片以 base64 塞进内容块会膨胀 4/3。
# 超过就只回传直链，不随响应返回图片字节。
MAX_INLINE_BYTES = max(1, _env_int("AIGW_IMAGE_INLINE_MB", 4)) << 20
# 一次响应内联图片的原始字节总预算。网关读取 MCP 响应的上限是 32 MB，
# base64 膨胀 4/3 后必须留余量，否则响应会被截断成非法 JSON。
MAX_INLINE_TOTAL_BYTES = max(1, _env_int("AIGW_IMAGE_INLINE_TOTAL_MB", 10)) << 20
LINK_TTL_HINT = "链接由上游直接签发，有效期不受网关控制"
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
    "参考图支持三种来源：公网 http(s) 图片地址（image_url 直接传）；"
    "本机文件（image_path 传路径，仅本服务与文件同机时可用）；"
    "对话里粘贴的图片或本机读不到的图片，先用 image_host_info 取对象存储预签名 upload_url，"
    "把图片原始字节 PUT 上去换取公网 url，再作为 image_url 传入。"
)

IMAGE_TOOLS = [
    {
        "name": "generate_image",
        "description": (
            "调用网关的 /v1/images/generations 生成图片，支持文生图与参考图编辑（image_url）。"
            "模型名不确定时先调用 list_image_models 查询，再显式传 model。"
            "本工具只负责提交，立即返回 job_id 与 status（通常为 queued），不等待出图；"
            "请每 5 秒调用 get_image_job(job_id)。作业状态共 5 种："
            "queued / running / succeeded / failed / canceled，只有 succeeded 才有图片，"
            "failed / canceled 会直接以错误返回。"
            "success 时返回的 url 是上游直接签发的链接，"
            + LINK_TTL_HINT + "，可直接下载或给用户；"
            "get_image_job 默认会把图片数据与直链一起返回，不必再单独下载。"
            "参考图用公网 http(s) URL（image_url）或本机文件路径（image_path）；"
            "内网 URL 由本服务下载后转发。"
            "对话里粘贴的图片、或本机读不到的文件，先调 image_host_info 取预签名直传地址，"
            "把图片原始字节 PUT 上去换成 url，再作为 image_url 传入。"
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
                        "参考图数组，传单个字符串也可以。"
                        "公网 http(s) 地址直接传；内网地址由本服务下载后转 base64。"
                        "对话里粘贴的图片、或本机读不到的文件，先调 image_host_info 拿直传端点，"
                        "把图片原始字节 PUT 上去换取 url，再传到这里。"
                        "建议最多 4 张。"
                    ),
                },
                "image_path": {
                    "type": "array",
                    "items": {"type": "string"},
                    "description": (
                        "参考图的本机文件路径数组（传单个字符串也可以）。"
                        "服务会读取文件转成 base64 再发给网关，适合对话附件已落盘成本地文件的场景。"
                        "仅当本服务与文件在同一台机器上时可用；服务部署在远端时路径读不到。"
                        "读不到时不要反复试其它路径，直接用 image_host_info 取预签名地址直传换 url。"
                        "支持 PNG/JPEG/WebP/GIF；"
                        f"超过 {MAX_REFERENCE_MB} MB 时自动直传对象存储换链接（需网关配置 S3）。"
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
        "description": (
            "查询 generate_image 创建的作业；status 为 queued / running / succeeded / failed / canceled。"
            "作业保存在网关 SQLite 中，网关或本服务重启都不会丢失（保留 24 小时）。"
            "出图成功时一次返回两样东西：images[].url（上游直链）与 image 内容块（图片字节），"
            "客户端可直接保存/显示，不必再按链接下载。"
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "job_id": {"type": "string", "description": "generate_image 返回的作业 ID"},
                "inline": {
                    "type": "boolean",
                    "description": (
                        "默认 true：把图片字节以 MCP image 内容块随响应返回，与直链一起给全。"
                        "设为 false 只返回直链（体积小，但客户端需自行下载）。"
                    ),
                },
            },
            "required": ["job_id"],
        },
    },
    {
        "name": "cancel_image_job",
        "description": (
            "放弃仍在排队或执行中的作业：立即停止网关侧的等待与轮询，作业标记为 canceled。"
            "注意它不保证终止上游任务——异步渠道可能继续执行且已产生费用（不退还）；"
            "同步渠道若已提交，同样可能已经计费。"
        ),
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
            "若网关侧为该模型声明过约束，还会返回 specs[模型名]：参考图上限、出图张数、"
            "输出格式与分辨率区间、尺寸倍数（宽高需为 16/32 等的正整数倍）、是否支持中文等。传参前先看 specs；未出现的字段表示未声明（未知），"
            "不代表不限制，用常规取值即可。"
        ),
        "inputSchema": {"type": "object", "properties": {}, "required": []},
    },
    {
        "name": "image_host_info",
        "description": (
            "向网关申请一个对象存储预签名直传地址（mode=presigned_put），"
            "用途：把本地图片换成可作为 image_url 的短链接。"
            "返回 upload_url / method / headers / public_url / download_url / expires_at；"
            "网关只签 URL、不接收字节，客户端把图片原始字节按 method PUT 到 upload_url 即完成上传；"
            "上传时必须设置 Content-Type（如 image/png、image/jpeg），否则对象存储会按二进制流保存，"
            "链接在浏览器里容易变成下载。"
            "上传是普通 HTTP（不经过 MCP，不受 8 MB 请求体限制），"
            "成功后把 public_url（私有桶则为 download_url）作为 image_url 使用。"
            "预签名地址有时效，过期后重新调用本工具签发。"
            "参考图在本机读不到、或超过单张 base64 上限时，优先走这里。"
            "建议传 filename（如 ref.png），网关会按扩展名生成带后缀的对象键；"
            "不传则对象键没有后缀，部分场景下浏览器会按下载处理而不是直接预览。"
            "网关未配置对象存储（501）或该密钥没有对象存储权限（403）时，"
            "返回 enabled=false 并在 reason / hint 里说明原因。"
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "filename": {
                    "type": "string",
                    "description": "可选，带扩展名的文件名（如 ref.png），仅用于生成对象键后缀；支持 png/jpg/jpeg/webp/gif",
                },
            },
            "required": [],
        },
    },
]

# initialize 返回的说明书：客户端不依赖外部技能包也能完成第一次调用。
INSTRUCTIONS = (
    "本服务把生图请求转发到 AI Gateway，作业由网关持久化（SQLite，保留 24 小时）。推荐流程："
    "1) 不确定模型时先调用 list_image_models，拿到 models 列表与 default_model；"
    "2) 调用 generate_image，至少传 prompt；没有 default_model 时必须显式传 model；"
    "它立即返回 job_id 与 status，不等待出图；"
    "3) 每 5 秒调用 get_image_job(job_id)；"
    "作业状态共 queued/running/succeeded/failed/canceled 五种，只有 succeeded 才有图片，"
    "failed/canceled 会以 isError 返回；"
    "   网关或本服务重启不会丢作业：异步任务会继续轮询，同步任务会标记为 gateway_restarted 让你重提；"
    "4) 成功时一次拿到两样东西：images[].url 是上游直接签发的链接（有效期不受网关控制），"
    "同一次响应里还会带上 image 内容块（图片字节），客户端可直接保存/显示，不必再按链接下载；"
    "只想要直链、不要图片数据时传 inline=false；"
    "5) 失败时 isError=true，structuredContent.error 含 code/message/retryable；"
    "6) 超时重试请带上同一个 request_id，网关会复用已有作业，不会重复生图；"
    "7) 需要放弃时调用 cancel_image_job(job_id)：只停止网关侧等待与轮询，"
    "不保证终止上游任务，异步渠道可能继续执行并已产生费用；"
    "8) 参考图三种来源：公网 http(s) 链接直接作为 image_url；本机文件用 image_path（仅同机可用）；"
    "对话里粘贴的图片、或本机读不到的文件，先调用 image_host_info（传 filename，如 ref.png）"
    "取对象存储预签名 upload_url，"
    "把图片原始字节 PUT 上去换取 url（记得设置 Content-Type，如 image/png），再作为 image_url 传入——"
    "该请求不经过 MCP，不受 8 MB 请求体限制。"
    "image_path 报文件不存在时不要换路径反复试，直接改走直传。"
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


# 访问本机网关不走系统代理；下载参考图同样直连（内网地址走代理只会失败）
_LOCAL_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


# ---------- 作业存储 ----------

# 作业记录由网关持久化（SQLite），本服务只做提交与轮询，进程重启不丢作业。


# ---------- 网关调用 ----------

# ---------- 网关作业 ----------

def _gateway_call(method, path, key="", body=None, timeout=30, not_found="not_found"):
    """调用网关的 JSON 接口；密钥优先取调用方带来的，其次回落 env。"""
    key = (key or "").strip() or _api_key()
    if not key:
        raise ToolError(
            "没有可用的 API 密钥：请在「MCP 服务器 → 生图 → 编辑 → 密钥」里填入面板签发的 sk- 密钥"
            "（也可在 mcp.json 的 env 里设置 AIGW_IMAGE_API_KEY）",
            code="auth_missing",
        )

    data = None
    if body is not None:
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(_base_url() + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("Accept", "application/json")
    req.add_header("User-Agent", "ai-gateway-image-mcp/1.0")
    if data is not None:
        req.add_header("Content-Type", "application/json")

    try:
        with _LOCAL_OPENER.open(req, timeout=timeout) as resp:
            raw = resp.read(16 << 20)
    except urllib.error.HTTPError as exc:
        message, code, retryable = _gateway_error(exc, not_found)
        raise ToolError(message, code=code, retryable=retryable, upstream_status=exc.code)
    except urllib.error.URLError as exc:
        reason = getattr(exc, "reason", exc)
        if isinstance(reason, TimeoutError):
            raise ToolError("请求网关超时（%d 秒）" % timeout, code="timeout", retryable=True)
        raise ToolError(
            "无法连接网关 %s：%s（确认网关已启动、端口正确）" % (_base_url(), reason),
            code="gateway_unreachable",
            retryable=True,
        )
    except TimeoutError:
        raise ToolError("请求网关超时（%d 秒）" % timeout, code="timeout", retryable=True)
    except Exception as exc:
        raise ToolError("请求网关失败：%s" % exc, code="gateway_unreachable", retryable=True)

    try:
        return json.loads(raw.decode("utf-8", "replace"))
    except ValueError:
        raise ToolError(
            "网关返回的不是 JSON：%s" % _clip(raw.decode("utf-8", "replace"), 300),
            code="invalid_response",
            retryable=True,
        )


def _submit_job(payload, request_id="", key=""):
    """提交异步生图作业，返回网关作业对象（id/status/...）。"""
    body = dict(payload)
    body["async"] = True
    if request_id:
        body["request_id"] = request_id
    data = _gateway_call("POST", "/v1/images/generations", key, body)
    if isinstance(data, dict) and data.get("status") and data.get("id"):
        return data
    raise ToolError(
        "网关返回格式不可识别（缺少作业 id/status）：%s"
        % _clip(json.dumps(data, ensure_ascii=False), 300),
        code="invalid_response",
        retryable=True,
    )


def _fetch_job(job_id, key=""):
    if not job_id:
        raise ToolError("缺少作业 ID", code="invalid_request")
    data = _gateway_call(
        "GET", "/v1/images/jobs/" + urllib.parse.quote(job_id, safe=""), key, not_found="job_not_found"
    )
    if not isinstance(data, dict) or not data.get("status"):
        raise ToolError("网关返回的作业格式不可识别", code="invalid_response", retryable=True)
    return data


def _cancel_job(job_id, key=""):
    if not job_id:
        raise ToolError("缺少作业 ID", code="invalid_request")
    data = _gateway_call(
        "DELETE", "/v1/images/jobs/" + urllib.parse.quote(job_id, safe=""), key, not_found="job_not_found"
    )
    if not isinstance(data, dict) or not data.get("status"):
        raise ToolError("网关返回的作业格式不可识别", code="invalid_response", retryable=True)
    return data


def _job_elapsed_ms(view):
    created = view.get("created_at")
    if not isinstance(created, (int, float)):
        return 0
    updated = view.get("updated_at")
    end = updated if isinstance(updated, (int, float)) else time.time()
    return max(0, int((float(end) - float(created)) * 1000))


def _size_text(num):
    """把字节数写成易读的 KB/MB，避免小图显示成「0 MB」。"""
    if num >= 1 << 20:
        return "%d MB" % (num >> 20)
    return "%d KB" % max(1, num >> 10)


def _image_items(view):
    """从作业视图里收集图片条目，兼容各种上游响应形状。

    网关严格透传上游响应体，作业视图按顶层字段平铺，因此图片可能出现在：
      - data: [{url|b64_json}]      OpenAI 标准形状；
      - output_images: ["url"]      ModelScope 等异步服务商；
      - images / result.images     部分服务商的自定义字段。
    值既可能是字符串直链，也可能是带 url / b64_json 的对象。
    与网关的校验口径一致：命中第一个非空字段就停止，避免同一张图被重复返回。
    """
    candidates = [view.get("data"), view.get("output_images"), view.get("images")]
    result = view.get("result")
    if isinstance(result, dict):
        candidates.extend([result.get("images"), result.get("data"), result.get("output_images")])
    for value in candidates:
        if isinstance(value, list) and value:
            return value
        if isinstance(value, (dict, str)) and value:
            return [value]
    return []


def _job_images(view, inline):
    """把网关作业结果转成 images[]，并按需内联图片字节。

    返回 (images, blocks)：blocks 是 MCP image 内容块，inline=True 时才填充。
    网关不落盘：图片要么是上游直链，要么是上游给的 base64，
    本服务只负责取字节并内联交付，拿不到字节就退回直链，二者在同一次响应里给全。
    """
    images = []
    blocks = []
    inlined = 0
    for item in _image_items(view):
        image_url = ""
        b64 = ""
        if isinstance(item, str):
            image_url = item.strip()
        elif isinstance(item, dict):
            image_url = (item.get("url") or item.get("image_url") or "").strip()
            b64 = item.get("b64_json") or item.get("b64") or ""
        if not image_url and not b64:
            continue

        entry = {}
        if image_url:
            entry["url"] = image_url
        if isinstance(item, dict) and isinstance(item.get("expires_at"), (int, float)):
            entry["expires_at"] = int(item["expires_at"])

        raw = b""
        mime = ""
        if inline and b64:
            # 上游直接给 base64（未落盘）：解码后只走 image 块，不再回显文本
            raw, mime = _decode_base64_image(b64)
        if inline and image_url and not raw:
            try:
                raw, mime = _fetch_image(image_url)
            except ToolError as exc:
                # 下载失败不影响已生成的直链
                entry["inline_error"] = exc.message
        if not entry and not raw:
            continue
        if raw:
            entry["bytes"] = len(raw)

        inlined_this = False
        if inline and raw and mime.startswith("image/"):
            if len(raw) > MAX_INLINE_BYTES:
                entry["inline_skipped"] = (
                    "图片 %s 超过单张内联上限 %d MB，未随响应返回；"
                    "请用 url 下载，或调大 AIGW_IMAGE_INLINE_MB"
                    % (_size_text(len(raw)), MAX_INLINE_BYTES >> 20)
                )
            elif inlined + len(raw) > MAX_INLINE_TOTAL_BYTES:
                entry["inline_skipped"] = (
                    "本次响应内联图片已达内联总量上限（%s），这张未随响应返回；"
                    "请用 url 下载，或减少数量 / 调大 AIGW_IMAGE_INLINE_TOTAL_MB"
                    % _size_text(MAX_INLINE_TOTAL_BYTES)
                )
            else:
                inlined += len(raw)
                inlined_this = True
                blocks.append({
                    "type": "image",
                    "data": base64.b64encode(raw).decode("ascii"),
                    "mimeType": mime,
                })

        # 内联了就不再回显 b64 文本（同一张图出现两份等于体积翻倍）；
        # 只有「这张没内联成功、又没有直链」时才把 b64 当作唯一可用的句柄。
        if b64 and not inlined_this and "url" not in entry:
            entry["b64_json"] = b64
        if not entry and inlined_this:
            # 只走 image 块的图片也要占一条，保证 count 与内容块数量对得上
            entry["inlined"] = True
        if entry:
            images.append(entry)
    return images, blocks


def _job_result(view, inline=True, reused=False):
    images, blocks = _job_images(view, inline)
    if not images:
        raise ToolError("网关返回的图片为空", code="empty_result", retryable=True)
    result = {
        "status": "succeeded",
        "job_id": view.get("id", ""),
        "model": view.get("model", ""),
        "count": len(images),
        "images": images,
    }
    if blocks:
        # 由 call_tool 取出拼成 image 内容块，不进入 structuredContent
        result["_inline_images"] = blocks
    if isinstance(view.get("expires_at"), (int, float)):
        # 作业记录的保留期（24 小时），与图片链接过期时间不是一回事
        result["job_expires_at"] = int(view["expires_at"])
    if reused:
        result["reused"] = True
    return result


def _raise_job_failure(view):
    err = view.get("error") or {}
    if not isinstance(err, dict):
        err = {"message": str(err)}
    code = err.get("code") or ("canceled" if view.get("status") == "canceled" else "upstream_error")
    raise ToolError(
        err.get("message") or "生图失败（上游未提供原因）",
        code=code,
        retryable=bool(err.get("retryable")),
        upstream_status=err.get("upstream_status") or 0,
    )


def _gateway_error(exc, not_found_code="not_found"):
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
        # 作业不存在/已过期也走 404，此时网关会带说明；有说明就原样透传，
        # 只有响应体为空才怀疑是网关地址或路径配置错了。
        if detail:
            return (detail, not_found_code, False)
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


# ---------- 图片取回 ----------

def _decode_base64_image(b64_text):
    """解码网关返回的 base64 图片，返回 (原始字节, mime)。

    网关严格透传上游响应，b64_json 可能带 data URI 前缀（data:image/png;base64,...），
    这里先剥掉前缀再解码，避免原样透传导致的解码失败。
    """
    text = (b64_text or "").strip()
    mime = ""
    if text.lower().startswith("data:"):
        header, _, payload = text.partition(",")
        text = payload.strip()
        mime = header[5:].split(";")[0].strip().lower()
    try:
        raw = base64.b64decode(text)
    except Exception:
        raise ToolError("网关返回的 base64 图片无法解码", code="invalid_response", retryable=True)
    if len(raw) > MAX_DOWNLOAD_BYTES:
        raise ToolError("图片超过 %d MB，无法取回" % (MAX_DOWNLOAD_BYTES >> 20), code="too_large")
    return raw, mime if mime.startswith("image/") else _mime_from_bytes(raw)


def _fetch_image(url):
    """按直链取回图片字节，返回 (原始字节, mime)。"""
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise ToolError(
            "图片地址不是 http/https，无法取回：%s" % _clip(url, 120),
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
            "取回图片失败（HTTP %d）：%s" % (exc.code, _clip(url, 120)),
            code="download_failed",
            retryable=exc.code >= 500,
            upstream_status=exc.code,
        )
    except Exception as exc:
        raise ToolError("取回图片失败：%s" % exc, code="download_failed", retryable=True)
    if len(raw) > MAX_DOWNLOAD_BYTES:
        raise ToolError("图片超过 %d MB，拒绝取回" % (MAX_DOWNLOAD_BYTES >> 20), code="too_large")
    # 优先信任响应头里的图片类型，否则按文件头嗅探
    mime = ""
    head = (content_type or "").split(";")[0].strip().lower()
    if head.startswith("image/"):
        mime = head
    return raw, mime or _mime_from_bytes(raw)


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


def _ext_from_mime(mime):
    """把 MIME 转成扩展名，用于对象键后缀。"""
    return {
        "image/png": "png",
        "image/jpeg": "jpg",
        "image/webp": "webp",
        "image/gif": "gif",
    }.get((mime or "").strip().lower(), "png")


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


def _storage_ticket(key, filename=""):
    """向网关申请对象存储预签名直传凭证，返回 (ticket, reason)。

    ticket 为 None 时 reason 说明原因：disabled=网关未配置对象存储，
    forbidden=该密钥没有对象存储权限，unauthorized=密钥无效，failed=其它失败。
    网关只签 URL，不接收图片字节：客户端拿到 upload_url 后自己 PUT 到对象存储。
    """
    key = (key or "").strip() or _api_key()
    if not key:
        return None, "unauthorized"
    body = json.dumps({"filename": filename}, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(_base_url() + "/v1/storage/tickets", data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + key)
    req.add_header("User-Agent", "ai-gateway-image-mcp")
    try:
        with _LOCAL_OPENER.open(req, timeout=15) as resp:
            data = json.loads(resp.read(1 << 20).decode("utf-8", "replace"))
    except urllib.error.HTTPError as exc:
        if exc.code == 401:
            return None, "unauthorized"
        if exc.code == 403:
            return None, "forbidden"
        if exc.code == 501:
            return None, "disabled"
        return None, "failed"
    except Exception:
        return None, "failed"
    if not isinstance(data, dict) or not data.get("upload_url"):
        return None, "failed"
    return data, ""


def _image_host_info(key="", filename=""):
    """返回对象存储直传说明 + 一张可直接使用的预签名上传凭证。

    只返回「预签名 URL + 方法」，文件本体由客户端 HTTP 直传对象存储，
    不进 MCP 的 JSON body。filename 仅用于让网关生成带后缀的对象键。
    """
    ticket, reason = _storage_ticket(key, filename)
    if not ticket:
        if reason == "forbidden":
            hint = (
                "当前 API 密钥没有对象存储权限（storage_scope=禁用）。"
                "请到网关面板「API 密钥」里为该密钥开启对象存储权限，"
                "或改用公网 http(s) 图片链接作为参考图。"
            )
        elif reason == "unauthorized":
            hint = (
                "网关鉴权失败：检查「MCP 服务器 → 生图 → 密钥」或 mcp.json 的 "
                "AIGW_IMAGE_API_KEY 是否为有效密钥。"
            )
        else:
            hint = (
                "网关未配置对象存储，无法签发直传地址。"
                "请在网关面板「设置 → 对象存储」中填写并保存。"
                "参考图请用公网 http(s) 链接，或压缩到单张 3 MB 以内转 base64。"
            )
        return {"enabled": False, "reason": reason, "hint": hint}
    out = {
        "enabled": True,
        "mode": "presigned_put",
        "driver": ticket.get("driver") or "s3",
        "bucket": ticket.get("bucket") or "",
        "key": ticket.get("key") or "",
        "method": ticket.get("method") or "PUT",
        "headers": ticket.get("headers") or {},
        "upload_url": ticket.get("upload_url") or "",
        "download_url": ticket.get("download_url") or "",
        "public_url": ticket.get("public_url") or "",
        "expires_at": int(ticket.get("expires_at") or 0),
        "ttl_seconds": int(ticket.get("expires_in") or 0),
        "hint": (
            "把图片原始字节 PUT 到 upload_url 即完成上传（签名在 URL 里，不需要额外的鉴权头）。"
            "务必设置 Content-Type，例如 image/png / image/jpeg / image/webp，且要与文件真实类型一致；"
            "不设置的话对象存储会按 application/octet-stream 保存，链接容易被浏览器当成下载。"
            "示例：curl -sS -X PUT '<upload_url>' -H 'Content-Type: image/png' --data-binary @/本地/图片.png 。"
            "上传成功后把 public_url（若为空则用 download_url）作为 generate_image 的 image_url。"
            "该请求是普通 HTTP，不经过 MCP，因此不受 8 MB 请求体限制。"
            "预签名地址有有效期（expires_at 为 Unix 秒），过期后重新调用本工具签发。"
        ),
    }
    if not out["public_url"]:
        out["hint"] += (
            "当前桶是私有的，没有公开地址：image_url 请用 download_url —— 它自带签名，"
            "在有效期内上游可以直接下载。"
        )
    return out


def _upload_reference(raw, mime, index, key):
    """超预算参考图直传对象存储，返回可作为 image_url 的链接。"""
    ticket, reason = _storage_ticket(key, "ref-%d.%s" % (index, _ext_from_mime(mime)))
    if not ticket:
        if reason == "forbidden":
            raise ToolError(
                "第 %d 张参考图超过内联上限，且当前 API 密钥没有对象存储权限" % index,
                code="storage_forbidden",
                hint="请到网关面板「API 密钥」中开启对象存储权限，或压缩图片后重试。",
            )
        raise ToolError(
            "第 %d 张参考图超过内联上限，且网关未配置对象存储（无法换取公网链接）" % index,
            code="reference_exceeds_base64_limit",
            hint="请压缩图片后重试，或改用公网图片地址。",
        )
    headers = dict(ticket.get("headers") or {})
    if mime and "Content-Type" not in headers:
        headers["Content-Type"] = mime
    req = urllib.request.Request(str(ticket["upload_url"]), data=raw, method=str(ticket.get("method") or "PUT"))
    for name, value in headers.items():
        req.add_header(name, value)
    req.add_header("User-Agent", "ai-gateway-image-mcp")
    try:
        with _LOCAL_OPENER.open(req, timeout=60):
            pass
    except urllib.error.HTTPError as exc:
        detail = ""
        try:
            detail = exc.read(4096).decode("utf-8", "replace")
        except Exception:
            pass
        raise ToolError(("第 %d 张参考图直传失败: HTTP %d %s" % (index, exc.code, detail)).strip(),
                        code="image_upload_failed",
                        hint="检查桶策略是否允许 PUT，以及预签名地址是否已过期。") from exc
    except Exception as exc:
        raise ToolError("第 %d 张参考图直传失败: %s" % (index, exc),
                        code="image_upload_failed") from exc
    url = str(ticket.get("public_url") or ticket.get("download_url") or "")
    if not url:
        raise ToolError("第 %d 张参考图上传后没有拿到链接" % index, code="image_upload_failed")
    return url


def _reference_from_bytes(raw, mime, index, key):
    """预算内直接转 base64；超预算时直传对象存储换成链接。"""
    if len(raw) <= MAX_REFERENCE_BYTES:
        return "data:%s;base64,%s" % (mime, base64.b64encode(raw).decode("ascii"))
    return _upload_reference(raw, mime, index, key)


def _normalize_reference(value, index, key):
    """公网链接原样透传；内网链接由本服务下载后再决定 base64 还是上传。"""
    text = str(value or "").strip()
    if not text:
        raise ToolError("第 %d 张参考图为空" % index, code="invalid_reference")
    if not text.lower().startswith(("http://", "https://")):
        raise ToolError("第 %d 张参考图无法识别。%s" % (index, REFERENCE_HINT),
                        code="invalid_reference",
                        hint="先用 image_host_info 取对象存储预签名地址，把图片字节 PUT 上去换 url。")
    if _is_public_url(text):
        return text
    raw, mime = _fetch_reference(text, index)
    return _reference_from_bytes(raw, mime, index, key)


def _reference_from_path(value, index, key):
    """读取本机图片文件，按预算决定转 base64 还是直传对象存储。"""
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
            hint="不要继续尝试其它本机路径。改用 image_host_info 拿带凭据的 upload_url，"
                 "把图片原始字节 POST 上去换取 url，再作为 image_url 传入。",
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
    # 网关手工声明的约束（尺寸、参考图上限等）：只有面板里配过才有这一项。
    # 旧网关没有 image 字段，这里自然为空，行为与之前一致。
    specs = {}
    if isinstance(items, list):
        for item in items:
            if isinstance(item, dict) and item.get("id"):
                name = str(item["id"])
                models.append(name)
                spec = _model_spec(item.get("image"))
                if spec:
                    specs[name] = spec
    if not models:
        raise ToolError(
            "当前没有可用的生图模型：请到面板「渠道」把模型加入「生图模型」并选择同步或异步",
            code="no_image_models",
        )

    payload = {"status": "succeeded", "count": len(models), "models": models}
    if specs:
        # 只保留有人工声明的字段，避免把一堆默认值塞给客户端
        payload["specs"] = specs
        payload["specs_hint"] = (
            "specs 里是网关侧声明的模型约束（参考图上限、出图张数、输出格式与分辨率、"
            "尺寸倍数（宽高需为其正整数倍）、能否写中文等）； "
            "未出现的字段表示未声明（未知），不代表不限制——请使用常规取值，不要推到极端。"
        )
    default = _default_model()
    if default:
        payload["default_model"] = default
    else:
        payload["hint"] = "generate_image 需要显式传 model（或在 mcp.json 的 env 里设置 AIGW_IMAGE_MODEL）"
    return payload


def _model_spec(raw):
    """把网关 /v1/models 里的 image 元数据压成「只留有值」的精简结构。

    网关为了表单往返方便，字段全是固定键、零值表示未声明；
    这里把零值全部丢掉，客户端看到的每一项都是真实约束。
    """
    if not isinstance(raw, dict):
        return None
    spec = {}

    desc = str(raw.get("description") or "").strip()
    if desc:
        spec["description"] = desc

    # 能力开关：只列出为真的项，省得客户端逐项判断 false
    abilities = []
    for key, label in (
        ("reference", "参考图"),
        ("negative_prompt", "反向提示词"),
        ("seed", "随机种子"),
        ("chinese_prompt", "中文提示词"),
        ("chinese_text", "画面写中文"),
    ):
        if raw.get(key):
            abilities.append(label)
    if abilities:
        spec["abilities"] = abilities

    # 参考图约束：reference 为 false 时 max_ref_count 没有意义，不输出，
    # 否则会出现「abilities 里没有参考图，却给了参考图上限」的矛盾
    ref = {}
    n = _as_int(raw.get("max_ref_count"))
    if raw.get("reference") and n > 0:
        ref["max_count"] = n
    if ref:
        spec["reference"] = ref

    # 出图张数；不支持参考图时，「带参考图」的上限同样不输出
    counts = {}
    for key, name in (
        ("max_n", "max"),
        ("max_n_ref", "max_with_reference"),
    ):
        if name == "max_with_reference" and not raw.get("reference"):
            continue
        n = _as_int(raw.get(key))
        if n > 0:
            counts[name] = n
    if counts:
        spec["n"] = counts

    # 输出规格
    out = {}
    formats = raw.get("output_formats")
    if isinstance(formats, list) and formats:
        out["formats"] = [str(f) for f in formats]
    for key, name in (
        ("min_width_px", "min_width"),
        ("min_height_px", "min_height"),
        ("max_width_px", "max_width"),
        ("max_height_px", "max_height"),
        ("size_multiple", "size_multiple"),
    ):
        n = _as_int(raw.get(key))
        if n > 0:
            out[name] = n
    if out:
        spec["output"] = out

    return spec or None


def _as_int(v):
    try:
        return int(v)
    except (TypeError, ValueError):
        return 0


def _generate(args, key=""):
    payload = _build_payload(args, key)
    request_id = _request_id(args)

    # 作业由网关持久化：相同 request_id 会复用同一个作业，重试不会重复生图
    view = _submit_job(payload, request_id, key)
    reused = bool(view.get("reused"))
    job_id = str(view.get("id") or "")
    status = str(view.get("status") or "")
    if status in ("failed", "canceled"):
        _raise_job_failure(view)
    if status == "succeeded":
        # 复用已完成的作业时结果已就绪，不必再让客户端多轮一次
        return _job_result(view, True, reused)

    # 只提交不等待：出图耗时不可预测，阻塞调用容易撞客户端超时
    return {
        "status": status or "queued",
        "job_id": job_id,
        "stage": view.get("stage") or "",
        "reused": reused,
        "hint": (
            "作业已提交，请每 5 秒调用 get_image_job(job_id=\"%s\") 获取结果；"
            "成功时直链与图片数据会一起返回" % job_id
        ),
    }


def _job_status(args, key=""):
    job_id = str(args.get("job_id") or "").strip()
    if not job_id:
        raise ToolError("缺少参数 job_id")
    inline = _bool_arg(args, "inline", True)
    view = _fetch_job(job_id, key)
    status = str(view.get("status") or "")

    if status == "succeeded":
        return _job_result(view, inline)
    if status in ("failed", "canceled"):
        err = view.get("error") or {}
        if not isinstance(err, dict):
            err = {"message": str(err)}
        return {
            "status": status,
            "job_id": job_id,
            "elapsed_ms": _job_elapsed_ms(view),
            "error": {
                "code": err.get("code") or ("canceled" if status == "canceled" else "upstream_error"),
                "message": err.get("message") or (
                    "作业已取消" if status == "canceled" else "生图失败（上游未提供原因）"
                ),
                "retryable": bool(err.get("retryable")),
            },
        }
    return {
        "status": status or "queued",
        "job_id": job_id,
        "stage": view.get("stage") or "",
        "poll_count": view.get("poll_count") or 0,
        "elapsed_ms": _job_elapsed_ms(view),
        "hint": "继续每 5 秒调用 get_image_job；作业保存在网关，重启不会丢失",
    }


def _cancel(args, key=""):
    job_id = str(args.get("job_id") or "").strip()
    if not job_id:
        raise ToolError("缺少参数 job_id")
    view = _cancel_job(job_id, key)
    return {
        "status": view.get("status") or "canceled",
        "job_id": job_id,
        "hint": (
            "只停止网关侧的等待与轮询；不保证终止上游任务，"
            "异步渠道可能继续执行并已产生费用，同步渠道若已提交同样可能已经计费。"
        ),
    }


def call_tool(name, arguments, key=""):
    if not isinstance(arguments, dict):
        arguments = {}
    try:
        if name == "generate_image":
            payload = _generate(arguments, key)
        elif name == "get_image_job":
            payload = _job_status(arguments, key)
        elif name == "cancel_image_job":
            payload = _cancel(arguments, key)
        elif name == "list_image_models":
            payload = _list_models(key)
        elif name == "image_host_info":
            payload = _image_host_info(key, str(arguments.get("filename") or ""))
        else:
            return _error_result("未知工具: %s" % name, code="unknown_tool")
    except ToolError as exc:
        return _error_result(exc.message, exc.code, exc.retryable, exc.upstream_status, exc.hint)
    except Exception as exc:
        return _error_result("内部错误: %s" % exc, code="internal_error")

    blocks = []
    if isinstance(payload, dict):
        # 内联图片块从 payload 里摘出来，避免把 base64 再塞进 structuredContent 和文本
        blocks = payload.pop("_inline_images", None) or []
    content = [{"type": "text", "text": json.dumps(payload, ensure_ascii=False)}]
    content.extend(blocks)
    return {
        "content": content,
        "structuredContent": payload,
        "isError": False,
    }


def _error_result(message, code="internal_error", retryable=False, upstream_status=0, hint=""):
    error = {"code": code, "message": message, "retryable": bool(retryable)}
    if upstream_status:
        error["upstream_status"] = int(upstream_status)
    if hint:
        error["hint"] = hint
    text = "[%s] %s" % (code, message)
    if hint:
        text += "：" + hint
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
                "instructions": INSTRUCTIONS,
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

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.daemon_threads = True
    print("image-generation MCP listening on 127.0.0.1:%d%s" % (port, MCP_PATH), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
