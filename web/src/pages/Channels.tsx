import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Channel, type ChannelInput } from '../lib/api'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  Card,
  Empty,
  ErrorText,
  Field,
  Input,
  Loading,
  Modal,
  Segmented,
  Textarea,
} from '../components/ui'

type KeyRow = { key: string; enabled: boolean; label?: string }
type MapRow = { pub: string; upstream: string }
type HeaderRow = { name: string; value: string }

type FormState = {
  id: string | null
  name: string
  base_url: string
  path: string
  keys: KeyRow[]
  modelsText: string
  modelMap: MapRow[]
  headers: HeaderRow[]
  keyStrategy: string
  enabled: boolean
  // 插件渠道：密钥由插件账号同步而来，面板只允许启停与排序，不允许增删改
  isPlugin: boolean
}

// 多密钥选用策略，与后端 model.KeyStrategy* 常量对应
const KEY_STRATEGIES = [
  { value: 'rotate', label: '轮询', desc: '按渠道依次轮换首选密钥（K1→K2→K3…），把流量均衡分摊到各密钥' },
  { value: 'ordered', label: '故障转移', desc: '按配置顺序依次尝试，前一个失败才用下一个' },
] as const

const emptyForm: FormState = {
  id: null,
  name: '',
  base_url: '',
  path: '',
  keys: [{ key: '', enabled: true }],
  modelsText: '',
  modelMap: [],
  headers: [],
  keyStrategy: 'rotate',
  enabled: true,
  isPlugin: false,
}

