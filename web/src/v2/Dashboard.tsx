import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Channel, type KeyCooldown, type UsageBucket } from '../lib/api'
import { fmtCost } from '../lib/money'
import { bjDateString, bjDayStart, bjFmtTime, bjParts } from '../lib/tz'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  Dot,
  Empty,
  ErrorText,
  fmtCompact,
  fmtCountdown,
  fmtInt,
  Loading,
  PageHead,
  Panel,
  SmallMultiples,
  useTick,
  type MiniSeries,
  type Tone,
} from './ui'

function isPluginChannel(ch: Channel): boolean {
  return ch.source.startsWith('plugin:')
}

/** 时间窗口统一为北京时区的自然日，与后端 beijingDayRange / 小时分桶一致 */
function dayStart(offsetDays = 0): number {
  return bjDayStart(Date.now(), offsetDays)
}

function localDateString(): string {
  return bjDateString(Date.now())
}

/**
 * 渠道可提供的模型数，按「上游模型名」归一化后去重：
 * - models 里的名字直接就是上游模型名（同名透传）
 * - model_map 的 value 是上游模型名，因此与 models 中同名项只算一个
 * - image_models 同样按名字计
 * 例：models 有 gpt-4o，model_map 又把 my-gpt 映射到 gpt-4o，则只算 1 个。
 */
function countChannelModels(c: Channel): number {
  const names = new Set<string>()
  for (const m of c.models) {
    const name = m.trim()
    if (name) names.add(name)
  }
  for (const upstream of Object.values(c.model_map ?? {})) {
    const name = upstream.trim()
    if (name) names.add(name)
  }
  for (const m of c.image_models ?? []) {
    const name = m.trim()
    if (name) names.add(name)
  }
  return names.size
}

