# AI Gateway

> 本地优先的 AI 模型网关 —— 一个绿色版 exe，统一管理你的所有模型渠道、密钥、MCP 工具与用量。

<p>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat&logo=go">
  <img alt="Platform" src="https://img.shields.io/badge/platform-Windows%2010%2F11%20%C2%B7%20Linux%20%2F%20Docker-0078D4?style=flat&logo=windows">
  <img alt="CGO" src="https://img.shields.io/badge/CGO_ENABLED-0-brightgreen?style=flat">
  <img alt="React" src="https://img.shields.io/badge/UI-React%2019%20%2B%20Vite-61DAFB?style=flat&logo=react">
  <img alt="License" src="https://img.shields.io/badge/license-MIT-blue?style=flat">
</p>

AI Gateway 是一个运行在你自己电脑（或自己的服务器）上的 API 网关。它把散落在各处的模型服务（OpenAI 兼容上游、Anthropic Claude、各类国产/海外站点、以及你自己写的插件）收敛成一个**统一的 OpenAI / Claude 双兼容入口**，并提供密钥签发、模型路由、负载均衡、故障转移、密钥失败冷却、用量与费用统计、MCP 工具聚合、Web 管理面板和桌面 GUI。

全程本地运行，默认只监听 `127.0.0.1`，数据不出机器。

---

## ✨ 特性

- **双协议入口** — 同一网关同时提供 OpenAI（`/v1/chat/completions` 等）与 Claude Messages（`/v1/messages`）API，请求自动互转，流式（SSE）完整支持
- **渠道管理** — 每个模型可挂多个上游渠道，按「模型优先级」排序择优；支持模型映射（公开名 → 上游名）、按渠道单独停用某个模型、自定义请求头（含占位符）
- **多密钥策略** — 同渠道多密钥可选「轮询分摊」或「顺序故障转移」
- **故障转移与冷却** — 上游返回 404/429/5xx/网络错误/无效响应时自动切下一个目标；失败的密钥进入指数递增冷却，冷却期内不再参与选路（参数可在面板调整，状态可查可重置）
- **API 密钥** — 面板一键签发 `sk-` 客户端密钥，可限定可用渠道、随时停用；校验失败返回标准 OpenAI 错误格式
- **MCP 聚合** — 对外是一个 MCP 服务器（`POST /mcp`），对内聚合多台 Streamable HTTP 上游：合并工具列表、按命名空间路由工具调用，支持密钥轮换、服务器/密钥/工具三级每日调用上限，以及按调用次数计费的口径（「成功即计费」或「响应含标记才计费」）
- **用量与费用** — 按密钥/模型/渠道/端点/上游密钥记录 token 用量，内置价格表（默认同步 OpenRouter 价目）自动核算费用，价格源与刷新间隔可配置
- **插件体系** — Python 写的本地 HTTP 服务放进 `plugins/` 目录即自动拉起、探活、注册为渠道；插件自选端口，可声明控制台能力（账号签到、额度查询），宿主统一托管登录回调
- **管理面板** — React 19 + Vite + Tailwind 构建，内嵌进单一二进制（仪表盘 · 渠道管理 · 模型路由 · API 密钥 · MCP 服务器 · 插件 · 用量 · 价格表 · 错误日志 · 设置）；管理员密码 PBKDF2 哈希存储，会话 Token 24h 有效
- **桌面 GUI** — Wails v3 + WebView2 原生窗口（Windows），自绘界面：启停网关、改端口、切换「仅本机 / 局域网」、管理插件、查看实时日志；系统托盘常驻，单实例锁，关窗不退出、托盘退出即停服
- **无界面部署** — 另有纯命令行入口与 Linux 镜像，适合服务器 / Docker / 反代部署
- **绿色版分发** — `CGO_ENABLED=0` 纯 Go 静态编译，解压即用，无需安装运行库

## 🚀 快速开始

### 方式一：桌面版（Windows，推荐）

从 [Releases](../../releases) 下载绿色压缩包，解压后**双击 `ai-gateway-v<版本号>.exe`**（如 `ai-gateway-v1.2.0.exe`）：

