# 新插件：ModelScope（魔搭）+ Kilo 免费池

## 结论先说

新增两个插件，代码全部在 `examples/` 下。**网关代码（`internal/`、`cmd/`、`web/`）一行未改**，
也不需要改；只动了打包脚本（`build/build.ps1`、`Dockerfile`）与三处说明文字，用于让插件随
安装包一起分发——具体见第 4 节。

| 插件 | 目录 | 端口 | 控制台 | 凭据 |
|---|---|---|---|---|
| ModelScope 魔搭 | `examples/modelscope/` | 20810 | 有（账号 / 签到 / 额度） | OAuth 或访问令牌 |
| Kilo 免费池 | `examples/kilo/` | 20812 | 无 | 无（免密免费池） |

两个插件都只用 Python 标准库（`http.server` / `http.client` / `json`），
不需要 `requirements`，不会触发宿主 pip 安装。

---

## 1. 参考来源

| 来源 | 用到了什么 |
|---|---|
| `https://github.com/HUIdada1/AgentHub` | `electron/backend/proxy/adapters.cjs` 的 `modelscope` 适配器与 `rules.cjs` 里的魔搭端点表；`discovery.cjs` 的 OAuth 动态注册 / 换令牌 / 续期流程 |
| `https://github.com/Ebony-Vinyl/dsh-our-free-model` | Kilo 车道（`src/kilo.js`）：免费池地址、`isFree` 清单、`PAID_MODEL_AUTH_REQUIRED` 错误语义；`docs/free-lane-wire.md` 里对「客户端指纹闸门」的记录 |

**没有移植的部分（有意为之）**

- **OpenCode / Zen 免费车道**（`opencode.ai/inference/v1`）：该车道的闸门是**客户端指纹**
  （固定 UA `opencode/latest/2.0.26/cli`、`ses_` 铸造形状会话 id、逐字一致的 `read`/`shell`
  工具定义、`tool_choice: none` 直接 403）。本次实测匿名请求返回
  `403 FreeTierError：free tier can only be used from within OpenCode`。
  复刻这套指纹属于「绕过上游风控」，与 `docs/plugin-spec.md` §8「请只使用你本人账号的
  正当额度；不要实现任何绕过上游额度或风控的功能」冲突，故**未实现**。
- AgentHub 的其它渠道（Trae / WorkBuddy / 小浣熊 / ZCode / Qoder / Cline 等）：仓库里已有
  `trae`、`workbuddy` 两个 Python 插件；Cline / MiniMax Code / Loomy / LobsterAI / Gemini
  已在后续一轮补齐，见 [docs/plugins-free-channels.md](plugins-free-channels.md)；
  其余渠道的评估结论也写在那篇文档的第 1 节。

---

## 2. ModelScope（魔搭）插件

### 2.1 为什么选它

魔搭是**官方公开 API**，不是逆向客户端：对话面是标准 OpenAI 兼容端点，控制面是
OAuth 2.0 + OIDC 与站点 openapi，没有客户端指纹闸门，因此最适合做成规范插件。

### 2.2 能力

| 接口 | 实现 |
|---|---|
| `GET /health` | 只反映插件进程自身（不掺上游状态，见 plugin-spec §7） |
| `POST /v1/chat/completions` | 转发到 `api-inference.modelscope.cn`，流式逐块透传、非流式原样返回 |
| `GET /v1/models` | 用 `X-Plugin-Account` 对应账号的令牌拉真实清单；失败回退 10 个常见模型 |
| `GET /console/accounts` | 账号列表 + `login_methods`（3 种） |
| `POST /console/login/oauth` | 动态注册互联应用（RFC 7591）→ 返回授权链接 + state |
| `GET /console/login/oauth?state=` | 轮询登录结果（`pending` / `ok` / `error` / `expired`，10 分钟 TTL） |
| `GET /console/callback/oauth/{state}` | 网关统一回调入口转发的落点，校验 state 后换令牌 |
| `POST /console/login/paste` | 粘贴令牌（`ms-` 用户令牌或 `ms_oauth` 令牌），可附 Cookie |
| `POST /console/login/cookie` | 给已有账号补 Web 会话 Cookie（只影响每日任务） |
| `POST /console/accounts/{id}/checkin` | 每日魔粒任务：Web 会话触碰 + 按余量点赞，**幂等** |
| `POST /console/accounts/{id}/quota` | 刷新魔粒余额与签到状态 |
| `DELETE /console/accounts/{id}` | 删除账号 |

### 2.3 登录方式（面板里全部是按钮，不用开终端）

1. **OAuth 授权登录**（link 类）— 全自动动态注册客户端，用户只需在授权页点一次「授权」。
   回调地址 = `X-Gateway-Callback-Base` + `/oauth/<state>`，即网关的
   `/plugin-callback/modelscope/oauth/<state>`；**插件不自建监听端口**（plugin-spec §3 / §6.7）。
2. **粘贴令牌登录**（fields 类）— 从魔搭「访问令牌」页复制 `ms-` 令牌；
   可同时粘贴浏览器 Cookie（可选）。
