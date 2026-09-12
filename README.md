<div align="center">

# AI Gateway

OpenAI 兼容的 AI API 代理网关：多上游渠道、模型映射、优先级路由、自动故障转移，部署在腾讯云 EdgeOne Makers 边缘节点（Cloud Functions）上。

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
- [模型路由与调度](#模型路由与调度)
- [渠道自定义请求头](#渠道自定义请求头)
- [API 端点](#api-端点)
- [环境变量](#环境变量)
- [存储](#存储)
- [项目结构](#项目结构)
- [License](#license)

## 功能特性

- **统一 API**：OpenAI 兼容接口（`/v1/chat/completions`、`/v1/embeddings`、`/v1/models`），并内置 Claude Messages API（`/v1/messages`）协议互转
- **多上游渠道**：任意 OpenAI 兼容服务均可作为渠道接入（NVIDIA NIM、OpenRouter、Azure、ModelScope 等）
- **模型映射**：支持「公开模型名 → 上游模型名」映射（`model_map`），未映射的模型按同名透传（`models`）
- **优先级路由**：同一公开模型可被多个渠道提供，按「公开模型 → 渠道」的优先级（`model_priority`）从大到小尝试，相同则按渠道存储顺序；渠道内 Key 以随机起点轮换展开（并发安全）
- **自动故障转移**：上游 5xx、429、404、网络错误或 HTTP 200 内嵌错误时，自动切换下一个 Key / 渠道
- **流式支持**：完整 SSE 透传，修复上游 `id: null` 等非规范 chunk，并识别流内 error 事件
- **渠道自定义请求头**：按渠道配置任意请求头，值支持 `{{占位符}}` 从客户端请求头取值，适配客户端私有头（如会话 ID）
- **Web 管理面板**：仪表盘 / 渠道管理 / 模型路由 / API 密钥 / 错误日志可视化，支持一键拉取上游模型列表与批量连通性诊断
- **API Key 鉴权**：支持 `Authorization: Bearer` 与 Claude 风格 `x-api-key`，单个 Key 可限定可用渠道（`channel_ids`）
- **管理面板鉴权**：管理员密码或任意客户端 Key 登录，会话 Token 为 HMAC 签名，24 小时有效
- **CORS 支持**：浏览器端跨域调用开箱即用

## 架构

```mermaid
flowchart LR
    Client[客户端<br/>OpenAI / Claude / 任意兼容客户端] -->|API Key 鉴权| GW[AI Gateway<br/>EdgeOne Cloud Functions]
    Admin[管理员浏览器] -->|HMAC 会话| GW
    GW -->|优先级路由 + 故障转移| C1[渠道 A<br/>NVIDIA NIM]
    GW -->|优先级路由 + 故障转移| C2[渠道 B<br/>OpenRouter]
    GW -->|优先级路由 + 故障转移| C3[渠道 C<br/>Azure / ModelScope]
    GW <-->|渠道配置 / 客户端 Key / 错误日志| Store[(EdgeOne Blob<br/>强一致读取)]
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

仓库内也提供了等价脚本：

```bash
npm run deploy
```

在 Makers 控制台配置环境变量：

| 变量 | 必填 | 说明 |
|------|------|------|
| `ADMIN_PASSWORD` | 是 | 管理面板登录密码 |

执行时长已通过 [edgeone.json](edgeone.json) 配置为 120 秒（默认 30 秒，LLM 调用需要）。

### 本地开发

```bash
npm install -g edgeone
edgeone login
edgeone makers dev    # 默认 http://localhost:8088
```

仓库内也提供了等价脚本：

```bash
npm run dev
```

本地调试的环境变量写入项目根目录 `.env.local`（已在 `.gitignore` 中，不会提交）：

```
ADMIN_PASSWORD=dev-password
```

### 使用限制

| 限制 | 值 |
|------|-----|
| 最大执行时长 | 120 秒（edgeone.json 配置，可设 10–120s） |
| 请求/响应 body | 6 MB |
| 代码包 | 128 MB |
| Node.js | v20.x |

## 使用方法

访问 `https://<你的Makers域名>/`，使用管理员密码登录。面板包含 **仪表盘**、**渠道管理**、**模型路由**、**API 密钥**、**错误日志** 五个页面。

### 1. 配置渠道

进入 **渠道管理** 页面，点击 **添加渠道**：

| 字段 | 说明 |
|------|------|
| **名称** | 渠道名称（如 "NVIDIA NIM"） |
| **API 主机** | 上游 API 地址（如 `https://integrate.api.nvidia.com/v1`） |
| **API 路径** | 对话接口路径（可选，默认 `/chat/completions`） |
| **API 密钥** | 上游 API Key 列表，每条可单独启用/禁用 |
| **模型列表** | 该渠道支持的模型名（同名透传），每行一个，可一键拉取上游模型 |
| **公开模型 → 上游模型 映射** | 请求路由优先使用该映射，未命中再回退到「模型列表」 |
| **自定义请求头** | 该渠道每次上游请求都会携带的额外请求头，详见下文 |
| **启用** | 渠道开关 |

> [!TIP]
> 管理面板支持一键拉取上游模型列表（获取上游模型）与对「渠道 + Key + 模型」组合的批量连通性诊断。

### 2. 调整模型路由优先级

进入 **模型路由** 页面，可查看每个公开模型在当前所有启用渠道中的路由路径（按优先级从大到小、相同则按渠道存储顺序；渠道内密钥随机起点轮换）。

- 公开模型可被多个渠道提供，直接修改渠道行前的 **优先级** 数字即可就地保存（数字越大越先尝试，`0` 为默认值）
- 如需调整「公开模型 → 上游模型」映射，请前往 **渠道管理** 编辑对应渠道

### 3. 生成客户端 API Key

- 进入 **API 密钥** 页面，点击 **生成密钥**
- 可设置 Key 名称，并限制其可访问的渠道（不选则使用全部渠道）
- 复制生成的 `sk-...` Key（关闭弹窗后不再显示完整值）

### 4. 调用 API

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

任意 OpenAI 兼容客户端接入方式：

- **API Base URL**：`https://<你的Makers域名>/v1`
- **API Key**：管理面板中生成的 Key（`Authorization: Bearer` 或 `x-api-key` 均可）
- **Model**：在渠道中配置的公开模型名

## 模型路由与调度

1. 按请求的公开模型名筛选候选渠道：命中 `model_map` 映射优先，未命中则回退到 `models` 同名透传
2. 仅启用、且存在至少一个「已启用」Key 的渠道参与
3. 客户端 Key 若设置了 `channel_ids`，仅在其允许的渠道中筛选
4. 候选渠道按「公开模型 → 渠道」的优先级（`model_priority`）降序尝试，相同优先级保持渠道存储顺序
5. 每个渠道内的 Key 以随机起点轮换展开（并发安全），并按「渠道 : Key : 上游模型」去重
6. 请求失败时自动尝试下一个目标：
   - `404`（模型不存在）→ 记录日志并切换
   - `429`（限流）→ 记录日志并立即切换下一个目标，不等待、不重试
   - `5xx` / 网络错误 → 记录日志并切换
   - HTTP 200 但响应体无效（内嵌 `error`、`choices` 非数组或为空）→ 记录日志并切换
7. 全部目标失败返回 `502`（Claude 端点返回同语义的 Claude 错误结构），并在错误信息中附带最后一次 429 的上游响应片段（截断 200 字符）

> [!NOTE]
> 找不到任何可用渠道时返回 `503`。诊断与真实转发使用同一套有效性判定标准（`200` 但内容为错误也视为失败）。

## 渠道自定义请求头

在渠道配置中可添加任意请求头，同名会覆盖网关内置请求头。值支持以下占位符语法：

| 语法 | 含义 |
|------|------|
| `{{x-session-id}}` | 取「客户端同名请求头」的值（大小写不敏感），未命中则置空 |
| `{{a \| b \| c}}` | 多候选回退：从左到右取第一个非空命中的客户端请求头 |
| `{{[常量]}}` | 方括号包裹表示字面常量，恒命中，适合放在末尾兜底 |
| `{{uuid}}` | 每次请求生成随机 UUID |
| `{{timestamp}}` | 当前毫秒时间戳 |

示例：适配不同客户端对「会话 ID」使用不同请求头名，并在客户端不传时用固定值兜底：

```
{{x-session-id | x-conversation-id | [gw-session-001]}}
```

> [!NOTE]
> 诊断与拉取模型列表时没有客户端请求，此时只有 `[常量]` 与 `{{uuid}}`/`{{timestamp}}` 能命中。

## API 端点

### 代理 / 页面

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查（返回纯文本） |
| GET | `/` | 管理面板 SPA |
| GET | `/v1/models` | 模型列表（按当前 Key 权限过滤） |
| POST | `/v1/chat/completions` | 聊天补全（OpenAI 格式，支持流式） |
| POST | `/v1/embeddings` | 文本嵌入 |
| POST | `/v1/messages` | Claude Messages API（自动协议转换） |
| OPTIONS | 任意 | CORS 预检 |

> [!NOTE]
> 除上述端点外，`/v1/*` 下的其他 POST 路径会按 OpenAI 透传方式转发至上游。当请求体带 `stream: true` 时，网关会注入 `stream_options.include_usage` 以获取 token 用量。

### 管理 API（`/admin/api/*`，除登录外均需鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/admin/api/login` | 管理登录（管理员密码或客户端 Key） |
| GET | `/admin/api/channels` | 渠道列表 |
| POST | `/admin/api/channels` | 新建渠道 |
| PUT | `/admin/api/channels/:id` | 更新渠道 |
| DELETE | `/admin/api/channels/:id` | 删除渠道 |
| PATCH | `/admin/api/channels/:id/toggle` | 启用 / 停用渠道 |
| PATCH | `/admin/api/channels/:id/priority` | 设置「公开模型 → 渠道」的优先级（传 `model` + 数字 `priority`，`0` 删除条目回退默认） |
| GET | `/admin/api/apikeys` | 客户端 Key 列表 |
| POST | `/admin/api/apikeys` | 生成客户端 Key |
| PATCH | `/admin/api/apikeys/:id` | 更新客户端 Key（名称 / 绑定渠道 / 启停） |
| DELETE | `/admin/api/apikeys/:id` | 删除客户端 Key |
| GET | `/admin/api/errors?date=YYYY-MM-DD` | 按渠道 / 日期查询错误日志 |
| DELETE | `/admin/api/errors` | 清理 7 天前的错误日志（所有渠道） |
| POST | `/admin/api/fetch-models` | 拉取上游模型列表（传 `channel_id` 或 `base_url` + `keys`） |
| POST | `/admin/api/test-upstream` | 上游连通性诊断（支持 `tasks: [{ channel_id, key, model }]` 批量） |

> [!NOTE]
> 鉴权方式为 `Authorization: Bearer <token|apiKey>`。管理会话 Token 使用 `ADMIN_PASSWORD` 作 HMAC 签名密钥，24 小时过期；也可直接使用任意启用的客户端 Key。

## 环境变量

| 变量 | 必填 | 说明 |
|------|------|------|
| `ADMIN_PASSWORD` | 是 | 管理面板登录密码，同时作为管理会话 HMAC Token 的签名密钥 |

## 存储

存储使用 EdgeOne Blob（[`@edgeone/pages-blob`](https://www.npmjs.com/package/@edgeone/pages-blob)），命名空间固定为 `ai-gateway`，强一致读取，函数内自动鉴权、首次调用自动创建。存储内容：

- `config:channels`：渠道配置（含密钥、模型、映射、优先级、自定义请求头）
- `config:apikeys`：客户端 API Key
- `errors:<channelId>:<日期>`：按渠道 / 北京日期保存的错误日志，每个渠道每日最多保留 100 条；超过 7 天的旧日志自动清理（写日志时惰性触发，每 6 小时最多一次）
- `meta:errors_cleanup`：上次错误日志清理的时间戳

> [!NOTE]
> 业务层（[kv.js](src/store/kv.js)）对 `config:*` 读取有 5 分钟内存缓存；错误日志读写直接落 Blob。

## 项目结构

```
cloud-functions/       # EdgeOne Cloud Functions 入口
├── index.js           # 根路径入口（/、/health）
├── [[default]].js     # 其余所有路径入口（/v1/*、/admin/*）
└── _handler.js        # 平台适配层（注入 env 与 Blob 存储，私有模块不生成路由）

src/
├── index.js           # 路由分发 + CORS
├── admin/
│   ├── auth.js        # 管理登录 / HMAC 会话校验（24h）
│   ├── api.js         # 管理 CRUD API（渠道 / 优先级 / Key / 错误日志 / 诊断）
│   └── page.js        # 管理面板 SPA（单文件内联 HTML/CSS/JS）
├── proxy/
│   ├── auth.js        # 客户端 API Key 校验
│   ├── handler.js     # 转发 + 故障转移 + SSE 修补
│   ├── claude.js      # Claude Messages ↔ OpenAI 协议互转
│   ├── headers.js     # 渠道自定义请求头与占位符解析
│   └── utils.js       # ID 生成与上游路径推导
├── lb/
│   └── balancer.js    # 目标（渠道 + Key）选择、优先级排序与负载均衡
└── store/
    ├── kv.js          # 业务存储（5 分钟内存缓存）
    └── blob-kv.js     # EdgeOne Blob 适配层（强一致读取）
```

## License

MIT
