/** 与网关后端交互的 API 封装与类型定义。 */

export type ChannelKey = { key: string; enabled: boolean; label?: string }
export type HeaderItem = { name: string; value: string }

export type Channel = {
  id: string
  name: string
  base_url: string
  path: string
  keys: ChannelKey[]
  models: string[]
  model_map: Record<string, string>
  model_priority: Record<string, number>
  /** 在本渠道上被单独停用的公开模型名 */
  disabled_models: string[]
  headers: HeaderItem[]
  key_strategy: string
  enabled: boolean
  source: string
  sort_order: number
  created_at: number
}

export type APIKey = {
  id: string
  name: string
  key: string
  channel_ids: string[]
  enabled: boolean
  created_at: number
}

export type ErrorLog = {
  id: number
  channel_id: string
  channel_name: string
  base_url: string
  model: string
  upstream_model: string
  status: number
  key_hint: string
  message: string
  created_at: number
}

export type ErrorGroup = {
  channel_id: string
  channel_name: string
  errors: ErrorLog[]
}

export type RouteEntry = {
  channel_id: string
  channel_name: string
  enabled: boolean
  priority: number
  upstream_model: string
  /** 该公开模型在本渠道上被单独停用 */
  model_disabled: boolean
  direct: boolean
}

export type RouteGroup = { model: string; routes: RouteEntry[] }

export type ProbeResult = {
  model: string
  channel: string
  channel_id: string
  key_hint: string
  status: number
  duration_ms: number
  rate_headers?: Record<string, string>
  body?: string
  error?: string
}

export type Plugin = {
  id: string
  name: string
  version: string
  dir: string
  port: number
  enabled: boolean
  autostart: boolean
  last_status: string
  last_error: string
  last_seen_at: number
  installed_at: number
  console: boolean
  console_title: string
  console_base_path: string
  /** 控制台能力：'checkin' / 'quota'，面板据此决定显示哪些列与按钮 */
  capabilities: string[]
}

/** 插件控制台「账号」对象（约定字段，见 docs/plugin-spec.md §6）。 */
export type PluginAccount = {
  id: string
  name: string
  site?: string
  enabled?: boolean
  plan?: string
  credits?: number
  checked_in_today?: boolean
  streak_days?: number
  expires_at?: number
  last_error?: string
}

/** 登录方式声明的输入项（可选）：声明后面板会先渲染表单，收集到的值随 POST 一并提交。
 *  字段类型刻意限死三种，面板不做任意扩展（见 docs/plugin-spec.md §6.6）。 */
export type PluginLoginField = {
  name: string
  label: string
  kind?: 'text' | 'secret' | 'multiline'
  placeholder?: string
  hint?: string
  required?: boolean
}

export type PluginLoginMethod = {
  id: string
  label: string
  kind: 'action' | 'link'
  hint?: string
  /** 非空时面板先弹输入表单；为空则维持原有「点一下即完成」行为 */
  fields?: PluginLoginField[]
}

export type PluginAccountsResponse = {
  accounts: PluginAccount[]
  notice?: string
  login_methods?: PluginLoginMethod[]
}

export type PluginActionResponse = {
  message?: string
  account?: PluginAccount
}

/** 登录流程的返回值：link 类会带 url/state，面板展示链接并轮询。 */
export type PluginLoginResponse = {
  status?: string
  message?: string
  url?: string
  state?: string
}

export type RequestLogItem = {
  id: number
  created_at: number
  api_key_id: string
  api_key_name: string
  channel_id: string
  channel_name: string
  endpoint: string
  stream: boolean
  model: string
  upstream_model: string
  upstream_key_hint: string
  status: number
  success: boolean
  attempts: number
  duration_ms: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  cost_prompt_usd: number
  cost_completion_usd: number
  cost_usd: number
  price_model: string
  price_source: string
  error: string
}

export type UsageBucket = {
  key: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cached_tokens: number
  cost_usd: number
}