1. 桌面窗口一打开就自动启动网关（默认端口 `8787`）；数据目录 `data/` 与插件目录 `plugins/` 就在 exe 同级
2. 窗口里可改端口后点「启动 / 重启」，用「访问范围」在「仅本机 / 局域网」间切换，还能添加/移除/重扫插件、查看实时日志
3. 点「管理面板」或在浏览器打开 <http://127.0.0.1:8787>，首次进入设置管理员密码
4. 「插件」页打开某个插件的控制台完成账号登录（可选）；或直接在「渠道管理」添加自己的上游（Base URL + 上游密钥）
5. 「模型路由」把上游模型映射为公开模型名，并调整各渠道优先级
6. 「API 密钥」生成一个客户端密钥，交给任意客户端使用

关闭窗口不会退出网关——它隐藏到系统托盘继续运行；右键托盘图标 →「退出」才会停服。

### 方式二：命令行 / 服务器

```bash
# 直接运行（Linux / macOS / Windows 均可）
go run ./cmd/gateway -host 127.0.0.1 -port 8787 -admin-password 'your-password'
```

或使用 Docker：

```bash
docker compose up -d --build
# 浏览器打开 http://localhost:8788（compose 默认映射到宿主机 8788，避开桌面版的 8787）
```

镜像内置 TRAE / WorkBuddy / ModelScope / Kilo / Cline / MiniMax / Loomy / LobsterAI / Gemini 九个插件；`/data` 与 `/plugins` 两个命名卷分别持久化数据与插件。

### 客户端接入

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

Claude 协议同理：把 `baseURL` 指向 `http://127.0.0.1:8787`，密钥放在 `x-api-key` 或 `Authorization` 头均可。

### MCP 接入

MCP 客户端（Claude Code、Cursor、各类 Agent）把网关当成一台 MCP 服务器：

```
URL    : http://127.0.0.1:8787/mcp        # 聚合全部启用的上游
URL    : http://127.0.0.1:8787/mcp/<alias> # 只连某一台上游，工具名保持原样
Header : Authorization: Bearer sk-你的密钥
```

聚合模式下对外工具名形如 `<alias>__<原工具名>`（超过 64 字符会截断并附稳定短哈希），避免多台上游重名。

## 🏗️ 架构

```
                 ┌──────────────────────────── AI Gateway（单二进制） ─────────────────────────────┐
 客户端 ──/v1──▶  代理层（密钥校验 · 模型路由 · 负载均衡 · 故障转移 · 密钥冷却 · 用量/费用统计）     │
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

- **代理层**（`internal/proxy`）：校验 `Authorization` / `x-api-key`，解析请求，选择路由与渠道，流式回传并记账；`/v1/models` 直接返回本网关可用的公开模型
- **协议转换**（`internal/claudeapi`）：Claude ⇄ OpenAI 请求/响应/流式事件双向转换
- **选路**（`internal/routing`）：按模型优先级排序渠道，再按渠道密钥策略展开目标列表，剔除冷却中的密钥
- **MCP 聚合**（`internal/mcp`）：对外是一个 MCP 服务器，对内是若干 MCP 客户端；工具列表按 60s 缓存，`tools/call` 依据对外工具名反查服务器与上游工具名
- **插件宿主**（`internal/plugin`）：扫描 `plugins/`，校验 `plugin.json`，解析 Python 解释器、按需安装依赖、注入环境变量、探活失败退避重启、退出时回收整个进程树
- **管理端**（`internal/admin`）：密码 PBKDF2-HMAC-SHA256（21 万次迭代）加盐哈希、常量时间比较、Bearer 会话 Token；客户端 API Key 无法登录面板

## ⚙️ 配置

优先级：**命令行参数 > 环境变量 > 默认值**。面板中可改的运行期设置优先于启动参数（详见下文）。

| 参数 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `-host` | `AIGW_HOST` | `127.0.0.1` | 监听地址（`0.0.0.0` 表示对外开放） |
| `-port` | `AIGW_PORT` | `8787` | 监听端口 |
| `-data-dir` | `AIGW_DATA_DIR` | `./data` | 数据目录（SQLite + 日志 + 插件私有数据） |
| `-plugins-dir` | `AIGW_PLUGINS_DIR` | `./plugins` | 插件目录 |
| `-log-level` | `AIGW_LOG_LEVEL` | `info` | 日志级别 `debug\|info\|warn\|error` |
| `-admin-password` | `AIGW_ADMIN_PASSWORD` | 空 | 首次初始化管理员密码（已初始化则忽略） |
| `-dev` | — | `false` | 开发模式（不自动拉起插件） |
| `-no-browser` | — | `false` | 启动后不自动打开浏览器（`cmd/gateway` 默认即不打开） |

仅环境变量生效的项：

| 环境变量 | 说明 |
|---|---|
| `AIGW_PRICE_ENDPOINT` | 价格清单地址，默认 `https://openrouter.ai/api/v1/models` |
| `AIGW_PYTHON_RUNTIME_DIR` | 便携 Python 根目录（离线部署用） |
| `AIGW_PYTHON_MIRROR` | 便携 Python 下载镜像前缀 |
| `AIGW_PIP_INDEX_URLS` | 追加的 pip 源（逗号分隔，留空用内置顺序：官方源 → 清华 → 阿里云 → 腾讯 → 中科大） |

