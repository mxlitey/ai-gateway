import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { fmtCost } from '../lib/money'
import RangePicker, { type RangeValue } from '../components/RangePicker'
import { Badge, Button, Empty, ErrorText, fmtCompact, fmtInt, Loading, Panel } from './ui'

const PAGE_SIZE = 50
const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR

/** 筛选与时间范围在刷新浏览器后保留（sessionStorage：仅当前标签页有效） */
const STORAGE_KEY = 'aigw.usage.filters'

type StoredFilters = {
  granularity: 'day' | 'hour'
  from: number
  to: number
  channelFilter: string
  modelFilter: string
  keyFilter: string
}

function loadFilters(): StoredFilters | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const v = JSON.parse(raw) as StoredFilters
    if (typeof v.from !== 'number' || typeof v.to !== 'number' || v.to <= v.from) return null
    if (v.granularity !== 'day' && v.granularity !== 'hour') return null
    return v
  } catch {
    return null
  }
}

function saveFilters(v: StoredFilters): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(v))
  } catch {
    // 存储不可用时忽略，不影响使用
  }
}

function dayRange(now: number): RangeValue {
  const d = new Date(now)
  d.setHours(0, 0, 0, 0)
  return { from: d.getTime(), to: d.getTime() + DAY }
}

function lastHoursRange(now: number): RangeValue {
  const d = new Date(now)
  d.setMinutes(0, 0, 0)
  return { from: d.getTime() - 23 * HOUR, to: d.getTime() + HOUR }
}

