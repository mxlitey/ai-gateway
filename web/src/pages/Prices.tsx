import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  api,
  type ModelPrice,
  type PriceInput,
  type PriceModelOption,
  type PriceTier,
  type PriceWindow,
  type PricesResponse,
} from '../lib/api'
import { toast } from '../lib/toast'
import { currencySymbol, fmtPrice } from '../lib/money'
import { Badge, Button, Card, Empty, Field, Input, Loading, Segmented } from '../components/ui'

/** 表单草稿：全部用字符串保存，避免输入过程中的中间态被改写成数字。 */
type Draft = {
  unit: string
  tiers: { up_to: string; prompt: string; completion: string }[]
  cache: string
  request: string
  windows: { start: string; end: string; factor: string; days: number[] }[]
  note: string
}

const EMPTY_TIER = { up_to: '', prompt: '', completion: '' }

/** 星期标签，索引即 model.TimeWindow.Days 的取值（0=周日）。 */
const WEEKDAYS = ['日', '一', '二', '三', '四', '五', '六']

function emptyDraft(image: boolean): Draft {
  return {
    unit: image ? 'per_request' : 'per_token',
    tiers: [{ ...EMPTY_TIER }],
    cache: '',
    request: '',
    windows: [],
    note: '',
  }
}

/** 把已保存的价格转成表单草稿；单位由模型类型决定（生图按次、对话按 token）。 */
function draftOf(p: ModelPrice, image: boolean): Draft {
  return {
    unit: image ? 'per_request' : 'per_token',
    tiers:
      p.tiers && p.tiers.length > 0
        ? p.tiers.map((t) => ({
            up_to: t.up_to > 0 ? String(t.up_to) : '',
            prompt: fmtPrice(t.prompt_price),
            completion: fmtPrice(t.completion_price),
          }))
        : [{ ...EMPTY_TIER }],
    cache: fmtPrice(p.cache_read_price),
    request: fmtPrice(p.request_price),
    windows: (p.time_windows ?? []).map((w) => ({
      start: w.start,
      end: w.end,
      factor: fmtPrice(w.factor),
      days: w.days ?? [],
    })),
    note: p.note,
  }
}

/** 草稿转提交体；上限留空按 0（不限）处理。 */
function payloadOf(model: string, d: Draft): PriceInput {
  const tiers: PriceTier[] =
    d.unit === 'per_request'
      ? []
      : d.tiers.map((t) => ({
          up_to: num(t.up_to),
          prompt_price: num(t.prompt),
          completion_price: num(t.completion),
        }))
  const windows: PriceWindow[] = d.windows.map((w) => ({
    start: w.start.trim(),
    end: w.end.trim(),
    factor: num(w.factor),
    // 七天全选与「不限定星期」等价，存空数组让规则更简洁
    days: w.days.length >= 7 ? [] : w.days,
  }))
  return {
    model,
    unit: d.unit,
    tiers,
    cache_read_price: num(d.cache),
    request_price: num(d.request),
    time_windows: windows,
    note: d.note.trim(),
  }
}

