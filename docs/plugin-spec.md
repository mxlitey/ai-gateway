# AI Gateway 插件开发规范

本文档面向第三方插件作者。读完这一篇，你应该能独立写出一个可用的插件：
把它放进 `plugins/<插件id>/`，网关启动后会自动拉起、探活、注册为一个渠道，并在面板里出现。

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
- 把插件注册成一个渠道（`source = plugin:<id>`，渠道名 = 插件 `name`），插件停用则渠道自动停用
- 转发面板对插件控制台的调用（见 §6）

插件负责：**实现一个 OpenAI 兼容（或任意协议 + 自转换）的上游**，以及可选的控制台接口。
插件进程不接触网关数据库，凭据由插件自己保管（放在宿主编入的私有数据目录里）。

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
  "entry": "start.cmd",
  "port": 20723
}
```

必填只有三个：`name` / `entry` / `port`。其余都是可选的（见 §3）。

固定约定（不用声明）：

| 约定 | 值 |
|---|---|
| 监听地址 | `127.0.0.1:<port>` |
| 上游根路径 | `/v1`（对话 `POST /v1/chat/completions`，模型列表 `GET /v1/models`） |
| 探活 | `GET /health` |
| 控制台前缀 | `/console`（声明 `"console": true` 后才有） |

程序至少要提供两个接口：

| 接口 | 作用 |
|---|---|
| `GET /health` | 探活。返回状态码 < 500 即视为健康（3xx/4xx 也算存活） |
| `POST /v1/chat/completions` | 实际转发目标。返回 OpenAI 兼容响应 |

上面是最小形态的示意（`hello` 只是示例名，语言不限），**不宜直接照抄上线**。
仓库里可直接运行的参考实现是 `examples/workbuddy/`（Go，含控制台、多账号、签到、额度与两种登录方式），
把它整个复制到 `plugins/workbuddy/` 后点面板「重新扫描」即可拉起。

---

## 3. `plugin.json` 字段参考

| 字段 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `name` | string | ✅ | — | 展示名，同时也是渠道名 |
| `entry` | string | ✅ | — | 入口文件，**相对插件目录**，不允许绝对路径或 `..` 逃逸 |
| `port` | int | ✅ | — | **插件自己监听的端口**（1–65535），由你选定；被别的程序占用时会在面板上报错 |
| `version` | string | | `""` | 仅用于展示 |
| `description` | string | | `""` | 仅用于展示 |
| `args` | string[] | | `[]` | 传给 entry 的参数，支持 `{{port}}` |
| `env` | object | | `{}` | 追加的环境变量，值支持 `{{port}}` |
| `console` | bool | | `false` | 是否提供控制台（实现 `/console/*` 约定接口，见 §6） |
| `capabilities` | string[] | | `[]` | 控制台能力：`checkin` / `quota`，见 §6.4 |

### 3.1 高级（通常不用写）

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `startup_timeout_ms` | int | `20000` | 启动后等待探活通过的最长时间，慢启动的程序可以调大 |

**端口规则**：`port` 由你自己选定并写死在清单里（1–65535）。网关只做两件事：把它注入到
`AI_GATEWAY_PLUGIN_PORT` 和 `{{port}}`，以及启动前确认它没被别的程序占用——被占用时插件会以
`failed` 停在面板上并给出提示，换一个端口即可。不存在「自动分配」。

---

## 4. 环境变量

宿主会注入这几个变量（无需声明）：

| 变量 | 含义 |
|---|---|
| `AI_GATEWAY_PLUGIN_ID` | 插件 id（= 目录名） |
| `AI_GATEWAY_PLUGIN_PORT` | 你清单里选的端口，**直接监听这个端口** |
| `AI_GATEWAY_PLUGIN_DIR` | 插件目录的绝对路径（只读，升级会被整体覆盖） |
| `AI_GATEWAY_PLUGIN_DATA` | 插件私有数据目录（可写，放凭据/缓存；不要写进插件目录） |

> 建议：端口读 `AI_GATEWAY_PLUGIN_PORT`，数据一律落 `AI_GATEWAY_PLUGIN_DATA`。
> `{{port}}` 占位符只在 `args` / `env` 里可用，等价于 `AI_GATEWAY_PLUGIN_PORT`。

---

## 5. 选号：账号即渠道密钥

网关统一按「账号」调用插件——**渠道密钥 = 账号 id**，转发时带 `X-Plugin-Account: <账号 id>`。
清单里没有任何密钥字段需要你声明，单账号和多账号是同一条路径。

- 只要 `"console": true`，宿主就会读取 `GET /console/accounts`，
  把每个账号同步成插件渠道里的一条密钥：**密钥值 = 账号 id、展示名 = 账号名**；
- 网关转发（`/v1/*`）时带 `X-Plugin-Account: <账号 id>`；探活（`GET /health`）与控制台调用**不带**该头；
- 于是「轮询 / 故障转移」「启用停用」「先后顺序」全部由用户在网关的渠道页配置，
  你的插件只需按收到的账号 id 选号即可。
- 只有一个账号时，渠道里就只有一条密钥——这就是「单账号」的全部含义。

插件侧实现要点：

```go
// X-Plugin-Account 命中账号 id 就用该账号，否则回退到自动挑号（兼容直连调试）
func (p *Plugin) accountForRequest(r *http.Request) (*Account, error) {
    if id := r.Header.Get("X-Plugin-Account"); id != "" {
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
- 账号不可用请返回 `4xx/5xx`，网关会按策略切到下一个账号（故障转移），或在下一次请求轮到别的账号（轮询）。

不声明 `console` 的插件没有账号概念，宿主用占位值 `gw-local` 作为渠道密钥——
`X-Plugin-Account` 不承载语义，你可以忽略它（插件只监听回环地址，本机调用即可）。

---

## 6. 插件控制台（可选，但推荐）

插件常常需要「账号登录 / 多账号 / 签到 / 额度」这类管理界面。
网关不要求你写前端，也不允许把用户赶到另一个网站的页面上——
你只要在 `plugin.json` 里写 `"console": true`，并按下面的**约定接口**实现 REST，
面板就会用统一风格把这些内容渲染出来。

### 6.1 声明

```json
{
  "console": true,
  "capabilities": ["checkin", "quota"]
}
```

控制台接口前缀固定 `/console`，标题用插件 `name`。

`capabilities` 是**可选的声明**，只影响面板渲染：

| 值 | 面板多出来的东西 |
|---|---|
| `checkin` | 「今日签到」列 + 每行的「签到」按钮 |
| `quota` | 「套餐」「额度」列 + 每行的「刷新额度」按钮 |

两个都不声明时，控制台只有账号列表、登录按钮与「删除」。
`capabilities` 只认得这两个值（由网关版本决定，写别的会被忽略），见 §6.3。

### 6.2 宿主如何转发

面板的请求走：

```
面板 ── /admin/api/plugins/<id>/console/<子路径> ──▶ 宿主（已鉴权）──▶ http://127.0.0.1:<port>/console/<子路径>
```

宿主原样透传 **方法 / 查询串 / JSON 正文 / 状态码 / 响应体**：请求体上限 1 MB，响应体上限 8 MB。
也就是说：面板请求 `/admin/api/plugins/hello/console/accounts/demo-1/checkin`
会变成对你的 `POST /console/accounts/demo-1/checkin`。

插件未运行、或未声明 `console` 时，宿主返回：

- 插件不存在 / 未声明控制台 / 未运行 → `409` + `{"error": "..."}`
- 插件返回非 2xx → 原样透传，面板以 toast 展示 `error` 字段

> 控制台接口**不需要**自己做鉴权：请求只可能来自本机宿主，且面板侧已经过管理员鉴权。

### 6.3 约定接口

| 方法 | 路径（相对 `/console`） | 必需 | 作用 |
|---|---|---|---|
| GET | `/accounts` | ✅ | 账号列表。面板靠它渲染，宿主也靠它把账号同步成渠道密钥 |
| DELETE | `/accounts/{id}` | ✅ | 删除账号（面板每行都有「删除」按钮） |
| POST | `/accounts` | | 手工新增账号：**面板不再调用**，留给脚本/手工调用 |
| POST | `/accounts/{id}/checkin` | | 签到（需声明 `checkin` 能力，见 §6.1） |
| POST | `/accounts/{id}/quota` | | 刷新额度（需声明 `quota` 能力，见 §6.1） |

`id` 由插件生成，**不要包含 `/`**（会被当成路径分隔符）。

注意：`capabilities` 不是自由扩展点——面板只认得 `checkin` / `quota`，写别的值不会渲染出任何新界面。
`/console/*` 下你仍可以实现**任意自定义端点**（宿主会原样透传），但面板不会替你渲染，
需要新能力时由网关侧扩展约定，而不是让你塞 HTML。

### 6.4 账号对象

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
| `id` | string | 主键，必填；渠道密钥就是它 |
| `name` | string | 账号列显示；为空则显示 id |
| `site` | string | `cn` → 国内站，`intl` → 国际站，其他值原样显示 |
| `enabled` | bool | 账号是否可用；新建渠道密钥时据此设置启用状态 |
| `expires_at` | number(ms) | 登录态过期时间；早于当前时间显示「已过期」 |
| `last_error` | string | 账号行下方的红色小字 |
| `plan` | string | 套餐列（仅声明 `quota` 能力时显示） |
| `credits` | number | 额度列，千分位（仅声明 `quota` 能力时显示） |
| `checked_in_today` | bool | 已签（显示连签天数）/ 未签（仅声明 `checkin` 能力时显示） |
| `streak_days` | number | 连签天数（仅声明 `checkin` 能力时显示） |

未知字段会被忽略；字段缺失显示 `—`。**凭据（token 等）不要出现在响应里**。

### 6.5 各接口的响应约定

- `GET /accounts` → `{"accounts": [...], "notice": "可选提示"}`
- `POST /accounts` → `{"message": "账号已添加", "account": {...}}`
  - 请求体（面板会带上你能用的字段，全部可选）：
    `{"name": "...", "site": "cn", "token": "...", "uid": "..."}`
- `DELETE /accounts/{id}` → `{"success": true}`
- `POST /accounts/{id}/checkin` → `{"message": "签到成功，+100 积分", "account": {...}}`
- `POST /accounts/{id}/quota` → `{"message": "额度已刷新", "account": {...}}`

出错时返回 `4xx/5xx` + `{"error": "给人看的中文说明"}`，面板会原样 toast。
接口应当是**幂等友好**的：例如「今天已签到」应返回成功语义（并附说明），而不是当成硬错误。

### 6.6 登录流程（可选）

在 `GET /console/accounts` 的响应里带上 `login_methods`，面板会把这些方法渲染成按钮：

| 字段 | 说明 |
|---|---|
| `id` | 方法 id，回调时用它区分 |
| `label` | 按钮文字 |
| `kind` | `link`：面板展示返回的授权链接并轮询；`action`：点一下当场完成 |
| `hint` | 可选说明，作为按钮 tooltip |

两类都走同一个入口：

| 方法 | 路径 | 返回 |
|---|---|---|
| POST | `/console/login/{method}` | `action`：`{"status":"ok","message":"..."}`；`link`：`{"status","message","url","state"}` |
| GET | `/console/login/{method}?state=xxx` | 仅 `link` 需要，轮询用：`{"status","message"}` |

`status` 取值：`pending`（还没完成，继续轮询）/ `ok`（完成）/ `error`。
面板在 `link` 类发起后会打开 `url`，并每 2 秒轮询一次；`ok` 时自动刷新账号列表。

### 6.7 面板行为速览

- 只有插件**运行中**时「控制台」按钮可用
- 打开后每 20 秒自动刷新账号列表；手动「刷新」可立即刷新
- 登录按钮由插件声明的 `login_methods` 决定，面板不写死任何登录方式
- 账号列表的列与按钮由 `capabilities` 决定（`checkin` / `quota`）
- 所有操作走 toast 反馈，失败展示插件返回的 `error`

参考实现见 `examples/workbuddy/`：含完整的账号 / 签到 / 额度，以及三种登录方式（本机登录态导入 + 国内站/国际站 OAuth）。

---

## 7. 生命周期与状态

```
discovered ──▶ starting ──▶ running ──┬──▶ unhealthy ──▶ restarting ──▶ running
                                      └──▶ failed（连续失败超限，放弃自动重启）
running/任何状态 ──▶ stopped（宿主停止或用户停用）
```

| 行为 | 规则 |
|---|---|
| 探活 | 每 2 秒一次，单次超时 2 秒；连续 3 次失败判定 `unhealthy` |
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
- 插件凭据不进入网关数据库；日志与控制台接口中不出现明文凭据
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
| 状态一直 `starting` | 探活没过：没监听 `AI_GATEWAY_PLUGIN_PORT`、没实现 `GET /health`、启动太慢（调大 `startup_timeout_ms`） |
| 状态 `failed` 且 `last_error` 提示端口被占用 | `port` 与别的程序冲突，改 `plugin.json` 里的 `port` 后点「启动」 |
| 状态 `failed` | 连续 5 次失败已放弃；看面板 `last_error` 与日志，修好后点「启动」 |
| 渠道里没有模型 | 模型不自动填充。在「渠道管理」里编辑该渠道，点「获取上游模型」后勾选；插件需实现 `GET /v1/models` 并返回 OpenAI 形状 `{"data":[{"id":"..."}]}` |
| 转发报 502 | 插件进程没起来，或没监听清单里写的 `port` |
| 控制台报 409 | 插件未运行或未声明 `console` |
| 控制台请求 404 | 实际路由与约定接口不匹配（见 §6.3） |
| 面板没有「签到 / 额度」按钮 | 清单里 `capabilities` 没声明 `checkin` / `quota` |
| Windows 上 `.cmd` 起不来 | entry 用 `.cmd/.bat` 时宿主会经 `cmd.exe /c` 转发；脚本里避免交互式命令 |

---

## 10. 分发给其他用户

一个插件就是 `plugins/<id>/` 整个目录，打包成 zip 即可分发。注意：

1. 目录名就是插件 id（小写字母/数字/`-`/`_`）
2. 入口文件与清单都在目录内，不要依赖目录外的路径
3. 如果插件需要额外运行时（Node/Python），在 `README` 里写清依赖与安装方式；
   或者直接编译成单文件可执行程序（**推荐**：用户解压即用）
4. 凭据、缓存等可写内容一律放 `AI_GATEWAY_PLUGIN_DATA`，不要写进插件目录
   （插件目录可能被整体覆盖升级）