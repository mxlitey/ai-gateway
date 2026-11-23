import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Plugin, type PluginAccount, type PluginLoginMethod } from '../lib/api'
import { bjFmtDateTime, bjFmtTime } from '../lib/tz'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  Empty,
  ErrorText,
  Field,
  Input,
  Loading,
  Modal,
  PageHead,
  Textarea,
} from '../components/AdminUI'

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

const transient = new Set(['starting', 'restarting', 'discovered'])
const LOGIN_TIMEOUT_SECONDS = 90

type LoginFlow = {
  method: string
  url: string
  state: string
  status: string
  message: string
}

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

  if (plugins.isLoading) {
    return (
      <div className="panel">
        <Loading />
      </div>
    )
  }
  if (plugins.isError) return <ErrorText text={(plugins.error as Error).message} />

  const list = [...(plugins.data ?? [])].sort(
    (a, b) => Number(b.last_status === 'running') - Number(a.last_status === 'running'),
  )

  return (
    <div className="stack">
      <PageHead
        title="插件"
        actions={
          <Button
            size="sm"
            variant="ghost"
            disabled={rescan.isPending}
            onClick={() => rescan.mutate()}
            title={rescan.isPending ? '扫描中…' : '重新扫描插件目录'}
          >
            {rescan.isPending ? '扫描中…' : '重新扫描'}
          </Button>
        }
      />

      {list.length === 0 ? (
        <div className="panel">
          <Empty text="尚未发现插件" />
        </div>
      ) : (
        <div className="admingrid g2">
          {list.map((plugin) => {
            const busy = action.isPending || setFlags.isPending
            const running = plugin.last_status === 'running'
            return (
              <div className="panel" key={plugin.id}>
                <div className="panel-b" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                  <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 12 }}>
                    <div>
                      <p style={{ margin: 0, fontWeight: 500 }}>{plugin.name}</p>
                      <p className="mono tiny faint" style={{ margin: '2px 0 0' }}>
                        {plugin.id} · v{plugin.version || '—'}
                      </p>
                    </div>
                    <Badge tone={statusTone[plugin.last_status] ?? 'off'}>
                      {statusText[plugin.last_status] ?? plugin.last_status}
                    </Badge>
                  </div>

                  <dl className="kvgrid">
                    <dt>端口</dt>
                    <dd className="mono">{plugin.port || '启动后分配'}</dd>
                    <dt>最后探活</dt>
                    <dd>
                      {plugin.last_seen_at ? bjFmtTime(plugin.last_seen_at) : '—'}
                    </dd>
                  </dl>

                  {plugin.last_error ? (
                    <p
                      className="tiny"
                      style={{
                        margin: 0,
                        border: '1px solid var(--danger)',
                        background: 'var(--danger-weak)',
                        color: 'var(--danger)',
                        borderRadius: 8,
                        padding: '6px 9px',
                        wordBreak: 'break-all',
                      }}
                    >
                      {plugin.last_error}
                    </p>
                  ) : null}

                  <p className="mono faint" style={{ margin: 0, fontSize: 10.5, wordBreak: 'break-all' }}>
                    {plugin.dir}
                  </p>

                  <div
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      alignItems: 'center',
                      gap: 6,
                      borderTop: '1px solid var(--hairline)',
                      paddingTop: 11,
                    }}
                  >
                    {running ? (
                      <>
                        <Button disabled={busy} onClick={() => action.mutate({ id: plugin.id, kind: 'restart' })}>
                          重启
                        </Button>
                        <Button disabled={busy} onClick={() => action.mutate({ id: plugin.id, kind: 'stop' })}>
                          停止
                        </Button>
                      </>
                    ) : (
                      <Button
                        variant="primary"
                        disabled={busy}
                        onClick={() => action.mutate({ id: plugin.id, kind: 'start' })}
                      >
                        启动
                      </Button>
                    )}
                    <label
                      className="tiny muted"
                      style={{ display: 'flex', alignItems: 'center', gap: 6, marginLeft: 8 }}
                    >
                      <input
                        type="checkbox"
                        checked={plugin.autostart}
                        disabled={busy}
                        onChange={(e) => setFlags.mutate({ plugin, body: { autostart: e.target.checked } })}
                      />
                      开机自启
                    </label>
                    {plugin.console ? (
                      <Button
                        variant="primary"
                        style={{ marginLeft: 'auto' }}
                        disabled={!running}
                        title={running ? '打开插件控制台' : '插件未运行'}
                        onClick={() => setOpenId(plugin.id)}
                      >
                        控制台
                      </Button>
                    ) : null}
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {openId && list.some((p) => p.id === openId) ? (
        <PluginConsoleModal plugin={list.find((p) => p.id === openId)!} onClose={() => setOpenId(null)} />
      ) : null}
    </div>
  )
}

function PluginConsoleModal({ plugin, onClose }: { plugin: Plugin; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [login, setLogin] = useState<LoginFlow | null>(null)
  const loginRef = useRef<LoginFlow | null>(null)
  const [remaining, setRemaining] = useState(0)
  const [callbackURL, setCallbackURL] = useState('')
  const autoRefreshed = useRef(false)
  const [autoRefreshing, setAutoRefreshing] = useState(false)
  const [form, setForm] = useState<{ method: PluginLoginMethod; values: Record<string, string> } | null>(
    null,
  )

  const accounts = useQuery({
    queryKey: ['plugin-console', plugin.id],
    queryFn: () => api.pluginAccounts(plugin.id),
    refetchInterval: 20000,
  })

  const reload = () => queryClient.invalidateQueries({ queryKey: ['plugin-console', plugin.id] })

  const keepLogin = (next: LoginFlow | null) => {
    loginRef.current = next
    setLogin(next)
    if (!next) {
      setCallbackURL('')
      setRemaining(0)
    }
  }

  const submitCallback = useMutation({
    mutationFn: (url: string) => api.pluginSubmitCallback(plugin.id, url),
    onSuccess: () => {
      toast('ok', '回调已提交，正在确认授权结果')
      setCallbackURL('')
    },
    onError: (err: Error) => toast('err', err.message),
  })

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
      setRemaining(LOGIN_TIMEOUT_SECONDS)
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
        if (status === 'expired') {
          keepLogin(null)
          return
        }
        keepLogin({ ...cur, status, message: res.message ?? cur.message })
        if (status === 'ok') {
          toast('ok', res.message ?? '授权成功')
          await reload()
        } else if (status === 'error') {
          toast('err', res.message ?? '授权失败')
        }
      } catch {
        // 单次轮询失败就忽略，下一次继续
      }
    }, 2000)
    return () => clearInterval(timer)
  }, [plugin.id])

  useEffect(() => {
    if (!login || login.status !== 'pending') return
    const timer = setInterval(() => setRemaining((n) => Math.max(0, n - 1)), 1000)
    return () => clearInterval(timer)
  }, [login?.status])

  useEffect(() => {
    if (!login || login.status !== 'pending' || remaining > 0) return
    keepLogin(null)
  }, [login, remaining])

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
    startLogin.isPending || checkin.isPending || quota.isPending || remove.isPending || autoRefreshing
  const items = accounts.data?.accounts ?? []
  const methods = accounts.data?.login_methods ?? []
  const caps = plugin.capabilities ?? []
  const hasCheckin = caps.includes('checkin')
  const hasQuota = caps.includes('quota')

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
      <div className="stack">
        {accounts.data?.notice ? (
          <p
            className="tiny"
            style={{
              margin: 0,
              border: '1px solid var(--brand)',
              background: 'var(--brand-weak)',
              color: 'var(--brand)',
              borderRadius: 8,
              padding: '6px 9px',
            }}
          >
            {accounts.data.notice}
          </p>
        ) : null}

        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
          <p className="tiny faint" style={{ margin: 0 }}>
            共 {items.length} 个账号
            {autoRefreshing ? ' · 正在刷新额度…' : ''}
          </p>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            <Button size="sm" disabled={autoRefreshing} onClick={() => accounts.refetch()}>
              刷新
            </Button>
            {methods.map((m) => (
              <Button
                key={m.id}
                size="sm"
                variant="primary"
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
          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: 9,
              border: '1px solid var(--brand)',
              background: 'var(--brand-weak)',
              borderRadius: 8,
              padding: 10,
            }}
          >
            <p style={{ margin: 0, fontSize: 11, fontWeight: 500, color: 'var(--brand)' }}>
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
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <Button size="sm" variant="primary" disabled={busy} onClick={submitForm}>
                提交
              </Button>
              <Button size="sm" disabled={busy} onClick={() => setForm(null)}>
                取消
              </Button>
            </div>
          </div>
        ) : null}

        {login ? (
          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: 8,
              border: '1px solid var(--brand)',
              background: 'var(--brand-weak)',
              borderRadius: 8,
              padding: 10,
            }}
          >
            <p className="tiny" style={{ margin: 0, color: 'var(--brand)' }}>
              {login.status === 'pending' ? `${login.message}（正在等待授权…）` : login.message}
            </p>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
              <a
                href={login.url}
                target="_blank"
                rel="noreferrer"
                className="tiny"
                style={{ color: 'var(--brand)', wordBreak: 'break-all' }}
              >
                {login.url}
              </a>
              <Button size="sm" onClick={() => keepLogin(null)}>
                {login.status === 'pending' ? `取消(${remaining})` : '取消'}
              </Button>
            </div>
            {login.status === 'pending' ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, paddingTop: 8, borderTop: '1px solid var(--border)' }}>
                <Input
                  value={callbackURL}
                  onChange={(e) => setCallbackURL(e.target.value)}
                  placeholder="127.0.0.1:xxxxx/...（可省略 http://）"
                />
                <Button
                  size="sm"
                  disabled={submitCallback.isPending || !callbackURL.trim()}
                  onClick={() => submitCallback.mutate(callbackURL.trim())}
                >
                  提交回调
                </Button>
              </div>
            ) : null}
          </div>
        ) : null}

        {accounts.isLoading ? (
          <Loading text="读取账号…" />
        ) : accounts.isError ? (
          <ErrorText text={(accounts.error as Error).message} />
        ) : items.length === 0 ? (
          <Empty text="还没有账号：用上面的按钮导入或授权登录" />
        ) : (
          <div className="tbl-wrap">
            <table className="tbl console-tbl" style={{ minWidth: '36rem' }}>
              <thead>
                <tr>
                  <th>账号</th>
                  <th>站点</th>
                  {hasQuota ? <th>套餐</th> : null}
                  {hasQuota ? <th className="r">额度</th> : null}
                  {hasCheckin ? <th>今日签到</th> : null}
                  <th>过期</th>
                  <th />
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
    <tr>
      <td>
        <span>{account.name || account.id}</span>
        {account.last_error ? (
          <span
            className="tiny"
            style={{ display: 'block', color: 'var(--danger)', wordBreak: 'break-all' }}
          >
            {account.last_error}
          </span>
        ) : null}
      </td>
      <td className="tiny faint">{siteText(account.site)}</td>
      {hasQuota ? <td className="tiny muted">{account.plan || '—'}</td> : null}
      {hasQuota ? (
        <td className="r mono tiny">
          {typeof account.credits === 'number' ? account.credits.toLocaleString('en-US') : '—'}
        </td>
      ) : null}
      {hasCheckin ? (
        <td>
          <Badge tone={account.checked_in_today ? 'ok' : 'warn'}>
            {account.checked_in_today ? `已签 · 连签 ${account.streak_days ?? 0}` : '未签'}
          </Badge>
        </td>
      ) : null}
      <td className="tiny" style={{ whiteSpace: 'nowrap' }}>
        {account.expires_at ? (
          expired ? (
            <span style={{ color: 'var(--danger)' }}>已过期</span>
          ) : (
            <span className="faint">{bjFmtDateTime(account.expires_at)}</span>
          )
        ) : (
          <span className="faint">—</span>
        )}
      </td>
      <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
        {hasCheckin ? (
          <button
            type="button"
            className="btn ghost sm"
            style={{ color: 'var(--brand)' }}
            disabled={busy || account.checked_in_today}
            onClick={onCheckin}
          >
            签到
          </button>
        ) : null}
        {hasQuota ? (
          <button type="button" className="btn ghost sm" disabled={busy} onClick={onQuota}>
            刷新额度
          </button>
        ) : null}
        <button
          type="button"
          className="btn ghost sm"
          style={{ color: 'var(--danger)' }}
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