export default function V2Dashboard() {
  // active = 点选固定的小时；悬停只用于图表内预览，不影响读数
  const [active, setActive] = useState<number | null>(null)

  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })
  const errors = useQuery({ queryKey: ['errors'], queryFn: () => api.errors() })
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.plugins })
  // queryKey 带上日期，跨零点后自动重新拉取，避免沿用昨天的缓存
  const today = localDateString()

  const usage = useQuery({
    queryKey: ['usage-summary', 'today', today],
    queryFn: () => api.usageSummary({ date: today }),
  })
  // 主图：今天 00:00 → 明天 00:00，按小时分桶
  const series = useQuery({
    queryKey: ['usage-series', 'today-hour', today],
    queryFn: () => api.usageSeries({ bucket: 'hour', from: dayStart(), to: dayStart(1) }),
  })
  // 昨日同期：昨天 00:00 → 今天 00:00，与今日横坐标逐点对应
  const yesterday = useQuery({
    queryKey: ['usage-series', 'yesterday-hour', today],
    queryFn: () => api.usageSeries({ bucket: 'hour', from: dayStart(-1), to: dayStart() }),
  })

  const loading = channels.isLoading || plugins.isLoading || usage.isLoading

  const model = useMemo(() => {
    const s = usage.data?.summary
    if (!s) return null
    const cacheHit = s.prompt_tokens > 0 ? (s.cached_tokens / s.prompt_tokens) * 100 : 0
    // 失败数直接取自用量汇总：按实际请求上游的次数统计，故障转移的每次尝试各占一条
    const errorCount = s.failed_requests
    const errorRate = s.requests > 0 ? (errorCount / s.requests) * 100 : 0
    return { s, errorCount, cacheHit, errorRate }
  }, [usage.data])

  // 今日已到达的小时数（含当前小时，按北京时间）。主图与趋势线都只画到这里，未来时段留空。
  const cutoff = useMemo(() => bjParts().h + 1, [])

  // 失败请求与成功请求同表（request_logs 每条记录带 success 标记），
  // 因此直接按小时聚合，不需要再从 error_logs 换算。
  // 口径是实际请求上游的次数：一次客户端请求发生多次故障转移时，每次尝试各占一条。
  const failuresByHour = useMemo(() => {
    const arr = new Array<number>(24).fill(0)
    // points 按小时升序且覆盖今天全部 24 个桶（含空桶），索引即小时
    for (const p of series.data?.points ?? []) {
      const h = Number(p.key.slice(-5, -3))
      if (!Number.isFinite(h) || h < 0 || h >= 24) continue
      arr[h] += Math.max(0, p.requests - p.success_requests)
    }
    return arr
  }, [series.data])

  // 主图：每个指标独占一行（不堆叠），共用同一组小时横坐标
  const miniRows = useMemo(() => {
    const pts = series.data?.points ?? []
    return pts.map((p, i) => {
      // 桶按小时升序生成，索引即小时；用 key 兜底解析，避免口径漂移
      const hour = Number(p.key.slice(-5, -3))
      return {
        label: p.key,
        values: {
          prompt: Math.max(0, p.prompt_tokens - p.cached_tokens),
          cached: p.cached_tokens,
          completion: p.completion_tokens,
          success: p.success_requests,
          failures: failuresByHour[Number.isFinite(hour) ? hour : i] ?? 0,
          cost: p.cost,
        },
      }
    })
  }, [series.data, failuresByHour])

  // 费用为 0 时（未配价格）整行没有意义，直接不显示这一行
  const miniSeries = useMemo(() => {
    const costTotal = miniRows.reduce((sum, r) => sum + r.values.cost, 0)
    const list: MiniSeries[] = [
      { key: 'prompt', label: '未缓存输入', color: 'var(--s1)', format: fmtCompact },
      { key: 'cached', label: '缓存输入', color: 'var(--s2)', format: fmtCompact },
      { key: 'completion', label: '输出', color: 'var(--s3)', format: fmtCompact },
      {
        key: 'success',
        label: '成功请求',
        color: 'var(--s3)',
        format: (n) => fmtInt(Math.round(n)),
      },
      { key: 'failures', label: '失败请求', color: 'var(--danger)', format: (n) => fmtInt(Math.round(n)) },
    ]
    if (costTotal > 0) {
      list.push({ key: 'cost', label: '费用', color: 'var(--s4)', format: (n) => fmtCost(n) })
    }
    return list
  }, [miniRows])

  const rows = miniRows

  // 昨日同期的每小时合计（token），供迷你趋势线画对比虚线
  const yesterdayTotals = useMemo(
    () => (yesterday.data?.points ?? []).map((p) => p.total_tokens),
    [yesterday.data],
  )

  // 渠道健康：只统计真实渠道（插件渠道需插件在运行）
  const runningPluginIDs = new Set(
    (plugins.data ?? []).filter((p) => p.last_status === 'running').map((p) => p.id),
  )
  const channelList = (channels.data ?? []).filter(
    (c) => !isPluginChannel(c) || runningPluginIDs.has(c.source.slice('plugin:'.length)),
  )
  const availableChannels = channelList.filter((c) => c.enabled && c.keys.some((k) => k.enabled)).length

  if (loading) {
    return (
      <div className="panel">
        <Loading />
      </div>
    )
  }

  const s = model?.s

  // 按模型横条：以全部模型的总和为分母，两条各自表示费用占比与请求占比
  const modelBuckets = s?.by_model ?? []
  const totalModelCost = modelBuckets.reduce((sum, b) => sum + b.cost, 0)
  const totalModelReq = modelBuckets.reduce((sum, b) => sum + b.requests, 0)
  const errorGroups = errors.data?.channels ?? []

  return (
    <div className="stack">
      {/* 页头统一由 PageHead 提供；顶部状态带作为副标题，避免顶栏与页头出现两个标题 */}
      <PageHead title="仪表盘" desc={<StatusStrip />} />

      {errors.isError ? <ErrorText text={(errors.error as Error).message} /> : null}

      {/* 主 KPI + 次级读数 */}
      <div className="hero">
        <div className="kpi">
          <span className="lbl">今日等价费用</span>
          <div className="val">{fmtCost(s?.cost ?? 0)}</div>
          <div className="meta">
            <span title="按实际请求上游的次数统计，故障转移的每次尝试各算一次">
              上游请求 <b className="num">{fmtInt(s?.requests ?? 0)}</b> 次
            </span>
            <span className="sep" style={{ width: 1, height: 9, background: 'var(--border)' }} />
            <span>
              Token <b className="num">{fmtCompact(s?.total_tokens ?? 0)}</b>
            </span>
            <span className="sep" style={{ width: 1, height: 9, background: 'var(--border)' }} />
            <span>今日 00:00 起</span>
          </div>
          <Sparkline points={rows} yesterday={yesterdayTotals} cutoff={cutoff} />
        </div>

        <div className="metrics">
          <MetricRow
            k="总 Token"
            v={fmtCompact(s?.total_tokens ?? 0)}
            sub={`输入 ${fmtCompact(s?.prompt_tokens ?? 0)} · 输出 ${fmtCompact(s?.completion_tokens ?? 0)}`}
            tone="good"
          />
          <MetricRow
            k="缓存命中率"
            v={`${(model?.cacheHit ?? 0).toFixed(1)}%`}
            sub={`缓存输入 ${fmtCompact(s?.cached_tokens ?? 0)}`}
            tone={(model?.cacheHit ?? 0) > 0 ? 'good' : undefined}
          />
          <MetricRow
            k="错误率"
            v={`${(model?.errorRate ?? 0).toFixed(2)}%`}
            sub={model?.errorCount ? `${model.errorCount} 次失败` : '无失败请求'}
            tone={model && model.errorCount > 0 ? 'bad' : undefined}
            subTone={model && model.errorCount > 0 ? 'bad' : undefined}
          />
          <MetricRow
            k="可用渠道"
            v={`${availableChannels} / ${channelList.length}`}
            sub={
              availableChannels === channelList.length
                ? '全部可用'
                : `${channelList.length - availableChannels} 个不可用`
            }
            tone={
              availableChannels === channelList.length
                ? 'good'
                : availableChannels === 0
                  ? 'bad'
                  : 'hot'
            }
            subTone={availableChannels === channelList.length ? undefined : 'warn'}
          />
          <MetricRow
            k="未定价请求"
            v={fmtInt(s?.unpriced_requests ?? 0)}
            sub={(s?.unpriced_requests ?? 0) === 0 ? '费用统计完整' : '这部分不计入费用'}
            tone={(s?.unpriced_requests ?? 0) > 0 ? 'hot' : 'good'}
            subTone={(s?.unpriced_requests ?? 0) > 0 ? 'warn' : undefined}
          />
        </div>
      </div>

      {/* 24 小时主图：每个指标独占一行（小倍数，不堆叠），共用横坐标。
          选中值直接体现在行标签下方，不再单独占右上角读数与底部统计。 */}
      <div className="panel">
        <div className="chart-head">
          <h2>今日 24 小时</h2>
        </div>
        <div className="chart-body">
          {series.isLoading ? (
            <Loading text="读取用量…" />
          ) : rows.length === 0 ? (
            <Empty text="今天还没有调用记录" />
          ) : (
            <SmallMultiples
              rows={rows}
              series={miniSeries}
              active={active}
              onActivate={setActive}
              maxIndex={cutoff - 1}
            />
          )}
        </div>
      </div>

      {/* 异常与熔断 + 按模型 */}
      <div className="v2grid g2">
        <Panel
          title="异常与熔断"
          action={
            <Link to="/errors" className="btn ghost sm">
              查看错误日志
            </Link>
          }
        >
          {errorGroups.length === 0 && (model?.errorCount ?? 0) === 0 ? (
            <div className="empty">今天没有错误，也没有被熔断的密钥</div>
          ) : (
            <>
              {errorGroups.length > 0 ? (
                <>
                  <div className="ab-sec">按渠道</div>
                  <div className="ab-list">
                    {errorGroups
                      .slice()
                      .sort((a, b) => b.errors.length - a.errors.length)
                      .map((g) => {
                        return (
                          <div key={g.channel_id} className="ab-line">
                            <span className="n">{g.channel_name}</span>
                            <span className="c">{g.errors.length}</span>
                          </div>
                        )
                      })
                      .slice(0, 5)}
                    {errorGroups.length > 5 ? (
                      <div className="faint tiny">还有 {errorGroups.length - 5} 个渠道有错误</div>
                    ) : null}
                  </div>
                </>
              ) : null}
              <CooldownList />
            </>
          )}
        </Panel>

        <Panel
          title="按模型"
          action={
            <span className="tiny faint">
              费用 / 请求 · {s?.by_model.length ?? 0} 个模型
            </span>
          }
        >
          {modelBuckets.length === 0 ? (
            <Empty text="今天没有调用记录" />
          ) : (
            <div className="modelbars">
              {modelBuckets.map((b) => (
                <div key={b.key} className="modelbar">
                  <div className="mb-head">
                    <span className="mono n" title={b.key}>
                      {b.key}
                    </span>
                    <span className="v num">
                      {fmtCost(b.cost)}
                      <em>{fmtInt(b.requests)} 次</em>
                    </span>
                  </div>
                  {/* 双条形：上=费用占比，下=请求占比。两者差距大说明单价差异明显 */}
                  <div className="mb-bar">
                    <i
                      style={{
                        width: `${totalModelCost > 0 ? (b.cost / totalModelCost) * 100 : 0}%`,
                        background: 'var(--brand)',
                      }}
                    />
                  </div>
                  <div className="mb-bar">
                    <i
                      style={{
                        width: `${totalModelReq > 0 ? (b.requests / totalModelReq) * 100 : 0}%`,
                        background: 'var(--s1)',
                      }}
                    />
                  </div>
                </div>
              ))}
            </div>
          )}
        </Panel>
      </div>

      {/* 按渠道 + 快速开始 */}
      <div className="v2grid g2">
        <Panel title="按渠道">
          {!s || s.by_channel.length === 0 ? (
            <Empty text="今天没有调用记录" />
          ) : (
            <BucketTable buckets={s.by_channel} />
          )}
        </Panel>

        <Panel title="渠道健康">
          {channelList.length === 0 ? (
            <Empty text="还没有渠道" />
          ) : (
            <div className="health">
              {channelList.map((c) => {
                const keysOk = c.keys.some((k) => k.enabled)
                const tone: Tone = !c.enabled ? 'off' : keysOk ? 'ok' : 'warn'
                return (
                  <div key={c.id} className="hrow">
                    <span className="l">
                      <Dot tone={tone} />
                      <span className="n">{c.name}</span>
                      {isPluginChannel(c) ? <Badge tone="info">插件</Badge> : null}
                    </span>
                    <span className="r">
                      <span>{countChannelModels(c)} 模型</span>
                      {isPluginChannel(c) ? <CheckinBadge pluginID={c.source.slice('plugin:'.length)} /> : null}
                      <Badge tone={tone}>{!c.enabled ? '停用' : keysOk ? '正常' : '无钥'}</Badge>
                    </span>
                  </div>
                )
              })}
            </div>
          )}
        </Panel>
      </div>
    </div>
  )
}

