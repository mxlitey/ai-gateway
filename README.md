# AI Gateway

> 本地优先的 AI 模型网关 —— 一个绿色版 exe，统一管理你的所有模型渠道、密钥与用量。

<p>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go">
  <img alt="Platform" src="https://img.shields.io/badge/platform-Windows%2010%2F11-0078D4?style=flat&logo=windows">
  <img alt="CGO" src="https://img.shields.io/badge/CGO_ENABLED-0-brightgreen?style=flat">
  <img alt="React" src="https://img.shields.io/badge/UI-React%2019%20%2B%20Vite-61DAFB?style=flat&logo=react">
  <img alt="License" src="https://img.shields.io/badge/license-MIT-blue?style=flat">
</p>

AI Gateway 是一个运行在你自己电脑上的 API 网关。它把散落在各处的模型服务（OpenAI 兼容上游、Anthropic Claude、各类国产/海外站点、以及你自己写的插件）收敛成一个**统一的 OpenAI / Claude 双兼容入口**，并提供密钥签发、模型路由、负载均衡、故障转移、用量与费用统计、Web 管理面板和桌面 GUI。

全程本地运行，默认只监听 `127.0.0.1`，数据不出机器。

---

## ✨ 特性

- **双协议入口** — 同一网关同时提供 OpenAI（`/v1/chat/completions`）与 Claude Messages（`/v1/messages`）API，请求自动互转，流式（SSE）完整支持
- **渠道管理** — 每个模型可挂多个上游渠道，按「模型优先级」排序择优；同渠道多密钥可轮询分摊或顺序故障转移
- **模型路由** — 对外暴露干净的公开模型名，内部映射到任意上游模型
- **API 密钥** — 面板一键签发 `sk-` 客户端密钥，可随时停用；密钥校验失败返回标准 OpenAI 错误格式
- **用量与费用** — 按密钥/模型/渠道记录 token 用量，内置价格表（默认同步 OpenRouter 价目）自动核算费用
- **插件体系** — 任何语言写的本地 HTTP 服务放进 `plugins/` 目录即自动拉起、探活、注册为渠道；端口由插件自己在清单里指定，并可声明控制台能力（账号签到、额度查询）
- **管理面板** — React 19 + Vite 构建，内嵌进单一 exe（仪表盘 · 渠道 · 路由 · 密钥 · 插件 · 用量 · 价格表 · 错误日志 · 设置）；管理员密码 PBKDF2 哈希存储，会话 Token 24h 有效
- **桌面 GUI** — Wails v3 + WebView2 原生窗口，自绘 Apple 风格界面：启停网关、切换「仅本机 / 局域网」访问范围、管理插件、查看实时日志；系统托盘常驻，单实例锁，关窗不退出、托盘退出即停服
- **绿色版分发** — `CGO_ENABLED=0` 纯 Go 静态编译，解压即用，无需安装运行库

## 🚀 快速开始

### 下载运行（推荐）

从 [Releases](../../releases) 下载绿色压缩包，解压后**双击 `ai-gateway.exe`**：

1. 桌面窗口一打开就自动启动网关（默认端口 `8787`）；数据目录 `data/` 与插件目录 `plugins/` 就在 exe 同级
2. 窗口里可改端口后点「启动 / 重启」，用「访问范围」在「仅本机 / 局域网」间切换，还能添加/移除/重扫插件、查看实时日志
3. 点「管理面板」或在浏览器打开 <http://127.0.0.1:8787>，首次进入设置管理员密码
4. 「渠道管理」添加你的上游（Base URL + `sk-xxx` 上游密钥）
5. 「模型路由」把上游模型映射为公开模型名
6. 「API 密钥」生成一个客户端密钥，交给任意客户端使用

关闭窗口不会退出网关——它隐藏到系统托盘继续运行；右键托盘图标 →「退出」才会停服。

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

## 🏗️ 架构

```
                 ┌───────────────────────── AI Gateway（单二进制） ─────────────────────────┐
 客户端 ──/v1──▶  代理层（密钥校验 · 模型路由 · 负载均衡 · 故障转移 · 用量/费用统计）        │
                 │        │                                                               │
                 │        │ 按渠道转发到                                                   │
                 │        ▼                                                               │
                 │  OpenAI 兼容上游 / Claude 上游 / 插件渠道                                │
                 │        ▲                                                               │
 面板 ──/admin─▶  管理 API（密码登录 · 会话鉴权） ◀── 内嵌 Web 管理面板（React）            │
                 │        │                                                               │
                 │        ▼                                                               │
                 │  插件宿主（发现 · 启动 · 探活 · 重启 · 回收 · 渠道注册）──▶ 插件进程     │
                 └─────────────────────────────────────────────────────────────────────────┘
                              SQLite（用量/配置） · data/ 目录
```

- **代理层**（`internal/proxy`）：校验 `Authorization` / `x-api-key`，解析请求，选择路由与渠道，流式回传并记账
- **协议转换**（`internal/claudeapi`）：Claude ⇄ OpenAI 请求/响应/流式事件双向转换
- **插件宿主**（`internal/plugin`）：扫描 `plugins/`，校验 `plugin.json`，读取插件自选端口并注入环境变量，探活失败自动退避重启，退出时回收整个进程树
- **管理端**（`internal/admin`）：密码 PBKDF2-HMAC-SHA256 哈希、常量时间比较、Bearer 会话 Token；客户端 API Key 无法登录面板

