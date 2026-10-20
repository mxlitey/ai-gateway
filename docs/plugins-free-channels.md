# 新增渠道插件：Cline / MiniMax Code / Loomy / LobsterAI / Gemini

## 结论先说

对照 `https://github.com/Ebony-Vinyl/dsh-our-free-model` 里的 11 个渠道逐一评估后，
本次**新增 5 个插件**，代码全部在 `examples/` 下：

| 插件 | 目录 | 端口 | 登录方式 | 控制台能力 | 上游协议 |
|---|---|---|---|---|---|
| Cline | `examples/cline/` | 20815 | WorkOS 设备码 / 粘贴令牌 | `quota` | OpenAI 兼容 |
| MiniMax Code | `examples/minimax/` | 20816 | OAuth 设备码 + PKCE | `checkin` + `quota` | Anthropic Messages（插件内转换） |
| Loomy（讯飞） | `examples/loomy/` | 20817 | 两步短信验证码 | `checkin` + `quota` | OpenAI 兼容 |
| LobsterAI（有道） | `examples/lobsterai/` | 20818 | 授权码回调（插件同端口 `/auth/callback`） | `quota` | OpenAI 兼容 |
| Gemini Code Assist | `examples/gemini/` | 20819 | 粘贴 Google refresh_token | `quota` | Cloud Code Assist 信封 |

五个插件都**只用 Python 标准库**（`http.server` / `http.client` / `json` / `hashlib` / `hmac`），
没有 `requirements`，不会触发宿主 pip 安装。

**网关代码（`internal/`、`cmd/`、`web/`）一行未改，也不需要改。**
只动了打包脚本与说明文字，见第 4 节。

ModelScope（魔搭）与 Kilo 免费池两个插件的说明仍见
[docs/plugins-modelscope-kilo.md](plugins-modelscope-kilo.md)。

---

## 1. 11 个渠道的评估结论

| # | 渠道 | 结论 | 说明 |
|---|---|---|---|
| 1 | **CodeArts**（华为云） | 未实现 | 需要 SDK-HMAC-SHA256 签名 + STS 临时凭据（约 2 小时轮换）+ 项目/实例发现，链路长且依赖华为云账号体系；不是「登录一次就能用」的形态，工作量和失败面都远超本次范围 |
| 2 | **CodeBuddy / WorkBuddy**（腾讯） | **已有插件** | 仓库原有 `examples/workbuddy/` 已实现同源 external-link 轮询（`/v2/plugin/auth/state` → `authUrl` → `/v2/plugin/auth/token`），本次不重复实现 |
| 3 | **LobsterAI**（有道） | **本次新增** | `POST /api/auth/exchange` 换令牌、`/api/auth/refresh` 续期、`/api/proxy/v1/chat/completions` 标准 OpenAI + SSE；仅登录回调要求固定回环路径 |
| 4 | **Qoder / Qoder 中国版**（阿里系） | 未实现 | 推理请求体是加密的，必须依赖上游下发的 `qoder-auth-wasm.wasm` 才能签名；在 Python 插件里复刻等于自带一个 WASM 运行时，且该 wasm 版本随客户端变化 |
| 5 | **TRAE**（字节） | **已有插件** | 仓库原有 `examples/trae/` 已完整实现（OAuth 回调 + 额度 + 签到），本次不重复实现 |
| 6 | **Cline** | **本次新增** | WorkOS 设备码 + `/api/v1/auth/register` 换 Cline token；推理是标准 OpenAI；免费模型由 `recommended-models` 的 `free` 数组下发 |
| 7 | **Loomy**（讯飞） | **本次新增** | 讯飞 CAccount HMAC-SHA1 短信登录；业务端点恒返回 HTTP 200，成败看 body `code`；**没有 refresh 端点** |
| 8 | **Raccoon**（商汤） | 未实现 | 登录页需要本地渲染二维码 / 滑块并在页面内完成手机号 AES-128-CFB 加密，属于「必须有浏览器 UI」的流程，与插件「只能监听一个端口、不得自绘页面」的约束冲突 |
| 9 | **MiniMax Code** | **本次新增** | OAuth 设备码 + PKCE；推理是 **Anthropic Messages**，插件内做 OpenAI ↔ Anthropic 双向转换 |
| 10 | **ZCode**（智谱） | 未实现 | CLI 设备授权流可做，但推理侧要求 `X-Device-Mid` 且 `system` 必须逐字包含官方身份块（否则 `405 + 3012`）；另外 captcha 需要 chromium 生成 `param`，插件内无法可靠复刻 |
| 11 | **Gemini**（Google Code Assist） | **本次新增** | Google OAuth + Cloud Code Assist 信封；因 Google 只接受 loopback 回调，改用**粘贴 refresh_token**（见 3.5） |

