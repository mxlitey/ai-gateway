import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  api,
  type MCPServer,
  type MCPServerInput,
  type MCPTool,
} from '../lib/api'
import { toast } from '../lib/toast'
import RangePicker, { type RangeValue } from '../components/RangePicker'
import {
  Badge,
  Button,
  Card,
  CrossIcon,
  Empty,
  ErrorText,
  Field,
  IconButton,
  Input,
  Loading,
  Modal,
  RefreshIcon,
  Segmented,
  Switch,
  Textarea,
} from '../components/ui'

type KeyRow = { key: string; label: string; enabled: boolean; daily_limit: string }
type HeaderRow = { name: string; value: string }
// 工具级日上限的一行：工具名固定（来自缓存），只编辑上限
type ToolLimitRow = { tool: string; limit: string }

type FormState = {
  id: string | null
  name: string
  alias: string
  url: string
  headers: HeaderRow[]
  keys: KeyRow[]
  keyStrategy: string
  // 整台服务器日上限不再在界面上提供，仅原样携带编辑前的值，避免保存时被清掉
  dailyLimit: string
  toolLimits: ToolLimitRow[]
  // 计费口径：success 成功即计费；marker 仅响应含标记时计费
  billableMode: string
  billableMarker: string
  timeoutMS: string
  enabled: boolean
}

const KEY_STRATEGIES = [
  { value: 'rotate', label: '轮询', desc: '按顺序轮换首选密钥（K1→K2→K3…），把调用均衡分摊到各密钥' },
  { value: 'ordered', label: '故障转移', desc: '按配置顺序依次尝试，前一个失败才用下一个' },
] as const