export default function Channels() {
  const queryClient = useQueryClient()
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })

  const [form, setForm] = useState<FormState | null>(null)
  // 翻转卡片：记录当前翻到背面的渠道 id
  const [flippedId, setFlippedId] = useState<string | null>(null)
  const [fetched, setFetched] = useState<{ models: string[]; picked: Record<string, boolean> } | null>(
    null,
  )

  const invalidateChannels = () =>
    queryClient.invalidateQueries({ queryKey: ['channels'] }).then(() =>
      queryClient.invalidateQueries({ queryKey: ['routes'] }),
    )

  const save = useMutation({
    mutationFn: async (state: FormState) => {
      const body: ChannelInput = {
        name: state.name.trim(),
        base_url: state.base_url.trim(),
        path: state.path.trim(),
        keys: state.keys
          .map((k) => ({ key: k.key.trim(), enabled: k.enabled, label: k.label }))
          .filter((k) => k.key !== ''),
        models: splitLines(state.modelsText),
        model_map: Object.fromEntries(
          state.modelMap
            .map((r) => [r.pub.trim(), r.upstream.trim()] as const)
            .filter(([pub]) => pub !== ''),
        ),
        headers: state.headers
          .map((h) => ({ name: h.name.trim(), value: h.value }))
          .filter((h) => h.name !== ''),
        key_strategy: state.keyStrategy,
        enabled: state.enabled,
      }
      return state.id ? api.updateChannel(state.id, body) : api.createChannel(body)
    },
    onSuccess: async () => {
      toast('ok', '渠道已保存')
      setFetched(null)
      setForm(null)
      await invalidateChannels()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const toggle = useMutation({
    mutationFn: (id: string) => api.toggleChannel(id),
    onSuccess: invalidateChannels,
    onError: (err: Error) => toast('err', err.message),
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteChannel(id),
    onSuccess: async () => {
      toast('ok', '渠道已删除')
      await invalidateChannels()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const fetchModels = useMutation({
    mutationFn: async (state: FormState) => {
      const res = await api.fetchModels({
        // 带 channel_id 让后端用渠道里保存的真实密钥（列表接口返回的是脱敏值）
        channel_id: state.id ?? undefined,
        base_url: state.base_url.trim(),
        keys: state.keys.map((k) => k.key.trim()).filter(Boolean),
      })
      return { models: res.models, existing: splitLines(state.modelsText) }
    },
    onSuccess: ({ models, existing }) => {
      if (models.length === 0) {
        toast('err', '上游没有返回模型')
        return
      }
      const have = new Set(existing)
      const picked: Record<string, boolean> = {}
      for (const m of models) picked[m] = !have.has(m)
      setFetched({ models, picked })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const togglePicked = (model: string) =>
    setFetched((prev) =>
      prev ? { ...prev, picked: { ...prev.picked, [model]: !prev.picked[model] } } : prev,
    )

  const setAllPicked = (on: boolean) =>
    setFetched((prev) => {
      if (!prev) return prev
      const picked: Record<string, boolean> = {}
      for (const m of prev.models) picked[m] = on
      return { ...prev, picked }
    })

  const applyPicked = () => {
    if (!fetched) return
    const chosen = fetched.models.filter((m) => fetched.picked[m])
    if (chosen.length === 0) return
    setForm((prev) => {
      if (!prev) return prev
      const lines = splitLines(prev.modelsText)
      const have = new Set(lines)
      for (const m of chosen) {
        if (!have.has(m)) {
          lines.push(m)
          have.add(m)
        }
      }
      return { ...prev, modelsText: lines.join('\n') }
    })
    setFetched(null)
    toast('ok', `已添加 ${chosen.length} 个模型`)
  }

  const openNew = () => {
    setFetched(null)
    setForm({ ...emptyForm })
  }

  const openEditChannel = (ch: Channel) => {
    setFetched(null)
    openEdit(ch, setForm)
  }

  const closeForm = () => {
    setFetched(null)
    setForm(null)
  }

  if (channels.isLoading) return <Loading />
  if (channels.isError) return <ErrorText text={(channels.error as Error).message} />

  const list = channels.data ?? []

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">渠道管理</h1>
        <Button variant="primary" onClick={openNew}>
          添加渠道
        </Button>
      </div>

      <Card>
        {list.length === 0 ? (
          <Empty
            text="还没有渠道，先添加一个上游服务"
            action={
              <Button variant="primary" onClick={openNew}>
                添加渠道
              </Button>
            }
          />
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {list.map((ch) => {
              const enabledKeys = ch.keys.filter((k) => k.enabled).length
              const modelCount = Object.keys(ch.model_map).length + ch.models.length
              const flipped = flippedId === ch.id
              return (
                <div
                  key={ch.id}
                  role="button"
                  tabIndex={0}
                  title={flipped ? '点击返回' : '点击查看操作'}
                  onClick={() => setFlippedId(flipped ? null : ch.id)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setFlippedId(flipped ? null : ch.id)
                    }
                  }}
                  className={`flip-card h-40 cursor-pointer ${flipped ? 'flipped' : ''}`}
                >
                  <div className="flip-card-inner">
                    {/* 正面：概览 */}
                    <div className="flip-face flex flex-col justify-between rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
                      <ChannelCardHead ch={ch} />
                      <div className="flex items-center gap-5 text-xs text-zinc-500 dark:text-zinc-400">
                        <span>
                          模型{' '}
                          <span className="font-mono text-zinc-700 dark:text-zinc-200">
                            {modelCount}
                          </span>
                        </span>
                        <span>
                          密钥{' '}
                          <span className="font-mono text-zinc-700 dark:text-zinc-200">
                            {enabledKeys} / {ch.keys.length}
                          </span>
                        </span>
                      </div>
                    </div>

                    {/* 背面：头部与正面一致，只把「模型 / 密钥」换成操作按钮 */}
                    <div className="flip-face flip-face-back flex flex-col justify-between rounded-xl border border-zinc-200 bg-zinc-50 p-4 dark:border-zinc-800 dark:bg-zinc-950">
                      <ChannelCardHead ch={ch} />
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Button
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            openEditChannel(ch)
                          }}
                        >
                          编辑
                        </Button>
                        <Button
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            toggle.mutate(ch.id)
                          }}
                        >
                          {ch.enabled ? '停用' : '启用'}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-red-600 dark:text-red-400"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            if (window.confirm(`确认删除渠道「${ch.name}」？`)) remove.mutate(ch.id)
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

      {form ? (
        <Modal
          open
          wide
          title={form.id ? '编辑渠道' : '添加渠道'}
          onClose={closeForm}
          footer={
            <>
              <Button onClick={closeForm}>取消</Button>
              <Button
                variant="primary"
                disabled={save.isPending || !form.name.trim() || !form.base_url.trim()}
                onClick={() => save.mutate(form)}
              >
                {save.isPending ? '保存中…' : '保存'}
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label="名称">
                <Input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="如 NVIDIA NIM"
                />
              </Field>
              <Field label="API 主机">
                <Input
                  value={form.base_url}
                  onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                  placeholder="https://integrate.api.nvidia.com/v1"
                />
              </Field>
              <Field label="API 路径" hint="留空默认 /chat/completions">
                <Input
                  value={form.path}
                  onChange={(e) => setForm({ ...form, path: e.target.value })}
                  placeholder="/chat/completions"
                />
              </Field>
            </div>

            <Field
              label="API 密钥"
              hint={
                form.isPlugin
                  ? '密钥由插件账号同步而来（key 即账号 id）：在这里启停账号、调整顺序即可'
                  : '同一渠道可配多个密钥'
              }
            >
              <div className="space-y-2">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-xs text-zinc-500 dark:text-zinc-400">密钥选用策略</span>
                  <Segmented
                    value={form.keyStrategy}
                    onChange={(v) => setForm({ ...form, keyStrategy: v })}
                    options={KEY_STRATEGIES.map((s) => ({ value: s.value, label: s.label }))}
                  />
                  <span className="text-xs text-zinc-400 dark:text-zinc-500">
                    {KEY_STRATEGIES.find((s) => s.value === form.keyStrategy)?.desc}
                  </span>
                </div>
                {form.keys.map((row, i) => (
                  <div key={i} className="flex items-center gap-2">
                    {form.isPlugin ? (
                      <div className="min-w-0 flex-1 rounded-lg border border-zinc-200 px-3 py-2 dark:border-zinc-700">
                        <span className="text-sm text-zinc-800 dark:text-zinc-100">
                          {row.label || row.key}
                        </span>
                        {row.label ? (
                          <span className="ml-2 font-mono text-xs text-zinc-400">{row.key}</span>
                        ) : null}
                      </div>
                    ) : (
                      <Input
                        value={row.key}
                        onChange={(e) => {
                          const keys = [...form.keys]
                          keys[i] = { ...keys[i], key: e.target.value }
                          setForm({ ...form, keys })
                        }}
                        placeholder="sk-..."
                      />
                    )}
                    <label className="flex shrink-0 items-center gap-1 text-xs text-zinc-600 dark:text-zinc-400">
                      <input
                        type="checkbox"
                        checked={row.enabled}
                        onChange={(e) => {
                          const keys = [...form.keys]
                          keys[i] = { ...keys[i], enabled: e.target.checked }
                          setForm({ ...form, keys })
                        }}
                      />
                      启用
                    </label>
                    {form.isPlugin ? (
                      <>
                        <Button
                          variant="ghost"
                          disabled={i === 0}
                          onClick={() => {
                            const keys = [...form.keys]
                            ;[keys[i - 1], keys[i]] = [keys[i], keys[i - 1]]
                            setForm({ ...form, keys })
                          }}
                        >
                          上移
                        </Button>
                        <Button
                          variant="ghost"
                          disabled={i === form.keys.length - 1}
                          onClick={() => {
                            const keys = [...form.keys]
                            ;[keys[i + 1], keys[i]] = [keys[i], keys[i + 1]]
                            setForm({ ...form, keys })
                          }}
                        >
                          下移
                        </Button>
                      </>
                    ) : null}
                    {form.isPlugin ? null : (
                      <Button
                        variant="ghost"
                        onClick={() =>
                          setForm({ ...form, keys: form.keys.filter((_, idx) => idx !== i) })
                        }
                      >
                        移除
                      </Button>
                    )}
                  </div>
                ))}
                {form.isPlugin ? (
                  <p className="text-xs text-zinc-400 dark:text-zinc-500">
                    账号的增删请在插件「控制台」里操作，网关会自动同步（账号变更后立即同步，运行期间约每 30 秒复查一次）。
                  </p>
                ) : (
                  <Button
                    onClick={() => setForm({ ...form, keys: [...form.keys, { key: '', enabled: true }] })}
                  >
                    添加密钥
                  </Button>
                )}
              </div>
            </Field>

            <Field
              label="模型列表"
              hint="每行一个，同名透传；点「获取上游模型」拉取后勾选要加入的"
            >
              <div className="space-y-2">
                <Textarea
                  rows={5}
                  value={form.modelsText}
                  onChange={(e) => setForm({ ...form, modelsText: e.target.value })}
                  placeholder={'meta/llama-3.1-405b-instruct\nqwen/qwen2.5-72b-instruct'}
                />
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    disabled={fetchModels.isPending || !form.base_url.trim()}
                    onClick={() => fetchModels.mutate(form)}
                  >
                    {fetchModels.isPending ? '拉取中…' : '获取上游模型'}
                  </Button>
                  {fetched ? (
                    <>
                      <Button
                        variant="primary"
                        disabled={!fetched.models.some((m) => fetched.picked[m])}
                        onClick={applyPicked}
                      >
                        添加选中
                      </Button>
                      <Button variant="ghost" onClick={() => setFetched(null)}>
                        取消
                      </Button>
                    </>
                  ) : null}
                </div>
                {fetched ? (
                  <div className="rounded-lg border border-zinc-200 dark:border-zinc-800">
                    <div className="flex items-center justify-between gap-2 border-b border-zinc-200 px-3 py-2 dark:border-zinc-800">
                      <span className="text-xs text-zinc-500 dark:text-zinc-400">
                        上游返回 {fetched.models.length} 个模型，勾选要加入列表的
                      </span>
                      <div className="flex shrink-0 gap-2 text-xs">
                        <button
                          type="button"
                          className="text-emerald-600 hover:underline dark:text-emerald-400"
                          onClick={() => setAllPicked(true)}
                        >
                          全选
                        </button>
                        <button
                          type="button"
                          className="text-zinc-500 hover:underline"
                          onClick={() => setAllPicked(false)}
                        >
                          全不选
                        </button>
                      </div>
                    </div>
                    <div className="max-h-52 space-y-0.5 overflow-y-auto p-2">
                      {fetched.models.map((m) => (
                        <label
                          key={m}
                          className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/60"
                        >
                          <input
                            type="checkbox"
                            checked={!!fetched.picked[m]}
                            onChange={() => togglePicked(m)}
                          />
                          <span className="font-mono text-xs break-all text-zinc-700 dark:text-zinc-300">
                            {m}
                          </span>
                          {splitLines(form.modelsText).includes(m) ? (
                            <Badge tone="off">已在列表</Badge>
                          ) : null}
                        </label>
                      ))}
                    </div>
                  </div>
                ) : null}
              </div>
            </Field>

            <Field label="模型映射" hint="公开模型名 → 上游模型名，优先于模型列表">
              <div className="space-y-2">
                {form.modelMap.map((row, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <Input
                      value={row.pub}
                      onChange={(e) => {
                        const modelMap = [...form.modelMap]
                        modelMap[i] = { ...modelMap[i], pub: e.target.value }
                        setForm({ ...form, modelMap })
                      }}
                      placeholder="公开模型名"
                    />
                    <span className="text-zinc-400">→</span>
                    <Input
                      value={row.upstream}
                      onChange={(e) => {
                        const modelMap = [...form.modelMap]
                        modelMap[i] = { ...modelMap[i], upstream: e.target.value }
                        setForm({ ...form, modelMap })
                      }}
                      placeholder="上游模型名"
                    />
                    <Button
                      variant="ghost"
                      onClick={() =>
                        setForm({ ...form, modelMap: form.modelMap.filter((_, idx) => idx !== i) })
                      }
                    >
                      移除
                    </Button>
                  </div>
                ))}
                <Button
                  onClick={() =>
                    setForm({ ...form, modelMap: [...form.modelMap, { pub: '', upstream: '' }] })
                  }
                >
                  添加映射
                </Button>
              </div>
            </Field>

            <Field
              label="自定义请求头"
              hint="支持 {{请求头名}}、{{a | b}}、{{[常量]}}、{{uuid}}、{{timestamp}}"
            >
              <div className="space-y-2">
                {form.headers.map((row, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <Input
                      value={row.name}
                      onChange={(e) => {
                        const headers = [...form.headers]
                        headers[i] = { ...headers[i], name: e.target.value }
                        setForm({ ...form, headers })
                      }}
                      placeholder="x-session-id"
                    />
                    <Input
                      value={row.value}
                      onChange={(e) => {
                        const headers = [...form.headers]
                        headers[i] = { ...headers[i], value: e.target.value }
                        setForm({ ...form, headers })
                      }}
                      placeholder="{{x-session-id | [gw-session-001]}}"
                    />
                    <Button
                      variant="ghost"
                      onClick={() =>
                        setForm({ ...form, headers: form.headers.filter((_, idx) => idx !== i) })
                      }
                    >
                      移除
                    </Button>
                  </div>
                ))}
                <Button
                  onClick={() =>
                    setForm({ ...form, headers: [...form.headers, { name: '', value: '' }] })
                  }
                >
                  添加请求头
                </Button>
              </div>
            </Field>

            <label className="flex items-center gap-2 text-sm text-zinc-700 dark:text-zinc-300">
              <input
                type="checkbox"
                checked={form.enabled}
                onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
              />
              启用该渠道
            </label>
          </div>
        </Modal>
      ) : null}
    </div>
  )
}

// ChannelCardHead 翻转卡片正反两面共用的头部：名称 / API 地址 / 状态圆点。
function ChannelCardHead({ ch }: { ch: Channel }) {
  return (
    <div className="flex items-start justify-between gap-2">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-semibold text-zinc-800 dark:text-zinc-100">
            {ch.name}
          </span>
          {ch.source.startsWith('plugin:') ? <Badge tone="info">插件</Badge> : null}
        </div>
        <p
          className="mt-1 line-clamp-2 font-mono text-xs break-all text-zinc-500 dark:text-zinc-400"
          title={ch.base_url}
        >
          {ch.base_url}
        </p>
      </div>
      <span
        className={`mt-1.5 size-2 flex-none rounded-full ${
          ch.enabled ? 'bg-[#34c759]' : 'bg-[#c7c7cc] dark:bg-zinc-600'
        }`}
        title={ch.enabled ? '启用' : '停用'}
      />
    </div>
  )
}

function openEdit(ch: Channel, setForm: (f: FormState) => void) {
  setForm({
    id: ch.id,
    name: ch.name,
    base_url: ch.base_url,
    path: ch.path,
    keys: ch.keys.length > 0
      ? ch.keys.map((k) => ({ key: k.key, enabled: k.enabled, label: k.label }))
      : [{ key: '', enabled: true }],
    modelsText: ch.models.join('\n'),
    modelMap: Object.entries(ch.model_map).map(([pub, upstream]) => ({ pub, upstream })),
    headers: ch.headers.map((h) => ({ name: h.name, value: h.value })),
    keyStrategy: ch.key_strategy || 'rotate',
    enabled: ch.enabled,
    isPlugin: ch.source.startsWith('plugin:'),
  })
}

function splitLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}