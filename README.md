# AI Gateway

> 本地优先的 AI 模型网关 —— 一个绿色版可执行文件，统一管理你的所有模型渠道、密钥、MCP 工具与用量。

<p>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat&logo=go">
  <img alt="Platform" src="https://img.shields.io/badge/platform-Windows%2010%2F11%20%C2%B7%20Linux%20%2F%20Docker-0078D4?style=flat&logo=windows">
  <img alt="CGO" src="https://img.shields.io/badge/CGO_ENABLED-0-brightgreen?style=flat">
  <img alt="React" src="https://img.shields.io/badge/UI-React%2019%20%2B%20Vite-61DAFB?style=flat&logo=react">
  <img alt="License" src="https://img.shields.io/badge/license-MIT-blue?style=flat">
</p>

AI Gateway 是一个运行在你自己电脑（或自己的服务器）上的 API 网关。它把散落各处的模型服务收敛成一个
**统一的 OpenAI / Claude 双兼容入口**：上游可以是任何 OpenAI 兼容服务、Anthropic Claude、
各类国产与海外站点，以及你自己用 Python 写进 `plugins/` 目录的插件渠道。

在「统一入口」之外，它还负责一整套运营能力：多渠道路由与负载均衡、失败转移与密钥熔断、
客户端密钥签发与细粒度权限、MCP 工具聚合、用量与费用核算、以及一个随二进制内置的 Web 管理面板。

整个系统是**单一可执行文件**：Go 编译的网关 + 内嵌的 React 管理面板，默认只监听 `127.0.0.1`，
数据落在同级的 `data/` 目录，不出本机。

---

## 目录