export type UsageSummary = {
  from: number
  to: number
  requests: number
  success_requests: number
  failed_requests: number
  unpriced_requests: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cached_tokens: number
  cost_usd: number
  by_model: UsageBucket[]
  by_channel: UsageBucket[]
  by_api_key: UsageBucket[]
  /** 按上游密钥（脱敏值）分组；空值表示未记录密钥的历史记录 */
  by_key: UsageBucket[]
  by_day: UsageBucket[]
}

export type UsageSummaryResponse = {
  days: number
  summary: UsageSummary
  price_count: number
  price_source: string
  refreshed_at: string
}

export type UsageSeriesResponse = {
  bucket: 'day' | 'hour'
  from: number
  to: number
  points: UsageBucket[]
}

export type UsageLogsResponse = {
  items: RequestLogItem[]
  total: number
  limit: number
  offset: number
  from: number
  to: number
}

export type RuntimeInfo = {
  lan: boolean
  listen_host: string
  port: number
  access_url: string
  api_url: string
  data_dir: string
  /** 面板配置的外部访问地址（反代 / 隧道场景），未配置为空串 */
  external_base: string
}

/** 可在面板调整的运行期设置；字符串项为空表示沿用启动参数 / 环境变量。 */
export type RuntimeSettings = {
  log_level: string
  price_endpoint: string
  price_refresh_minutes: number
  plugin_probe_interval_seconds: number
  plugin_probe_timeout_seconds: number
  plugin_unhealthy_threshold: number
  plugin_max_failures: number
  plugin_auto_checkin_minutes: number
  /** 是否启用上游密钥失败冷却 */
  key_cooldown_enabled: boolean
  /** 首次失败的冷却时长（秒） */
  key_cooldown_base_seconds: number
  /** 单次冷却时长上限（秒） */
  key_cooldown_max_seconds: number
}

/** 上游密钥冷却条目（仪表盘展示）。 */
export type KeyCooldown = {
  channel_id: string
  channel_name: string
  key_hint: string
  fail_streak: number
  cooldown_until: number
  updated_at: number
  remaining_seconds: number
}

export type SettingsResponse = {
  settings: RuntimeSettings
  /** 内置默认值，供面板展示「留空即默认」的提示 */
  defaults: RuntimeSettings
  /** 启动参数 / 环境变量里的日志级别，作为留空时的回退说明 */
  env_log_level: string
  /** 未在面板配置时的价格源地址（环境变量或内置默认） */
  env_price_endpoint: string
  /** 当前生效的价格源地址 */
  effective_price_source: string
}

/** Python 插件运行时探测结果。 */
export type PythonStatus = {
  found: boolean
  path: string
  version: string
  /** 是否来自便携目录（false 表示系统 Python） */
  portable: boolean
  /** 便携 Python 存放根目录 */
  runtime_dir: string
  /** 便携 Python 下载镜像前缀 */
  mirror: string
  /** 当前平台会使用的官方下载地址 */
  download_url: string
  /** 生效的 pip 源尝试顺序 */
  index_urls: string[]
  error: string
}

/** Python 插件运行时相关设置（字符串留空表示沿用默认）。 */
export type PythonSettings = {
  python_runtime_dir: string
  python_mirror: string
  pip_index_urls: string
}

/** 读取 Python 插件运行时设置的响应。 */
export type PythonSettingsResponse = {
  settings: PythonSettings
}

export type ModelPrice = {
  model: string
  display_name: string
  prompt_price: number
  completion_price: number
  cache_read_price: number
  context_length: number
  source: string
  updated_at: number
}

export type PricesResponse = {
  items: ModelPrice[]
  count: number
  endpoint: string
  refreshed_at: string
  unit: string
}

export type ChannelInput = {
  name: string
  base_url: string
  path: string
  keys: ChannelKey[]
  models: string[]
  model_map: Record<string, string>
  headers: HeaderItem[]
  key_strategy: string
  enabled: boolean
}

const TOKEN_KEY = 'aigw.token'

// 会话被服务端拒绝（401）时的回调，由 AuthProvider 注册：用于清空本地登录态并回到登录页
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn
}