3. **补充 Web 会话 Cookie**（fields 类）— 给已有账号补 Cookie，用于每日登录奖励与点赞。

> Cookie 与令牌是**两套凭据、各管一段**：令牌负责推理，Cookie 负责 daily_active 与点赞。
> 只粘令牌时对话完全可用，签到会如实报错「没有 Web 会话 Cookie」，不会假装成功。

### 2.4 错误语义（对应 plugin-spec §5.2）

| 上游情况 | 插件返回 | 网关行为 |
|---|---|---|
| 令牌无效 / 过期 / 被吊销 | `401`（并硬禁用该账号） | 切号 |
| 未绑定阿里云账号 / 未实名认证 | `403` + 可操作指引 | 切号 |
| 魔粒不足 / 限流 | `403` / `429` | 切号 |
| 模型 id 不存在 | `400`（**不切号**） | 直接回给客户端 |
| 上游 5xx / 连不上 | `502` | 切号 |
| 没有任何可用账号 | `503` | 直接报错 |

模型清单对所有账号一致，所以「模型不存在」必须是 `400`——否则一个坏模型会把渠道里所有账号
轮流试一遍（plugin-spec §5.2 明确点名的坑）。

### 2.5 已实测（2026-10-19，Windows + Python 3.14）

真实上游：

- `GET https://api-inference.modelscope.cn/v1/models` → `200`，37 个模型 id
- 无效令牌 `POST /v1/chat/completions` → `401 Authentication failed...`（插件把它映射成 `401`）
- `POST https://www.modelscope.cn/oauth/register`（RFC 7591，无鉴权）→ `200` + `client_id/client_secret`
- 授权页 `GET /oauth/authorize?...` → `302 /login?...`（说明链接形态被上游接受）
- 身份 / 魔粒端点未带令牌 → `401`（鉴权边界正确）
- MCP 点赞端点未带 Cookie → `400/401`（说明该族确实只认 Cookie）

本地端到端（用临时 mock 上游，因为手上没有真实魔搭账号）：

| 场景 | 结果 |
|---|---|
| `/health` | `200 {"status":"ok",...}` |
| `/console/accounts` 空列表 | 返回 3 种登录方式 + 引导 notice |
| 粘贴令牌（mock） | `200`，账号入库，`plan=访问令牌` |
| OAuth 发起 → 回调 → 轮询 | 回调 `200`，轮询 `ok`；凭据落 `auths/ms-<uid>.json`（含 refresh_token / client_secret） |
| OAuth 令牌过期 → 自动续期 | 续期后 `expiresAt` 刷新且新 refresh 立刻落盘 |
| 非流式对话 | 原样返回上游 `chat.completion` |
| 流式对话 | `text/event-stream` 逐块透传，最后一帧含 usage |
| 非流式聚合工具调用 | `tool_calls` 分片正确拼接，`finish_reason=tool_calls` |
| 签到（有 Cookie） | `登录奖励已领 · 点赞 2 次 +4 魔粒 · …` |
| 签到（无 Cookie） | `400` + 明确错误「没有 Web 会话 Cookie」，不伪造成功 |
| 签到重复调用 | 相同的幂等结果（宿主每小时会调） |
| 额度刷新 | 魔粒 `1280.5`，签到状态同步 |
| 网关 `fetch-models` | 正确拉到 mock 的模型清单 |
| 网关转发（OpenAI 入口） | 正常；`/v1/messages`（Claude 入口）也被网关自动转换后打通 |

---

## 3. Kilo 免费池插件

### 3.1 特点

- **零凭据**：Kilo 公共网关的免费池本身免鉴权，因此清单里没有 `console`，
  渠道密钥固定为占位值 `gw-local`（plugin-spec §5）。
- **清单跟随上游**：只广播 `isFree: true` 的条目 + 自动路由 `kilo-auto/free`，
  不保存静态快照（TTL 10 分钟，失败回退 60 秒）。
- **reasoning 归一**：Kilo 的思考型免费模型把思考链放在 `message.reasoning` / `delta.reasoning`
  且正文为 `null`；多数 OpenAI 客户端只认 `reasoning_content`，直接透传会显示「空回复」。
  插件把 `reasoning` 映射为 `reasoning_content`（流式按行改写，仍逐行即时转发）。

### 3.2 已实测（2026-10-19）

- `GET https://api.kilo.ai/api/gateway/models` → `200`，402 个模型，其中 `isFree=true` 17 个
- 免费模型对话（`nvidia/nemotron-3.5-super-vl:free`）→ `200`
- 流式对话（`kilo-auto/free`）→ SSE 正常，逐帧返回
- 付费模型（`anthropic/claude-sonnet-5.5`）→ 上游 `401 PAID_MODEL_AUTH_REQUIRED`，
  插件映射为 **`400`**（「该模型不在免费池」是请求问题，不该触发切号）
- 网关端到端：`fetch-models` 拉到 17 个免费模型；转发流式 / 非流式均 `200`