- [能力概览](#能力概览)
- [快速开始](#快速开始)
- [客户端接入](#客户端接入)
- [架构](#架构)
- [配置](#配置)
- [插件与本地 MCP](#插件与本地-mcp)
- [对象存储直传](#对象存储直传)
- [生图接口](#生图接口)
- [从源码构建](#从源码构建)
- [项目结构](#项目结构)
- [安全](#安全)
- [License](#license)

## 能力概览

### 统一接入

- **双协议入口** — 同一端口同时提供 OpenAI（`/v1/chat/completions`、`/v1/models` 等）与
  Claude Messages（`/v1/messages`）API；请求、响应与 SSE 流式事件在两种协议间双向转换，
  客户端用哪种 SDK 都行
- **多协议上游** — 渠道可以是 OpenAI 兼容地址，也可以是 Claude 原生地址；插件渠道则是由网关
  拉起的本地进程
- **MCP 聚合** — 对外是一个 MCP 服务器（`POST /mcp`），对内聚合多台 Streamable HTTP 上游：
  合并工具列表、按命名空间路由工具调用
- **管理 MCP** — 可选开启 `POST /admin/mcp`，把网关自身的能力（用量、渠道、密钥、插件、
  MCP 上游、诊断、价格、生图模型元数据）暴露为 MCP 工具，供 AI 客户端查询与操作

### 路由与容错

- **渠道与模型映射** — 每个公开模型名可挂多个渠道，按「模型优先级」择优；支持公开名 → 上游名
  的映射、按渠道单独停用某个模型、自定义上游请求头（含占位符）
- **多密钥策略** — 同一渠道可配多把上游密钥，选「轮询分摊」或「顺序故障转移」
- **失败转移** — 上游返回 404 / 429 / 5xx、网络错误或响应不可解析时自动切到下一个目标；
  调用方主动断开导致的取消不计为上游故障
- **密钥熔断** — 失败的密钥进入指数递增冷却期，冷却中不参与选路；状态可查、可手动解除，
  熔断开关与基础/上限时长可在面板调整

### 密钥与权限

- **客户端密钥** — 面板一键签发 `sk-` 密钥，渠道权限、MCP 权限分别可设为「全部 / 仅勾选 / 禁用」，
  对象存储权限单独开关，随时停用；鉴权失败返回标准 OpenAI 错误格式
- **管理端隔离** — 管理面板走独立的密码登录与会话 Token，客户端 API 密钥无法登录面板
- **登录保护** — 管理员密码以 PBKDF2-HMAC-SHA256 加盐哈希存储；连续登录失败后全局指数退避
  （不依赖客户端 IP，适配反代/CDN 场景）

### 运营观测

- **用量记账** — 按密钥 / 模型 / 渠道 / 端点 / 上游密钥记录 token 用量与请求日志
- **价格表** — 手工维护、只按公开模型计价；支持输入分档、缓存命中价、峰谷时段折扣与
  生图按次计价，方便与厂商官方价格对照
- **仪表盘** — 概览 KPI、时间序列图表、渠道健康度、冷却中的密钥一览
- **错误日志** — 按日期筛选，区分「客户端→网关」与「网关→上游」两类来源，展开查看原始错误
- **管理面板** — 仪表盘 · 渠道管理 · 模型路由 · API 密钥 · MCP 服务器 · 插件 · 用量 ·
  价格表 · 错误日志 · 设置，桌面与移动端自适应

### 扩展与部署

- **插件体系** — 用 Python 写一个本地 HTTP 服务放进 `plugins/`，网关自动拉起、探活、
  注册为渠道；插件可声明控制台能力（账号登录、签到、额度查询）
- **本地 MCP 服务器** — 自写的 Python MCP 服务放进 `mcp/`，自动登记、自动分配回环端口、
  自动安装依赖、崩溃自动重启
- **三种形态** — Windows 桌面 GUI（Wails v3 + WebView2，托盘常驻）、纯命令行、Linux/Docker
  无界面部署，三者的配置与数据格式一致
- **绿色版分发** — `CGO_ENABLED=0` 纯 Go 静态编译，解压即用

## 快速开始

### 方式一：桌面版（Windows）

从 [Releases](../../releases) 下载压缩包，解压后双击 `ai-gateway-v<版本号>.exe`：

> 发版分为两个包：`ai-gateway-v<版本号>-windows-amd64.zip` 是网关本体，
> `ai-gateway-v<版本号>-plugins-mcp.zip` 是插件与本地 MCP 扩展包。
> 需要内置插件或 MCP 时，把扩展包解压到与 exe 同一目录即可（两者可分别升级）。

1. 窗口打开即自动启动网关（默认端口 `8787`），数据目录 `data/` 位于 exe 同级
2. 窗口内可改端口后「启动 / 重启」，用「访问范围」在「仅本机 / 局域网」间切换，
   也可添加 / 移除 / 重扫插件、查看实时日志
3. 点「管理面板」或在浏览器打开 <http://127.0.0.1:8787>，首次进入设置管理员密码
4. 「插件」页打开插件控制台完成账号登录（可选）；或直接在「渠道管理」添加自己的上游
   （Base URL + 上游密钥）
5. 「模型路由」把上游模型映射为公开模型名，并调整各渠道优先级
6. 「API 密钥」生成客户端密钥，交给任意客户端使用

关闭窗口不会停服——网关隐藏到系统托盘继续运行；右键托盘图标 →「退出」才会停止。

### 方式二：命令行 / 服务器

```bash
# 直接运行（Linux / macOS / Windows 均可）
go run ./cmd/gateway -host 127.0.0.1 -port 8787 -admin-password 'your-password'
```

或使用 Docker Compose：

```bash
# 管理员密码必须显式提供，否则 compose 会拒绝启动
AIGW_ADMIN_PASSWORD='你的强密码' docker compose up -d --build
# 浏览器打开 http://localhost:8788（compose 默认映射到宿主机 8788，避开桌面版的 8787）
```

镜像**只含网关本体**。`/data` 用命名卷持久化数据；`/plugins` 与 `/mcp` 是 bind mount，
把插件与本地 MCP 放进 compose 同级的 `./plugins` 与 `./mcp` 目录即可
（从 `ai-gateway-v<版本>-plugins-mcp.zip` 解压，或从仓库 `examples/` 复制）。插件放好后可在
面板「插件」页点「重新扫描」立即生效；本地 MCP 只在启动时扫描，需重启容器。

## 客户端接入

任何支持 OpenAI 或 Anthropic 协议的客户端都能直接对接：

```
API Base URL : http://127.0.0.1:8787/v1
API Key      : 面板「API 密钥」页生成
Model        : 「模型路由」页的公开模型名
```

```bash
# OpenAI 协议
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "你好"}]}'
```

```python
# Python SDK
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8787/v1",
    api_key="sk-你的密钥",
)
resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

```javascript
// Node.js SDK
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://127.0.0.1:8787/v1",
  apiKey: "sk-你的密钥",
});
const resp = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "你好" }],
});
```

Claude 协议同理：把 `baseURL` 指向 `http://127.0.0.1:8787`，密钥放在 `x-api-key`
或 `Authorization` 头均可。

### MCP 接入

MCP 客户端（Claude Code、Cursor、各类 Agent）把网关当成一台 MCP 服务器：

```
URL    : http://127.0.0.1:8787/mcp         # 聚合全部启用的上游
URL    : http://127.0.0.1:8787/mcp/<alias> # 只连某一台上游，工具名保持原样
Header : Authorization: Bearer sk-你的密钥
```

聚合模式下对外工具名形如 `<alias>__<原工具名>`（超过 64 字符会截断并附稳定短哈希），
避免多台上游重名。上游支持密钥轮换、服务器 / 密钥 / 工具三级每日调用上限，
以及两种计费口径（「调用成功即计费」或「响应含指定标记才计费」）。

## 架构

```
                 ┌──────────────────────────── AI Gateway（单二进制） ─────────────────────────────┐
 客户端 ──/v1──▶  代理层（密钥校验 · 模型路由 · 负载均衡 · 故障转移 · 密钥熔断 · 用量/费用统计）     │
                 │        │                                                                       │
                 │        │ 按渠道转发到                                                   │
                 │        ▼                                                                       │
                 │  OpenAI 兼容上游 / Claude 上游 / 插件渠道（插件进程）                           │
                 │                                                                                │
  MCP 客户端 ─/mcp▶ MCP 聚合层（工具合并 · 命名空间路由 · 配额/计费）──▶ 多台 Streamable HTTP 上游  │
                 │        ▲                                                                       │
 面板 ──/admin─▶  管理 API（密码登录 · 会话鉴权） ◀── 内嵌 Web 管理面板（React）                    │
                 │        │                                                                       │
                 │        ▼                                                                       │
                 │  插件宿主（发现 · 启动 · 探活 · 重启 · 回收 · 渠道注册 · 控制台代理）──▶ 插件进程 │
                 └────────────────────────────────────────────────────────────────────────────────┘
                              SQLite（配置/用量/日志） · data/ 目录
```

| 包 | 职责 |
|---|---|
| `internal/proxy` | 代理主逻辑：校验 `Authorization` / `x-api-key`，解析请求、选路、转发、SSE 修补与记账；`/v1/models` 直接返回可用的公开模型 |
| `internal/claudeapi` | Claude ⇄ OpenAI 的请求 / 响应 / 流式事件双向转换 |
| `internal/routing` | 目标选择：渠道优先级 + 密钥策略 + 熔断过滤 |
| `internal/penalty` | 上游密钥失败熔断（指数递增冷却） |
| `internal/mcp` | MCP 聚合：工具缓存与路由、配额、探活、JSON-RPC 处理 |
| `internal/adminmcp` | 管理 MCP 工具集（只读 / 读写两档令牌） |
| `internal/plugin` | 插件宿主：发现、启动、探活、退避重启、进程树回收、控制台代理、Python 运行时 |
| `internal/storage` | 对象存储（S3 兼容）预签名能力，网关不中转字节 |
| `internal/pricing` | 价格表与费用核算 |
| `internal/admin` | 管理 API：密码、会话鉴权、面板后端 |
| `internal/store` | SQLite 存储（`migrations/` 为版本化建表脚本） |
| `internal/app` | 启动组装（HTTP server、SQLite、插件宿主、MCP） |
| `internal/httpapi` | 路由分发：`/v1` · `/mcp` · `/admin/api` · `/admin/mcp` · `/health` · SPA 静态资源 |
| `internal/channel` | 渠道自定义请求头占位符与上游端点路径推导 |
| `internal/config` | 命令行参数与环境变量 |
| `internal/model` | 领域模型（渠道 / 密钥 / 路由 / 价格 / 日志 / MCP） |
| `internal/netinfo` | 本机局域网地址探测 |

## 配置

优先级：**命令行参数 > 环境变量 > 默认值**。面板中保存的运行期设置优先于启动参数（详见下文）。

| 参数 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `-host` | `AIGW_HOST` | `127.0.0.1` | 监听地址（`0.0.0.0` 表示对外开放） |
| `-port` | `AIGW_PORT` | `8787` | 监听端口 |
| `-data-dir` | `AIGW_DATA_DIR` | `./data` | 数据目录（SQLite + 日志 + 插件私有数据） |
| `-plugins-dir` | `AIGW_PLUGINS_DIR` | `./plugins` | 插件目录 |
| `-mcp-dir` | `AIGW_MCP_DIR` | `./mcp` | 本地 MCP 服务器目录 |
| `-log-level` | `AIGW_LOG_LEVEL` | `info` | 日志级别 `debug\|info\|warn\|error` |
| `-admin-password` | `AIGW_ADMIN_PASSWORD` | 空 | 首次初始化管理员密码（已初始化则忽略） |
| `-dev` | — | `false` | 开发模式（不自动拉起插件） |
| `-no-browser` | — | `false` | 启动后不自动打开浏览器（`cmd/gateway` 默认即不打开） |

仅环境变量生效的项：

| 环境变量 | 说明 |
|---|---|
| `AIGW_PIP_INDEX_URLS` | 追加的 pip 源（逗号分隔，留空用内置顺序：官方源 → 清华 → 阿里云 → 腾讯 → 中科大） |

面板「设置」页可热改、无需重启的运行期设置：日志级别、货币符号、插件探活间隔 / 超时 /
不健康阈值 / 最大失败次数、自动签到间隔、上游密钥熔断开关与基础 / 上限时长、pip 源。

> 对象存储（S3 兼容）**不走启动参数与环境变量**，只能在面板「设置 → 对象存储」里配置，
> 保存后立即生效；未配置时 `/v1/storage/tickets` 返回 501。

端口与访问范围在桌面版中持久化到数据库，下次启动沿用。插件端口由宿主自动分配
（`127.0.0.1` 上的空闲端口，通过 `AI_GATEWAY_PLUGIN_PORT` 注入），插件只监听这一个端口，
因此不受本机其它软件占用影响。插件登录所需的本机 OAuth 回调页直接落在插件自己的回环端口上
（宿主通过 `X-Gateway-Callback-Base` 告知基址），不需要额外端口或映射；Docker 或远程访问时
浏览器打不开该地址，把地址栏链接粘回面板即可。详见
[插件开发规范](docs/plugin-spec.md)。

## 插件与本地 MCP

### 插件

一个插件就是一个**独立的本地进程**（用 **Python** 写），放进 `plugins/<id>/` 即可被网关自动
拉起并注册为渠道：

```
plugins/
└── hello/
    ├── plugin.json      # 清单：name / entry（+ 可选 console / capabilities / requirements）
    └── main.py          # 你的程序（实现 OpenAI 兼容上游即可）
```

同一份 `main.py` 在 Windows / macOS / Linux（含 Docker）上通用，不需要编译多份。网关只支持
Python 插件：`entry` 必须是插件目录内的 `.py` 脚本。宿主用系统 Python
（`py -3` → `python3` → `python`）拉起插件，并按清单声明的 `requirements` 安装依赖到插件
私有目录；Docker 镜像已内置 Python，Windows 桌面版需自行安装 Python 3。

插件端口由宿主自动分配（`127.0.0.1` 空闲端口），清单里不需要也不能写死。

示例插件（`examples/plugins/`，均为纯 Python 标准库实现）；Windows 扩展包打包其中的
TRAE 与 WorkBuddy，Docker 镜像不含任何插件，需要时挂载目录进去：

| 插件 | 说明 |
|---|---|
| TRAE | TRAE 通道 → OpenAI 兼容上游，支持签到与额度查询 |
| WorkBuddy | OAuth 授权登录（国内站 / 国际站），多账号、签到、额度 |

`examples/plugins/` 里还有 hello-py / Kilo / Cline / MiniMax / Loomy / LobsterAI / Gemini /
Qoder 等插件源码，需要时把对应目录复制到 `plugins/` 即可。账号类插件在面板「插件 → 控制台」
完成登录与签到，之后到「渠道管理」点「获取上游模型」勾选要用的模型。完整开发规范见
[docs/plugin-spec.md](docs/plugin-spec.md)。

### 本地 MCP 服务器

`mcp/` 目录用于放自写的 Python MCP 服务器：每个子目录一个服务（可选 `mcp.json` + 入口脚本 +
`requirements.txt`），网关启动时扫描并自动登记到「MCP 服务器」页（卡片带「本地」徽章），
自动分配 `127.0.0.1` 端口、自动安装依赖、崩溃自动重启；删除目录后重启即从列表移除，
历史调用记录保留。

```text
mcp/
└── local-demo/
    ├── mcp.json          # 可选：name / alias / entry / path / startup_timeout_ms
    ├── main.py           # 监听 AIGW_MCP_PORT，实现 initialize / tools/list / tools/call
    └── requirements.txt  # 可选：有实际依赖时自动 pip 安装
```

扩展包与仓库 `examples/` 提供 `mcp/github-search`（GitHub 仓库 / 代码 / issue / PR / 提交 /
用户搜索）与 `mcp/image-generation`（生图，别名 `image-gen`）。前者纯标准库、匿名即可用，
在 `mcp.json` 的 `env` 里填 `GITHUB_TOKEN` 可提升额度并解锁代码搜索；后者调用网关的
`/v1/images/generations`，网关密钥填在 `mcp.json` 的 `AIGW_IMAGE_API_KEY`（留空则沿用调用方
带来的 Authorization）。生图 MCP 的参考图接受公网 `image_url` 与本机 `image_path`；
内网 `image_url` 由 MCP 自行下载后转 base64，超过单张 3 MB 时改用对象存储预签名 PUT 直传
换链接，不经过客户端上下文。想加自己的服务，参考 `examples/mcp/local-demo`（最小实现），
把目录整个复制到 `mcp/` 下即可。完整规范见 [docs/local-mcp-spec.md](docs/local-mcp-spec.md)。

客户端仍然只连网关的 MCP 聚合入口，并可在「API 密钥」里把该密钥的 MCP 权限设为
「全部 / 仅勾选 / 禁用」，按服务器粒度授权。

## 对象存储直传

需要「把文件变成 URL」时，使用兼容 S3 的对象存储，网关只签发预签名链接、完全不接触字节：

- **只能在面板配置**：「设置 → 对象存储」填写 Endpoint / Bucket / 凭证等，保存即生效，
  无需重启；不支持启动参数与环境变量。
- `POST /v1/storage/tickets` 签发一对预签名 URL（需要 API Key）：

```bash
curl -sS -X POST https://gw.example.com/v1/storage/tickets \
  -H 'Authorization: Bearer sk-...' -H 'Content-Type: application/json' \
  -d '{"filename":"ref.png","ttl_seconds":600}'
# → {"driver":"s3","bucket":"...","key":"aigw/uploads/2026-11-20/ab12….png","method":"PUT",
#    "headers":{},"upload_url":"https://…&X-Amz-Signature=…","download_url":"https://…",
#    "public_url":"","expires_at":1793…,"expires_in":1800}
```

客户端把原始字节 `PUT` 到 `upload_url` 即完成上传。之后拿哪个字段当 `image_url`：

| 场景 | 使用字段 |
|---|---|
| 公开桶 / 配了「公开访问前缀」 | `public_url` |
| 私有桶（推荐） | `download_url`（自带签名，有效期内上游可直接下载） |

约束：

- 未配置对象存储（驱动选「不启用」或必填项没填全）时该接口返回 **501**，
  `code=storage_not_configured`
- 密钥的「对象存储权限」为**禁用**时返回 **403**，`code=storage_forbidden`
  （面板「API 密钥」里可单独开关）
- 预签名有效期由面板的「预签名有效期」决定（默认 1800 秒，上限 604800）；请求里的
  `ttl_seconds` 只能收紧，不能延长
- 不传 `key` 时网关生成高熵对象键；签名只授予单个对象的 PUT / GET 权限
- 网关不代理上传、不做文件清理：过期对象由桶的生命周期规则回收

## 生图接口

- `POST /v1/images/generations`：OpenAI 标准 JSON，需要 API Key。可用私有扩展字段
  `image_url`（字符串或数组）传参考图；带 `async: true` 或 `X-AIGW-Async` 头时返回
  202 + 作业对象
- `POST /v1/images/edits`：OpenAI 标准 `multipart/form-data`，参考图用 `image`
  （可重复，`image[]` 同样识别）。网关把文件转成 data URI 后走与 `generations` 相同的
  渠道改写链路。只接受 PNG / JPEG / WebP（按文件头嗅探），单张 3 MB、合计 7 M 字符、
  请求体 32 MB；`mask` 暂忽略，**不支持异步**
- `GET /v1/images/jobs/<id>`、`DELETE /v1/images/jobs/<id>`：查询与取消异步作业。
  作业落 SQLite，网关重启后可恢复，默认保留 24 小时
- 生图结果**严格透传、不落盘**：同步与异步都原样返回上游响应体，`url`、`b64_json`、
  `usage`、`output_images` 等顶层字段一个不改，网关不下载或转码图片。唯一的加工是校验：
  上游 2xx 但按渠道 `images_field` 找不到图片时按失败处理。异步作业视图把上游响应顶层字段
  平铺进作业对象，网关自带字段（`id` / `status` / `stage` / `object` / `expires_at` 等）
  同名优先

## 从源码构建

依赖 [Go 1.26+](https://go.dev/dl/) 与 [Node.js 20+](https://nodejs.org/)（Windows，PowerShell）：

```powershell
pwsh -File build/build.ps1
# 同时构建 Linux 镜像（默认 amd64 + arm64 双架构）
pwsh -File build/build.ps1 -Docker
# 只构建单架构时显式指定
pwsh -File build/build.ps1 -Docker -DockerPlatforms linux/amd64
```

版本号统一写在仓库根目录的 [`VERSION`](VERSION) 文件中；构建脚本会自动同步到
`web/package.json`、`cmd/gateway-wails/info.json` 等派生位置，改版本只需改这一处。
临时覆盖可传 `-Version 1.3.4`。

脚本会依次：构建前端（`web/`，产物经 `//go:embed` 内嵌进二进制）→ `go mod tidy` →
生成 Wails bindings 与图标/版本资源（未装 `wails3` 时跳过，沿用已提交产物）→
编译 `dist/ai-gateway-v<版本号>.exe` → 组装绿色版目录 → 打包「插件 + 本地 MCP」扩展包 →
（可选）用 `docker buildx` 逐架构构建并导出镜像 tar。
构建 arm64 需要 buildx 与 QEMU 模拟（Docker Desktop 默认已启用；Linux 上执行
`docker run --privileged --rm tonistiigi/binfmt --install arm64`）。

单独构建命令行版：

```powershell
$env:CGO_ENABLED = "0"
go build -o dist/ai-gateway.exe ./cmd/gateway
```

只构建管理面板：

```powershell
cd web
npm install
npm run build   # 产物写入 web/dist，供 //go:embed 内嵌
```

跑测试：

```powershell
go test ./...
```

## 项目结构

```
ai-gateway/
├── VERSION                # 版本号唯一来源（构建时自动同步到各处）
├── cmd/
│   ├── gateway/           # 无界面命令行入口（Linux / macOS / Docker）
│   └── gateway-wails/     # Windows 桌面 GUI（Wails v3，自绘界面 + 托盘 + 单实例；assets/ 为窗口前端）
├── internal/
│   ├── admin/             # 管理 API：密码、会话鉴权、面板后端（渠道/密钥/插件/用量/价格/MCP）
│   ├── adminmcp/          # 管理 MCP 工具集（只读 / 读写两档令牌）
│   ├── app/               # 网关启动组装（HTTP server、SQLite、插件宿主、MCP）
│   ├── channel/           # 渠道自定义请求头占位符与上游端点路径推导
│   ├── claudeapi/         # Claude ⇄ OpenAI 协议转换
│   ├── config/            # CLI 参数与环境变量
│   ├── httpapi/           # 路由分发（/v1 · /mcp · /admin/api · /admin/mcp · SPA 静态资源）
│   ├── id/                # 随机 ID / UUID 生成
│   ├── mcp/               # MCP 聚合：工具缓存与路由、配额、探活、JSON-RPC 处理
│   ├── model/             # 领域模型（渠道 / 密钥 / 路由 / 价格 / 日志 / MCP）
│   ├── netinfo/           # 本机局域网地址探测
│   ├── penalty/           # 上游密钥失败熔断
│   ├── plugin/            # 插件宿主（发现/启动/探活/回收/控制台代理/Python 运行时）
│   ├── pricing/           # 价格表与费用核算
│   ├── proxy/             # 代理主逻辑（校验/选路/转发/SSE 修补/记账/生图）
│   ├── routing/           # 目标选择（渠道优先级 + 密钥策略 + 熔断过滤）
│   ├── storage/           # 对象存储（S3 兼容）预签名
│   ├── store/             # SQLite 存储（migrations/ 为版本化建表脚本）
│   └── version/           # 构建期注入的版本信息
├── web/                   # 管理面板前端（React 19 + Vite + Tailwind，embed.go 内嵌产物）
├── examples/              # 示例（plugins/：插件；mcp/：本地 MCP 服务器）均为 Python
├── docs/                  # 插件 / 本地 MCP 开发规范
├── build/                 # 一键构建脚本与绿色版说明
├── Dockerfile             # 多阶段构建的无界面 Linux 镜像
└── docker-compose.yml     # 一键部署示例
```

## 安全

- 默认仅监听本机回环地址；需要局域网 / 公网访问时显式改监听地址，并先确认网络环境安全
  （建议前置反代与 HTTPS）
- 管理面板与客户端 API 密钥体系完全隔离：客户端密钥无法登录面板
- 管理员密码使用 PBKDF2-HMAC-SHA256 加盐哈希存储，校验采用常量时间比较；会话 Token
  24 小时有效，选择「记住此设备」时 30 天
- Docker Compose 要求显式提供 `AIGW_ADMIN_PASSWORD`，避免弱口令实例直接暴露到公网
- 插件凭据只存在本地磁盘（`data/plugins/<id>/`），MCP 服务器密钥只存本地数据库，
  接口读取时以脱敏形式返回
- 本工具仅用于调用你本人账号的正当额度，请遵守各上游服务条款

## License

本项目基于 [MIT License](LICENSE) 开源。