> 未实现的 6 个渠道不是「没看」，而是复刻它们需要额外运行时（WASM / chromium / 本地 Web UI）
> 或违背插件规范（自建额外端口、自行渲染页面）。**没有任何一个需要改网关代码。**

---

## 2. 五个插件的共同设计

### 2.1 目录结构（统一）

```
examples/<id>/
├── plugin.json      清单（port / console / capabilities）
├── main.py          HTTP 路由与进程入口
├── config.py        上游主机 / 路径 / 客户端身份 / 兜底模型（可用 config.json 覆盖）
├── accounts.py      凭据与状态分文件落盘（auths/<prefix>-<uid>.json + state.json）
├── net.py           标准库 HTTP 传输（逐块读、可控超时）
├── upstream.py      上游请求、错误分类、登录 / 续期 / 目录 / 额度 / 推理
├── chat.py          选号、转发、协议转换、SSE 聚合
└── console.py       控制台接口（accounts / login / quota 等）
```

### 2.2 账号存储

- 凭据落 `auths/<prefix>-<uid>.json`（0600），状态落 `state.json`（schema=1）；
- **凭据永不进入控制台响应**（`GET /console/accounts` 只有 id / name / plan / credits / last_error）；
- 续期成功后立刻回写凭据文件（Cline / MiniMax / LobsterAI / Gemini 都会轮换 refresh_token，
  不落盘下次就会用已作废的旧值）。

### 2.3 选号与错误语义

严格按 `docs/plugin-spec.md` §5：网关把账号 id 放进 `X-Plugin-Account`，收到谁就用谁；
插件**不做轮询、不做降级、不按额度挑号**。

| 上游情况 | 插件返回 | 网关行为 |
|---|---|---|
| 401 / 403 / 404 / 429 / 5xx | 原状态码（429 归 429，5xx 归 502） | 切号 |
| 400 及其它 4xx | 原状态码 | 不切号 |
| 余额 / 额度不足文案 | 402 或额度文案 → 429 | 切号 |
| 地域限制（Cline 的 403 `not available in your region`） | **400** | 不切号（换号也没用） |
| 没有任何可用账号 | 503 | 直接报错 |

### 2.4 流式

- 上游本身是 SSE 的（Cline / Loomy / LobsterAI）：逐块透传，不做二次解析；
- 上游是 Anthropic Messages 的（MiniMax）：逐事件转成 OpenAI `chat.completion.chunk`；
- 上游是 Cloud Code Assist 的（Gemini）：解析 `{"response": {...}}` 帧后转成 OpenAI chunk。

非流式请求一律由插件把上游 SSE 聚合为一条完整 `chat.completion`。

---

## 3. 各插件要点

### 3.1 Cline

| 步骤 | 端点 | 关键坑 |
|---|---|---|
| 申请设备码 | `POST https://api.workos.com/user_management/authorize/device` | 用户码拼进 `verification_uri_complete`，用户不必手输 |
| 轮询令牌 | `POST /user_management/authenticate` | `authorization_pending` **不是错误**；`slow_down` 必须累积退避 |
| 换 Cline 令牌 | `POST /api/v1/auth/register` | 体是驼峰 `accessToken`/`refreshToken`，响应套 `{success,data}` |
| 续期 | `POST /api/v1/auth/refresh` | 体是 `{refreshToken, grantType:"refresh_token"}` |
| 推理 | `POST /api/v1/chat/completions` | 标准 OpenAI + SSE |
| 余额 | `GET /api/v1/users/{usr-...}/balance` | **必须用 `usr-` 账号 id，不能用 JWT 的 `sub`**；返回 micro-USD，除以 1e5 |

- `Authorization: Bearer workos:<jwt>`，**`workos:` 前缀不可剥**（剥掉即 401）；
- 免费模型只由 `GET /api/v1/ai/cline/recommended-models` 的 `free` 数组下发，
  插件把免费项排在 `/v1/models` 前面；远端不可用时回退 3 个兜底模型。

### 3.2 MiniMax Code