> 免费池的上游会在模型卡里声明「prompt 可能被记录用于改进服务」，
> 这条也写进了 `plugin.json` 的 `description`，请勿用它发送敏感内容。

### 3.3 为什么不直接复用 `dsh-our-free-model`

它是 **dsh 宿主插件**，只依赖宿主的 `llm` 服务与 `webServer`，没有独立 HTTP 上游接口
（`packages/standalone` 尚未发布安装制品）。要接进网关只能重写成「本地 HTTP 服务」，
这正是本插件做的事；同时只取它公开、免密、无需指纹的那条 Kilo 车道。

---

## 4. 部署 / 打包：已做的改动（**只动了打包脚本**）

插件本身零改动即可用：把目录复制到 `plugins/` 后点面板「重新扫描」即可拉起。
为了让它们随安装包 / 镜像一起分发，只改了两处**打包脚本**（不是网关代码）：

### 4.1 `build/build.ps1`

```powershell
foreach ($name in @("trae", "workbuddy", "modelscope", "kilo")) {
```

### 4.2 `Dockerfile`

```dockerfile
COPY --chown=gateway:gateway examples/trae /plugins/trae
COPY --chown=gateway:gateway examples/workbuddy /plugins/workbuddy
COPY --chown=gateway:gateway examples/modelscope /plugins/modelscope
COPY --chown=gateway:gateway examples/kilo /plugins/kilo
```

### 4.3 `build/README.txt`

在「内置插件」一节补了 ModelScope 与 Kilo 两段使用说明；`README.md` 的示例清单与目录树也同步加了这两项。

### 4.4 网关代码

`internal/`、`cmd/`、`web/` **未做任何修改**，也**不需要**修改：
插件按 `docs/plugin-spec.md` 的既有约定实现（`/health`、`/v1/*`、`/console/*`、
统一回调 `/plugin-callback/<id>/...`），全部能力都已被现有宿主支持。

---

## 5. 使用步骤

### ModelScope

1. 面板 →「插件」→ ModelScope 的「控制台」
2. 点「OAuth 授权登录」→ 浏览器完成授权 → 面板自动刷新出账号
   （或者「粘贴令牌登录」，从 <https://modelscope.cn/my/myaccesstoken> 生成令牌）
3. 想让「签到」真正领到魔粒：确认账号有 Web 会话
   （OAuth 登录未必能拿到 Cookie；可在「补充 Web 会话 Cookie」里粘贴浏览器 Cookie）
4. 「渠道管理」→ 编辑「ModelScope 魔搭」→「获取上游模型」→ 勾选要用的模型
5. 「API 密钥」生成客户端密钥，即可从任意 OpenAI / Claude 客户端调用

### Kilo

1. 面板 →「插件」→ 确认「Kilo 免费池」是 `running`
2. 「渠道管理」→ 编辑「Kilo 免费池」→「获取上游模型」→ 勾选免费模型
3. 不需要任何账号；`kilo-auto/free` 会自动在可用免费模型间路由

---

## 6. 目录结构

```
examples/modelscope/
├── plugin.json      清单（console + capabilities: checkin/quota）
├── main.py          HTTP 路由与进程入口
├── config.py        上游主机 / 路径 / 可调参数（可用 config.json 覆盖）
├── accounts.py      凭据与状态分文件落盘（auths/ + state.json）
├── net.py           标准库 HTTP 传输（可逐块读、可控超时）
├── upstream.py      请求头、错误分类、OAuth、对话、魔粒
├── chat.py          选号、转发、SSE 聚合
├── login.py         OAuth 会话（state TTL）+ 粘贴令牌 / Cookie
├── console.py       控制台接口（accounts / checkin / quota）
└── util.py          截断、脱敏、JSON 取值、原子写

examples/kilo/
├── plugin.json      清单（无 console）
├── main.py          HTTP 路由、免费清单缓存、转发、reasoning 归一
└── config.py        上游地址与超时
```

运行时数据（不进插件目录）：

```
<AI_GATEWAY_PLUGIN_DATA>/modelscope/
├── auths/ms-<uid>.json   凭据（0600）：token / refresh_token / OAuth 客户端 / Cookie
└── state.json            状态：启停、额度快照、签到快照、last_error
```

---

## 7. 已知限制

- **签到依赖 Cookie**：魔搭只把 Web 会话活动算作 daily_active，纯 OpenAPI 调用不计。
  OAuth 登录只能保证推理可用；要拿每日魔粒必须另贴一次浏览器 Cookie。
- **粘贴的 OAuth 令牌无法自动续期**（拿不到 `client_secret`）：令牌过期后需重新授权。
  OAuth 登录路径可以自动续期（refresh_token 一次性轮换，插件会用新值覆盖旧值）。
- **Kilo 免费池可能随时调整**：清单每次刷新都重新向上游拉，模型下线不会导致插件报错，
  但客户端可能拿到 `400 该模型不在 Kilo 免费池里`。
- **OpenCode / Zen 免费车道未实现**（原因见第 1 节）。