## ⚙️ 配置

优先级：**命令行参数 > 环境变量 > 默认值**。

| 参数 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `-host` | `AIGW_HOST` | `127.0.0.1` | 监听地址 |
| `-port` | `AIGW_PORT` | `8787` | 监听端口 |
| `-data-dir` | `AIGW_DATA_DIR` | `./data` | 数据目录（SQLite + 日志） |
| `-plugins-dir` | `AIGW_PLUGINS_DIR` | `./plugins` | 插件目录 |
| `-log-level` | `AIGW_LOG_LEVEL` | `info` | 日志级别 `debug\|info\|warn\|error` |
| `-admin-password` | `AIGW_ADMIN_PASSWORD` | 空 | 首次初始化管理员密码 |
| `-dev` | — | `false` | 开发模式 |
| `-no-browser` | — | `false` | 启动后不自动打开浏览器 |

插件端口由插件自己写在 `plugin.json` 的 `port` 里；网关启动前会确认它没被其他程序占用，被占用时在面板上明确提示。

> 绿色版 `ai-gateway.exe` 与直接运行后端略有差异：数据/插件目录固定为 exe 同级的 `data/`、`plugins/`，且默认不自动打开浏览器；端口与访问范围会持久化到数据库，下次启动沿用。上表默认值适用于以命令行直接运行后端时。

## 🔌 插件系统

一个插件就是一个**独立的本地进程**（任意语言），放进 `plugins/<id>/` 即可被网关自动拉起并注册为渠道：

```
plugins/
└── hello/
    ├── plugin.json      # 清单：name / entry / port（+ 可选 console / capabilities）
    └── main.js          # 你的程序（实现 OpenAI 兼容上游即可）
```

内置示例：`examples/workbuddy` 演示了如何把第三方账号的模型额度接入网关（导入本机登录态 / OAuth 授权 → 获取上游模型 → 路由暴露）。

完整开发规范见 [docs/plugin-spec.md](docs/plugin-spec.md)。

## 🛠️ 从源码构建

依赖 [Go 1.23+](https://go.dev/dl/) 与 [Node.js 20+](https://nodejs.org/)（Windows，PowerShell）：

```powershell
pwsh -File build/build.ps1 -Version 0.1.0
```

脚本会依次：构建前端（`web/`，产物内嵌进 Go 二进制）→ `go mod tidy` → 编译 `dist/ai-gateway.exe` → 组装绿色版目录（含内置示例插件）。

开发模式单独构建后端：

```powershell
$env:CGO_ENABLED = "0"
go build -o dist/ai-gateway.exe ./cmd/gateway-wails
```

## 📁 项目结构

```
ai-gateway-v2/
├── cmd/gateway-wails/     # 桌面 GUI 入口（Wails v3，自绘界面 + 托盘 + 单实例；assets/ 为窗口前端）
├── internal/
│   ├── admin/             # 管理 API：密码、会话鉴权、面板后端
│   ├── app/               # 网关启动组装（HTTP server、SQLite、插件宿主）
│   ├── channel/           # 上游渠道请求头与端点路径处理
│   ├── claudeapi/         # Claude ⇄ OpenAI 协议转换
│   ├── config/            # CLI 参数与环境变量
│   ├── httpapi/           # 路由分发（/v1 · /admin/api · 静态资源）
│   ├── id/                # 随机 ID 生成
│   ├── model/             # 领域模型（渠道 / 密钥 / 路由 / 价格 / 日志）
│   ├── netinfo/           # 本机局域网地址探测（供访问范围展示）
│   ├── plugin/            # 插件宿主（发现/启动/探活/回收）
│   ├── pricing/           # 价格表与费用核算
│   ├── proxy/             # 代理主逻辑（校验/路由/转发/记账）
│   ├── routing/           # 目标选择（渠道排序 + 密钥策略）
│   ├── store/             # SQLite 存储（migrations/ 为版本化建表脚本）
│   └── version/           # 构建期注入的版本信息
├── web/                   # 管理面板前端（React 19 + Vite + Tailwind，embed.go 内嵌产物）
├── examples/              # 示例插件（workbuddy）
├── docs/                  # 插件开发规范
└── build/                 # 一键构建脚本与绿色版模板
```

## 🔐 安全

- 默认仅监听本机回环地址；需要局域网访问时在桌面窗口把「访问范围」切到「局域网」，并先确认网络环境安全
- 管理面板与客户端 API 密钥体系完全隔离：客户端密钥无法登录面板
- 管理员密码使用 PBKDF2-HMAC-SHA256 加盐哈希存储，校验采用常量时间比较
- 插件凭据只存在本地磁盘（`data/plugins/<id>/`），请勿提交到代码仓库或分享给他人
- 本工具仅用于调用你本人账号的正当额度，请遵守各上游服务条款

## 📄 License

本项目基于 [MIT License](LICENSE) 开源。