export default function V2Usage() {
  const stored = useMemo(loadFilters, [])
  const [granularity, setGranularity] = useState<'day' | 'hour'>(stored?.granularity ?? 'day')
  const [range, setRange] = useState<RangeValue>(() =>
    stored ? { from: stored.from, to: stored.to } : dayRange(Date.now()),
  )
  const [offset, setOffset] = useState(0)
  const [channelFilter, setChannelFilter] = useState(stored?.channelFilter ?? '')
  const [modelFilter, setModelFilter] = useState(stored?.modelFilter ?? '')
  const [keyFilter, setKeyFilter] = useState(stored?.keyFilter ?? '')

  const persist = (patch: Partial<StoredFilters>) =>
    saveFilters({ granularity, from: range.from, to: range.to, channelFilter, modelFilter, keyFilter, ...patch })

  const summary = useQuery({
    queryKey: ['usage-summary', range.from, range.to, channelFilter, modelFilter, keyFilter],
    queryFn: () =>
      api.usageSummary({
        from: range.from,
        to: range.to,
        channel_name: channelFilter || undefined,
        model: modelFilter || undefined,
        key_hint: keyFilter || undefined,
      }),
  })
  const logs = useQuery({
    queryKey: ['usage-logs', range.from, range.to, channelFilter, modelFilter, keyFilter, offset],
    queryFn: () =>
      api.usageLogs({
        from: range.from,
        to: range.to,
        channel_name: channelFilter || undefined,
        model: modelFilter || undefined,
        key_hint: keyFilter || undefined,
        limit: PAGE_SIZE,
        offset,
      }),
  })

  const switchGranularity = (g: 'day' | 'hour') => {
    if (g === granularity) return
    const next = g === 'hour' ? lastHoursRange(Date.now()) : dayRange(Date.now())
    setGranularity(g)
    setRange(next)
    setOffset(0)
    persist({ granularity: g, from: next.from, to: next.to })
  }

  const onRangeChange = (v: RangeValue) => {
    setRange(v)
    setOffset(0)
    persist({ from: v.from, to: v.to })
  }

  if (summary.isLoading || logs.isLoading) return <Panel><Loading /></Panel>
  if (summary.isError) return <ErrorText text={(summary.error as Error).message} />

  const s = summary.data!.summary
  const priceCount = summary.data!.price_count
  const cacheHitRate =
    s.prompt_tokens > 0 ? `${((s.cached_tokens / s.prompt_tokens) * 100).toFixed(1)}%` : '—'
  const items = logs.data?.items ?? []
  const total = logs.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const page = Math.floor(offset / PAGE_SIZE) + 1

  return (
    <div className="stack">
      <h1 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>用量</h1>

      {/* 控件：沿用原版一行 flex-wrap，渠道 → 模型 → 上游密钥 → 粒度 → 时间范围 */}
      {/* panel-overflow：让日期选择器的日历浮层能超出卡片边界显示 */}
      <div className="panel panel-overflow">
        <div className="panel-b">
          <div className="usage-ctrl">
            <select
              className="input usage-sel"
              value={channelFilter}
              onChange={(e) => {
                setChannelFilter(e.target.value)
                // 模型 / 密钥下拉都按渠道过滤，换渠道后原先选中的值可能已不在列表中，一并清空
                setModelFilter('')
                setKeyFilter('')
                setOffset(0)
                persist({ channelFilter: e.target.value, modelFilter: '', keyFilter: '' })
              }}
            >
              <option value="">全部渠道</option>
              {s.by_channel.map((b) => (
                <option key={b.key} value={b.key}>
                  {b.key}
                </option>
              ))}
            </select>
            <select
              className="input usage-sel w2"
              value={modelFilter}
              onChange={(e) => {
                setModelFilter(e.target.value)
                setKeyFilter('')
                setOffset(0)
                persist({ modelFilter: e.target.value, keyFilter: '' })
              }}
            >
              <option value="">全部模型</option>
              {s.by_model.map((b) => (
                <option key={b.key} value={b.key}>
                  {b.key}
                </option>
              ))}
            </select>
            <select
              className="input usage-sel w3"
              value={keyFilter}
              onChange={(e) => {
                setKeyFilter(e.target.value)
                setOffset(0)
                persist({ keyFilter: e.target.value })
              }}
            >
              <option value="">全部上游密钥</option>
              {s.by_key
                .filter((b) => b.key)
                .map((b) => (
                  <option key={b.key} value={b.key}>
                    {b.key}
                  </option>
                ))}
            </select>
            <div className="seg">
              <button
                type="button"
                className={granularity === 'hour' ? 'on' : ''}
                onClick={() => switchGranularity('hour')}
              >
                按小时
              </button>
              <button
                type="button"
                className={granularity === 'day' ? 'on' : ''}
                onClick={() => switchGranularity('day')}
              >
                按天
              </button>
            </div>
            <RangePicker value={range} onChange={onRangeChange} granularity={granularity} />
          </div>
        </div>
      </div>

      {priceCount === 0 ? (
        <div
          style={{
            border: '1px solid var(--warn)',
            background: 'var(--warn-weak)',
            color: 'var(--warn)',
            borderRadius: 8,
            padding: '7px 10px',
            fontSize: 11.5,
          }}
        >
          价格表为空，费用按 0 计
        </div>
      ) : null}

      <div className="v2grid g4">
        <Kpi label="请求数" value={fmtInt(s.requests)} />
        <Kpi label="总 token" value={fmtCompact(s.total_tokens)} />
        <Kpi label="缓存命中率" value={cacheHitRate} />
        <Kpi label="市场价" value={fmtCost(s.cost)} />
      </div>

      <Panel title={`调用明细（${fmtInt(total)} 条）`} bodyClass="">
        {items.length === 0 ? (
          <Empty text="所选范围内没有调用记录" />
        ) : (
          <>
            <div className="tbl-wrap">
              <table className="tbl" style={{ minWidth: '46rem' }}>
                <thead>
                  <tr>
                    <th>时间</th>
                    <th>模型</th>
                    <th>渠道</th>
                    <th>端点</th>
                    <th>状态</th>
                    <th className="r">未缓存输入</th>
                    <th className="r">缓存输入</th>
                    <th className="r">输出</th>
                    <th className="r">耗时</th>
                    <th className="r">等价费用</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((r) => (
                    <tr key={r.id}>
                      <td className="tiny faint num" style={{ whiteSpace: 'nowrap' }}>
                        {new Date(r.created_at).toLocaleString()}
                      </td>
                      <td className="mono tiny">
                        <span style={{ display: 'block', whiteSpace: 'nowrap' }}>
                          {r.model || '—'}
                          {r.attempts > 1 ? <span className="faint">（试 {r.attempts} 次）</span> : null}
                        </span>
                        {r.upstream_model && r.upstream_model !== r.model ? (
                          <span
                            className="faint"
                            style={{ display: 'block', whiteSpace: 'nowrap', fontSize: 10 }}
                          >
                            → {r.upstream_model}
                          </span>
                        ) : null}
                      </td>
                      <td className="tiny">
                        {r.channel_name || '—'}
                        {r.upstream_key_hint ? (
                          <span className="mono faint" style={{ display: 'block', fontSize: 10 }}>
                            {r.upstream_key_hint}
                          </span>
                        ) : null}
                      </td>
                      <td className="tiny faint" style={{ whiteSpace: 'nowrap' }}>
                        {r.endpoint}
                        {r.stream ? ' · 流式' : ''}
                      </td>
                      <td>
                        <Badge tone={r.success ? 'ok' : 'warn'}>
                          {r.status === 0 ? '网络' : r.status}
                        </Badge>
                      </td>
                      <td className="r num tiny">{fmtInt(Math.max(0, r.prompt_tokens - r.cached_tokens))}</td>
                      <td className="r num tiny">{fmtInt(r.cached_tokens)}</td>
                      <td className="r num tiny">{fmtInt(r.completion_tokens)}</td>
                      <td className="r num tiny faint" style={{ whiteSpace: 'nowrap' }}>
                        {r.duration_ms} ms
                      </td>
                      <td className="r num tiny">
                        {r.price_source === 'none' ? (
                          <span className="faint">无价格</span>
                        ) : (
                          <span style={{ color: 'var(--brand)' }}>{fmtCost(r.cost)}</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="usage-page">
              <span>
                第 {page} / {totalPages} 页
              </span>
              <div style={{ display: 'flex', gap: 6 }}>
                <Button
                  size="sm"
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                >
                  上一页
                </Button>
                <Button
                  size="sm"
                  disabled={offset + PAGE_SIZE >= total}
                  onClick={() => setOffset(offset + PAGE_SIZE)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </Panel>
    </div>
  )
}

function Kpi({ label, value }: { label: string; value: string }) {
  return (
    <div className="panel" style={{ padding: '11px 13px' }}>
      <div className="tiny" style={{ color: 'var(--text-2)' }}>
        {label}
      </div>
      <div className="num" style={{ fontSize: 19, fontWeight: 600, marginTop: 1 }}>
        {value}
      </div>
    </div>
  )
}
