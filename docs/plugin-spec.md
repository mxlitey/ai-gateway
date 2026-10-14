# AI Gateway 插件开发规范

本文档面向第三方插件作者。读完这一篇，你应该能独立写出一个可用的插件：
把它放进 `plugins/<插件id>/`，网关启动后会自动拉起、探活、注册渠道，并在面板里出现。

---

## 1. 插件是什么

**一个插件就是一个独立的本地进程**，通常是一个 HTTP 服务：

```
              ┌──────────────────────── AI Gateway（单二进制） ────────────────────────┐
客户端 ──/v1──▶ 代理层（密钥校验 / 负载均衡 / 故障转移 / 用量统计）                    │
                   │                                                                │
                   │ 按渠道把请求转发到                                              │
                   ▼                                                                │
            插件渠道  http://127.0.0.1:<你自选的端口>/v1/...  ───────▶ 插件进程（你写的）│
                   ▲                                                                │
面板 ──/admin──▶ 插件宿主（发现 / 启动 / 探活 / 重启 / 回收 / 渠道注册 / 控制台代理）   │
              └────────────────────────────────────────────────────────────────────┘
```

宿主负责：

- 扫描 `plugins/` 目录、校验 `plugin.json`
- 读取你在清单里自选的端口、注入环境变量、拉起进程
- 轮询探活，不健康时自动重启（带退避），退出时回收整个进程树
- 把插件声明的渠道注册进渠道列表（`source = plugin:<id>`），插件停用则渠道自动停用
- 转发面板对插件控制台的调用（见 §6）

插件负责：**实现一个 OpenAI 兼容（或任意协议 + 自转换）的上游**，以及可选的控制台接口。
插件进程不接触网关数据库，凭据由插件自己保管（建议放在宿主编入的私有数据目录里）。

---

## 2. 最小可用插件

目录结构（**目录名就是插件 id**，用小写字母/数字/`-`/`_` 命名即可）：

```
plugins/
└── hello/
    ├── plugin.json      # 清单：必须叫这个名字
    ├── main.js          # 你的程序（任意语言/任意可执行文件）
    └── start.cmd        # 可选：启动脚本（也可以把 entry 直接指向 exe）
```

`plugin.json`：

```json
{
  "name": "Hello 示例插件",
  "version": "1.0.0",
  "description": "一个最小的 OpenAI 兼容上游",
  "entry": "start.cmd",
  "port": 20723,
  "channels": [{ "name": "Hello 示例", "public_models": ["hello-model"] }]
}
```

规则只有几条：`name` / `entry` / `port` 必填；目录名即插件 id；
探活默认请求 `/health`；`base_url` 默认 `http://127.0.0.1:{{port}}/v1`。

程序至少要提供两个接口：

| 接口 | 作用 |
|---|---|
| `GET /health`（可用 `health_path` 改） | 探活。返回状态码 < 500 即视为健康（3xx/4xx 也算存活） |
| `POST <base_url>/chat/completions` | 实际转发目标。返回 OpenAI 兼容响应 |

上面是最小形态的示意（`hello` 只是示例名，语言不限），**不宜直接照抄上线**。
仓库里可直接运行的参考实现是 `examples/workbuddy/`（Go，含控制台、多账号、签到、额度与两种登录方式），
把它整个复制到 `plugins/workbuddy/` 后点面板「重新扫描」即可拉起。

---

