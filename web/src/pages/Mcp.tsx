import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type MCPServer, type MCPServerInput, type MCPTool } from '../lib/api'
import { bjDayStart, bjFmtDateTime } from '../lib/tz'
import { toast } from '../lib/toast'
import RangePicker, { type RangeValue } from '../components/RangePicker'
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
  Segmented,
  Switch,
  Textarea,
} from '../components/AdminUI'

type KeyRow = { key: string; label: string; enabled: boolean; daily_limit: string }
type HeaderRow = { name: string; value: string }
type ToolLimitRow = { tool: string; limit: string }

type FormState = {
  id: string | null
  name: string
  alias: string
  url: string
  headers: HeaderRow[]
  keys: KeyRow[]
  keyStrategy: string
  dailyLimit: string
  toolLimits: ToolLimitRow[]
  billableMode: string
  billableMarker: string
  timeoutMS: string
  enabled: boolean
}

const KEY_STRATEGIES = [
  { value: 'rotate', label: '负载均衡' },
  { value: 'ordered', label: '故障转移' },
] as const

const BILLABLE_MODES = [
  { value: 'success', label: '成功即计费' },
  { value: 'marker', label: '按响应标记计费' },
] as const

const emptyForm: FormState = {
  id: null,
  name: '',
  alias: '',
  url: '',
  headers: [],
  keys: [{ key: '', label: '', enabled: true, daily_limit: '' }],
  keyStrategy: 'rotate',
  dailyLimit: '',
  toolLimits: [],
  billableMode: 'success',
  billableMarker: '',
  timeoutMS: '',
  enabled: true,
}

const statusTone: Record<string, 'ok' | 'off' | 'warn' | 'info'> = {
  ok: 'ok',
  error: 'warn',
  idle: 'off',
}
const statusText: Record<string, string> = {
  ok: '正常',
  error: '异常',
  idle: '未探测',
}

function maskKey(key: string): string {
  if (key.length <= 12) return key
  return `${key.slice(0, 7)}...${key.slice(-4)}`
}

function limitValue(input: string): number {
  const n = Number(input.trim() === '' ? -1 : input)
  return Number.isFinite(n) ? n : -1
}

const PAGE_SIZE = 50
const DAY = 24 * 60 * 60 * 1000

