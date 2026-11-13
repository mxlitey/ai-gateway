---
name: image-gen
description: Generate or edit images through the ai-gateway image-gen MCP tools. Use when the user wants a picture created, rendered, or edited, including from reference images. Do not use for video, audio, or text-only work.
---

# 生图（image-gen）

本仓库已内置本地 MCP 服务 `examples/mcp/image-generation`（分发到 `mcp/image-generation`，
别名 `image-gen`），它把生图请求转给网关的
OpenAI 兼容接口 `POST /v1/images/generations`。异步服务商（如 ModelScope）的「提交任务 + 轮询」
由网关内部完成，本工具只需发起一次调用。

## 开工前确认

1. 工具是否在列：聚合接入时叫 `image-gen__generate_image` / `image-gen__get_image_job`；
   单服务器接入（`/mcp/image-gen`）时叫 `generate_image` / `get_image_job`。
2. 网关「MCP 服务器」页里 `生图` 状态为就绪；否则让用户先在网关里启动它。
3. 该 MCP 服务器的密钥需有生图渠道权限（面板「API 密钥 → 渠道权限」）。
4. 模型：`model` 必填。用 `GET /v1/models?type=image` 查可生图的模型（只返回已标记生图、
   且渠道配了生图能力的），让用户选；不带 `type=image` 会把对话模型也列出来，不能直接照着传。
   返回 `没有被标记为生图模型` 就换一个已配置的，不要重复重试同一个模型。

工具不可用时不要伪造结果，直接说明缺哪一步并提供上面的排查项。

## 工作流

1. 明确提示词、尺寸、数量、参考图，缺什么问什么，不要替用户编造主体。
2. 调 `generate_image`，`wait_seconds` 给 20–60 秒。
3. 返回 `status=succeeded` 直接交付；返回 `status=running` 就每 5 秒调 `get_image_job(job_id)`，
   直到 `succeeded` / `failed`。不要因为一次 running 就重复提交新任务。

**不要绕过本工具直连 `POST /v1/images/generations`**：同步等待会一直阻塞到出图（几十秒到几分钟）。
网关本身不限制对话/生图的总时长，但网关前面的 CDN（如 Cloudflare）通常约 100 秒就断，
实测直接返回 524。异步服务商必须走 `generate_image` 拿 `job_id` 再轮询。

## 参数

| 参数 | 说明 |
| --- | --- |
| `prompt` | 必填。用具体可见的细节描述最终画面 |
| `model` | 必填（除非服务端配了默认模型） |
| `size` | 如 `1024x1024`、`1328x1328`；留空由服务商决定 |
| `n` | 1–4，默认 1 |
| `negative_prompt` | 反向提示词，上游支持时生效 |
| `seed` | 复现同一张图时固定 |
| `loras` | 原样透传的 LoRA 配置对象 |
| `extra` | 其它上游参数，原样透传 |
| `response_format` | 默认 `url`；`b64_json` 体积很大，一般不用 |
| `download` | `true` 时把图存到本地并返回路径 |
| `wait_seconds` | 本次最多阻塞秒数，默认 20 |

## 参考图

按「图 1、图 2」的顺序传，**顺序即提示词里的 Picture 1/2**。

- `image_url`：公网 http(s) 链接、`data:image/...;base64,...`、纯 base64（会自动补 data URI）。
- `image_path`：本机文件路径，服务读文件后转 data URI。**只在 MCP 服务与文件同机时可用**，
  即网关本机部署；MCP 跑在远端时路径会被拼成 `/mcp/image-generation/C:/...` 报文件不存在，
  这种情况改用公网 URL 或 base64。
- 约束：单张 ≤3 MB，base64 合计 ≤7 M 字符（解码后约 5.5 MB），建议最多 4 张。
  上限来自网关 MCP 入口的 8 MB 请求体，别按原始文件大小算总量——base64 会膨胀 4/3。
- 超过上限不要硬塞：改用公网 URL（图床），请求体只有几百字节，也能绕开这个上限。
- 对话里直接粘贴的图片不会自动进入工具参数。用户只给了图、没给路径或链接时，
  先问清楚「图片在本地的完整路径」，或让用户提供可访问的链接。