## 3. `plugin.json` 字段参考

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `name` | string | ✅ | — | 展示名 |
| `version` | string | | `""` | 仅用于展示 |
| `description` | string | | `""` | 仅用于展示 |
| `entry` | string | ✅ | — | 入口文件，**相对插件目录**，不允许绝对路径或 `..` 逃逸 |
| `port` | int | ✅ | — | **插件自己监听的端口**（1–65535），由你选定；被别的程序占用时会在面板上报错 |
| `args` | string[] | | `[]` | 传给 entry 的参数，支持占位符 |
| `env` | object | | `{}` | 追加的环境变量，值支持占位符 |
| `base_url` | string | | `http://127.0.0.1:{{port}}/v1` | 插件对外提供的上游根地址 |
| `health_path` | string | | `/health` | 探活路径 |
| `health_timeout_ms` | int | | `2000` | 单次探活超时 |
| `startup_timeout_ms` | int | | `20000` | 启动后等待探活通过的最长时间 |
| `api_key_env` | string | | `""` | 见 §5 |
| `channels` | object[] | | `[]` | 要注册的渠道，缺省按 `name` 生成一个 |
| `console` | object | | 无 | 插件控制台声明，见 §6 |

`channels[]` 的字段：

| 字段 | 说明 |
|---|---|
| `name` | 渠道展示名 |
| `public_models` | 该渠道对客户端暴露的模型名列表，只作为渠道**首次创建**时的初始值（通常留空，由用户在渠道管理里手动获取并勾选） |
| `model_map` | `{"公开名": "上游名"}` 映射 |

**端口规则**：`port` 由你自己选定并写死在清单里（1–65535）。网关只做两件事：把它注入到
`AI_GATEWAY_PLUGIN_PORT` 和 `{{port}}`，以及启动前确认它没被别的程序占用——被占用时插件会以
`failed` 停在面板上并给出提示，换一个端口即可。不存在「自动分配」，也就不会出现「这次装和上次端口不一样」的问题。

---

## 4. 占位符与环境变量

### 4.1 占位符（可用在 `args` / `env` / `base_url` 中）

| 占位符 | 含义 |
|---|---|
| `{{port}}` | 你在 `plugin.json` 里选的端口 |
| `{{data}}` | 插件私有数据目录的绝对路径（`<data-dir>/plugins/<id>`），适合放凭据、缓存 |
| `{{api_key}}` | 宿主生成的访问密钥（见 §5） |
| `{{plugin_dir}}` | 插件目录的绝对路径 |

### 4.2 宿主编入的环境变量（无需声明）

| 变量 | 含义 |
|---|---|
| `AI_GATEWAY_PLUGIN_ID` | 插件 id |
| `AI_GATEWAY_PLUGIN_PORT` | 你在清单里选的端口，**直接监听这个端口** |
| `AI_GATEWAY_PLUGIN_DATA` | 私有数据目录（与 `{{data}}` 相同） |

> 建议：端口读 `AI_GATEWAY_PLUGIN_PORT`（值就是你清单里写的那个），数据一律落 `AI_GATEWAY_PLUGIN_DATA`。

---

## 5. 访问密钥（`api_key_env`）

如果 `api_key_env` 非空，宿主会：

1. 生成（或复用）一个随机密钥，通过 **同名环境变量** 传给插件进程；
2. 探活和所有上游请求都带上 `Authorization: Bearer <该密钥>`；
3. 把该密钥写进插件渠道的 keys，供网关转发时使用。

**插件应该校验这个头**，拒绝其他来源的调用。示例：

```js
const API_KEY = process.env.PLUGIN_HELLO_KEY || ''
function authorized(req) {
  return !API_KEY || req.headers['authorization'] === 'Bearer ' + API_KEY
}
```

留空 `api_key_env` 时，网关用占位值 `gw-local` 作为渠道密钥（即不校验）。
仅在本机回环、且你清楚风险时才这么做。

### 5.1 多账号插件：账号即渠道密钥（推荐）

如果你的插件管理多个上游账号（见 §6 控制台），**不要把账号轮换写在插件里**——
那样用户只能在插件里改，网关面板帮不上忙。约定做法是把账号交给网关当密钥：

- 宿主（未声明 `api_key_env`、且声明了控制台时）会读取 `GET <base_path>/accounts`，
  把每个账号同步成插件渠道里的一条密钥：**密钥值 = 账号 id、展示名 = 账号名**；