/* ---------- 顶部状态带 ---------- */

function StatusStrip() {
  const runtime = useQuery({ queryKey: ['runtime'], queryFn: api.runtimeInfo })
  const now = useTick(1000)
  return (
    <div className="strip">
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <span
          style={{
            width: 6,
            height: 6,
            borderRadius: 999,
            background: 'var(--ok)',
            boxShadow: '0 0 0 3px var(--ok-weak)',
          }}
        />
        网关运行中
      </span>
      <span className="sep" />
      <span className="num">{bjFmtTime(now)}</span>
      <span className="sep hide-xs" />
      <span className="hide-xs mono">{runtime.data?.access_url ?? '—'}</span>
      <span className="sep hide-xs" />
      <span className="hide-xs">v{runtime.data?.version ?? '—'}</span>
    </div>
  )
}

/* ---------- 次级读数行 ---------- */

function MetricRow({
  k,
  v,
  sub,
  tone,
  subTone,
}: {
  k: string
  v: string
  sub?: string
  tone?: 'good' | 'hot' | 'bad'
  subTone?: 'warn' | 'bad'
}) {
  return (
    <div className={`mrow${tone ? ` ${tone}` : ''}`}>
      <span className="k">{k}</span>
      <span className="v">
        {v}
        {sub ? <span className={`sub${subTone ? ` ${subTone === 'warn' ? 'warn' : 'bad'}` : ''}`}>{sub}</span> : null}
      </span>
    </div>
  )
}