// 这几个接口的 401 属于「密码不正确」等业务错误，不代表会话过期，不触发自动登出
const CREDENTIAL_PATHS = ['/admin/api/login', '/admin/api/setup', '/admin/api/password']

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) ?? ''
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers)
  const token = getToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (init?.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const res = await fetch(path, { ...init, headers })
  const text = await res.text()

  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = null
    }
  }

  if (!res.ok) {
    // 会话失效（Token 过期、换了数据目录导致签名对不上）时自动登出，
    // 否则面板会停在「每个页面都报 Unauthorized」的状态里出不来
    if (res.status === 401 && !CREDENTIAL_PATHS.includes(path)) {
      clearToken()
      onUnauthorized?.()
    }
    throw new ApiError(res.status, extractError(data, res.status))
  }
  return data as T
}

function extractError(data: unknown, status: number): string {
  if (data && typeof data === 'object') {
    const obj = data as Record<string, unknown>
    if (typeof obj.error === 'string') return obj.error
    const err = obj.error
    if (err && typeof err === 'object') {
      const msg = (err as Record<string, unknown>).message
      if (typeof msg === 'string') return msg
    }
  }
  return `请求失败（HTTP ${status}）`
}

function json(body: unknown): RequestInit {
  return { body: JSON.stringify(body) }
}

