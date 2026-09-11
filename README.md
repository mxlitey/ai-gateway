# AI Gateway

OpenAI 兼容的 API 代理网关。支持多上游服务、多 Key 负载均衡、Web 管理面板。部署在腾讯云 EdgeOne Makers 的 Cloud Functions（边缘节点）上。

## 功能

- **多上游渠道**：支持配置多个不同的 AI 服务（NVIDIA NIM、OpenRouter、Azure 等）
- **负载均衡**：优先级分组 + 加权随机 + 渠道内 Key 轮询
- **自动故障转移**：上游 5xx 或网络错误时自动切换到下一个 Key/渠道
- **Web 管理面板**：可视化管理渠道、Key，无需改代码
- **API Key 鉴权**：生成客户端 API Key，控制代理访问权限
- **OpenAI 兼容**：支持 `/v1/chat/completions`、`/v1/embeddings`、`/v1/models`
- **流式支持**：完整支持 SSE 流式响应
- **Token 用量细分**（可选）：配置 MySQL 后启用，分别统计未命中输入、缓存命中输入与输出 token（OpenAI `prompt_tokens_details.cached_tokens` 规范）

## 快速部署

本项目仅支持边缘节点部署，运行在腾讯云 EdgeOne Makers 的 Cloud Functions（Node.js 运行时）上。

### 前置条件

- [Node.js](https://nodejs.org/) 18+（本地调试用）
- 腾讯云 EdgeOne 账号

### 部署步骤

1. 将代码推送到 Git 仓库，在 [EdgeOne Makers](https://cloud.tencent.com/product/1552) 控制台创建项目并关联仓库；或本地安装 CLI 直接部署：

```bash
npm install -g edgeone
edgeone login
edgeone makers deploy
```

2. 项目内已包含 Cloud Functions 入口：

```
cloud-functions/
├── index.js           # 根路径（/、/health）
├── [[default]].js     # 其余所有路径（/v1/*、/admin/*）
└── _handler.js        # 共享核心（私有模块，不生成路由）
```

3. 在 Makers 控制台配置环境变量：

| 变量 | 必填 | 说明 |
|------|------|------|
| `ADMIN_PASSWORD` | 是 | 管理面板登录密码 |
| `MYSQL_URL` | 否 | MySQL 连接串（`mysql://user:pass@host:port/db`），存请求次数与 Token 用量统计；不配置则统计功能关闭，其余功能正常 |

> 存储双后端：
> - **MySQL**（`MYSQL_URL`）：usage / apikey-usage 请求次数与 Token 统计（细分未命中输入 / 缓存命中输入 / 输出），原子 SQL 计数（`INSERT ... ON DUPLICATE KEY UPDATE`），首次调用自动建表、旧表自动补列，无需手动初始化；未配置 `MYSQL_URL` 时统计功能关闭，代理不受影响
> - **EdgeOne Blob**（[`@edgeone/pages-blob`](https://www.npmjs.com/package/@edgeone/pages-blob)）：渠道配置 / 客户端 Key / 限流状态 / 错误日志，**强一致读取**，命名空间固定为 `ai-gateway`，函数内自动鉴权、首次调用自动创建

4. 执行时长已通过 `edgeone.json` 配置为 120 秒（默认 30 秒，LLM 调用需要）。如需调整：

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

本地调试的环境变量写入项目根目录 `.env.local`（不会提交到 git）：

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

访问 `https://<你的Makers域名>/admin`，使用管理密码登录。

- 进入 **Channels** 页面
- 点击 **Add Channel**
- 填写上游服务信息：
  - **Name**: 渠道名称（如 "NVIDIA NIM"）
  - **Base URL**: 上游 API 地址（如 `https://integrate.api.nvidia.com/v1`）
  - **API Keys**: 上游的 API Key，每行一个
  - **Models**: 该渠道支持的模型，每行一个（必填；未配置则渠道不参与路由）
  - **Priority**: 优先级（数字越小越优先）
  - **Weight**: 权重（同优先级内的流量分配比例）

### 2. 生成客户端 API Key

- 进入 **API Keys** 页面
- 点击 **Generate Key**
- 复制生成的 Key

### 3. 调用 API

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

支持在任何兼容 OpenAI API 的客户端中使用：
- **API Base URL**: `https://<你的Makers域名>/v1`
- **API Key**: 管理面板中生成的 Key
- **Model**: 在渠道中配置的模型名

## 负载均衡策略

```
请求到达
  ↓
按模型筛选可用渠道
  ↓
按优先级分组（Priority 0 → 1 → 2 → ...）
  ↓
同优先级内按 Weight 加权随机排序
  ↓
渠道内 Key 轮询（Round-Robin）
  ↓
发送请求 → 成功则返回
  ↓ 失败（5xx / 网络错误）
尝试下一个 Key → 下一个渠道 → 下一个优先级组
  ↓ 全部失败
返回 502 错误
```

## API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/` | 健康检查 |
| GET | `/admin` | 管理面板 |
| POST | `/v1/chat/completions` | 聊天补全（支持流式） |
| POST | `/v1/embeddings` | 文本嵌入 |
| GET | `/v1/models` | 模型列表 |

## 环境变量

| 变量 | 说明 |
|------|------|
| `ADMIN_PASSWORD` | 管理面板登录密码（在 Makers 控制台配置） |
| `MYSQL_URL` | MySQL 连接串，存请求次数与 Token 用量统计（原子计数） |

## 架构

```
cloud-functions/       # EdgeOne Cloud Functions 入口
├── index.js           # 根路径（/、/health）
├── [[default]].js     # 其余所有路径（/v1/*、/admin/*）
└── _handler.js        # 共享核心（私有模块，不生成路由）

src/
├── index.js           # 统一请求处理器 + 路由
├── admin/
│   ├── auth.js        # 管理员认证（HMAC Token）
│   ├── api.js         # 管理 CRUD API
│   └── page.js        # 管理面板 SPA
├── proxy/
│   ├── auth.js        # 客户端 API Key 校验
│   └── handler.js     # 代理转发 + 故障转移
├── lb/
│   └── balancer.js    # 负载均衡调度器
└── store/
    ├── kv.js          # KV 存储（带内存缓存）
    └── blob-kv.js     # EdgeOne Blob 适配层（强一致）
```

## License

MIT