/* ---------- 主 KPI 下的迷你趋势线 ----------
   横轴固定为 0–24 小时：今日实线只画到当前小时，右侧留空表示尚未到达；
   昨日虚线完整画 24 小时，与实线共用同一纵轴尺度，便于逐小时对照。 */

/** 沿折线按固定长度切成一段段短线，返回可直接绘制成虚线效果的 path。
    不用 strokeDasharray 是因为 viewBox 被 preserveAspectRatio="none" 横向拉伸后，
    部分内核会把虚线间隔一起放大，导致虚线看起来消失。 */
function dashSegments(pts: { x: number; y: number }[], dash: number, gap: number): string {
  if (pts.length < 2) return ''
  const out: string[] = []
  let on = true
  let remain = dash
  let cur: string | null = `M${pts[0].x.toFixed(1)},${pts[0].y.toFixed(1)}`
  let px = pts[0].x
  let py = pts[0].y
  for (let i = 1; i < pts.length; i++) {
    const bx = pts[i].x
    const by = pts[i].y
    let ax = px
    let ay = py
    let len = Math.hypot(bx - ax, by - ay)
    while (len >= remain) {
      const t = remain / len
      const nx = ax + (bx - ax) * t
      const ny = ay + (by - ay) * t
      if (on && cur) out.push(`${cur} L${nx.toFixed(1)},${ny.toFixed(1)}`)
      on = !on
      remain = on ? dash : gap
      cur = on ? `M${nx.toFixed(1)},${ny.toFixed(1)}` : null
      ax = nx
      ay = ny
      len = Math.hypot(bx - ax, by - ay)
    }
    remain -= len
    if (on) {
      if (!cur) cur = `M${ax.toFixed(1)},${ay.toFixed(1)}`
      cur += ` L${bx.toFixed(1)},${by.toFixed(1)}`
    }
    px = bx
    py = by
  }
  if (on && cur) out.push(cur)
  return out.join(' ')
}