面板「设置」页可热改的运行期设置：日志级别、价格源地址与刷新间隔、插件探活间隔/超时/不健康阈值/最大重启次数、自动签到间隔、上游密钥冷却开关与基础/上限时长、便携 Python 目录与镜像、外部访问地址（反代/隧道场景下用于生成插件登录回调基址）。

端口与访问范围在桌面版中会持久化到数据库，下次启动沿用。插件端口由插件自己写在 `plugin.json` 的 `port` 里（插件只监听这一个端口）；网关启动前会确认它没被其他程序占用，被占用时在面板上明确提示。插件登录所需的本机 OAuth 回调页由网关统一入口 `/plugin-callback/<插件id>/...` 托管，详情见 [插件开发规范](docs/plugin-spec.md)。

## 🔌 插件系统

一个插件就是一个**独立的本地进程**（用 **Python** 写），放进 `plugins/<id>/` 即可被网关自动拉起并注册为渠道：

```
plugins/
└── hello/
    ├── plugin.json      # 清单：name / entry / port（+ 可选 console / capabilities / requirements）
    └── main.py          # 你的程序（实现 OpenAI 兼容上游即可）
```

同一份 `main.py` 在 Windows / macOS / Linux（含 Docker）上通用，不需要编译多份。网关只支持 Python 插件：`entry` 必须是插件目录内的 `.py` 脚本。宿主会依次尝试系统 Python（`py -3` → `python3` → `python`）与 `data/runtime/python/` 下的便携 Python，并按清单声明的 `requirements` 安装依赖到插件私有目录。

内置九个示例插件（`examples/`，均为纯 Python 标准库实现）：

| 插件 | 端口 | 说明 |
|---|---|---|
| TRAE | 20727 | TRAE 通道 → OpenAI 兼容上游，支持签到与额度查询 |
| WorkBuddy | 20800 | 导入本机桌面端登录态 / OAuth 授权，多账号、签到、额度 |
| ModelScope 魔搭 | 20810 | 官方 OAuth / 粘贴令牌，魔粒额度与每日任务 |
| Kilo 免费池 | 20812 | 免密免费模型池，模型清单实时跟随上游 |
| Cline | 20815 | WorkOS 设备码登录，余额查询 |
| MiniMax Code | 20816 | 设备码登录，签到积分，Anthropic Messages 协议转换 |
| Loomy（讯飞） | 20817 | 短信验证码登录，每日额度领取 |
| LobsterAI（有道） | 20818 | 授权登录，额度查询 |
| Gemini Code Assist | 20819 | 粘贴 Google refresh_token，额度查询 |

账号类插件在面板「插件 → 控制台」里完成登录与签到，之后到「渠道管理」点「获取上游模型」勾选要用的模型即可。说明见 [docs/plugins-free-channels.md](docs/plugins-free-channels.md) 与 [docs/plugins-modelscope-kilo.md](docs/plugins-modelscope-kilo.md)；完整开发规范见 [docs/plugin-spec.md](docs/plugin-spec.md)。

## 🛠️ 从源码构建