- 设备码 + PKCE（S256）：`POST https://account.minimax.cn/oauth2/device/code` → `/oauth2/token`；
- 「还在等」有**两种形态**，两个都认：HTTP 200 + `status=pending`，以及 OAuth 标准
  `error=authorization_pending`；`slow_down` 退避 +5 秒；
- 令牌校验：`token_type` 必须 Bearer、`scope` 必须含 `agent.default`、`expires_in` 必填；
  `access_token` 是 `mmoat_` 前缀、**不是 JWT**，故账号 id 用令牌摘要；
- 推理走 **Anthropic Messages**：`POST https://agent.minimax.cn/mavis/api/v1/llm/v1/messages`；
- `MiniMax-M3.1-Flash-Preview` 的 `thinking.type` 必须是 `adaptive`
  （传 `disabled` 会硬 400 / 2013）；
- 签到与积分走 `base_resp.status_code`（HTTP 200 也可能是业务失败）；
  `claim_result == 2` 表示「今天已领过」，按幂等成功处理。

### 3.3 Loomy（讯飞）

- 登录是**两步短信**，面板只能一次收集一组字段，故声明两个登录方法：
  `sms-code`（发验证码，msgid 在插件内存缓存 5 分钟）与 `sms-login`（手机号 + 验证码）；
- 讯飞 CAccount 签名：9 段签名串（方法 / 路径 / 查询 / Content-MD5 / Content-Type /
  Date / Nonce / 空 / 空），HMAC-SHA1 后 base64，头是 `Authorization: account <ak>:<sig>`；
- **两套认证头**：`/chat/completions` 只认 `Authorization: Bearer`，
  `/models` 与 `/points/*` 只认小写 `token` 头；带错会得到 HTTP 200 + `code=100002`；
- 业务失败**恒返回 HTTP 200**，成败只读 body 的 `code`（成功 `000000`、登录态失效 `100002`）；
- **没有 refresh 端点**：`refreshable()` 恒为 False，过期如实报 401 并要求重新短信登录；
- 签到 = `POST /points/first-login` 初始化每日额度，`alreadyProcessed` 为真即幂等成功。

### 3.4 LobsterAI（有道）

- 授权门户要求 `redirect_uri` 是 `http://127.0.0.1:<端口>/auth/callback`（路径逐字固定）。
  插件**不额外监听端口**，而是复用宿主拉起的那个唯一端口挂这个路径
  （与 `examples/trae` 处理固定回环回调的方式一致）；
- 换令牌体：`authCode` + `firstKeyfrom` / `latestKeyfrom` / `uuid` / `version`；
  **续期必须带 `uuid` / `firstKeyfrom` / `latestKeyfrom`**，故一并持久化到凭据；
- 模型列表必须带 `X-LobsterAI-Client-Capabilities` 与 `X-LobsterAI-Client-Version`
  （少了前者会少 `kimi-k3`，还会让「关闭思考」500）；
- 统一信封 `{code,msg,data}`，`code !== 0` 即失败；chat 端点例外（裸 SSE）。

### 3.5 Gemini Code Assist

- 身份五头**逐字写死、不许随机**，且**不带** `x-goog-api-key`：
  `User-Agent: antigravity/4.3.0 (cmdc-pak)`、`x-client-name`、`x-client-version`、
  `x-machine-id`、`x-vscode-sessionid`；流式请求**刻意不带 `Accept`**；
- 信封 `{model, project, request:{contents, generationConfig, sessionId,
  systemInstruction, tools}, requestId, userAgent}`，**逐层字母序序列化**——
  上游看到的是字节，Python 的 `json.dumps` 保持插入序，必须显式排序；
- 模型名必须带档位后缀（`gemini-3.8-flash-low|-medium|-high|-tiered`），
  档位映射思考预算 1000 / 4000 / 10000 / 不设上限；
- `includeThoughts` 恒为 true（「关闭思考」是假关，关掉照样思考照样计费）；
- `project` 由 `loadCodeAssist` 探测，返回空则回落 `aicode-consumers`，探测失败直接报错；
- `sessionId` 由 `(project, 首条 user 文本, lane)` 确定性派生（FNV-1a 64 位），
  不是常量——写死会让所有对话共用一个会话；
- SSE 每帧是 `{"response": {...}}`，`parts[].thought == true` 映射为 `reasoning_content`。