- 网关转发时按 `Authorization: Bearer <账号 id>` 调用你的插件；
- 于是「轮询 / 故障转移」「启用停用」「先后顺序」全部由用户在网关的渠道页配置，
  你的插件只需按收到的账号 id 选号即可。

插件侧实现要点：

```go
// Authorization 命中账号 id 就用该账号，否则回退到自动挑号（兼容直连调试）
func (p *Plugin) accountForRequest(r *http.Request) (*Account, error) {
    if id := bearerToken(r.Header.Get("Authorization")); id != "" {
        if acc, ok := p.store.get(id); ok {
            if !acc.Enabled {
                return nil, fmt.Errorf("账号 %s 已在插件侧停用", id)
            }
            return &acc, nil
        }
    }
    return p.pickAccount()
}
```

- 账号增删只在控制台里做，宿主会自动把变更同步到渠道密钥（账号变更后立即同步，运行期间约每 30 秒复查一次），无需人工操作；
- 账号列表为空时该渠道没有可用密钥，网关返回 `503 无可用渠道`（而不是打到你这边再报错）；
- 账号不可用请返回 `4xx/5xx`，网关会按策略切到下一个账号（故障转移），或在下一次请求轮到别的账号（轮询）；
- 声明了 `api_key_env` 的插件**不适用**本约定：`Authorization` 要留给宿主鉴权，
  账号请在插件内部自行轮换。

---

## 6. 插件控制台（可选，但推荐）

插件常常需要「账号登录 / 多账号 / 签到 / 额度」这类管理界面。
网关不要求你写前端，也不允许把用户赶到另一个网站的页面上——
你只要在 `plugin.json` 里声明 `console`，并按下面的**约定接口**实现 REST，
面板就会用统一风格把这些内容渲染出来。

### 6.1 声明

```json
"console": {
  "title": "Hello 控制台",
  "base_path": "/console"
}
```

| 字段 | 默认 | 说明 |
|---|---|---|
| `title` | 插件 `name` | 控制台标题 |
| `base_path` | `/console` | 控制台接口在**插件进程内**的前缀 |

未声明 `console` 的插件，面板不会显示「控制台」入口。

### 6.2 宿主如何转发

面板的请求走：

```
面板 ── /admin/api/plugins/<id>/console/<子路径> ──▶ 宿主（已鉴权）──▶ http://127.0.0.1:<port><base_path>/<子路径>
```

宿主原样透传 **方法 / 查询串 / JSON 正文 / 状态码 / 响应体**：请求体上限 1 MB，响应体上限 8 MB。
也就是说：面板请求 `/admin/api/plugins/hello/console/accounts/demo-1/checkin`
会变成对你的 `POST /console/accounts/demo-1/checkin`。

插件未运行、或未声明 `console` 时，宿主返回：

- 插件不存在 / 未声明控制台 / 未运行 → `409` + `{"error": "..."}`
- 插件返回非 2xx → 原样透传，面板以 toast 展示 `error` 字段

> 控制台接口**不需要**自己做鉴权：请求只可能来自本机宿主，且面板侧已经过管理员鉴权。
> 但务必放在你自家 `api_key_env` 鉴权**之前**，因为宿主不会带上游密钥来调控制台。

### 6.3 约定接口

| 方法 | 路径（相对 `base_path`） | 作用 |
|---|---|---|
| GET | `/accounts` | 账号列表 |
| POST | `/accounts` | 新增 / 登录账号 |
| DELETE | `/accounts/{id}` | 删除账号 |
| POST | `/accounts/{id}/checkin` | 签到 |
| POST | `/accounts/{id}/quota` | 刷新额度 |

`id` 由插件生成，**不要包含 `/`**（会被当成路径分隔符）。面板的账号 / 签到 / 额度操作只使用上面这 5 个接口
（登录流程另有约定，见 §6.6）；需要更多能力时，后续版本会扩展约定，而不是让你塞 HTML。

### 6.4 账号对象（面板会渲染这些字段）