const BILLABLE_MODES = [
  {
    value: 'success',
    label: '成功即计费',
    desc: '每次成功的工具调用都计入次数，适用于大多数 MCP 服务',
  },
  {
    value: 'marker',
    label: '按响应标记计费',
    desc: '仅当上游响应包含标记时才计入次数，用于「未触发上游计费就不该占配额」的服务',
  },
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

// 运行时状态到徽标色调/文案的映射
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

/** 与后端 model.KeyHint 保持一致：短密钥原样返回，长密钥显示 abcd123...wxyz。 */
function maskKey(key: string): string {
  if (key.length <= 12) return key
  return `${key.slice(0, 7)}...${key.slice(-4)}`
}

/** 日上限输入框语义：留空或 -1 表示不限，0 表示不允许调用，正数为每日上限。 */
function limitValue(input: string): number {
  const n = Number(input.trim() === '' ? -1 : input)
  return Number.isFinite(n) ? n : -1
}

export default function Mcp() {
  const queryClient = useQueryClient()
  const servers = useQuery({ queryKey: ['mcp-servers'], queryFn: api.mcpServers })
  const [form, setForm] = useState<FormState | null>(null)
  // 工具抽屉：展示某台服务器的工具列表
  const [toolsOf, setToolsOf] = useState<MCPServer | null>(null)
  // 试调用弹层
  const [calling, setCalling] = useState<{ tool: string } | null>(null)
  // 翻转卡片：记录当前翻到背面的服务器 id
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
        // 工具上限：留空不提交（= 不单独限量但仍校验密钥次数），-1 原样提交（= 完全不限）
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

  if (servers.isLoading) return <Loading />
  if (servers.isError) return <ErrorText text={(servers.error as Error).message} />

  const list = servers.data ?? []

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">MCP 服务器</h1>
        <Button variant="primary" onClick={() => setForm({ ...emptyForm })}>
          添加服务器
        </Button>
      </div>

      <Card>
        {list.length === 0 ? (
          <Empty
            text="还没有 MCP 上游服务器"
            action={
              <Button variant="primary" onClick={() => setForm({ ...emptyForm })}>
                添加服务器
              </Button>
            }
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 sm:gap-4 xl:grid-cols-3">
            {list.map((s) => {
              const enabledKeys = s.keys.filter((k) => k.enabled).length
              // 次数口径与饼图一致：只统计设了日上限的启用密钥
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
                  onClick={() => setFlippedId(flipped ? null : s.id)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setFlippedId(flipped ? null : s.id)
                    }
                  }}
                  className={`flip-card h-40 max-sm:h-44 cursor-pointer ${flipped ? 'flipped' : ''}`}
                >
                  <div className="flip-card-inner">
                    {/* 正面：概览 */}
                    <div className="flip-face flex flex-col justify-between gap-2 rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
                      <McpCardHead s={s} onToggle={() => toggle.mutate(s.id)} />
                      <div className="flex items-end justify-between gap-3">
                        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1 text-xs text-zinc-500 dark:text-zinc-400">
                          <span>
                            工具{' '}
                            <span className="font-mono text-zinc-700 dark:text-zinc-200">
                              {s.tool_count}
                            </span>
                          </span>
                          <span>
                            密钥{' '}
                            <span className="font-mono text-zinc-700 dark:text-zinc-200">
                              {enabledKeys} / {s.keys.length}
                            </span>
                          </span>
                          <span title="与上游控制台的计费次数对齐：默认按成功调用计，也可配置为响应含标记才计">
                            计费次数{' '}
                            <span className="font-mono text-zinc-700 dark:text-zinc-200">
                              {limitedUsed} / {s.daily_limit_total > 0 ? s.daily_limit_total : '不限'}
                            </span>
                          </span>
                        </div>
                        <KeyPie s={s} />
                      </div>
                    </div>

                    {/* 背面：头部与正面一致，只把概览换成操作按钮 */}
                    <div className="flip-face flip-face-back flex flex-col justify-between rounded-xl border border-zinc-200 bg-zinc-50 p-4 dark:border-zinc-800 dark:bg-zinc-950">
                      <McpCardHead s={s} onToggle={() => toggle.mutate(s.id)} />
                      <div className="flex flex-wrap items-center gap-1.5">
                        <IconButton
                          title="重新拉取工具列表"
                          onClick={(e) => {
                            e.stopPropagation()
                            refresh.mutate(s.id)
                          }}
                          disabled={refresh.isPending}
                        >
                          <RefreshIcon />
                        </IconButton>
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
                          className="text-red-600 dark:text-red-400"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            if (confirm(`确定删除「${s.name}」？其工具将不再对外暴露。`)) {
                              remove.mutate(s.id)
                            }
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
      </Card>

      <McpUsageCard />

      <ClientAccessCard />

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
          <div className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-2">
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
              <span className="mt-1 block text-xs text-zinc-500 dark:text-zinc-400">
                {KEY_STRATEGIES.find((s) => s.value === form.keyStrategy)?.desc}
              </span>
            </Field>

            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium text-zinc-700 dark:text-zinc-300">密钥</span>
                <Button
                  size="sm"
                  onClick={() =>
                    setForm({
                      ...form,
                      keys: [
                        ...form.keys,
                        { key: '', label: '', enabled: true, daily_limit: '' },
                      ],
                    })
                  }
                >
                  添加密钥
                </Button>
              </div>
              {form.keys.map((k, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Switch
                    checked={k.enabled}
                    title={k.enabled ? '已启用，点击停用' : '已停用，点击启用'}
                    onChange={(v) => {
                      const keys = [...form.keys]
                      keys[i] = { ...k, enabled: v }
                      setForm({ ...form, keys })
                    }}
                    className="flex-none"
                  />
                  <Input
                    className="min-w-0 flex-1 basis-40"
                    value={k.key}
                    onChange={(e) => {
                      const keys = [...form.keys]
                      keys[i] = { ...k, key: e.target.value }
                      setForm({ ...form, keys })
                    }}
                    placeholder="上游 Token（可留空表示无需鉴权）"
                  />
                  <Input
                    className="flex-none"
                    style={{ width: '5.5rem' }}
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
                  <IconButton
                    className="flex-none"
                    title="移除密钥"
                    onClick={() =>
                      setForm({ ...form, keys: form.keys.filter((_, idx) => idx !== i) })
                    }
                  >
                    <CrossIcon />
                  </IconButton>
                </div>
              ))}
              <p className="text-xs text-zinc-500 dark:text-zinc-400">
                日上限按「计费调用」计数（默认成功即计费，可改按响应标记），每天 0 点重置；留空或 -1
                表示不限，0 表示不允许调用。
              </p>
            </div>

            <Field
              label="计费口径"
              hint="决定哪些调用计入「次数」与日上限；选「按响应标记计费」后需填写标记串"
            >
              <Segmented
                value={form.billableMode}
                options={BILLABLE_MODES.map((m) => ({ value: m.value, label: m.label }))}
                onChange={(v) => setForm({ ...form, billableMode: v })}
              />
              <span className="mt-1 block text-xs text-zinc-500 dark:text-zinc-400">
                {BILLABLE_MODES.find((m) => m.value === form.billableMode)?.desc}
              </span>
            </Field>

            {form.billableMode === 'marker' ? (
              <Field
                label="计费标记"
                hint="上游响应正文包含该字符串时才算一次；留空会退化为「成功即计费」"
              >
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
              <div className="space-y-2">
                {form.headers.map((h, i) => (
                  <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
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
                    <IconButton
                      title="移除请求头"
                      onClick={() =>
                        setForm({
                          ...form,
                          headers: form.headers.filter((_, idx) => idx !== i),
                        })
                      }
                    >
                      <CrossIcon />
                    </IconButton>
                  </div>
                ))}
                <Button
                  size="sm"
                  onClick={() => setForm({ ...form, headers: [...form.headers, { name: '', value: '' }] })}
                >
                  添加请求头
                </Button>
              </div>
            </Field>
          </div>
        </Modal>
      ) : null}

      {toolsOf ? (
        <ToolsModal
          server={toolsOf}
          onClose={() => setToolsOf(null)}
          onCall={(tool) => setCalling({ tool })}
        />
      ) : null}

      {calling ? <CallModal tool={calling.tool} onClose={() => setCalling(null)} /> : null}
    </div>
  )
}

// ToolLimitsField 工具级日上限：按上游工具名逐个配置，留空表示该工具不限。
//
// 工具清单来自该服务器的工具缓存（编辑时拉取）；新建服务器还没缓存工具，
// 保存并刷新工具列表后再回来配置即可。
function ToolLimitsField({
  form,
  setForm,
}: {
  form: FormState
  setForm: (f: FormState) => void
}) {
  const tools = useQuery({
    queryKey: ['mcp-tools', form.id],
    queryFn: () => api.mcpTools(form.id as string),
    enabled: !!form.id,
  })
  const cached = (tools.data ?? []).map((t) => t.tool.name ?? t.exposed_name)

  // 缓存里的工具 + 已配置但不在缓存里的工具，合并成一份可编辑清单
  const names = Array.from(new Set([...cached, ...form.toolLimits.map((t) => t.tool)])).filter(
    (n) => n !== '',
  )
  const rows = names.map((name) => {
    const hit = form.toolLimits.find((t) => t.tool === name)
    // 未设置时默认 0：该工具不单独限量，共享上游密钥的剩余次数
    return { tool: name, limit: hit?.limit ?? '0' }
  })

  const update = (tool: string, limit: string) => {
    const rest = form.toolLimits.filter((t) => t.tool !== tool)
    setForm({ ...form, toolLimits: [...rest, { tool, limit }] })
  }

  if (!form.id) {
    return (
      <Field label="工具日上限" hint="保存后会拉取工具列表，届时可在编辑里为每个工具单独设上限">
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          新建服务器时还没有工具清单，先保存即可。
        </p>
      </Field>
    )
  }

  return (
    <Field
      label="工具日上限"
      hint="按上游工具名匹配，上限按每条上游密钥独立计算。不填时默认 0（显示为「共享」）：该工具不单独限量，共享上游密钥的剩余次数，用完即不能调用；-1（显示为「不限」）：完全不限，也不校验密钥次数；正数：该工具每条密钥的每日上限。"
    >
      {tools.isLoading ? (
        <Loading />
      ) : rows.length === 0 ? (
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          还没有工具缓存，先点卡片上的「重新拉取工具列表」。
        </p>
      ) : (
        <div className="space-y-2">
          {rows.map((row) => (
            <div key={row.tool} className="flex items-center gap-2">
              <span
                className="min-w-0 flex-1 basis-0 truncate font-mono text-xs text-zinc-600 dark:text-zinc-400"
                title={row.tool}
              >
                {row.tool}
              </span>
              <Input
                className="flex-none"
                style={{ width: '5.5rem' }}
                // 用 text 而非 number：number 输入框会把中间的「-」丢掉，导致 -1 无法键入
                type="text"
                inputMode="numeric"
                // 0 与 -1 不显示数字，只显示灰色说明文字：共享 / 不限
                value={row.limit === '' || row.limit === '0' || row.limit === '-1' ? '' : row.limit}
                onChange={(e) => update(row.tool, e.target.value)}
                placeholder={row.limit === '-1' ? '不限' : '共享'}
                title="不填或 0：不单独限量，共享上游密钥的剩余次数（用完即不能调用）；-1：完全不限，不校验密钥次数；正数：该工具每条密钥的每日上限"
              />
            </div>
          ))}
        </div>
      )}
    </Field>
  )
}

// KeyPie 服务器翻转卡片内的配额饼图，悬停或点击时展示各密钥的调用明细。
//
// 整个圆代表「总可调用次数」= 各启用密钥日上限之和；每把密钥按已用次数占一圈扇区。
// 未设日上限的密钥没有配额可参与分摊，不计入饼图与次数统计。
//
// 点击只切换明细浮层、不翻转卡片：触屏端没有 hover，靠点击查看调用次数，
// 因此必须阻断事件冒泡，避免触发卡片的翻转。
function KeyPie({ s }: { s: MCPServer }) {
  // hover 只响应鼠标；pinned 由点击切换，供触屏端使用
  const [hover, setHover] = useState(false)
  const [pinned, setPinned] = useState(false)
  const boxRef = useRef<HTMLDivElement>(null)
  const open = hover || pinned

  // 浮层展开时点击空白处收起，避免触屏端点开后无处关闭
  useEffect(() => {
    if (!pinned) return
    const onDown = (e: PointerEvent) => {
      if (!boxRef.current?.contains(e.target as Node)) setPinned(false)
    }
    document.addEventListener('pointerdown', onDown)
    return () => document.removeEventListener('pointerdown', onDown)
  }, [pinned])

  const slices = s.keys
    .filter((k) => k.enabled && k.daily_limit > 0)
    .map((k, i) => ({
      label: k.label || maskKey(k.key),
      limit: k.daily_limit,
      used: s.key_daily_calls?.[maskKey(k.key)] ?? 0,
      color: PIE_COLORS[i % PIE_COLORS.length],
    }))
  const totalLimit = slices.reduce((n, x) => n + x.limit, 0)
  const totalUsed = slices.reduce((n, x) => n + x.used, 0)

  if (totalLimit === 0) {
    return (
      <span
        className="flex size-16 flex-none items-center text-center text-[11px] text-zinc-400"
        onClick={(e) => e.stopPropagation()}
      >
        未设日上限
      </span>
    )
  }

  return (
    <div
      ref={boxRef}
      className="relative flex-none cursor-pointer"
      /* 只让鼠标触发悬停；触屏的 pointerenter 不置位，否则会和点击态互相干扰 */
      onPointerEnter={(e) => {
        if (e.pointerType === 'mouse') setHover(true)
      }}
      onPointerLeave={(e) => {
        if (e.pointerType === 'mouse') setHover(false)
      }}
      onClick={(e) => {
        e.stopPropagation()
        setPinned((v) => !v)
      }}
    >
      <svg viewBox="0 0 42 42" className="size-16 -rotate-90">
        <circle
          cx="21"
          cy="21"
          r="15.9"
          fill="none"
          strokeWidth="6"
          className="stroke-zinc-200 dark:stroke-zinc-800"
        />
        {(() => {
          let offset = 0
          return slices.map((slice) => {
            // 扇区长度按「已用 / 总配额」画；剩余圆环即尚未使用的配额
            const pct = Math.min(100, (slice.used / totalLimit) * 100)
            const el = (
              <circle
                key={slice.label}
                cx="21"
                cy="21"
                r="15.9"
                fill="none"
                strokeWidth="6"
                stroke={slice.color}
                strokeDasharray={`${pct} ${100 - pct}`}
                strokeDashoffset={-offset}
              />
            )
            offset += pct
            return el
          })
        })()}
      </svg>

      {open ? (
        <div className="absolute right-full bottom-0 z-10 mr-2 w-max rounded-lg border border-zinc-200 bg-white px-2.5 py-2 text-[11px] leading-relaxed shadow-lg dark:border-zinc-700 dark:bg-zinc-800">
          <p className="text-zinc-500 dark:text-zinc-400">
            今日计费调用{' '}
            <span className="font-mono text-zinc-700 dark:text-zinc-200">
              {totalUsed} / {totalLimit}
            </span>
          </p>
          <ul className="mt-1 space-y-0.5">
            {slices.map((x) => (
              <li key={x.label} className="flex items-center gap-1.5">
                <span className="size-2 flex-none rounded-sm" style={{ background: x.color }} />
                <span className="max-w-40 truncate text-zinc-600 dark:text-zinc-300" title={x.label}>
                  {x.label}
                </span>
                <span className="ml-auto pl-2 font-mono text-zinc-700 dark:text-zinc-200">
                  {x.used}/{x.limit}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}

const PIE_COLORS = ['#007aff', '#34c759', '#ff9500', '#af52de', '#ff3b30', '#5ac8fa', '#ffcc00']

const PAGE_SIZE = 50
const DAY = 24 * 60 * 60 * 1000

// 筛选下拉框样式（与「用量」页保持一致）
const selectClass =
  'rounded-lg border border-zinc-300 bg-white px-2 py-1.5 text-sm text-zinc-900 outline-none focus:border-emerald-500 max-md:py-2.5 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100'

// McpUsageCard 用量明细：与「用量」页的调用明细一致，顶部按服务器（对应渠道）、
// 工具（对应模型）、上游密钥与日期范围筛选，日期只按天选。
function McpUsageCard() {
  const [range, setRange] = useState<RangeValue>(() => dayRange(Date.now()))
  const [serverFilter, setServerFilter] = useState('')
  const [toolFilter, setToolFilter] = useState('')
  const [keyFilter, setKeyFilter] = useState('')
  const [offset, setOffset] = useState(0)

  // 服务器 / 工具 / 密钥下拉都只列本期有调用记录的值（与「用量」页一致），没有用量的不出现
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

  // 密钥下拉按服务器过滤（后端已按服务器名筛选 by_key），同一 hint 只保留一项
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

  const refresh = () => {
    summary.refetch()
    logs.refetch()
  }

  return (
    <Card title="用量明细">
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <select
          value={serverFilter}
          onChange={(e) => {
            setServerFilter(e.target.value)
            // 工具 / 密钥下拉按服务器过滤，换服务器后原先选中的值可能已不在列表中，一并清空
            setToolFilter('')
            setKeyFilter('')
            setOffset(0)
          }}
          className={`${selectClass} w-full sm:w-40`}
        >
          <option value="">全部服务器</option>
          {serverOptions.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
        <select
          value={toolFilter}
          onChange={(e) => {
            setToolFilter(e.target.value)
            // 换工具后原先选中的密钥可能已不在列表中，一并清空
            setKeyFilter('')
            setOffset(0)
          }}
          className={`${selectClass} w-full sm:w-48`}
        >
          <option value="">全部工具</option>
          {(summary.data?.by_tool ?? []).map((b) => (
            <option key={b.key || '(未记录工具)'} value={b.key}>
              {b.key || '(未记录工具)'}
            </option>
          ))}
        </select>
        <select
          value={keyFilter}
          onChange={(e) => {
            setKeyFilter(e.target.value)
            setOffset(0)
          }}
          className={`${selectClass} w-full sm:w-44`}
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
        <IconButton title="刷新" onClick={refresh}>
          <RefreshIcon />
        </IconButton>
      </div>

      {logs.isLoading ? (
        <Loading />
      ) : logs.isError ? (
        <ErrorText text={(logs.error as Error).message} />
      ) : items.length === 0 ? (
        <Empty text="所选范围内没有调用记录" />
      ) : (
        <>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[46rem] text-sm">
              <thead>
                <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                  <th className="py-2 pr-3 font-medium">时间</th>
                  <th className="py-2 pr-3 font-medium">工具</th>
                  <th className="py-2 pr-3 font-medium">服务器</th>
                  <th className="py-2 pr-3 font-medium">状态</th>
                  <th className="py-2 pr-3 font-medium">计费</th>
                  <th className="py-2 pr-3 text-right font-medium">尝试</th>
                  <th className="py-2 pr-3 text-right font-medium">耗时</th>
                  <th className="py-2 font-medium">错误</th>
                </tr>
              </thead>
              <tbody>
                {items.map((r) => (
                  <tr key={r.id} className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
                    <td className="py-2 pr-3 text-xs whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                      {new Date(r.created_at).toLocaleString()}
                    </td>
                    <td className="py-2 pr-3 font-mono text-xs text-zinc-700 dark:text-zinc-300">
                      {r.tool || '—'}
                    </td>
                    <td className="py-2 pr-3 text-xs text-zinc-600 dark:text-zinc-400">
                      {r.server_name || '—'}
                      {r.key_hint ? (
                        <span className="mt-0.5 block font-mono text-[11px] text-zinc-400 dark:text-zinc-500">
                          {r.key_hint}
                        </span>
                      ) : null}
                    </td>
                    <td className="py-2 pr-3">
                      <Badge tone={r.success ? 'ok' : 'warn'}>
                        {r.http_status === 0 ? '网络' : r.http_status}
                      </Badge>
                    </td>
                    <td className="py-2 pr-3 text-xs text-zinc-500">
                      {r.billable ? (
                        <span className="text-emerald-600 dark:text-emerald-400">计入</span>
                      ) : (
                        '不计入'
                      )}
                    </td>
                    <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-600 dark:text-zinc-400">
                      {r.attempts}
                    </td>
                    <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-500">
                      {r.duration_ms} ms
                    </td>
                    <td
                      className="max-w-[16rem] truncate py-2 text-xs text-red-600 dark:text-red-400"
                      title={r.error}
                    >
                      {r.error || '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-zinc-500">
            <span>
              第 {page} / {totalPages} 页 · 共 {total.toLocaleString('en-US')} 条
            </span>
            <div className="flex gap-2">
              <Button
                className="py-1 text-xs"
                disabled={offset === 0}
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              >
                上一页
              </Button>
              <Button
                className="py-1 text-xs"
                disabled={offset + PAGE_SIZE >= total}
                onClick={() => setOffset(offset + PAGE_SIZE)}
              >
                下一页
              </Button>
            </div>
          </div>
        </>
      )}
    </Card>
  )
}

// dayRange 当天的自然日范围（[今天 00:00, 明天 00:00)）
function dayRange(now: number): RangeValue {
  const d = new Date(now)
  d.setHours(0, 0, 0, 0)
  return { from: d.getTime(), to: d.getTime() + DAY }
}

// ClientAccessCard 客户端接入说明，并提供开箱可用的 JSON 配置一键复制。
function ClientAccessCard() {
  const origin = window.location.origin
  // 各主流客户端（Claude Desktop / Cursor / Cline 等）通用的 mcpServers 片段；
  // 密钥留占位符，由用户替换为面板「API 密钥」页生成的 sk- 密钥。
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
    <Card
      title="客户端接入"
      action={
        <Button
          size="sm"
          onClick={async () => {
            if (await writeClipboard(snippet)) toast('ok', '已复制接入配置')
            else toast('err', '复制失败，请手动选择复制')
          }}
        >
          一键复制
        </Button>
      }
    >
      <div className="space-y-3">
        <div className="space-y-1 text-sm text-zinc-600 dark:text-zinc-300">
          <p>
            聚合端点：<code className="break-all">{`${origin}/mcp`}</code>
          </p>
          <p>
            单服务器直通：<code className="break-all">{`${origin}/mcp/<工具命名空间>`}</code>
            （客户端可看到原始工具名）
          </p>
        </div>
        <pre className="overflow-x-auto rounded-lg border border-zinc-200 bg-zinc-50 p-3 font-mono text-xs leading-relaxed text-zinc-700 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-300">
          {snippet}
        </pre>
        <p className="text-xs text-zinc-500 dark:text-zinc-400">
          使用「API 密钥」页生成的 <code>sk-</code> 密钥，通过
          <code> Authorization: Bearer</code> 或 <code>x-api-key</code> 传递。聚合模式下工具名带
          <code> 命名空间__</code> 前缀，避免多台服务器重名。
        </p>
      </div>
    </Card>
  )
}

// writeClipboard 优先用剪贴板 API，失败（如局域网 IP 经 http 访问时非安全上下文）再回退 execCommand。
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

// McpCardHead 翻转卡片正反两面共用的头部：服务器名 / 工具命名空间 / 启用开关。
function McpCardHead({ s, onToggle }: { s: MCPServer; onToggle: () => void }) {
  return (
    <div className="flex items-start justify-between gap-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-semibold text-zinc-800 dark:text-zinc-100">
            {s.name}
          </span>
          <Badge tone={statusTone[s.last_status] ?? 'off'}>
            {statusText[s.last_status] ?? s.last_status}
          </Badge>
          {s.source?.startsWith('local:') ? <Badge tone="info">本地</Badge> : null}
        </div>
        <p
          className="mt-1 truncate font-mono text-xs break-all text-zinc-500 dark:text-zinc-400"
          title={s.url}
        >
          {s.alias || '—'}
        </p>
      </div>
      <Switch
        checked={s.enabled}
        onChange={onToggle}
        title={s.enabled ? '已启用，点击停用' : '已停用，点击启用'}
        className="mt-1"
      />
    </div>
  )
}

// ToolsModal 展示某台服务器缓存的工具列表，并可直接试调用。
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
        <div className="space-y-2">
          {(tools.data ?? []).map((t: MCPTool) => (
            <div
              key={t.exposed_name}
              className="rounded-lg border border-zinc-200 p-2 dark:border-zinc-800"
            >
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="min-w-0">
                  <code className="break-all text-xs font-medium text-zinc-800 dark:text-zinc-100">
                    {t.exposed_name}
                  </code>
                  {t.tool.description ? (
                    <p className="mt-1 text-xs text-zinc-500 dark:text-zinc-400">
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

// CallModal 输入参数并执行一次真实调用。
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
      <div className="space-y-3">
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