export default function Prices() {
  const prices = useQuery({ queryKey: ['prices'], queryFn: api.prices })

  const savePrice = useMutation({
    mutationFn: (body: PriceInput) => api.upsertPrice(body),
    onSuccess: async () => {
      toast('ok', '价格已保存')
      await prices.refetch()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const deletePrice = useMutation({
    mutationFn: (name: string) => api.deletePrice(name),
    onSuccess: async () => {
      toast('ok', '价格已删除')
      await prices.refetch()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  return (
    <div className="space-y-4">
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">价格表</h1>
      <PriceSection
        data={prices.data}
        loading={prices.isLoading}
        saving={savePrice.isPending}
        onSave={(body) => savePrice.mutate(body)}
        onDelete={(name) => deletePrice.mutate(name)}
      />
    </div>
  )
}

function PriceSection({
  data,
  loading,
  saving,
  onSave,
  onDelete,
}: {
  data?: PricesResponse
  loading: boolean
  saving: boolean
  onSave: (body: PriceInput) => void
  onDelete: (name: string) => void
}) {
  const [query, setQuery] = useState('')
  // 只显示未定价模型：填表时不必在长列表里翻找
  const [onlyUnpriced, setOnlyUnpriced] = useState(false)
  const [expanded, setExpanded] = useState('')
  const [draft, setDraft] = useState<Draft | null>(null)

  // 价格按归一化后的模型名存储，因此回填也要用同一个键匹配
  const priceByKey = useMemo(() => {
    const m = new Map<string, ModelPrice>()
    for (const p of data?.items ?? []) m.set(p.model, p)
    return m
  }, [data])

  // 先按「有价格的模型」和「仅有公开模型」合并成一行的候选清单，
  // 保证已经手工填过、但渠道里已不存在的条目仍可编辑或删除。
  const rows = useMemo(() => {
    const list: (PriceModelOption & { price?: ModelPrice })[] = []
    const known = new Set<string>()
    for (const opt of data?.models ?? []) {
      known.add(opt.normalized)
      list.push({ ...opt, price: priceByKey.get(opt.normalized) })
    }
    for (const p of data?.items ?? []) {
      if (known.has(p.model)) continue
      // 渠道里已不存在的历史条目：没有原始名，用归一化键当展示名
      list.push({ model: p.model, normalized: p.model, image: false, priced: true, price: p })
    }
    return list
  }, [data, priceByKey])

  const q = query.trim().toLowerCase()
  const shown = rows.filter((r) => {
    if (onlyUnpriced && r.priced) return false
    return !q || r.model.toLowerCase().includes(q)
  })
  const unpriced = rows.filter((r) => !r.priced).length

  function openRow(model: string, price?: ModelPrice, image?: boolean) {
    setExpanded(model)
    // 生图模型只能按次，对话模型只能按 token：单位由模型类型决定，用户不用选
    setDraft(price ? draftOf(price, Boolean(image)) : emptyDraft(Boolean(image)))
  }

  function close() {
    setExpanded('')
    setDraft(null)
  }

  return (
    <Card
      title="价格表"
      action={
        <span className="text-xs text-zinc-500">
          {data ? `已定价 ${data.count} / ${data.model_count} 个模型` : ''}
        </span>
      }
    >
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Input
          className="w-full py-1 text-xs sm:w-56"
          placeholder="搜索模型名"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Segmented
          value={onlyUnpriced ? 'unpriced' : 'all'}
          onChange={(v) => setOnlyUnpriced(v === 'unpriced')}
          options={[
            { value: 'all', label: '全部' },
            { value: 'unpriced', label: `未定价 ${unpriced}` },
          ]}
        />
        <span className="text-xs text-zinc-500 dark:text-zinc-400">
          共 {rows.length} 行{q ? ` · 匹配 ${shown.length} 行` : ''}
        </span>
      </div>

      {loading ? (
        <Loading text="读取价格表…" />
      ) : rows.length === 0 ? (
        <Empty text="还没有任何公开模型：先在「渠道管理」里添加模型，再回来填价格" />
      ) : shown.length === 0 ? (
        <Empty text="没有匹配的行" />
      ) : (
        <div className="space-y-2">
          {shown.map((r) => {
            const isOpen = expanded === r.model
            return (
              <div
                key={r.model}
                className="rounded-lg border border-zinc-200 px-3 py-2 dark:border-zinc-800"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    onClick={() => (isOpen ? close() : openRow(r.model, r.price, r.image))}
                    className="flex min-w-0 flex-1 items-center gap-2 text-left"
                  >
                    <span className="font-mono text-xs text-zinc-700 dark:text-zinc-300">
                      {r.model}
                    </span>
                    {r.image ? <Badge tone="info">生图</Badge> : null}
                    <Badge tone={r.priced ? 'ok' : 'warn'}>
                      {r.priced ? '已定价' : '未定价'}
                    </Badge>
                  </button>
                  <span className="font-mono text-xs text-zinc-500 dark:text-zinc-400">
                    {r.price ? summaryOf(r.price) : '—'}
                  </span>
                  <Button
                    size="sm"
                    variant={isOpen ? 'default' : 'primary'}
                    onClick={() => (isOpen ? close() : openRow(r.model, r.price, r.image))}
                  >
                    {isOpen ? '收起' : r.priced ? '编辑' : '填写'}
                  </Button>
                  {r.priced ? (
                    <button
                      className="text-xs text-red-600 hover:underline dark:text-red-400"
                      onClick={() => {
                        if (window.confirm(`删除 ${r.model} 的价格？历史用量不受影响。`)) {
                          onDelete(r.normalized)
                          if (isOpen) close()
                        }
                      }}
                    >
                      删除
                    </button>
                  ) : null}
                </div>

                {isOpen && draft ? (
                  <PriceForm
                    model={r.model}
                    perRequest={r.image}
                    draft={draft}
                    saving={saving}
                    onChange={setDraft}
                    onSubmit={() => onSave(payloadOf(r.normalized, draft))}
                    onCancel={close}
                  />
                ) : null}
              </div>
            )
          })}
        </div>
      )}
    </Card>
  )
}

function PriceForm({
  model,
  perRequest,
  draft,
  saving,
  onChange,
  onSubmit,
  onCancel,
}: {
  model: string
  /** 生图模型只能按次，对话模型只能按 token；由模型类型决定，不提供切换 */
  perRequest: boolean
  draft: Draft
  saving: boolean
  onChange: (d: Draft) => void
  onSubmit: () => void
  onCancel: () => void
}) {
  function setTier(i: number, key: keyof Draft['tiers'][number], value: string) {
    const tiers = draft.tiers.slice()
    tiers[i] = { ...tiers[i], [key]: value }
    onChange({ ...draft, tiers })
  }

  function addTier() {
    onChange({ ...draft, tiers: [...draft.tiers, { ...EMPTY_TIER }] })
  }

  function removeTier(i: number) {
    onChange({ ...draft, tiers: draft.tiers.filter((_, j) => j !== i) })
  }

  function setWindow(i: number, key: 'start' | 'end' | 'factor', value: string) {
    const windows = draft.windows.slice()
    windows[i] = { ...windows[i], [key]: value }
    onChange({ ...draft, windows })
  }

  // 点一次切换某天的选中状态
  function toggleDay(i: number, day: number) {
    const windows = draft.windows.slice()
    const days = windows[i].days.includes(day)
      ? windows[i].days.filter((d) => d !== day)
      : [...windows[i].days, day].sort((a, b) => a - b)
    windows[i] = { ...windows[i], days }
    onChange({ ...draft, windows })
  }

  return (
    <div className="mt-3 space-y-3 border-t border-zinc-100 pt-3 dark:border-zinc-800">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="info">{perRequest ? '按次计费' : '按 token 计费'}</Badge>
        <span className="font-mono text-xs text-zinc-400">{model}</span>
      </div>

      {perRequest ? (
        <div className="grid gap-2 sm:grid-cols-3">
          <Field label="每次请求单价" hint="生图等按次收费的端点，与返回几张图无关">
            <Input
              value={draft.request}
              onChange={(e) => onChange({ ...draft, request: e.target.value })}
              placeholder="0.04"
            />
          </Field>
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <span className="text-xs text-zinc-500 dark:text-zinc-400">输入分档</span>
            <Button size="sm" variant="ghost" onClick={addTier}>
              加一档
            </Button>
          </div>
          {draft.tiers.map((t, i) => (
            <div key={i} className="grid items-end gap-2 sm:grid-cols-4">
              <Field label={i === 0 ? '输入上限（token）' : '本档上限'}>
                <Input
                  value={t.up_to}
                  onChange={(e) => setTier(i, 'up_to', e.target.value)}
                  placeholder="不限"
                />
              </Field>
              <Field label="输入价 / 百万">
                <Input
                  value={t.prompt}
                  onChange={(e) => setTier(i, 'prompt', e.target.value)}
                  placeholder="2"
                />
              </Field>
              <Field label="输出价 / 百万">
                <Input
                  value={t.completion}
                  onChange={(e) => setTier(i, 'completion', e.target.value)}
                  placeholder="8"
                />
              </Field>
              <div className="flex items-end">
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={draft.tiers.length <= 1}
                  onClick={() => removeTier(i)}
                >
                  删除本档
                </Button>
              </div>
            </div>
          ))}
          <div className="grid gap-2 sm:grid-cols-3">
            <Field label="缓存命中价 / 百万" hint="留空或 0 表示按输入价计">
              <Input
                value={draft.cache}
                onChange={(e) => onChange({ ...draft, cache: e.target.value })}
                placeholder="0.3"
              />
            </Field>
          </div>
        </div>
      )}

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-xs text-zinc-500 dark:text-zinc-400">峰谷时段（北京时间）</span>
          <Button
            size="sm"
            variant="ghost"
            onClick={() =>
              onChange({
                ...draft,
                windows: [...draft.windows, { start: '', end: '', factor: '', days: [] }],
              })
            }
          >
            加一段
          </Button>
        </div>
        {draft.windows.map((w, i) => (
          <div
            key={i}
            className="space-y-2 rounded-md border border-zinc-100 p-2 dark:border-zinc-800"
          >
            <div className="grid items-end gap-2 sm:grid-cols-4">
              <Field label="开始">
                <Input
                  value={w.start}
                  onChange={(e) => setWindow(i, 'start', e.target.value)}
                  placeholder="00:30"
                />
              </Field>
              <Field label="结束">
                <Input
                  value={w.end}
                  onChange={(e) => setWindow(i, 'end', e.target.value)}
                  placeholder="08:30"
                />
              </Field>
              <Field label="系数">
                <Input
                  value={w.factor}
                  onChange={(e) => setWindow(i, 'factor', e.target.value)}
                  placeholder="0.5"
                />
              </Field>
              <div className="flex items-end">
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() =>
                    onChange({ ...draft, windows: draft.windows.filter((_, j) => j !== i) })
                  }
                >
                  删除
                </Button>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-1">
              <span className="mr-1 text-xs text-zinc-500 dark:text-zinc-400">适用星期</span>
              {WEEKDAYS.map((label, day) => {
                const on = w.days.includes(day)
                return (
                  <button
                    key={day}
                    type="button"
                    onClick={() => toggleDay(i, day)}
                    className={`h-6 w-7 rounded-md text-xs transition-colors ${
                      on
                        ? 'bg-emerald-600 text-white'
                        : 'bg-zinc-100 text-zinc-600 hover:bg-zinc-200 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-700'
                    }`}
                  >
                    {label}
                  </button>
                )
              })}
              {w.days.length === 0 ? (
                <span className="ml-1 text-xs text-zinc-400 dark:text-zinc-500">每天</span>
              ) : null}
              {w.days.length > 0 ? (
                <button
                  type="button"
                  className="ml-1 text-xs text-zinc-500 hover:underline dark:text-zinc-400"
                  onClick={() => {
                    const windows = draft.windows.slice()
                    windows[i] = { ...windows[i], days: [] }
                    onChange({ ...draft, windows })
                  }}
                >
                  清空
                </button>
              ) : null}
            </div>
          </div>
        ))}
      </div>

      <Field label="备注">
        <Input
          value={draft.note}
          onChange={(e) => onChange({ ...draft, note: e.target.value })}
          placeholder="如：官网价，2026-11 调整"
        />
      </Field>

      <div className="flex items-center gap-2">
        <Button variant="primary" disabled={saving} onClick={onSubmit}>
          保存
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          取消
        </Button>
      </div>
    </div>
  )
}

/** summaryOf 一行摘要：按 token 时给出首档与档数，按次时给出单价。 */
function summaryOf(p: ModelPrice): string {
  const s = currencySymbol()
  if (p.unit === 'per_request') return `${s}${fmtPrice(p.request_price)} / 次`
  const tiers = p.tiers ?? []
  if (tiers.length === 0) return '—'
  const first = tiers[0]
  const tail = tiers.length > 1 ? ` 等 ${tiers.length} 档` : ''
  return `${s}${fmtPrice(first.prompt_price)} / ${s}${fmtPrice(first.completion_price)}${tail}`
}

function num(s: string): number {
  const n = Number(s)
  return Number.isFinite(n) ? n : 0
}