依赖 [Go 1.26+](https://go.dev/dl/) 与 [Node.js 20+](https://nodejs.org/)（Windows，PowerShell）：

```powershell
pwsh -File build/build.ps1 -Version 1.2.0
# 同时构建 Linux 镜像（默认 amd64 + arm64 双架构）
pwsh -File build/build.ps1 -Version 1.2.0 -Docker
# 只构建单架构时显式指定
pwsh -File build/build.ps1 -Version 1.2.0 -Docker -DockerPlatforms linux/amd64
```

脚本会依次：构建前端（`web/`，产物经 `//go:embed` 内嵌进二进制）→ 生成 Wails bindings / 图标资源 → `go mod tidy` → 编译 `dist/ai-gateway-v<版本号>.exe` → 组装绿色版目录（含内置示例插件）→（可选）用 `docker buildx` 逐架构构建并导出 `dist/ai-gateway-v<版本号>-docker-amd64.tar` / `-arm64.tar`。构建 arm64 需要 buildx 与 QEMU 模拟（Docker Desktop 默认已启用；Linux 上执行 `docker run --privileged --rm tonistiigi/binfmt --install arm64`）。

单独构建命令行版：

```powershell
$env:CGO_ENABLED = "0"
go build -o dist/ai-gateway.exe ./cmd/gateway
```

跑测试：

```powershell
go test ./...
```

## 📁 项目结构

```
ai-gateway-v2/
├── cmd/
│   ├── gateway/           # 无界面命令行入口（Linux / macOS / Docker）
│   └── gateway-wails/     # Windows 桌面 GUI（Wails v3，自绘界面 + 托盘 + 单实例；assets/ 为窗口前端）
├── internal/
│   ├── admin/             # 管理 API：密码、会话鉴权、面板后端（渠道/密钥/插件/用量/价格/MCP）
│   ├── app/               # 网关启动组装（HTTP server、SQLite、插件宿主、价格刷新）
│   ├── channel/           # 渠道自定义请求头占位符与上游端点路径推导
│   ├── claudeapi/         # Claude ⇄ OpenAI 协议转换
│   ├── config/            # CLI 参数与环境变量
│   ├── httpapi/           # 路由分发（/v1 · /mcp · /admin/api · 插件回调 · SPA 静态资源）
│   ├── id/                # 随机 ID / UUID 生成
│   ├── mcp/               # MCP 聚合：工具缓存与路由、配额、探活、JSON-RPC 处理
│   ├── model/             # 领域模型（渠道 / 密钥 / 路由 / 价格 / 日志 / MCP）
│   ├── netinfo/           # 本机局域网地址探测
│   ├── penalty/           # 上游密钥失败冷却
│   ├── plugin/            # 插件宿主（发现/启动/探活/回收/控制台代理/Python 运行时）
│   ├── pricing/           # 价格表与费用核算
│   ├── proxy/             # 代理主逻辑（校验/选路/转发/SSE 修补/记账）
│   ├── routing/           # 目标选择（渠道优先级 + 密钥策略 + 冷却过滤）
│   ├── store/             # SQLite 存储（migrations/ 为版本化建表脚本）
│   └── version/           # 构建期注入的版本信息
├── web/                   # 管理面板前端（React 19 + Vite + Tailwind，embed.go 内嵌产物）
├── examples/              # 示例插件（hello-py / trae / workbuddy / modelscope / kilo /
│                          #   cline / minimax / loomy / lobsterai / gemini，均为 Python）
├── docs/                  # 插件开发规范与各插件说明
├── build/                 # 一键构建脚本与绿色版说明
├── Dockerfile             # 多阶段构建的无界面 Linux 镜像
└── docker-compose.yml     # 一键部署示例
```

## 🔐 安全

- 默认仅监听本机回环地址；需要局域网 / 公网访问时显式改监听地址，并先确认网络环境安全（建议前置反代与 HTTPS）
- 管理面板与客户端 API 密钥体系完全隔离：客户端密钥无法登录面板
- 管理员密码使用 PBKDF2-HMAC-SHA256 加盐哈希存储，校验采用常量时间比较
- 插件凭据只存在本地磁盘（`data/plugins/<id>/`），请勿提交到代码仓库或分享给他人
- MCP 服务器密钥同样只存本地数据库，接口读取时以脱敏形式返回
- 本工具仅用于调用你本人账号的正当额度，请遵守各上游服务条款

## 📄 License

本项目基于 [MIT License](LICENSE) 开源。
