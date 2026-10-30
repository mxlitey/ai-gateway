# 本地 MCP 服务器开发规范

本规范适用于放在网关 `mcp/` 目录、由网关自动拉起并托管的 Python MCP 服务器（下称「本地 MCP」）。
目标是：把目录丢进 `mcp/`、重启网关，就能在「MCP 服务器」页看到它、被客户端通过统一入口调用，无需改网关代码。

参考实现：[examples/mcp/local-demo](../examples/mcp/local-demo)（纯标准库，无第三方依赖，可直接复制改名）。

---

## 1. 目录与文件约定

```
mcp/
└── <目录名>/
    ├── mcp.json            # 可选，清单；缺省用目录名 + main.py + /mcp 推断
    ├── main.py             # 入口脚本（默认名 main.py）
    ├── requirements.txt    # 可选，第三方依赖（也可写在 mcp.json 里）
    └── 其它 .py / 数据文件
```

- 一级子目录 = 一个独立服务器；目录名即 `Source = local:<目录名>`，也是登记去重的依据。
- 目录名建议用小写字母、数字、`-`，与别名保持一致（如 `local-demo`）。
- 入口脚本与 `requirements.txt` 必须留在目录内，不允许用 `../` 或绝对路径逃逸。
- 运行期数据请写到环境变量 `AIGW_MCP_DATA` 指向的目录（即 `data/mcp/<目录名>/`，网关已创建），**不要写进 mcp 目录**（升级/重建镜像会覆盖）。

## 2. mcp.json 清单

```json
{
  "name": "本地示例",
  "alias": "local-demo",
  "entry": "main.py",
  "path": "/mcp",
  "args": [],
  "env": { "DEMO_LEVEL": "info" },
  "requirements": [],
  "startup_timeout_ms": 30000
}
```

| 字段 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `name` | 否 | 目录名 | 面板展示名；同名时网关自动加序号去重 |
| `alias` | 否 | 目录名 | 工具命名空间前缀：聚合入口里工具名是 `alias__tool`；不得与已有服务器重复 |
| `entry` | 否 | `main.py` | 入口脚本相对路径 |
| `path` | 否 | `/mcp` | MCP 端点的 URL 路径，必须与脚本监听路径一致 |
| `args` | 否 | `[]` | 传给入口脚本的命令行参数（启动命令为 `python entry [args...]`） |
| `env` | 否 | `{}` | 追加环境变量；网关注入的端口变量优先级更高 |
| `requirements` | 否 | `[]` | 依赖列表（与 `requirements.txt` 等价），二者可同时使用 |
| `startup_timeout_ms` | 否 | `30000` | 启动到就绪的最长等待时间，超时视为启动失败并退避重启 |

注意：`callback_paths` 是插件专用的字段，本地 MCP 不涉及。

## 3. 运行时约定

### 3.1 监听地址与端口

- **只监听 `127.0.0.1`**，端口由网关自动分配（`127.0.0.1:0`），插件不得自行固定端口。
- 端口通过环境变量告知，按以下顺序读取（至少支持第一种）：

  | 变量 | 说明 |
  |---|---|
  | `AIGW_MCP_PORT` | 本次分配的端口（权威值） |
  | `PORT` | 兼容常见 PaaS 习惯，值与上者相同 |
  | `AIGW_MCP_DATA` | 该服务器的私有可写目录（`data/mcp/<目录名>/`），用于保存运行期状态 |
  | `PYTHONPATH` | 已包含声明安装的依赖目录与服务器目录 |

  shell 模板里也可用 `{{port}}`（例如 `args: ["--port", "{{port}}"]`），网关会替换后传入。
- 网关就绪后会把自己的地址写回服务器记录（`http://127.0.0.1:<端口><path>`），客户端只连网关的 MCP 聚合入口，不直连你的端口。

### 3.2 协议要求

必须实现 **MCP Streamable HTTP**（JSON-RPC 2.0 over HTTP POST）：

| 请求 | 行为 |
|---|---|
| `POST <path>`，JSON-RPC `initialize` | 返回 `protocolVersion`（协商 2024-11-05 / 2025-03-26 等）、`capabilities.tools`、`serverInfo` |
| `notifications/initialized` | 返回 `202 Accepted`（无响应体） |
| `tools/list` | 返回工具数组，每个工具含 `name / description / inputSchema` |
| `tools/call` | 返回 `content: [{type:"text", text:...}]`，失败时 `isError: true` |
| `ping` | 返回空结果，用于探活 |
| 其它方法（如 `resources/*`） | 返回 `-32601 Method not found` 或按协议实现 |

硬性要求：