- **不要**自己把看到的图片"转成 base64"填进参数：模型拿不到原始字节，编出来的内容会解码失败。
- 裸 base64 已验证可用（工具按文件头嗅探 MIME 再补 `data:` 前缀），编排时无需手工拼接。
- 参考图 base64 有几十万字符，**不要**把它写进对话或工具参数：既撑爆上下文，Read 也会截断长行
  导致内容失真。同机场景传 `image_path`；远端场景用本地脚本自己读文件、编码、按 MCP 协议发
  `tools/call`，base64 全程不经过对话。
  本目录的 [image-gen-cli.py](image-gen-cli.py) 已实现这套流程：自动探测 MCP 配置、
  自动识别工具名（聚合入口 `image-gen__generate_image` 与单服务器入口都适用）、
  未传 `--model` 时拉 `/v1/models?type=image` 让你选、按文件头校验参考图、失败重试；
  参考图超出 base64 上限时，自动传到 Flyimg 图床改用公网直链（默认 Worker
  `https://flyimg.lii.qzz.io`，可用 `--worker` 或环境变量 `FLYIMG_WORKER_URL` 覆盖，
  传 `--worker ""` 可禁用）。

参考图的角色分工写在提示词里，工具参数没有 role 字段。详见 [references/prompt-guide.md](references/prompt-guide.md)。

## 耗时与并发

- 单张通常 7–120 秒。MCP 工具超过 `wait_seconds` 会返回 `running`，属正常，不是失败。
- `wait_seconds` 要小于网关对该 MCP 服务器的调用超时（面板默认 120 秒，且底层等响应头也是 120 秒，
  调到更大也突破不了），否则网关先断开，连 `job_id` 都拿不到。所以给 20–60 秒、靠轮询等结果。
- 网关对每个生图渠道有 `timeout_ms`（默认 300 秒，**只作用于异步轮询**），多渠道路由是串行故障转移；
  渠道多时逐个等满会超过本地服务自身上限（`AIGW_IMAGE_TIMEOUT_SECONDS`，默认 600 秒）。
- 渠道若配成 `sync` 模式，生图就是一次 POST 等到出图，受网关「等上游响应头 120 秒」限制，
  `timeout_ms` 对它不生效。耗时超过 120 秒的服务商必须配成 `async`，否则必然 502。
- 一个任务失败前不要连续重试多次；先看错误文案。

## 失败处理

| 返回 | 处理 |
| --- | --- |
| `没有可用的 API 密钥` | 让用户在「MCP 服务器 → 生图 → 编辑 → 密钥」填 `sk-` 密钥 |
| `网关鉴权失败（401）` | 密钥无效/停用，重新签发 |
| `网关拒绝（403）` | 密钥没有该 MCP 服务器权限，或没有渠道权限 |
| `没有被标记为生图模型` | 到「渠道」把该模型加进「生图模型」并选同步/异步 |
| `生图上游拒绝（HTTP 4xx）` | 多为参数或参考图问题，把上游原文转述给用户 |
| `轮询超时` | 上游排队过久，建议稍后重试或换模型 |
| `作业 xxx 不存在或已过期` | 本地服务重启会丢作业，重新 `generate_image` |
| `无法连接网关` | 网关没启动或端口不对 |

直连 `/v1/images/generations` 得到的 524 不属于本工具的返回，说明调用方绕过了 MCP（524 是 CDN 超时，
不是网关产生的）；504 才是网关的轮询超时。两者都改回 `generate_image` + `get_image_job`。

## 交付

- `download=true` 且返回了 `path`：用可点击的文件链接给出本地图片路径。注意 `path` 是**MCP 服务所在
  机器**的路径；MCP 跑在远端时本地打不开，改成给出 `url`。
- 只有 `url`：把链接给用户；链接是上游临时地址，可能过期，要长期保存就下载到本地。
- 一次生成多张就全部返回，不要只挑一张。
- 不要把生成结果再交给别的工具"检查一遍"，也不要自己重画；用户要求修图时走
  `generate_image` 的参考图编辑路径，而不是用代码拼接。