```json
{
  "id": "demo-1",
  "name": "主号",
  "site": "cn",
  "enabled": true,
  "plan": "标准版",
  "credits": 1280,
  "checked_in_today": false,
  "streak_days": 3,
  "expires_at": 1760000000000,
  "last_error": ""
}
```

| 字段 | 类型 | 面板行为 |
|---|---|---|
| `id` | string | 主键，必填 |
| `name` | string | 账号列显示；为空则显示 id |
| `site` | string | `cn` → 国内站，`intl` → 国际站，其他值原样显示 |
| `enabled` | bool | 预留 |
| `plan` | string | 套餐列 |
| `credits` | number | 额度列，千分位显示 |
| `checked_in_today` | bool | 徽标：已签（显示连签天数）/ 未签；**已签时「签到」按钮禁用** |
| `streak_days` | number | 连签天数 |
| `expires_at` | number(ms) | 登录态过期时间；早于当前时间显示「已过期」 |
| `last_error` | string | 账号行下方的红色小字 |

未知字段会被忽略；字段缺失显示 `—`。这样你可以在不破坏面板的前提下多做返回。

### 6.5 各接口的响应约定

- `GET /accounts` → `{"accounts": [...], "notice": "可选提示"}`（`notice` 会显示在面板顶部）
- `POST /accounts` → `{"message": "账号已添加", "account": {...}}`
  - 请求体（面板会带上你能用的字段，全部可选）：
    `{"name": "...", "site": "cn", "auth_path": "本机凭据文件路径", "token": "...", "uid": "..."}`
    —— `auth_path` 用于「从本机 WorkBuddy / CodeBuddy 登录态文件导入」这类场景；
    `token` + `uid` 用于手工粘贴
- `DELETE /accounts/{id}` → `{"success": true}`
- `POST /accounts/{id}/checkin` → `{"message": "签到成功，+100 积分", "account": {...}}`
- `POST /accounts/{id}/quota` → `{"message": "额度已刷新", "account": {...}}`

出错时返回 `4xx/5xx` + `{"error": "给人看的中文说明"}`，面板会原样 toast。
接口应当是**幂等友好**的：例如「今天已签到」应返回成功语义（并附说明），而不是当成硬错误。

### 6.6 登录流程（可选，两种形态）

如果插件需要「让用户登录账号」，在 `GET /console/accounts` 的响应里带上 `login_methods`，
面板会把这些方法渲染成按钮：

| 字段 | 说明 |
|---|---|
| `id` | 方法 id，回调时用它区分 |
| `label` | 按钮文字 |
| `kind` | `action`：面板点一下直接调用；`link`：面板展示返回的授权链接并轮询结果 |
| `hint` | 可选说明，作为按钮 tooltip |

**`action` 类**（例如「从本机某客户端导入」）：面板调用 `POST /console/accounts`，
body 为 `{"source": "<id>"}`；插件完成后返回 `{"message": "..."}`。

**`link` 类**（例如 OAuth 授权登录）：

| 方法 | 路径 | 返回 |
|---|---|---|
| POST | `/console/login/{method}` | `{"status", "message", "url", "state"}` |
| GET | `/console/login/{method}?state=xxx` | `{"status", "message"}` |

`status` 取值：`pending`（还没完成，继续轮询）/ `ok`（完成）/ `error` / `expired`。
面板在 `link` 类发起后会打开 `url`，并每 2 秒轮询一次；`ok` 时自动刷新账号列表。

### 6.7 面板行为速览

- 只有插件**运行中**时「控制台」按钮可用
- 打开后每 20 秒自动刷新账号列表；手动「刷新」可立即刷新
- 登录按钮由插件声明的 `login_methods` 决定，面板不写死任何登录方式
- 所有操作走 toast 反馈，失败展示插件返回的 `error`

参考实现见 `examples/workbuddy/`：含完整的账号 / 签到 / 额度，以及两种登录方式（本机登录态导入 + OAuth）。

---

## 7. 生命周期与状态