**登录方式的取舍**：Google 原生客户端走 loopback 回调（`http://localhost:<任意端口>/...`），
插件规范禁止插件自行监听额外端口，网关统一回调地址又不在 Google 的 loopback 白名单里，
因此本插件只支持**粘贴 refresh_token**（换取令牌那一步由用户在 Google 侧或其它工具完成）。
这是本插件唯一需要用户多点一步的地方，已写进控制台提示。

---

## 4. 部署 / 打包：已做的改动（**只动了打包脚本**）

### 4.1 `build/build.ps1`

```powershell
foreach ($name in @("trae", "workbuddy", "modelscope", "kilo", "cline", "minimax", "loomy", "lobsterai", "gemini")) {
```

### 4.2 `Dockerfile`

```dockerfile
COPY --chown=gateway:gateway examples/cline /plugins/cline
COPY --chown=gateway:gateway examples/minimax /plugins/minimax
COPY --chown=gateway:gateway examples/loomy /plugins/loomy
COPY --chown=gateway:gateway examples/lobsterai /plugins/lobsterai
COPY --chown=gateway:gateway examples/gemini /plugins/gemini
```

### 4.3 `build/README.txt` / `README.md`

补了五个插件的端口与使用步骤、示例清单与目录树。

### 4.4 网关代码

`internal/`、`cmd/`、`web/` **未做任何修改，也不需要修改**：五个插件都只用
`docs/plugin-spec.md` 已有的约定（`/health`、`/v1/*`、`/console/*`、
`X-Plugin-Account`、`capabilities: checkin/quota`、`login_methods` + `fields` / `link`）。
没有新增网关能力，也没有绕过插件宿主。

---

## 5. 使用步骤

通用三步：**控制台登录 → 渠道管理「获取上游模型」→ 勾选模型**。

### Cline

1. 插件 → Cline →「控制台」→「Cline 设备码登录」，浏览器里确认用户码；
2. 面板轮询到 `ok` 后账号自动入库，「刷新额度」可查余额；
3. 「渠道管理」→ 编辑 Cline →「获取上游模型」→ 勾选（免费模型排在前面）。

### MiniMax Code

1. 控制台 →「MiniMax Code 设备码登录」→ 浏览器确认用户码；
2. 「签到」领每日积分，「刷新额度」查积分；
3. 「渠道管理」→「获取上游模型」。

### Loomy（讯飞）

1. 「① 发送短信验证码」填手机号；
2. 收到短信后「② 提交短信验证码」，填**同一个手机号** + 验证码；
3. 之后可「签到」初始化每日额度、「刷新额度」看积分；
4. 登录态约 14 天，过期后需重新短信登录（上游没有续期接口）。

### LobsterAI（有道）

1. 控制台 →「LobsterAI 授权登录」→ 浏览器完成授权 → 回调页显示成功后账号入库；
2. 「刷新额度」查余额；
3. 「渠道管理」→「获取上游模型」。

### Gemini Code Assist

1. 准备一个 Google 账号的 refresh_token（来自已登录的 Gemini CLI / Code Assist 凭据）；
2. 控制台 →「粘贴 Google refresh_token」；
3. 「刷新额度」查剩余 tokens；
4. 「渠道管理」→「获取上游模型」→ 选带档位后缀的模型名。

---

## 6. 已知限制

| 限制 | 影响 | 原因 |
|---|---|---|
| 五个插件都**未做真实上游联调** | 首次使用可能需要按上游报错微调端点/字段 | 开发环境终端不可用（`RunCommand` 报 `Toolhost terminal commands must be run through the agent runtime`），只能做静态自检 |
| Gemini 只能粘贴 refresh_token | 比其他渠道多一步手动操作 | Google 只接受 loopback 回调，插件不得额外监听端口 |
| Loomy 无法自动续期 | 约 14 天后需重新短信登录 | 上游没有 refresh 端点 |
| MiniMax 的 M3.1 不能关思考 | 每次调用都会产生思考 token | 传 `disabled` 会硬 400（2013 错误） |
| Cline 的地域限制 | 部分地区直接不可用 | 上游 403 `not available in your region`，插件归 400 不切号 |
| Qoder / ZCode / Raccoon / CodeArts 未实现 | 这四类渠道无法通过本仓库插件接入 | 需要 WASM / chromium / 本地 Web UI / 华为云 STS，见第 1 节 |

> 所有插件都只应使用**你本人账号的正当额度**。插件不做指纹伪造、不做风控绕过、
> 不代持他人凭据；违反上游条款的使用后果由使用者自负。
