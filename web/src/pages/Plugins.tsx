import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Plugin, type PluginAccount, type PluginLoginMethod } from '../lib/api'
import { toast } from '../lib/toast'
import { Badge, Button, Card, Empty, ErrorText, Field, IconButton, Input, Loading, Modal, RefreshIcon, Textarea } from '../components/ui'

const statusTone: Record<string, 'ok' | 'off' | 'warn' | 'info'> = {
  running: 'ok',
  starting: 'info',
  restarting: 'info',
  unhealthy: 'warn',
  failed: 'warn',
  stopped: 'off',
  discovered: 'off',
}

const statusText: Record<string, string> = {
  running: '运行中',
  starting: '启动中',
  restarting: '重启中',
  unhealthy: '不健康',
  failed: '失败',
  stopped: '已停止',
  discovered: '已发现',
}

// 处于过渡状态的插件需要轮询刷新
const transient = new Set(['starting', 'restarting', 'discovered'])

export default function Plugins() {
  const queryClient = useQueryClient()
  const [openId, setOpenId] = useState<string | null>(null)

  const plugins = useQuery({
    queryKey: ['plugins'],
    queryFn: api.plugins,
    refetchInterval: (query) => {
      const list = query.state.data
      if (!list) return false
      return list.some((p) => transient.has(p.last_status)) ? 1500 : false
    },
  })

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['plugins'] })
    await queryClient.invalidateQueries({ queryKey: ['channels'] })
    await queryClient.invalidateQueries({ queryKey: ['routes'] })
  }

  const action = useMutation({
    mutationFn: (p: { id: string; kind: 'start' | 'stop' | 'restart' }) => {
      switch (p.kind) {
        case 'start':
          return api.startPlugin(p.id)
        case 'stop':
          return api.stopPlugin(p.id)
        default:
          return api.restartPlugin(p.id)
      }
    },
    onSuccess: async (_res, p) => {
      const label = { start: '已启动', stop: '已停止', restart: '已重启' }[p.kind]
      toast('ok', `${p.id} ${label}`)
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const rescan = useMutation({
    mutationFn: () => api.rescanPlugins(),
    onSuccess: async () => {
      toast('ok', '已重新扫描插件目录')
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const setFlags = useMutation({
    mutationFn: (p: { plugin: Plugin; body: { enabled?: boolean; autostart?: boolean } }) =>
      api.updatePlugin(p.plugin.id, p.body),
    onSuccess: async () => {
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  if (plugins.isLoading) return <Loading />
  if (plugins.isError) return <ErrorText text={(plugins.error as Error).message} />

  // 运行中的插件置顶展示
  const list = [...(plugins.data ?? [])].sort(
    (a, b) => Number(b.last_status === 'running') - Number(a.last_status === 'running'),
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">插件</h1>
        <IconButton
          title={rescan.isPending ? '扫描中…' : '重新扫描插件目录'}
          disabled={rescan.isPending}
          onClick={() => rescan.mutate()}
        >
          <RefreshIcon />
        </IconButton>
      </div>

      {list.length === 0 ? (
        <Card>
          <Empty text="尚未发现插件" />
          <div className="mx-auto max-w-xl space-y-2 rounded-lg border border-zinc-200 bg-zinc-50 p-4 text-sm text-zinc-600 dark:border-zinc-800 dark:bg-zinc-950/40 dark:text-zinc-400">
            <p className="font-medium text-zinc-700 dark:text-zinc-300">如何安装插件</p>
            <p>
              1. 在程序目录下新建 <code className="text-xs">plugins/&lt;插件id&gt;/</code>
              （目录名就是插件 id，用小写字母、数字、- 或 _）
            </p>
            <p>
              2. 放入 <code className="text-xs">plugin.json</code> 与入口文件
            </p>
            <p>3. 点上方「重新扫描」，插件会被拉起并自动注册为渠道</p>
            <p className="pt-1 text-xs text-zinc-500">
              仓库内置了一个可直接试用的示例：把 <code>examples/workbuddy</code> 复制到{' '}
              <code>plugins/workbuddy</code> 即可
            </p>
          </div>
        </Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {list.map((plugin) => {
            const busy = action.isPending || setFlags.isPending
            const running = plugin.last_status === 'running'
            return (
              <Card key={plugin.id}>
                <div className="space-y-3">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <p className="font-medium text-zinc-800 dark:text-zinc-100">{plugin.name}</p>
                      <p className="mt-0.5 font-mono text-xs text-zinc-500">
                        {plugin.id} · v{plugin.version || '—'}
                      </p>
                    </div>
                    <Badge tone={statusTone[plugin.last_status] ?? 'off'}>
                      {statusText[plugin.last_status] ?? plugin.last_status}
                    </Badge>
                  </div>

                  <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
                    <dt className="text-zinc-500">端口</dt>
                    <dd className="font-mono text-zinc-700 dark:text-zinc-300">{plugin.port || '—'}</dd>
                    <dt className="text-zinc-500">最后探活</dt>
                    <dd className="text-zinc-700 dark:text-zinc-300">
                      {plugin.last_seen_at
                        ? new Date(plugin.last_seen_at).toLocaleTimeString()
                        : '—'}
                    </dd>
                  </dl>

                  {plugin.last_error ? (
                    <p className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-xs break-all text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
                      {plugin.last_error}
                    </p>
                  ) : null}

                  <p className="font-mono text-[11px] break-all text-zinc-400">{plugin.dir}</p>

                  <div className="flex flex-wrap gap-1 border-t border-zinc-100 pt-3 dark:border-zinc-800">
                    {running ? (
                      <>
                        <Button
                          disabled={busy}
                          onClick={() => action.mutate({ id: plugin.id, kind: 'restart' })}
                        >
                          重启
                        </Button>
                        <Button
                          disabled={busy}
                          onClick={() => action.mutate({ id: plugin.id, kind: 'stop' })}
                        >
                          停止
                        </Button>
                      </>
                    ) : (
                      <Button
                        variant="primary"
                        disabled={busy || plugin.port === 0}
                        onClick={() => action.mutate({ id: plugin.id, kind: 'start' })}
                      >
                        启动
                      </Button>
                    )}
                    <label className="ml-2 flex items-center gap-1.5 text-xs text-zinc-600 dark:text-zinc-400">
                      <input
                        type="checkbox"
                        checked={plugin.autostart}
                        disabled={busy}
                        onChange={(e) =>
                          setFlags.mutate({
                            plugin,
                            body: { autostart: e.target.checked },
                          })
                        }
                      />
                      开机自启
                    </label>
                    {plugin.console ? (
                      <Button
                        className="ml-auto"
                        variant="primary"
                        disabled={!running}
                        title={running ? '打开插件控制台' : '插件未运行'}
                        onClick={() => setOpenId(plugin.id)}
                      >
                        控制台
                      </Button>
                    ) : null}
                  </div>
                </div>
              </Card>
            )
          })}
        </div>
      )}

      {openId && list.some((p) => p.id === openId) ? (
        <PluginConsoleModal
          plugin={list.find((p) => p.id === openId)!}
          onClose={() => setOpenId(null)}
        />
      ) : null}
    </div>
  )
}

/* ---------- 插件控制台（约定接口 + 宿主统一渲染） ---------- */

type LoginFlow = {
  method: string
  url: string
  state: string
  status: string
  message: string
}

function PluginConsoleModal({ plugin, onClose }: { plugin: Plugin; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [login, setLogin] = useState<LoginFlow | null>(null)
  const loginRef = useRef<LoginFlow | null>(null)
  // 打开控制台时自动刷新一轮额度与签到状态（只跑一次，见下方 effect）
  const autoRefreshed = useRef(false)
  const [autoRefreshing, setAutoRefreshing] = useState(false)
  // 声明了 fields 的登录方式：先收集输入再提交；未声明的一律直接提交（行为不变）
  const [form, setForm] = useState<{ method: PluginLoginMethod; values: Record<string, string> } | null>(null)

  const accounts = useQuery({
    queryKey: ['plugin-console', plugin.id],
    queryFn: () => api.pluginAccounts(plugin.id),
    refetchInterval: 20000,
  })

  const reload = () => queryClient.invalidateQueries({ queryKey: ['plugin-console', plugin.id] })

  const keepLogin = (next: LoginFlow | null) => {
    loginRef.current = next
    setLogin(next)
  }

  // link 类登录（OAuth）：发起后拿到授权链接，随后每 2 秒问一次结果
  // action 类（例如从本机桌面端导入）：插件直接返回 ok，无需轮询
  const startLogin = useMutation({
    mutationFn: (input: { method: string; values?: Record<string, string> }) =>
      api.pluginLoginStart(plugin.id, input.method, input.values),
    onSuccess: (res, input) => {
      setForm(null)
      if (!res.url || !res.state) {
        toast('ok', res.message ?? '已完成')
        void reload()
        return
      }
      keepLogin({
        method: input.method,
        url: res.url,
        state: res.state,
        status: res.status ?? 'pending',
        message: res.message ?? '请在浏览器中完成授权',
      })
      window.open(res.url, '_blank', 'noopener')
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const openForm = (method: PluginLoginMethod) => {
    const values: Record<string, string> = {}
    for (const f of method.fields ?? []) values[f.name] = ''
    setForm({ method, values })
  }

  const submitForm = () => {
    if (!form) return
    const missing = (form.method.fields ?? []).find(
      (f) => f.required !== false && !form.values[f.name]?.trim(),
    )
    if (missing) {
      toast('err', `请填写「${missing.label}」`)
      return
    }
    startLogin.mutate({ method: form.method.id, values: form.values })
  }

  useEffect(() => {
    const timer = setInterval(async () => {
      const cur = loginRef.current
      if (!cur || cur.status !== 'pending') return
      try {
        const res = await api.pluginLoginPoll(plugin.id, cur.method, cur.state)
        const status = res.status ?? cur.status
        keepLogin({ ...cur, status, message: res.message ?? cur.message })
        if (status === 'ok') {
          toast('ok', res.message ?? '授权成功')
          await reload()
        } else if (status === 'error' || status === 'expired') {
          toast('err', res.message ?? '授权失败')
        }
      } catch {
        // 单次轮询失败就忽略，下一次继续
      }
    }, 2000)
    return () => clearInterval(timer)
  }, [plugin.id])

  const checkin = useMutation({
    mutationFn: (id: string) => api.pluginCheckin(plugin.id, id),
    onSuccess: async (res) => {
      toast('ok', res.message ?? '签到完成')
      await reload()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const quota = useMutation({
    mutationFn: (id: string) => api.pluginQuota(plugin.id, id),
    onSuccess: async (res) => {
      toast('ok', res.message ?? '已刷新额度')
      await reload()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.pluginDeleteAccount(plugin.id, id),
    onSuccess: async () => {
      toast('ok', '账号已删除')
      await reload()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const busy =
    startLogin.isPending ||
    checkin.isPending ||
    quota.isPending ||
    remove.isPending ||
    autoRefreshing
  const items = accounts.data?.accounts ?? []
  const methods = accounts.data?.login_methods ?? []
  const caps = plugin.capabilities ?? []
  const hasCheckin = caps.includes('checkin')
  const hasQuota = caps.includes('quota')

  // 打开控制台时自动刷新一轮额度，只执行一次（签到仍由宿主定时调度、用户手动触发）。
  // 接口幂等友好（见 docs/plugin-spec.md §6.1），单个账号失败只忽略：
  // 插件会把原因写进账号的 last_error，刷新后自然显示在列表里。
  useEffect(() => {
    if (autoRefreshed.current || !accounts.data) return
    autoRefreshed.current = true
    const targets = (accounts.data.accounts ?? []).filter((a) => a.enabled !== false)
    if (targets.length === 0 || !hasQuota) return

    let cancelled = false
    setAutoRefreshing(true)
    void (async () => {
      try {
        for (const acc of targets) {
          if (cancelled) return
          try {
            await api.pluginQuota(plugin.id, acc.id)
          } catch {
            /* 单账号失败忽略，原因由插件记到 last_error */
          }
        }
      } finally {
        if (!cancelled) {
          setAutoRefreshing(false)
          await reload()
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [accounts.data, hasQuota, plugin.id])

  return (
    <Modal
      open
      wide
      title={plugin.console_title || `${plugin.name} 控制台`}
      onClose={onClose}
      footer={<Button onClick={onClose}>关闭</Button>}
    >
      <div className="space-y-4">
        {accounts.data?.notice ? (
          <p className="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2 text-xs text-sky-800 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-200">
            {accounts.data.notice}
          </p>
        ) : null}

        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            账号与凭据由插件负责，界面由面板统一渲染 · 共 {items.length} 个账号
            {autoRefreshing ? ' · 正在刷新额度…' : ''}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              className="py-1 text-xs"
              disabled={autoRefreshing}
              onClick={() => accounts.refetch()}
            >
              刷新
            </Button>
            {methods.map((m) => (
              <Button
                key={m.id}
                variant="primary"
                className="py-1 text-xs"
                disabled={busy}
                title={m.hint}
                onClick={() => (m.fields?.length ? openForm(m) : startLogin.mutate({ method: m.id }))}
              >
                {m.label}
              </Button>
            ))}
          </div>
        </div>

        {form ? (
          <div className="space-y-3 rounded-lg border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-900 dark:bg-emerald-950/40">
            <p className="text-xs font-medium text-emerald-800 dark:text-emerald-200">
              {form.method.label}
            </p>
            {(form.method.fields ?? []).map((f) => (
              <Field key={f.name} label={f.label} hint={f.hint}>
                {f.kind === 'multiline' ? (
                  <Textarea
                    rows={3}
                    placeholder={f.placeholder}
                    value={form.values[f.name] ?? ''}
                    onChange={(e) =>
                      setForm({ ...form, values: { ...form.values, [f.name]: e.target.value } })
                    }
                  />
                ) : (
                  <Input
                    type={f.kind === 'secret' ? 'password' : 'text'}
                    autoComplete={f.kind === 'secret' ? 'off' : undefined}
                    placeholder={f.placeholder}
                    value={form.values[f.name] ?? ''}
                    onChange={(e) =>
                      setForm({ ...form, values: { ...form.values, [f.name]: e.target.value } })
                    }
                  />
                )}
              </Field>
            ))}
            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                className="py-1 text-xs"
                disabled={busy}
                onClick={submitForm}
              >
                提交
              </Button>
              <Button className="py-1 text-xs" disabled={busy} onClick={() => setForm(null)}>
                取消
              </Button>
            </div>
          </div>
        ) : null}

        {login ? (
          <div className="space-y-2 rounded-lg border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-900 dark:bg-emerald-950/40">
            <p className="text-xs text-emerald-800 dark:text-emerald-200">
              {login.status === 'pending' ? `${login.message}（正在等待授权…）` : login.message}
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <a
                href={login.url}
                target="_blank"
                rel="noreferrer"
                className="text-xs break-all text-emerald-700 underline dark:text-emerald-300"
              >
                {login.url}
              </a>
              <Button className="py-1 text-xs" onClick={() => keepLogin(null)}>
                取消
              </Button>
            </div>
          </div>
        ) : null}

        {accounts.isLoading ? (
          <Loading text="读取账号…" />
        ) : accounts.isError ? (
          <ErrorText text={(accounts.error as Error).message} />
        ) : items.length === 0 ? (
          <Empty text="还没有账号：用上面的按钮导入或授权登录" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[36rem] text-sm">
              <thead>
                <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                  <th className="py-2 pr-3 font-medium">账号</th>
                  <th className="py-2 pr-3 font-medium">站点</th>
                  {hasQuota ? <th className="py-2 pr-3 font-medium">套餐</th> : null}
                  {hasQuota ? <th className="py-2 pr-3 text-right font-medium">额度</th> : null}
                  {hasCheckin ? <th className="py-2 pr-3 font-medium">今日签到</th> : null}
                  <th className="py-2 pr-3 font-medium">过期</th>
                  <th className="py-2 font-medium" />
                </tr>
              </thead>
              <tbody>
                {items.map((acc) => (
                  <AccountRow
                    key={acc.id}
                    account={acc}
                    busy={busy}
                    hasCheckin={hasCheckin}
                    hasQuota={hasQuota}
                    onCheckin={() => checkin.mutate(acc.id)}
                    onQuota={() => quota.mutate(acc.id)}
                    onDelete={() => {
                      if (window.confirm(`删除账号 ${acc.name || acc.id}？`)) remove.mutate(acc.id)
                    }}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Modal>
  )
}

function AccountRow({
  account,
  busy,
  hasCheckin,
  hasQuota,
  onCheckin,
  onQuota,
  onDelete,
}: {
  account: PluginAccount
  busy: boolean
  hasCheckin: boolean
  hasQuota: boolean
  onCheckin: () => void
  onQuota: () => void
  onDelete: () => void
}) {
  const expired = !!account.expires_at && account.expires_at < Date.now()
  return (
    <tr className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
      <td className="py-2 pr-3">
        <span className="text-zinc-700 dark:text-zinc-300">{account.name || account.id}</span>
        {account.last_error ? (
          <span className="block text-[11px] break-all text-red-600 dark:text-red-400">
            {account.last_error}
          </span>
        ) : null}
      </td>
      <td className="py-2 pr-3 text-xs text-zinc-500">{siteText(account.site)}</td>
      {hasQuota ? (
        <td className="py-2 pr-3 text-xs text-zinc-600 dark:text-zinc-400">{account.plan || '—'}</td>
      ) : null}
      {hasQuota ? (
        <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-700 dark:text-zinc-300">
          {typeof account.credits === 'number' ? account.credits.toLocaleString('en-US') : '—'}
        </td>
      ) : null}
      {hasCheckin ? (
        <td className="py-2 pr-3">
          <Badge tone={account.checked_in_today ? 'ok' : 'warn'}>
            {account.checked_in_today ? `已签 · 连签 ${account.streak_days ?? 0}` : '未签'}
          </Badge>
        </td>
      ) : null}
      <td className="py-2 pr-3 text-xs whitespace-nowrap">
        {account.expires_at ? (
          expired ? (
            <span className="text-red-600 dark:text-red-400">已过期</span>
          ) : (
            <span className="text-zinc-500">{new Date(account.expires_at).toLocaleString()}</span>
          )
        ) : (
          <span className="text-zinc-400">—</span>
        )}
      </td>
      <td className="py-2 text-right whitespace-nowrap">
        {hasCheckin ? (
          <button
            className="mr-2 text-xs text-emerald-600 hover:underline disabled:opacity-50 dark:text-emerald-400"
            disabled={busy || account.checked_in_today}
            onClick={onCheckin}
          >
            签到
          </button>
        ) : null}
        {hasQuota ? (
          <button
            className="mr-2 text-xs text-zinc-600 hover:underline disabled:opacity-50 dark:text-zinc-300"
            disabled={busy}
            onClick={onQuota}
          >
            刷新额度
          </button>
        ) : null}
        <button
          className="text-xs text-red-600 hover:underline disabled:opacity-50 dark:text-red-400"
          disabled={busy}
          onClick={onDelete}
        >
          删除
        </button>
      </td>
    </tr>
  )
}

function siteText(site?: string): string {
  switch (site) {
    case 'cn':
      return '国内站'
    case 'intl':
      return '国际站'
    default:
      return site || '—'
  }
}