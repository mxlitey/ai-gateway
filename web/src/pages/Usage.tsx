import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type UsageBucket } from '../lib/api'
import { Badge, Button, Card, Empty, ErrorText, Loading, Segmented } from '../components/ui'
import RangePicker, { type RangeValue } from '../components/RangePicker'

const PAGE_SIZE = 50
const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR

// 趋势图筛选用的下拉框样式（与 Input 视觉一致）
const selectClass =
  'rounded-lg border border-zinc-300 bg-white px-2 py-1.5 text-sm text-zinc-900 outline-none focus:border-emerald-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100'

export default function Usage() {
  // 粒度与时间范围（日历选择器）：按天选日期范围，按小时可精确到小时
  const [granularity, setGranularity] = useState<'day' | 'hour'>('day')
  const [range, setRange] = useState<RangeValue>(() => dayRange(Date.now()))
  const [offset, setOffset] = useState(0)
  // 渠道名 / 公开模型名筛选（取自当前范围内的分组结果）
  const [channelFilter, setChannelFilter] = useState('')
  const [modelFilter, setModelFilter] = useState('')

  const summary = useQuery({
    queryKey: ['usage-summary', range.from, range.to, channelFilter, modelFilter],
    queryFn: () =>
      api.usageSummary({
        from: range.from,
        to: range.to,
        channel_name: channelFilter || undefined,
        model: modelFilter || undefined,
      }),
  })
  const logs = useQuery({
    queryKey: ['usage-logs', range.from, range.to, channelFilter, modelFilter, offset],
    queryFn: () =>
      api.usageLogs({
        from: range.from,
        to: range.to,
        channel_name: channelFilter || undefined,
        model: modelFilter || undefined,
        limit: PAGE_SIZE,
        offset,
      }),
  })
  const series = useQuery({
    queryKey: ['usage-series', granularity, range.from, range.to, channelFilter, modelFilter],
    queryFn: () =>
      api.usageSeries({
        from: range.from,
        to: range.to,
        bucket: granularity,
        channel_name: channelFilter || undefined,
        model: modelFilter || undefined,
      }),
  })

  const switchGranularity = (g: 'day' | 'hour') => {
    if (g === granularity) return
    setGranularity(g)
    setRange(g === 'hour' ? lastHoursRange(Date.now()) : dayRange(Date.now()))
    setOffset(0)
  }

  const onRangeChange = (v: RangeValue) => {
    setRange(v)
    setOffset(0)
  }

  if (summary.isLoading || logs.isLoading) return <Loading />
  if (summary.isError) return <ErrorText text={(summary.error as Error).message} />

  const s = summary.data!.summary
  const priceCount = summary.data!.price_count
  const items = logs.data?.items ?? []
  const total = logs.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const page = Math.floor(offset / PAGE_SIZE) + 1
  const points = series.data?.points ?? []

  return (
    <div className="space-y-4">
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">用量</h1>

      <Card>
        <div className="flex flex-wrap items-center gap-2">
          <select
            value={channelFilter}
            onChange={(e) => {
              setChannelFilter(e.target.value)
              // 模型下拉按渠道过滤，换渠道后原先选中的模型可能已不在列表中，一并清空
              setModelFilter('')
              setOffset(0)
            }}
            className={`${selectClass} w-40`}
          >
            <option value="">全部渠道</option>
            {s.by_channel.map((b) => (
              <option key={b.key} value={b.key}>
                {b.key}
              </option>
            ))}
          </select>
          <select
            value={modelFilter}
            onChange={(e) => {
              setModelFilter(e.target.value)
              setOffset(0)
            }}
            className={`${selectClass} w-48`}
          >
            <option value="">全部模型</option>
            {s.by_model.map((b) => (
              <option key={b.key} value={b.key}>
                {b.key}
              </option>
            ))}
          </select>
          <Segmented
            value={granularity}
            onChange={switchGranularity}
            options={[
              { value: 'hour', label: '按小时' },
              { value: 'day', label: '按天' },
            ]}
          />
          <RangePicker value={range} onChange={onRangeChange} granularity={granularity} />
          <Button
            onClick={() => {
              summary.refetch()
              logs.refetch()
              series.refetch()
            }}
          >
            刷新
          </Button>
        </div>
      </Card>

      {priceCount === 0 ? (
        <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
          价格表还是空的：去「价格表」页刷新或手工添加价格。价格表为空时费用一律按 0 计。
        </div>
      ) : null}

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label="请求数" value={fmtInt(s.requests)} />
        <Stat
          label="总 token"
          value={fmtCompact(s.total_tokens)}
          hint={`未缓存输入 ${fmtCompact(Math.max(0, s.prompt_tokens - s.cached_tokens))} / 缓存输入 ${fmtCompact(
            s.cached_tokens,
          )} / 输出 ${fmtCompact(s.completion_tokens)}`}
        />
        <Stat label="市场价" value={fmtUSD(s.cost_usd)} />
        <Stat label="未匹配价格" value={fmtInt(s.unpriced_requests)} />
      </div>

      <Card title="用量趋势">
        {series.isLoading ? (
          <Loading />
        ) : series.isError ? (
          <ErrorText text={(series.error as Error).message} />
        ) : points.length === 0 ? (
          <Empty text="所选范围内没有调用记录" />
        ) : (
          <TrendChart points={points} />
        )}
      </Card>

      <Card title={`调用明细（${fmtInt(total)} 条）`}>
        {items.length === 0 ? (
          <Empty text="所选范围内没有调用记录" />
        ) : (
          <>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                    <th className="py-2 pr-3 font-medium">时间</th>
                    <th className="py-2 pr-3 font-medium">模型</th>
                    <th className="py-2 pr-3 font-medium">渠道</th>
                    <th className="py-2 pr-3 font-medium">端点</th>
                    <th className="py-2 pr-3 font-medium">状态</th>
                    <th className="py-2 pr-3 text-right font-medium">未缓存输入</th>
                    <th className="py-2 pr-3 text-right font-medium">缓存输入</th>
                    <th className="py-2 pr-3 text-right font-medium">输出</th>
                    <th className="py-2 pr-3 text-right font-medium">耗时</th>
                    <th className="py-2 text-right font-medium">等价费用</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((r) => (
                    <tr
                      key={r.id}
                      className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60"
                    >
                      <td className="py-2 pr-3 text-xs whitespace-nowrap text-zinc-600 dark:text-zinc-400">
                        {new Date(r.created_at).toLocaleString()}
                      </td>
                      <td className="py-2 pr-3 font-mono text-xs text-zinc-700 dark:text-zinc-300">
                        {r.model || '—'}
                        {r.upstream_model && r.upstream_model !== r.model ? (
                          <span className="text-zinc-400"> → {r.upstream_model}</span>
                        ) : null}
                        {r.attempts > 1 ? (
                          <span className="ml-1 text-zinc-400">(试 {r.attempts} 次)</span>
                        ) : null}
                      </td>
                      <td className="py-2 pr-3 text-xs text-zinc-600 dark:text-zinc-400">
                        {r.channel_name || '—'}
                      </td>
                      <td className="py-2 pr-3 text-xs text-zinc-500">
                        {r.endpoint}
                        {r.stream ? ' · 流式' : ''}
                      </td>
                      <td className="py-2 pr-3">
                        <Badge tone={r.success ? 'ok' : 'warn'}>
                          {r.status === 0 ? '网络' : r.status}
                        </Badge>
                      </td>
                      <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-600 dark:text-zinc-400">
                        {fmtInt(Math.max(0, r.prompt_tokens - r.cached_tokens))}
                      </td>
                      <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-600 dark:text-zinc-400">
                        {fmtInt(r.cached_tokens)}
                      </td>
                      <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-600 dark:text-zinc-400">
                        {fmtInt(r.completion_tokens)}
                      </td>
                      <td className="py-2 pr-3 text-right font-mono text-xs text-zinc-500">
                        {r.duration_ms} ms
                      </td>
                      <td className="py-2 text-right font-mono text-xs">
                        {r.price_source === 'none' ? (
                          <span className="text-zinc-400">无价格</span>
                        ) : (
                          <span className="text-emerald-600 dark:text-emerald-400">
                            {fmtUSD(r.cost_usd)}
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div className="mt-3 flex items-center justify-between text-xs text-zinc-500">
              <span>
                第 {page} / {totalPages} 页
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
    </div>
  )
}

// TrendChart 用量趋势柱状图：三色堆叠（未缓存输入 / 缓存输入 / 输出），悬停显示明细与缓存命中率。
function TrendChart({ points }: { points: UsageBucket[] }) {
  const [hover, setHover] = useState(-1)

  const { top, step } = chartScale(Math.max(...points.map((p) => p.total_tokens), 0))
  const ticks = [4, 3, 2, 1, 0].map((i) => step * i)
  // 柱子太多时抽稀横轴标签，保证可读
  const labelEvery = Math.max(1, Math.ceil(points.length / 8))

  // 未缓存输入 = 输入 - 缓存命中；三段之和即总 token
  const parts = (p: UsageBucket) => {
    const cached = Math.max(0, p.cached_tokens)
    const uncached = Math.max(0, p.prompt_tokens - cached)
    return { uncached, cached, output: p.completion_tokens }
  }
  const pct = (part: number, whole: number) => (whole > 0 ? (part / whole) * 100 : 0)

  // 单个时间桶的最大宽度：按「24 个桶 + 23 个 4px 空隙」均分区块宽度。
  // 桶变少时不会被拉宽（否则单个桶会占满整个区块），整组居中、桶间保留 4px 空隙。
  const barWidth = 'calc((100% - 92px) / 24)'

  return (
    <div>
      <p className="mb-2 text-xs text-zinc-500 dark:text-zinc-400">单位 Tokens</p>
      <div className="flex gap-3">
        <div className="flex h-40 w-14 shrink-0 flex-col justify-between text-right text-[11px] leading-none text-zinc-400 dark:text-zinc-500">
          {ticks.map((t, i) => (
            <span key={i}>{fmtCompact(t)}</span>
          ))}
        </div>

        <div className="relative flex h-40 flex-1 items-end justify-center gap-1">
          {ticks.map((_, i) => (
            <div
              key={i}
              className="pointer-events-none absolute inset-x-0 border-t border-zinc-100 dark:border-zinc-800"
              style={{ top: `${(i / 4) * 100}%` }}
            />
          ))}
          {points.map((p, i) => {
            const { uncached, cached, output } = parts(p)
            return (
              <div
                key={p.key}
                className="relative flex h-full items-end"
                style={{ width: barWidth }}
                onMouseEnter={() => setHover(i)}
                onMouseLeave={() => setHover(-1)}
              >
                <div
                  className={`flex w-full flex-col overflow-hidden rounded-t ${
                    hover === i
                      ? 'ring-2 ring-emerald-500 ring-offset-1 ring-offset-white dark:ring-offset-zinc-900'
                      : ''
                  }`}
                  style={{
                    height: `${top > 0 ? Math.min(100, (p.total_tokens / top) * 100) : 0}%`,
                    minHeight: p.total_tokens > 0 ? '2px' : '0',
                  }}
                >
                  {/* 自上而下：输出 → 缓存输入 → 未缓存输入（三色区分：紫 / 蓝 / 浅蓝） */}
                  <div style={{ height: `${pct(output, p.total_tokens)}%` }} className="bg-violet-500" />
                  <div style={{ height: `${pct(cached, p.total_tokens)}%` }} className="bg-emerald-600" />
                  <div
                    style={{ height: `${pct(uncached, p.total_tokens)}%` }}
                    className="bg-emerald-300 dark:bg-emerald-700"
                  />
                </div>

                {hover === i ? (
                  <div className={tipBoxClass(i, points.length)}>
                    <div className="flex items-baseline justify-between gap-6">
                      <span className="font-medium text-zinc-700 dark:text-zinc-200">
                        {p.key.slice(5)}
                      </span>
                      <span className="font-mono text-zinc-500 dark:text-zinc-400">
                        {fmtInt(p.requests)} 次
                      </span>
                    </div>
                    <div className="mt-1.5 space-y-1">
                      <TipRow label="总 Tokens" value={fmtInt(p.total_tokens)} />
                      <TipRow
                        label="缓存命中率"
                        value={
                          p.prompt_tokens > 0
                            ? `${pct(cached, p.prompt_tokens).toFixed(1)}%`
                            : '—'
                        }
                      />
                    </div>
                    <div className="my-1.5 border-t border-zinc-100 dark:border-zinc-800" />
                    <div className="space-y-1">
                      <TipRow
                        dot="bg-emerald-300 dark:bg-emerald-700"
                        label="未缓存输入"
                        value={fmtInt(uncached)}
                      />
                      <TipRow dot="bg-emerald-600" label="缓存输入" value={fmtInt(cached)} />
                      <TipRow dot="bg-violet-500" label="输出" value={fmtInt(output)} />
                      <TipRow
                        label="费用"
                        value={fmtUSD(p.cost_usd)}
                        valueClass="text-emerald-600 dark:text-emerald-400"
                      />
                    </div>
                  </div>
                ) : null}
              </div>
            )
          })}
        </div>
      </div>

      <div className="mt-1 flex gap-3">
        <div className="w-14 shrink-0" />
        <div className="flex flex-1 justify-center gap-1">
          {points.map((p, i) => (
            <span
              key={p.key}
              className="whitespace-nowrap text-center text-[10px] text-zinc-400 dark:text-zinc-500"
              style={{ width: barWidth }}
            >
              {i % labelEvery === 0 ? shortBucketKey(p.key) : ''}
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}

// tipBoxClass 悬浮提示框样式：始终贴在柱子两侧（靠近中心的一侧），避免遮挡当前桶。
function tipBoxClass(i: number, total: number): string {
  const side = i * 2 >= total ? 'right-full mr-2' : 'left-full ml-2'
  return `pointer-events-none absolute top-0 z-10 w-max max-w-[18rem] rounded-lg border border-zinc-200 bg-white px-3 py-2 text-xs leading-relaxed shadow-lg dark:border-zinc-700 dark:bg-zinc-900 ${side}`
}

// TipRow 悬浮提示里的一行：可选的色块 + 名称 + 右对齐的数值。
function TipRow({
  dot,
  label,
  value,
  valueClass = '',
}: {
  dot?: string
  label: string
  value: string
  valueClass?: string
}) {
  return (
    <div className="flex items-center justify-between gap-6">
      <span className="flex items-center gap-1.5 text-zinc-500 dark:text-zinc-400">
        {dot ? <span className={`inline-block h-2 w-2 rounded-sm ${dot}`} /> : null}
        {label}
      </span>
      <span className={`font-mono tabular-nums text-zinc-700 dark:text-zinc-200 ${valueClass}`}>
        {value}
      </span>
    </div>
  )
}

// chartScale 把最大值归一到「4 等分刻度」，让 Y 轴刻度落在 1/2/2.5/5×10^n 这类整齐的数上。
function chartScale(max: number): { top: number; step: number } {
  if (max <= 0) return { top: 1, step: 0.25 }
  const raw = max / 4
  const base = Math.pow(10, Math.floor(Math.log10(raw)))
  const n = raw / base
  const mult = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10
  const step = mult * base
  return { top: step * 4, step }
}

// shortBucketKey 把时间桶键压缩成横轴短标签："2026-10-14" → "10-14"，"2026-10-14 14:00" → "14:00"。
function shortBucketKey(key: string): string {
  return key.length > 10 ? key.slice(11) : key.slice(5)
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <p className="text-xs text-zinc-500 dark:text-zinc-400">{label}</p>
      <p className="mt-1 font-mono text-xl font-semibold text-zinc-800 dark:text-zinc-100">{value}</p>
      {hint ? <p className="mt-0.5 text-xs text-zinc-400 dark:text-zinc-500">{hint}</p> : null}
    </div>
  )
}

function fmtInt(n: number): string {
  return (n ?? 0).toLocaleString('en-US')
}

function fmtCompact(n: number): string {
  const v = n ?? 0
  if (v < 1000) return String(v)
  if (v < 1_000_000) return `${(v / 1000).toFixed(1)}K`
  return `${(v / 1_000_000).toFixed(2)}M`
}

function fmtUSD(n: number): string {
  const v = n ?? 0
  if (v === 0) return '$0'
  if (v < 0.01) return `$${v.toFixed(4)}`
  if (v < 1) return `$${v.toFixed(3)}`
  return `$${v.toFixed(2)}`
}

function startOfDay(ms: number): number {
  const d = new Date(ms)
  d.setHours(0, 0, 0, 0)
  return d.getTime()
}

function startOfHour(ms: number): number {
  const d = new Date(ms)
  d.setMinutes(0, 0, 0)
  return d.getTime()
}

// dayRange 当天的自然日范围（[今天 00:00, 明天 00:00)）
function dayRange(now: number): RangeValue {
  const from = startOfDay(now)
  return { from, to: from + DAY }
}

// lastHoursRange 最近 24 个小时桶范围（含当前小时）
function lastHoursRange(now: number): RangeValue {
  const cur = startOfHour(now)
  return { from: cur - 23 * HOUR, to: cur + HOUR }
}