```
discovered ──▶ starting ──▶ running ──┬──▶ unhealthy ──▶ restarting ──▶ running
                                      └──▶ failed（连续失败超限，放弃自动重启）
running/任何状态 ──▶ stopped（宿主停止或用户停用）
```

| 行为 | 规则 |
|---|---|
| 探活 | 每 2 秒一次；连续 3 次失败判定 `unhealthy` |
| 重启 | 不健康或进程退出即重启，退避 1s/2s/4s/8s/16s |
| 放弃 | 连续失败 5 次后置 `failed` 并停止自动重启（面板会展示 `last_error`） |
| 启动超时 | 超过 `startup_timeout_ms` 仍未探活通过，判为启动失败 |
| 优雅停止 | 先发 Ctrl-Break（Windows）/ SIGTERM（类 Unix），等待 5 秒，再强杀 |
| 退出回收 | 插件进程及其子进程挂在 Windows 作业对象上，网关退出时整棵树一起回收 |

插件应处理停止信号并尽快退出：

```js
for (const sig of ['SIGINT', 'SIGTERM', 'SIGBREAK']) {
  process.on(sig, () => server.close(() => process.exit(0)))
}
```

---

## 8. 安全约束

- `entry` 不允许绝对路径，也不允许 `..` 逃逸出插件目录（校验失败插件不会启动）
- 插件凭据不进入网关数据库；日志与接口中不出现明文密钥
- 插件只应监听 `127.0.0.1`
- 请只使用你**本人账号的正当额度**；不要实现任何绕过上游额度或风控的功能

---

## 9. 调试与排错

排错入口：

- 插件列表 / 状态 / 端口：桌面窗口的「插件」区（点「重扫」重新读取插件目录）
- 运行日志（含插件 stdout/stderr 转发）：桌面窗口底部的日志区

常见问题：

| 现象 | 原因 / 处理 |
|---|---|
| 面板里看不到插件 | 目录名不合规（需小写字母/数字/`-`/`_`）；或缺 `plugin.json`；或校验失败（看日志） |
| 状态一直 `starting` | 探活没过：没监听 `AI_GATEWAY_PLUGIN_PORT`、`health_path` 写错、启动太慢（调大 `startup_timeout_ms`） |
| 状态 `failed` 且 `last_error` 提示端口被占用 | `port` 与别的程序冲突，改 `plugin.json` 里的 `port` 后点「启动」 |
| 状态 `failed` | 连续 5 次失败已放弃；看面板 `last_error` 与日志，修好后点「启动」 |
| 渠道里没有模型 | 模型不自动填充。在「渠道管理」里编辑该渠道，点「获取上游模型」后勾选要加入的；插件需实现 `GET <base_url>/models`（如 `http://127.0.0.1:{{port}}/v1/models`）并返回 OpenAI 形状 `{"data":[{"id":"..."}]}` |
| 转发报 502 | 插件进程没起来，或 `base_url` 与实际监听的 `port` 不一致 |
| 控制台报 409 | 插件未运行或未声明 `console` |
| 控制台请求 404 | `base_path` 与实际路由不一致；或面板路径与约定接口不匹配（见 §6.3） |
| Windows 上 `.cmd` 起不来 | entry 用 `.cmd/.bat` 时宿主会经 `cmd.exe /c` 转发；脚本里避免交互式命令 |

---

## 10. 分发给其他用户

一个插件就是 `plugins/<id>/` 整个目录，打包成 zip 即可分发。注意：

1. 目录名就是插件 id（小写字母/数字/`-`/`_`）
2. 入口文件与清单都在目录内，不要依赖目录外的路径
3. 如果插件需要额外运行时（Node/Python），在 `README` 里写清依赖与安装方式；
   或者直接编译成单文件可执行程序（**推荐**：用户解压即用）
4. 凭据、缓存等可写内容一律放 `{{data}}` / `AI_GATEWAY_PLUGIN_DATA`，不要写进插件目录
   （插件目录可能被整体覆盖升级）