1. **Content-Type**：接受 `application/json`；响应可以是 `application/json`，也可以 `text/event-stream`（SSE，`data:` 行），网关两种都解析。
2. **会话无关**：网关不做 `Mcp-Session-Id` 往返，每次请求都应是自包含的；如返回 `Mcp-Session-Id` 网关会忽略。
3. **无鉴权**：本地进程只对回环开放，网关不会带 Authorization；脚本不需要校验密钥。
4. **无状态优先**：重启后旧会话不可依赖；工具列表应可随时重建。
5. **单一进程**：不要 fork 常驻子进程；退出即整体退出，网关靠重启恢复。

### 3.3 启动与就绪

1. 先读取端口，再绑定 127.0.0.1，最后打印一行就绪日志到 stdout（网关会收集到运行日志）。
2. 网关每 300ms 用 `initialize` 探测一次，**2xx/JSON-RPC 正常响应**即视为就绪；超过 `startup_timeout_ms` 未就绪会杀掉进程并按退避重试（1s→2s→…→最长 60s）。
3. 就绪后网关会自动拉一次 `tools/list`，面板无需手动刷新。

## 4. 依赖与环境

- `requirements.txt` / `requirements` 中**有实际依赖**时，网关在每次启动前执行 `pip install --target data/mcp/<目录>/deps`，并把该目录加入 `PYTHONPATH`；纯注释或空文件会被跳过。
- 依赖安装按文件修改时间缓存，部署时改动依赖需更新文件（或删除 `data/mcp/<目录>/.stamp`）。
- pip 源默认按 PyPI → 清华 → 阿里 → 腾讯 → 中科大 的顺序回退，可用 `AIGW_PIP_INDEX_URLS` 覆盖（逗号分隔）。
- 解释器选择顺序：`AIGW_MCP_PYTHON` → 插件页下载的便携运行时（`data/runtime/python`）→ 系统 `py -3` / `python3` / `python`。
- 推荐只用 Python 3.10+ 标准库或纯 Python 依赖；避免需要编译的包（Windows 下没有编译器时安装会失败）。

## 5. 生命周期与配额

- **目录即生命周期**：新增目录重启后自动登记；删除目录重启后从列表移除（工具同步下线），历史调用记录保留。
- 面板不提供本地服务器的启停与日志：进程随网关启动/退出，异常自动重启；排查请看网关运行日志（stdout/stderr 都会被记录）。
- 本地 MCP 无上游费用，默认**不校验密钥/工具级配额**，只保留可选的整台服务器日上限（`daily_limit`，-1 表示不限）。
- 每次调用都会写入用量与调用明细（服务器 / 工具），供「MCP 用量明细」查看。
- 客户端密钥可按服务器粒度授权（「限定可用 MCP 服务器」），未授权的服务器不会出现在该密钥的 `tools/list` 中。

## 6. 安全要求

1. 只监听 `127.0.0.1`，禁止 `0.0.0.0`；不要自行暴露端口。
2. 不读取网关数据库、密钥文件；通过环境变量或自己的数据目录保存状态。
3. 不执行用户输入的 shell 命令；工具参数按 JSON Schema 校验。
4. 日志不得打印完整密钥、token；必要时打码。
5. 工具名只用 `[a-z0-9_-]`，避免与其它服务器冲突（冲突会以 `alias__tool` 消歧）。

## 7. 自检清单

- [ ] `mcp.json` JSON 合法，`alias` 与目录名一致且未被占用
- [ ] 启动后监听 `127.0.0.1:$AIGW_MCP_PORT`，`POST <path>` 的 `initialize` 能正常返回
- [ ] `notifications/initialized` 返回 202
- [ ] `tools/list` 返回稳定顺序的工具数组，`inputSchema` 合法
- [ ] `tools/call` 成功/失败分别返回 `content` / `isError`
- [ ] 无第三方依赖时 `requirements.txt` 为空或只含注释
- [ ] Ctrl+C / 被强杀后不残留子进程
- [ ] 删除目录并重启网关后，面板记录消失且历史调用仍可查

## 8. 常见问题

| 现象 | 排查 |
|---|---|
| 卡片一直是「异常」，`last_error` 提示连接失败 | 端口变量是否读了 `AIGW_MCP_PORT`；是否监听了 `127.0.0.1`；`path` 与 `mcp.json` 是否一致 |
| 工具数一直是 0 | `tools/list` 是否返回 `tools` 数组；`initialize` 是否声明 `capabilities.tools` |
| 启动超时 | 解释器/依赖是否可用；启动是否阻塞（应尽快绑定端口） |
| 依赖安装失败 | 是否包含需要编译的包；能否访问 pip 源；可设置 `AIGW_PIP_INDEX_URLS` |
| 调用返回「该密钥无权访问此 MCP 服务器」 | 到「API 密钥」里勾选该服务器，或清空限定表示全部 |