export default function Mcp() {
  const queryClient = useQueryClient()
  const servers = useQuery({ queryKey: ['mcp-servers'], queryFn: api.mcpServers })
  const [form, setForm] = useState<FormState | null>(null)
  const [toolsOf, setToolsOf] = useState<MCPServer | null>(null)
  const [calling, setCalling] = useState<{ tool: string } | null>(null)
  const [flippedId, setFlippedId] = useState<string | null>(null)

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['mcp-servers'] })
  }

  const save = useMutation({
    mutationFn: async (state: FormState) => {
      const body: MCPServerInput = {
        name: state.name.trim(),
        alias: state.alias.trim(),
        url: state.url.trim(),
        headers: state.headers
          .map((h) => ({ name: h.name.trim(), value: h.value }))
          .filter((h) => h.name !== ''),
        keys: state.keys
          .map((k) => ({
            key: k.key.trim(),
            label: k.label.trim(),
            enabled: k.enabled,
            daily_limit: limitValue(k.daily_limit),
          }))
          .filter((k) => k.key !== ''),
        key_strategy: state.keyStrategy,
        daily_limit: limitValue(state.dailyLimit),
        tool_limits: Object.fromEntries(
          state.toolLimits
            .filter((t) => t.tool !== '' && t.limit.trim() !== '' && Number.isFinite(Number(t.limit)))
            .map((t) => [t.tool, limitValue(t.limit)] as const)
            .filter(([, n]) => n >= -1),
        ),
        timeout_ms: Number(state.timeoutMS) || 0,
        billable_mode: state.billableMode,
        billable_marker: state.billableMarker.trim(),
        enabled: state.enabled,
      }
      return state.id ? api.updateMCPServer(state.id, body) : api.createMCPServer(body)
    },
    onSuccess: async () => {
      toast('ok', 'MCP 服务器已保存，正在拉取工具列表')
      setForm(null)
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const toggle = useMutation({
    mutationFn: (id: string) => api.toggleMCPServer(id),
    onSuccess: invalidate,
    onError: (err: Error) => toast('err', err.message),
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteMCPServer(id),
    onSuccess: async () => {
      toast('ok', 'MCP 服务器已删除')
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const refresh = useMutation({
    mutationFn: (id: string) => api.refreshMCPServer(id),
    onSuccess: async (r) => {
      toast('ok', `已刷新工具列表（${r.tools} 个）`)
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const test = useMutation({
    mutationFn: (id: string) => api.testMCPServer(id),
    onSuccess: (r) => {
      if (r.success) toast('ok', `连接正常（${r.duration_ms}ms）`)
      else toast('err', r.error || '连接失败')
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const openEdit = (s: MCPServer) => {
    setForm({
      id: s.id,
      name: s.name,
      alias: s.alias,
      url: s.url,
      headers: s.headers.map((h) => ({ name: h.name, value: h.value })),
      keys:
        s.keys.length > 0
          ? s.keys.map((k) => ({
              key: k.key,
              label: k.label ?? '',
              enabled: k.enabled,
              daily_limit: String(k.daily_limit),
            }))
          : [{ key: '', label: '', enabled: true, daily_limit: '' }],
      keyStrategy: s.key_strategy || 'rotate',
      dailyLimit: String(s.daily_limit),
      toolLimits: Object.entries(s.tool_limits ?? {}).map(([tool, n]) => ({
        tool,
        limit: String(n),
      })),
      billableMode: s.billable_mode || 'success',
      billableMarker: s.billable_marker ?? '',
      timeoutMS: s.timeout_ms > 0 ? String(s.timeout_ms) : '',
      enabled: s.enabled,
    })
  }

  if (servers.isLoading) {
    return (
      <div className="panel">
        <Loading />
      </div>
    )
  }
  if (servers.isError) return <ErrorText text={(servers.error as Error).message} />

  const list = servers.data ?? []

  return (
    <div className="stack">
      <PageHead
        title="MCP 服务器"
        actions={
          <Button variant="primary" onClick={() => setForm({ ...emptyForm })}>
            添加服务器
          </Button>
        }
      />

      {list.length === 0 ? (
        <div className="panel">
          <Empty text="还没有 MCP 上游服务器" />
        </div>
      ) : (
        <div className="ch-grid">
          {list.map((s) => {
            const enabledKeys = s.keys.filter((k) => k.enabled).length
            const limitedUsed = s.keys
              .filter((k) => k.enabled && k.daily_limit > 0)
              .reduce((n, k) => n + (s.key_daily_calls?.[maskKey(k.key)] ?? 0), 0)
            const flipped = flippedId === s.id
            return (
              <div
                key={s.id}
                role="button"
                tabIndex={0}
                title={flipped ? '点击返回' : '点击查看操作'}
                className={`flip-card${flipped ? ' flipped' : ''}`}
                onClick={() => setFlippedId(flipped ? null : s.id)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    setFlippedId(flipped ? null : s.id)
                  }
                }}
              >
                <div className="flip-card-inner">
                  <div className="flip-face">
                    <McpHead s={s} onToggle={() => toggle.mutate(s.id)} />
                    <div className="ch-foot-info">
                      <span>
                        工具 <b className="num">{s.tool_count}</b>
                      </span>
                      <span>
                        密钥{' '}
                        <b className="num">
                          {enabledKeys} / {s.keys.length}
                        </b>
                      </span>
                      <span
                        title="与上游控制台的计费次数对齐：默认按成功调用计，也可配置为响应含标记才计"
                      >
                        计费次数{' '}
                        <b className="num">
                          {limitedUsed} / {s.daily_limit_total > 0 ? s.daily_limit_total : '不限'}
                        </b>
                      </span>
                    </div>
                  </div>

                  <div className="flip-face fl-back">
                    <McpHead s={s} onToggle={() => toggle.mutate(s.id)} />
                    <div className="ch-foot-btns">
                      <Button
                        size="sm"
                        variant="ghost"
                        title="重新拉取工具列表"
                        disabled={refresh.isPending}
                        onClick={(e) => {
                          e.stopPropagation()
                          refresh.mutate(s.id)
                        }}
                      >
                        刷新
                      </Button>
                      <Button
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          test.mutate(s.id)
                        }}
                      >
                        测试
                      </Button>
                      <Button
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          setToolsOf(s)
                        }}
                      >
                        工具
                      </Button>
                      <Button
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          setFlippedId(null)
                          openEdit(s)
                        }}
                      >
                        编辑
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={(e) => {
                          e.stopPropagation()
                          setFlippedId(null)
                          if (confirm(`确定删除「${s.name}」？其工具将不再对外暴露。`)) remove.mutate(s.id)
                        }}
                      >
                        删除
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      )}

      <McpUsagePanel />

      <ClientAccessPanel />

      {form ? (
        <Modal
          open
          wide
          title={form.id ? '编辑 MCP 服务器' : '添加 MCP 服务器'}
          onClose={() => setForm(null)}
          footer={
            <>
              <Button onClick={() => setForm(null)}>取消</Button>
              <Button
                variant="primary"
                onClick={() => save.mutate(form)}
                disabled={save.isPending || form.name.trim() === '' || form.url.trim() === ''}
              >
                保存
              </Button>
            </>
          }
        >
          <div className="stack">
            <div className="admingrid g2">
              <Field label="名称">
                <Input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="例如 GitHub"
                />
              </Field>
              <Field label="工具命名空间" hint="留空时由名称推导；对外工具名为 前缀__工具名">
                <Input
                  value={form.alias}
                  onChange={(e) => setForm({ ...form, alias: e.target.value })}
                  placeholder="github"
                />
              </Field>
            </div>

            <Field label="服务地址" hint="Streamable HTTP 端点，例如 https://example.com/mcp">
              <Input
                value={form.url}
                onChange={(e) => setForm({ ...form, url: e.target.value })}
                placeholder="https://example.com/mcp"
              />
            </Field>

            <Field label="密钥策略">
              <Segmented
                value={form.keyStrategy}
                options={KEY_STRATEGIES.map((s) => ({ value: s.value, label: s.label }))}
                onChange={(v) => setForm({ ...form, keyStrategy: v })}
              />
            </Field>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <span style={{ fontSize: 11.5, fontWeight: 500 }}>密钥</span>
                <Button
                  size="sm"
                  onClick={() =>
                    setForm({
                      ...form,
                      keys: [...form.keys, { key: '', label: '', enabled: true, daily_limit: '' }],
                    })
                  }
                >
                  添加密钥
                </Button>
              </div>
              {form.keys.map((k, i) => (
                <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <Switch
                    checked={k.enabled}
                    title={k.enabled ? '已启用，点击停用' : '已停用，点击启用'}
                    onChange={(v) => {
                      const keys = [...form.keys]
                      keys[i] = { ...k, enabled: v }
                      setForm({ ...form, keys })
                    }}
                  />
                  <Input
                    value={k.key}
                    onChange={(e) => {
                      const keys = [...form.keys]
                      keys[i] = { ...k, key: e.target.value }
                      setForm({ ...form, keys })
                    }}
                    placeholder="上游 Token（可留空表示无需鉴权）"
                  />
                  <Input
                    style={{ width: '5.5rem', flex: 'none' }}
                    type="number"
                    min={-1}
                    value={k.daily_limit}
                    onChange={(e) => {
                      const keys = [...form.keys]
                      keys[i] = { ...k, daily_limit: e.target.value }
                      setForm({ ...form, keys })
                    }}
                    placeholder="日上限"
                    title="该密钥每日计费调用上限：留空或 -1 表示不限，0 表示不允许调用"
                  />
                  <Button
                    variant="ghost"
                    title="移除密钥"
                    onClick={() => setForm({ ...form, keys: form.keys.filter((_, idx) => idx !== i) })}
                  >
                    ✕
                  </Button>
                </div>
              ))}
            </div>

            <Field label="计费口径" hint="决定哪些调用计入日上限">
              <Segmented
                value={form.billableMode}
                options={BILLABLE_MODES.map((m) => ({ value: m.value, label: m.label }))}
                onChange={(v) => setForm({ ...form, billableMode: v })}
              />
            </Field>

            {form.billableMode === 'marker' ? (
              <Field label="计费标记" hint="响应正文包含该串才计一次">
                <Input
                  value={form.billableMarker}
                  onChange={(e) => setForm({ ...form, billableMarker: e.target.value })}
                  placeholder="details:"
                />
              </Field>
            ) : null}

            <Field label="单次调用超时（毫秒）" hint="留空使用默认 120000">
              <Input
                type="number"
                min={5000}
                value={form.timeoutMS}
                onChange={(e) => setForm({ ...form, timeoutMS: e.target.value })}
                placeholder="120000"
              />
            </Field>

            <ToolLimitsField form={form} setForm={setForm} />

            <Field label="自定义请求头" hint="值支持 {{header}} 取客户端同名头、{{uuid}}、{{timestamp}}">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {form.headers.map((h, i) => (
                  <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <Input
                      value={h.name}
                      onChange={(e) => {
                        const headers = [...form.headers]
                        headers[i] = { ...h, name: e.target.value }
                        setForm({ ...form, headers })
                      }}
                      placeholder="Authorization"
                    />
                    <Input
                      value={h.value}
                      onChange={(e) => {
                        const headers = [...form.headers]
                        headers[i] = { ...h, value: e.target.value }
                        setForm({ ...form, headers })
                      }}
                      placeholder="Bearer {{token}}"
                    />
                    <Button
                      variant="ghost"
                      title="移除请求头"
                      onClick={() =>
                        setForm({ ...form, headers: form.headers.filter((_, idx) => idx !== i) })
                      }
                    >
                      ✕
                    </Button>
                  </div>
                ))}
                <div>
                  <Button
                    size="sm"
                    onClick={() => setForm({ ...form, headers: [...form.headers, { name: '', value: '' }] })}
                  >
                    添加请求头
                  </Button>
                </div>
              </div>
            </Field>
          </div>
        </Modal>
      ) : null}

      {toolsOf ? (
        <ToolsModal server={toolsOf} onClose={() => setToolsOf(null)} onCall={(tool) => setCalling({ tool })} />
      ) : null}

      {calling ? <CallModal tool={calling.tool} onClose={() => setCalling(null)} /> : null}
    </div>
  )
}

/* 工具级日上限 */
function ToolLimitsField({ form, setForm }: { form: FormState; setForm: (f: FormState) => void }) {
  const tools = useQuery({
    queryKey: ['mcp-tools', form.id],
    queryFn: () => api.mcpTools(form.id as string),
    enabled: !!form.id,
  })
  const cached = (tools.data ?? []).map((t) => t.tool.name ?? t.exposed_name)
  const names = Array.from(new Set([...cached, ...form.toolLimits.map((t) => t.tool)])).filter(
    (n) => n !== '',
  )
  const rows = names.map((name) => {
    const hit = form.toolLimits.find((t) => t.tool === name)
    return { tool: name, limit: hit?.limit ?? '0' }
  })

  const update = (tool: string, limit: string) => {
    const rest = form.toolLimits.filter((t) => t.tool !== tool)
    setForm({ ...form, toolLimits: [...rest, { tool, limit }] })
  }

  if (!form.id) {
    return (
      <Field label="工具日上限" hint="保存并拉取工具列表后可逐工具设置">
        <div className="tiny faint">先保存服务器。</div>
      </Field>
    )
  }

  return (
    <Field label="工具日上限" hint="按工具名匹配；正数为每条密钥每日上限，0 共享密钥额度，-1 不限">
      {tools.isLoading ? (
        <Loading />
      ) : rows.length === 0 ? (
        <div className="tiny faint">暂无工具缓存，先重新拉取工具列表。</div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {rows.map((row) => (
            <div key={row.tool} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span className="mono truncate tiny" style={{ flex: 1, minWidth: 0 }} title={row.tool}>
                {row.tool}
              </span>
              <Input
                style={{ width: '5.5rem', flex: 'none' }}
                type="text"
                inputMode="numeric"
                value={row.limit === '' || row.limit === '0' || row.limit === '-1' ? '' : row.limit}
                onChange={(e) => update(row.tool, e.target.value)}
                placeholder={row.limit === '-1' ? '不限' : '共享'}
                title="不填或 0：不单独限量，共享上游密钥的剩余次数；-1：完全不限；正数：该工具每条密钥的每日上限"
              />
            </div>
          ))}
        </div>
      )}
    </Field>
  )
}

/* 用量明细 */
function McpUsagePanel() {
  const [range, setRange] = useState<RangeValue>(() => dayRange(Date.now()))
  const [serverFilter, setServerFilter] = useState('')
  const [toolFilter, setToolFilter] = useState('')
  const [keyFilter, setKeyFilter] = useState('')
  const [offset, setOffset] = useState(0)

  const summary = useQuery({
    queryKey: ['mcp-usage-summary', range.from, range.to, serverFilter, toolFilter, keyFilter],
    queryFn: () =>
      api.mcpUsageSummary({
        from: range.from,
        to: range.to,
        server_name: serverFilter || undefined,
        tool: toolFilter || undefined,
        key_hint: keyFilter || undefined,
      }),
  })
  const logs = useQuery({
    queryKey: ['mcp-usage-logs', range.from, range.to, serverFilter, toolFilter, keyFilter, offset],
    queryFn: () =>
      api.mcpCallLogs({
        from: range.from,
        to: range.to,
        server_name: serverFilter || undefined,
        tool: toolFilter || undefined,
        key_hint: keyFilter || undefined,
        limit: PAGE_SIZE,
        offset,
      }),
  })

  const serverOptions = useMemo(
    () => (summary.data?.by_server ?? []).map((b) => b.key).filter(Boolean),
    [summary.data],
  )

  const keyOptions = useMemo(() => {
    const map = new Map<string, string>()
    for (const k of summary.data?.by_key ?? []) {
      if (!k.key_hint || !k.server_name) continue
      if (serverFilter && k.server_name !== serverFilter) continue
      if (k.calls <= 0 || map.has(k.key_hint)) continue
      map.set(k.key_hint, k.label ? `${k.label}（${k.key_hint}）` : k.key_hint)
    }
    return Array.from(map, ([hint, label]) => ({ hint, label }))
  }, [summary.data, serverFilter])

  const items = logs.data?.items ?? []
  const total = logs.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const page = Math.floor(offset / PAGE_SIZE) + 1

  return (
    <div className="panel panel-overflow">
      <div className="panel-h">
        <h2>用量明细</h2>
        <div className="r">
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              void summary.refetch()
              void logs.refetch()
            }}
          >
            刷新
          </Button>
        </div>
      </div>
      <div className="panel-b">
        <div className="usage-ctrl" style={{ marginBottom: 10 }}>
          <select
            className="input usage-sel"
            value={serverFilter}
            onChange={(e) => {
              setServerFilter(e.target.value)
              setToolFilter('')
              setKeyFilter('')
              setOffset(0)
            }}
          >
            <option value="">全部服务器</option>
            {serverOptions.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
          <select
            className="input usage-sel w2"
            value={toolFilter}
            onChange={(e) => {
              setToolFilter(e.target.value)
              setKeyFilter('')
              setOffset(0)
            }}
          >
            <option value="">全部工具</option>
            {(summary.data?.by_tool ?? []).map((b) => (
              <option key={b.key || '(未记录工具)'} value={b.key}>
                {b.key || '(未记录工具)'}
              </option>
            ))}
          </select>
          <select
            className="input usage-sel w3"
            value={keyFilter}
            onChange={(e) => {
              setKeyFilter(e.target.value)
              setOffset(0)
            }}
          >
            <option value="">全部上游密钥</option>
            {keyOptions.map((k) => (
              <option key={k.hint} value={k.hint}>
                {k.label}
              </option>
            ))}
          </select>
          <RangePicker
            value={range}
            granularity="day"
            onChange={(v) => {
              setRange(v)
              setOffset(0)
            }}
          />
        </div>

        {logs.isLoading ? (
          <Loading />
        ) : logs.isError ? (
          <ErrorText text={(logs.error as Error).message} />
        ) : items.length === 0 ? (
          <Empty text="所选范围内没有调用记录" />
        ) : (
          <>
            <div className="tbl-wrap">
              <table className="tbl" style={{ minWidth: '46rem' }}>
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>工具</th>
                    <th>服务器</th>
                    <th>状态</th>
                    <th>计费</th>
                    <th className="r">尝试</th>
                    <th className="r">耗时</th>
                    <th>错误</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((r) => (
                    <tr key={r.id}>
                      <td className="tiny faint num" style={{ whiteSpace: 'nowrap' }}>
                        {bjFmtDateTime(r.created_at)}
                      </td>
                      <td className="mono tiny">{r.tool || '—'}</td>
                      <td className="tiny">
                        {r.server_name || '—'}
                        {r.key_hint ? (
                          <span className="mono faint" style={{ display: 'block', fontSize: 10 }}>
                            {r.key_hint}
                          </span>
                        ) : null}
                      </td>
                      <td>
                        <Badge tone={r.success ? 'ok' : 'warn'}>
                          {r.http_status === 0 ? '网络' : r.http_status}
                        </Badge>
                      </td>
                      <td className="tiny">
                        {r.billable ? <span style={{ color: 'var(--brand)' }}>计入</span> : '不计入'}
                      </td>
                      <td className="r num tiny">{r.attempts}</td>
                      <td className="r num tiny faint">{r.duration_ms} ms</td>
                      <td className="tiny truncate" style={{ maxWidth: 240, color: 'var(--danger)' }} title={r.error}>
                        {r.error || '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="usage-page">
              <span>
                第 {page} / {totalPages} 页 · 共 {total.toLocaleString('en-US')} 条
              </span>
              <div style={{ display: 'flex', gap: 6 }}>
                <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>
                  上一页
                </Button>
                <Button size="sm" disabled={offset + PAGE_SIZE >= total} onClick={() => setOffset(offset + PAGE_SIZE)}>
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function dayRange(now: number): RangeValue {
  const from = bjDayStart(now)
  return { from, to: from + DAY }
}

/* 客户端接入 */
function ClientAccessPanel() {
  const origin = window.location.origin
  const snippet = `{
  "mcpServers": {
    "mcp-hub": {
      "type": "streamableHttp",
      "url": "${origin}/mcp",
      "headers": {
        "Authorization": "Bearer sk-你的密钥"
      }
    }
  }
}`

  return (
    <div className="panel">
      <div className="panel-h">
        <h2>客户端接入</h2>
        <div className="r">
          <Button
            size="sm"
            onClick={async () => {
              if (await writeClipboard(snippet)) toast('ok', '已复制接入配置')
              else toast('err', '复制失败，请手动选择复制')
            }}
          >
            一键复制
          </Button>
        </div>
      </div>
      <div className="panel-b" style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
        <div className="small muted" style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
          <p style={{ margin: 0 }}>
            聚合端点：<code style={{ wordBreak: 'break-all' }}>{`${origin}/mcp`}</code>
          </p>
          <p style={{ margin: 0 }}>
            单服务器直通：<code style={{ wordBreak: 'break-all' }}>{`${origin}/mcp/<工具命名空间>`}</code>
            （客户端可看到原始工具名）
          </p>
        </div>
        <pre
          className="mono"
          style={{
            margin: 0,
            overflowX: 'auto',
            border: '1px solid var(--border)',
            background: 'var(--surface-2)',
            borderRadius: 8,
            padding: 10,
            fontSize: 11,
            lineHeight: 1.7,
            color: 'var(--text-2)',
          }}
        >
          {snippet}
        </pre>
      </div>
    </div>
  )
}

async function writeClipboard(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 继续尝试回退方案
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.cssText = 'position:fixed;top:-1000px;opacity:0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}

function McpHead({ s, onToggle }: { s: MCPServer; onToggle: () => void }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8 }}>
      <div style={{ minWidth: 0 }}>
        <div className="ch-name-cell">
          <span className="ch-name" style={{ fontSize: 13, fontWeight: 600 }}>
            {s.name}
          </span>
          <Badge tone={statusTone[s.last_status] ?? 'off'}>
            {statusText[s.last_status] ?? s.last_status}
          </Badge>
          {s.source?.startsWith('local:') ? <Badge tone="info">本地</Badge> : null}
        </div>
        <div
          className="mono truncate"
          style={{ marginTop: 5, fontSize: 10.5, color: 'var(--text-3)' }}
          title={s.url}
        >
          {s.alias || '—'}
        </div>
      </div>
      <Switch
        checked={s.enabled}
        onChange={onToggle}
        title={s.enabled ? '已启用，点击停用' : '已停用，点击启用'}
      />
    </div>
  )
}

function ToolsModal({
  server,
  onClose,
  onCall,
}: {
  server: MCPServer
  onClose: () => void
  onCall: (tool: string) => void
}) {
  const tools = useQuery({
    queryKey: ['mcp-tools', server.id],
    queryFn: () => api.mcpTools(server.id),
  })

  return (
    <Modal open wide title={`${server.name} 的工具`} onClose={onClose}>
      {tools.isLoading ? (
        <Loading />
      ) : tools.isError ? (
        <ErrorText text={(tools.error as Error).message} />
      ) : (tools.data ?? []).length === 0 ? (
        <Empty text="还没有工具缓存，点「重新拉取工具列表」试试" />
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {(tools.data ?? []).map((t: MCPTool) => (
            <div
              key={t.exposed_name}
              style={{ border: '1px solid var(--border)', borderRadius: 8, padding: 8 }}
            >
              <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8 }}>
                <div style={{ minWidth: 0 }}>
                  <code className="mono" style={{ fontSize: 11, fontWeight: 500, wordBreak: 'break-all' }}>
                    {t.exposed_name}
                  </code>
                  {t.tool.description ? (
                    <p className="tiny faint" style={{ margin: '4px 0 0' }}>
                      {t.tool.description}
                    </p>
                  ) : null}
                </div>
                <Button size="sm" onClick={() => onCall(t.exposed_name)}>
                  试调用
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </Modal>
  )
}

function CallModal({ tool, onClose }: { tool: string; onClose: () => void }) {
  const [args, setArgs] = useState('{}')
  const [result, setResult] = useState('')
  const call = useMutation({
    mutationFn: () => api.callMCPTool(tool, JSON.parse(args || '{}')),
    onSuccess: (r) => setResult(JSON.stringify(r, null, 2)),
    onError: (err: Error) => setResult(`调用失败：${err.message}`),
  })

  return (
    <Modal
      open
      wide
      title={`调用 ${tool}`}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>关闭</Button>
          <Button
            variant="primary"
            onClick={() => {
              try {
                JSON.parse(args || '{}')
              } catch {
                setResult('参数不是合法 JSON')
                return
              }
              call.mutate()
            }}
            disabled={call.isPending}
          >
            执行
          </Button>
        </>
      }
    >
      <div className="stack">
        <Field label="参数（JSON 对象）">
          <Textarea rows={6} value={args} onChange={(e) => setArgs(e.target.value)} />
        </Field>
        {result ? (
          <Field label="结果">
            <Textarea rows={12} readOnly value={result} />
          </Field>
        ) : null}
      </div>
    </Modal>
  )
}
