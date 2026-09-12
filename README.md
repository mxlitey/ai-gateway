<div align="center">

# AI Gateway

OpenAI 兼容的 AI API 代理网关：多上游渠道、负载均衡、自动故障转移，部署在腾讯云 EdgeOne Makers 边缘节点（Cloud Functions）上。

![License](https://img.shields.io/badge/License-MIT-blue.svg)
![Node.js](https://img.shields.io/badge/Node.js-20.x-339933)
![Platform](https://img.shields.io/badge/Platform-EdgeOne%20Makers-8A2BE2)

</div>

> 面向需要把多个 AI 上游（NVIDIA NIM、OpenRouter、Azure、ModelScope 等）聚合为统一 API 的团队与个人开发者：一个网关、一套 API Key、一个 Web 管理面板，即可管理全部模型渠道。

## 目录

- [功能特性](#功能特性)
- [架构](#架构)
- [快速开始](#快速开始)
- [使用方法](#使用方法)
- [负载均衡与故障转移](#负载均衡与故障转移)
- [API 端点](#api-端点)
- [环境变量](#环境变量)
- [项目结构](#项目结构)
- [License](#license)

## 功能特性

- **统一 API**：OpenAI 兼容接口（`/v1/chat/completions`、`/v1/embeddings`、`/v1/models`），并内置 Claude Messages API（`/v1/messages`）与 OpenAI Responses API（`/v1/responses`）协议互转
- **多上游渠道**：任意 OpenAI 兼容服务均可作为渠道接入（NVIDIA NIM、OpenRouter、Azure、ModelScope 等）
- **模型路由**：支持「公开模型名 → 上游模型名」映射，同一公开模型可被多个渠道提供
- **负载均衡**：渠道按存储顺序尝试，渠道内 Key 以随机起点轮询展开（并发安全）
- **自动故障转移**：上游 5xx、网络错误、404 或 HTTP 200 内嵌错误时，自动切换下一个 Key / 渠道
- **限流感知**：识别上游 429 与配额响应头（ModelScope 等），自动标记冷却或整轮退避重试，避免共享 IP 下连续触发限流
- **流式支持**：完整 SSE 透传，修复上游 `id: null`、`choices: null` 等非规范 chunk，流内错误也会被记录
- **Web 管理面板**：渠道 / 客户端 Key / 错误日志可视化，支持一键拉取上游模型列表与连通性诊断
- **API Key 鉴权**：支持 `Authorization: Bearer` 与 Claude 风格 `x-api-key`，单个 Key 可限定可用渠道（`channel_ids`）
- **CORS 支持**：浏览器端跨域调用开箱即用

## 架构

```mermaid
flowchart LR
    Client[客户端<br/>OpenAI / Claude / 任意兼容客户端] -->|API Key 鉴权| GW[AI Gateway<br/>EdgeOne Cloud Functions]
    Admin[管理员浏览器] -->|HMAC 登录| GW
    GW -->|负载均衡 + 故障转移| C1[渠道 A<br/>NVIDIA NIM]
    GW -->|负载均衡 + 故障转移| C2[渠道 B<br/>OpenRouter]
    GW -->|负载均衡 + 故障转移| C3[渠道 C<br/>Azure / ModelScope]
    GW <-->|渠道配置 / Key / 限流状态 / 错误日志| Store[(EdgeOne Blob<br/>强一致读取)]
```

## 快速开始

> [!NOTE]
> 本项目仅支持边缘节点部署，运行在腾讯云 EdgeOne Makers 的 Cloud Functions（Node.js v20 运行时）上。

### 前置条件

- [Node.js](https://nodejs.org/) 18+（本地开发调试用）
- 腾讯云 EdgeOne 账号

### 部署到 EdgeOne Makers

方式一：控制台部署

1. 将代码推送到 Git 仓库
2. 在 [EdgeOne Makers](https://cloud.tencent.com/product/1552) 控制台创建项目并关联仓库

方式二：CLI 部署

```bash
npm install -g edgeone
edgeone login
edgeone makers deploy
```

在 Makers 控制台配置环境变量：

| 变量 | 必填 | 说明 |
|------|------|------|
| `ADMIN_PASSWORD` | 是 | 管理面板登录密码 |

> [!NOTE]
> 存储使用 EdgeOne Blob（[`@edgeone/pages-blob`](https://www.npmjs.com/package/@edgeone/pages-blob)），命名空间固定为 `ai-gateway`，强一致读取，函数内自动鉴权、首次调用自动创建。渠道配置、客户端 Key、限流状态、错误日志均存储于此。

执行时长已通过 `edgeone.json` 配置为 120 秒（默认 30 秒，LLM 调用需要）：

```json
{
  "nodeVersion": "20.18.0",
  "cloudFunctions": {
    "nodejs": {
      "maxDuration": 120
    }
  }
}
```

### 本地开发

```bash
npm install -g edgeone
edgeone login
edgeone makers dev    # 默认 http://localhost:8088
```

本地调试的环境变量写入项目根目录 `.env.local`（已在 `.gitignore` 中，不会提交）：

```
ADMIN_PASSWORD=dev-password
```

### 使用限制

| 限制 | 值 |
|------|-----|
| 最大执行时长 | 120 秒（edgeone.json 配置） |
| 请求/响应 body | 6 MB |
| 代码包 | 128 MB |
| Node.js | v20.x |

## 使用方法

### 1. 配置渠道

访问 `https://<你的Makers域名>/admin`，使用管理密码登录后进入 **Channels** 页面，点击 **Add Channel**：

- **Name**：渠道名称（如 "NVIDIA NIM"）
- **Base URL**：上游 API 地址（如 `https://integrate.api.nvidia.com/v1`）
- **Path**：对话接口路径（可选，默认 `/chat/completions`）
- **API Keys**：上游的 API Key，每行一个
- **Models**：该渠道支持的模型（同名透传），每行一个
- **Model Map**：公开模型名 → 上游模型名映射（路由时优先于 Models）
- **Enabled**：渠道开关

> [!TIP]
> 管理面板支持一键拉取上游模型列表（Fetch Models）与连通性诊断（Test Upstream）。

### 2. 生成客户端 API Key

- 进入 **API Keys** 页面，点击 **Generate Key**
- 可设置 Key 名称，并限制其可访问的渠道（Channel IDs，留空则不限制）
- 复制生成的 `sk-...` Key

### 3. 调用 API

OpenAI 兼容格式：

```bash
curl -X POST https://<你的Makers域名>/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-generated-key" \
  -d '{
    "model": "meta/llama-3.1-405b-instruct",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false
  }'
```

Claude Messages API（自动转换为上游 OpenAI 格式）：

```bash
curl -X POST https://<你的Makers域名>/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: sk-your-generated-key" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "meta/llama-3.1-405b-instruct",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

OpenAI Responses API：

```bash
curl -X POST https://<你的Makers域名>/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-generated-key" \
  -d '{
    "model": "meta/llama-3.1-405b-instruct",
    "input": "Hello"
  }'
```

任意 OpenAI 兼容客户端接入方式：

- **API Base URL**：`https://<你的Makers域名>/v1`
- **API Key**：管理面板中生成的 Key
- **Model**：在渠道中配置的模型名

## 负载均衡与故障转移

1. 按请求模型筛选可提供该公开模型的启用渠道（`model_map` 映射优先，未映射则同名透传）
2. 客户端 Key 若设置了 `channel_ids`，仅在其允许的渠道中筛选
3. 渠道按存储顺序尝试；每个渠道内的 Key 以随机起点轮询展开
4. 处于限流状态（当日配额耗尽 / 冷却中）的 Key+模型组合会被过滤，不参与本次调度
5. 请求失败（5xx / 网络错误 / 404 / HTTP 200 内嵌错误）自动尝试下一个 Key → 下一个渠道
6. 全部失败返回 `502`

429 处理：识别 ModelScope 等上游的限流响应头（`retry-after`、`modelscope-ratelimit-*`），区分「当日配额耗尽」与「临时限流」并分别标记；临时限流按 `retry-after` 或指数退避（最长 15s）等待后重试，整轮 429 最多重试 2 轮。

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/` `/health` | 健康检查 |
| GET | `/v1/models` | 模型列表（按当前 Key 权限过滤） |
| POST | `/v1/chat/completions` | 聊天补全（OpenAI 格式，支持流式） |
| POST | `/v1/embeddings` | 文本嵌入 |
| POST | `/v1/messages` | Claude Messages API（自动协议转换） |
| POST | `/v1/responses` | OpenAI Responses API（自动协议转换） |
| GET | `/admin` | 管理面板 |
| POST | `/admin/api/login` | 管理登录（密码或客户端 Key） |
| GET/POST/PUT/DELETE | `/admin/api/channels` | 渠道管理 |
| GET/POST/PATCH/DELETE | `/admin/api/apikeys` | 客户端 Key 管理 |
| GET | `/admin/api/errors` | 按渠道 / 日期查询错误日志 |
| POST | `/admin/api/fetch-models` | 拉取上游模型列表 |
| POST | `/admin/api/test-upstream` | 上游连通性诊断 |

## 环境变量

| 变量 | 必填 | 说明 |
|------|------|------|
| `ADMIN_PASSWORD` | 是 | 管理面板登录密码，同时作为管理会话 HMAC Token 的签名密钥 |

## 项目结构

```
cloud-functions/       # EdgeOne Cloud Functions 入口
├── index.js           # 根路径（/、/health）
├── [[default]].js     # 其余所有路径（/v1/*、/admin/*）
└── _handler.js        # 共享核心（私有模块，不生成路由）

src/
├── index.js           # 统一请求处理器 + 路由
├── admin/
│   ├── auth.js        # 管理员认证（HMAC Token，24h 有效）
│   ├── api.js         # 管理 CRUD API
│   └── page.js        # 管理面板 SPA
├── proxy/
│   ├── auth.js        # 客户端 API Key 校验
│   ├── handler.js     # 代理转发 + 故障转移 + 429 处理
│   ├── claude.js      # Claude Messages ↔ OpenAI 协议转换
│   └── responses.js   # Responses ↔ Chat Completions 协议转换
├── lb/
│   └── balancer.js    # 负载均衡调度器
└── store/
    ├── kv.js          # KV 存储（5 分钟内存缓存）
    └── blob-kv.js     # EdgeOne Blob 适配层（强一致读取）
```

## License

MIT