function Sparkline({
  points,
  yesterday,
  cutoff,
}: {
  points: { values: Record<string, number> }[]
  yesterday?: number[]
  /** 今日已到达的小时数（含当前小时）；之后的点不画 */
  cutoff: number
}) {
  const totals = points.map((p) => p.values.prompt + p.values.cached + p.values.completion)
  if (totals.length < 2) return <div className="spark" />

  // 今日只取已到达的部分；昨日取完整 24 小时
  const todayCount = Math.max(1, Math.min(totals.length, cutoff))
  const today = totals.slice(0, todayCount)
  const prev = (yesterday ?? []).slice(0, 24)

  const w = 300
  const h = 38
  // 纵轴上限取全部已到达数据与昨日全天，两者同尺度才有可比性
  const max = Math.max(1, ...today, ...prev)
  // 横轴固定 24 格，不随今日数据长度伸缩
  const stepX = w / 23
  // 上下各留 2px，零值不会贴边而看不见
  const y = (v: number) => h - 2 - (v / max) * (h - 4)

  const toPath = (vals: number[]): string =>
    vals.map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * stepX).toFixed(1)},${y(v).toFixed(1)}`).join(' ')

  const line = toPath(today)
  const area = `${line} L${((today.length - 1) * stepX).toFixed(1)},${h} L0,${h} Z`
  // 昨日虚线不用 strokeDasharray：viewBox 被 preserveAspectRatio="none" 横向拉伸时，
  // 部分内核会把虚线间隔一起放大，间隔超出可视区就只剩零星实线段，看起来像没画。
  // 这里直接按屏幕像素切段，画成一段段短线，跨浏览器稳定可见。
  const prevPath =
    prev.length >= 2
      ? dashSegments(
          prev.map((v, i) => ({ x: i * stepX, y: y(v) })),
          5,
          4,
        )
      : ''

  return (
    <div className="spark">
      <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
        <defs>
          <linearGradient id="v2spark" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" className="spark-stop-0" />
            <stop offset="100%" className="spark-stop-1" />
          </linearGradient>
        </defs>
        <path d={area} fill="url(#v2spark)" />
        <path d={line} className="spark-now" fill="none" />
        {/* 虚线最后画，避免被实线（起始段重合）盖住 */}
        {prevPath ? (
          <path d={prevPath} className="spark-prev" fill="none" />
        ) : null}
      </svg>
    </div>
  )
}

/* ---------- 渠道健康里的签到读数 ---------- */

/**
 * 插件渠道的今日签到情况：已签 / 账号总数。
 * 数据来自插件控制台（checked_in_today 已由插件按天派生），60 秒刷新一次；
 * 读不到账号时整块不渲染，避免在渠道行里塞一条误导性的 0/0。
 */
function CheckinBadge({ pluginID }: { pluginID: string }) {
  const accounts = useQuery({
    queryKey: ['plugin-accounts', pluginID],
    queryFn: () => api.pluginAccounts(pluginID),
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: false,
  })
  const list = accounts.data?.accounts ?? []
  if (list.length === 0) return null

  const checked = list.filter((a) => a.checked_in_today).length
  const tone: Tone = checked === list.length ? 'ok' : checked > 0 ? 'warn' : 'off'
  return (
    <Badge tone={tone}>
      签到 {checked}/{list.length}
    </Badge>
  )
}

/* ---------- 按渠道表 ---------- */

function BucketTable({ buckets }: { buckets: UsageBucket[] }) {
  return (
    <div className="tbl-wrap">
      <table className="tbl">
        <thead>
          <tr>
            <th>名称</th>
            <th className="r">请求</th>
            <th className="r">Token</th>
            <th className="r">等价费用</th>
          </tr>
        </thead>
        <tbody>
          {buckets.map((b) => (
            <tr key={b.key}>
              <td>
                <span className="truncate" style={{ display: 'block' }}>
                  {b.key}
                </span>
              </td>
              <td className="r num">{fmtInt(b.requests)}</td>
              <td className="r num">{fmtCompact(b.total_tokens)}</td>
              <td className="r num" style={{ color: 'var(--brand)' }}>
                {fmtCost(b.cost)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/* ---------- 临时禁用（熔断） ---------- */

function CooldownList() {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['key-cooldowns'],
    queryFn: api.keyCooldowns,
    refetchInterval: 5000,
  })
  const reset = useMutation({
    mutationFn: (c: KeyCooldown) => api.resetKeyCooldown(c.channel_id, c.key_hint),
    onSuccess: () => {
      toast('ok', '已重置该密钥冷却')
      void queryClient.invalidateQueries({ queryKey: ['key-cooldowns'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })
  const now = useTick(1000)
  const items = query.data?.items ?? []

  if (query.isError) return <ErrorText text={(query.error as Error).message} />
  if (items.length === 0) {
    return (
      <div style={{ padding: '6px 14px 11px' }} className="faint tiny">
        当前没有被临时禁用的密钥
      </div>
    )
  }
  return (
    <>
      <div className="ab-sec">临时禁用</div>
      {items.map((c) => {
        const remain = Math.max(0, Math.ceil((c.cooldown_until - now) / 1000))
        return (
          <div key={`${c.channel_id}:${c.key_hint}`} className="cool">
            <span className="who">
              <span className="dot warn" />
              <b>{c.channel_name || c.channel_id}</b>
              <span className="mono faint" style={{ fontSize: 10 }}>
                {c.key_hint}
              </span>
            </span>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, flex: 'none' }}>
              <span className="t">{fmtCountdown(remain)}</span>
              <Button
                variant="ghost"
                size="sm"
                disabled={reset.isPending}
                onClick={() => reset.mutate(c)}
                title="立即恢复"
              >
                恢复
              </Button>
            </span>
          </div>
        )
      })}
    </>
  )
}