export const api = {
  setupState: () => request<{ initialized: boolean }>('/admin/api/setup'),

  setup: (password: string) =>
    request<{ token?: string }>('/admin/api/setup', { method: 'POST', ...json({ password }) }),

  login: (password: string) =>
    request<{ token: string }>('/admin/api/login', { method: 'POST', ...json({ password }) }),

  changePassword: (oldPassword: string, newPassword: string) =>
    request<{ success: boolean }>('/admin/api/password', {
      method: 'POST',
      ...json({ old_password: oldPassword, new_password: newPassword }),
    }),

  // 运行信息（监听地址、端口、数据目录，按访问范围给出访问/API 地址）
  runtimeInfo: () => request<RuntimeInfo>('/admin/api/runtime'),

  // 外部访问地址：反代 / 隧道场景下插件登录回调使用的基址；留空即清除
  setExternalBase: (externalBase: string) =>
    request<{ success: boolean; external_base: string }>('/admin/api/external-base', {
      method: 'PUT',
      ...json({ external_base: externalBase }),
    }),

  // 运行期设置：日志级别、价格源与刷新间隔、插件探活与签到节奏
  settings: () => request<SettingsResponse>('/admin/api/settings'),
  updateSettings: (body: RuntimeSettings) =>
    request<{ success: boolean; settings: RuntimeSettings }>('/admin/api/settings', {
      method: 'PUT',
      ...json(body),
    }),

  // Python 插件运行时：探测状态与便携 Python 下载
  pythonStatus: () => request<PythonStatus>('/admin/api/python/status'),
  downloadPython: (version: string) =>
    request<{ success: boolean; path: string; status: PythonStatus }>('/admin/api/python/download', {
      method: 'POST',
      ...json({ version }),
    }),
  pythonSettings: () => request<PythonSettingsResponse>('/admin/api/python/settings'),
  updatePythonSettings: (body: PythonSettings) =>
    request<{ success: boolean; settings: PythonSettings }>('/admin/api/python/settings', {
      method: 'PUT',
      ...json(body),
    }),

  // 上游密钥冷却
  keyCooldowns: () => request<{ items: KeyCooldown[] }>('/admin/api/key-cooldowns'),
  resetKeyCooldown: (channelId: string, keyHint: string) =>
    request<{ success: boolean }>(
      `/admin/api/key-cooldowns?channel_id=${encodeURIComponent(channelId)}&key_hint=${encodeURIComponent(keyHint)}`,
      { method: 'DELETE' },
    ),

  // 渠道
  channels: () => request<Channel[]>('/admin/api/channels'),
  createChannel: (body: ChannelInput) =>
    request<Channel>('/admin/api/channels', { method: 'POST', ...json(body) }),
  updateChannel: (id: string, body: Partial<ChannelInput>) =>
    request<Channel>(`/admin/api/channels/${id}`, { method: 'PUT', ...json(body) }),
  deleteChannel: (id: string) =>
    request<{ success: boolean }>(`/admin/api/channels/${id}`, { method: 'DELETE' }),
  toggleChannel: (id: string) =>
    request<Channel>(`/admin/api/channels/${id}/toggle`, { method: 'PATCH' }),
  setPriority: (id: string, model: string, priority: number) =>
    request<unknown>(`/admin/api/channels/${id}/priority`, {
      method: 'PATCH',
      ...json({ model, priority }),
    }),
  // 按模型维度启停：enabled=false 停用该渠道上的这个模型
  setModelEnabled: (id: string, model: string, enabled: boolean) =>
    request<{ success: boolean }>(`/admin/api/channels/${id}/model-enabled`, {
      method: 'PATCH',
      ...json({ model, enabled }),
    }),
  fetchModels: (body: { channel_id?: string; base_url?: string; keys?: string[] }) =>
    request<{ models: string[] }>('/admin/api/fetch-models', { method: 'POST', ...json(body) }),
  testUpstream: (body: { tasks: { channel_id?: string; key?: string; model: string }[] }) =>
    request<{ summary: { total: number; ok: number; rate_limited: number }; results: ProbeResult[] }>(
      '/admin/api/test-upstream',
      { method: 'POST', ...json(body) },
    ),

  // 客户端密钥
  apiKeys: () => request<APIKey[]>('/admin/api/apikeys'),
  createApiKey: (body: { name: string; channel_ids: string[] }) =>
    request<APIKey>('/admin/api/apikeys', { method: 'POST', ...json(body) }),
  updateApiKey: (id: string, body: { name?: string; enabled?: boolean; channel_ids?: string[] }) =>
    request<APIKey>(`/admin/api/apikeys/${id}`, { method: 'PATCH', ...json(body) }),
  deleteApiKey: (id: string) =>
    request<{ success: boolean }>(`/admin/api/apikeys/${id}`, { method: 'DELETE' }),

  // 错误日志
  errors: (date?: string) =>
    request<{ date: string; channels: ErrorGroup[] }>(
      `/admin/api/errors${date ? `?date=${encodeURIComponent(date)}` : ''}`,
    ),

  // 路由视图 / 插件
  routes: () => request<RouteGroup[]>('/admin/api/public-models'),
  plugins: () => request<Plugin[]>('/admin/api/plugins'),
  rescanPlugins: () => request<Plugin[]>('/admin/api/plugins/rescan', { method: 'POST' }),
  startPlugin: (id: string) =>
    request<{ success: boolean }>(`/admin/api/plugins/${id}/start`, { method: 'POST' }),
  stopPlugin: (id: string) =>
    request<{ success: boolean }>(`/admin/api/plugins/${id}/stop`, { method: 'POST' }),
  restartPlugin: (id: string) =>
    request<{ success: boolean }>(`/admin/api/plugins/${id}/restart`, { method: 'POST' }),
  updatePlugin: (id: string, body: { enabled?: boolean; autostart?: boolean }) =>
    request<{ success: boolean }>(`/admin/api/plugins/${id}`, { method: 'PATCH', ...json(body) }),

  // 插件控制台（约定接口，由插件进程实现，宿主只做反向代理）
  pluginAccounts: (id: string) =>
    request<PluginAccountsResponse>(`/admin/api/plugins/${id}/console/accounts`),
  // link / action 类登录统一入口：拿到授权链接就展示并轮询，否则按已完成处理
  // values 为登录方式声明的 fields 收集到的值（无 fields 时省略，行为与原先一致）
  pluginLoginStart: (id: string, method: string, values?: Record<string, string>) =>
    request<PluginLoginResponse>(
      `/admin/api/plugins/${id}/console/login/${method}`,
      { method: 'POST', ...(values ? json(values) : {}) },
    ),
  pluginLoginPoll: (id: string, method: string, state: string) =>
    request<PluginLoginResponse>(
      `/admin/api/plugins/${id}/console/login/${method}?state=${encodeURIComponent(state)}`,
    ),
  pluginDeleteAccount: (id: string, accountId: string) =>
    request<{ success: boolean }>(
      `/admin/api/plugins/${id}/console/accounts/${encodeURIComponent(accountId)}`,
      { method: 'DELETE' },
    ),
  pluginCheckin: (id: string, accountId: string) =>
    request<PluginActionResponse>(
      `/admin/api/plugins/${id}/console/accounts/${encodeURIComponent(accountId)}/checkin`,
      { method: 'POST' },
    ),
  pluginQuota: (id: string, accountId: string) =>
    request<PluginActionResponse>(
      `/admin/api/plugins/${id}/console/accounts/${encodeURIComponent(accountId)}/quota`,
      { method: 'POST' },
    ),

  // 用量与费用：优先用 from/to（毫秒，[from, to)）时间范围，其次 date（YYYY-MM-DD）单日，最后按最近 days 天
  usageSummary: (params: {
    days?: number
    date?: string
    from?: number
    to?: number
    channel_name?: string
    model?: string
    key_hint?: string
  }) => {
    const q = new URLSearchParams()
    if (params.from != null && params.to != null) {
      q.set('from', String(params.from))
      q.set('to', String(params.to))
    } else if (params.date) {
      q.set('date', params.date)
    }
    if (params.days != null) q.set('days', String(params.days))
    if (params.channel_name) q.set('channel_name', params.channel_name)
    if (params.model) q.set('model', params.model)
    if (params.key_hint) q.set('key_hint', params.key_hint)
    return request<UsageSummaryResponse>(`/admin/api/usage/summary?${q.toString()}`)
  },
  usageLogs: (params: {
    days?: number
    from?: number
    to?: number
    model?: string
    channel_name?: string
    channel_id?: string
    api_key_id?: string
    key_hint?: string
    limit?: number
    offset?: number
  }) => {
    const q = new URLSearchParams()
    if (params.from != null) q.set('from', String(params.from))
    if (params.to != null) q.set('to', String(params.to))
    if (params.days != null) q.set('days', String(params.days))
    if (params.model) q.set('model', params.model)
    if (params.channel_name) q.set('channel_name', params.channel_name)
    if (params.channel_id) q.set('channel_id', params.channel_id)
    if (params.api_key_id) q.set('api_key_id', params.api_key_id)
    if (params.key_hint) q.set('key_hint', params.key_hint)
    if (params.limit) q.set('limit', String(params.limit))
    if (params.offset) q.set('offset', String(params.offset))
    return request<UsageLogsResponse>(`/admin/api/usage/logs?${q.toString()}`)
  },
  // 用量趋势：按小时 / 天聚合，可按渠道名 / 公开模型名 / 上游密钥筛选；from/to（毫秒）优先，否则按最近 days 天
  usageSeries: (params: {
    days?: number
    from?: number
    to?: number
    bucket: 'day' | 'hour'
    channel_name?: string
    model?: string
    key_hint?: string
  }) => {
    const q = new URLSearchParams()
    if (params.from != null && params.to != null) {
      q.set('from', String(params.from))
      q.set('to', String(params.to))
    } else if (params.days != null) {
      q.set('days', String(params.days))
    }
    q.set('bucket', params.bucket)
    if (params.channel_name) q.set('channel_name', params.channel_name)
    if (params.model) q.set('model', params.model)
    if (params.key_hint) q.set('key_hint', params.key_hint)
    return request<UsageSeriesResponse>(`/admin/api/usage/series?${q.toString()}`)
  },
  // 清理用量明细：days 表示保留最近多少天，0 表示连当天一起清理（错误日志按同一时间点一起清）
  cleanupUsage: (days: number) =>
    request<{ success: boolean; removed: number; removed_errors: number; days: number }>(
      `/admin/api/usage?days=${days}`,
      { method: 'DELETE' },
    ),

  // 价格表
  prices: () => request<PricesResponse>('/admin/api/prices'),
  refreshPrices: () =>
    request<{ success: boolean; models: number }>('/admin/api/prices/refresh', { method: 'POST' }),
  upsertPrice: (body: {
    model: string
    display_name?: string
    prompt_price: number
    completion_price: number
    cache_read_price?: number
  }) => request<ModelPrice>('/admin/api/prices', { method: 'PUT', ...json(body) }),
  deletePrice: (model: string) =>
    request<{ success: boolean }>(`/admin/api/prices?model=${encodeURIComponent(model)}`, {
      method: 'DELETE',
    }),
}