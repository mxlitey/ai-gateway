import { useEffect, useRef, useState } from 'react'
import { toast } from '../lib/toast'

const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR

// 选择器可选的范围上限：按天最多一年，按小时最多一天（后端会再各自校验一次）
const MAX_DAYS = 366
const MAX_HOURS = 24

export type RangeValue = { from: number; to: number }

// RangePicker 时间范围选择器：点击展开日历，按天时选日期范围，按小时时精确到小时（[from, to) 毫秒）。
export default function RangePicker({
  value,
  onChange,
  granularity,
}: {
  value: RangeValue
  onChange: (v: RangeValue) => void
  granularity: 'day' | 'hour'
}) {
  const [open, setOpen] = useState(false)
  // 选完开始再选结束；tab 为 'start' 时点一下即开新范围
  const [tab, setTab] = useState<'start' | 'end'>('start')
  const [pendingStart, setPendingStart] = useState<number | null>(null)
  // 按小时时先点日期、再点小时
  const [pendingDay, setPendingDay] = useState<number | null>(null)
  const [hoverDay, setHoverDay] = useState<number | null>(null)
  const [month, setMonth] = useState(() => monthOf(value.to - 1))
  const boxRef = useRef<HTMLDivElement>(null)

  // 粒度决定一格的时长与可选范围上限
  const step = granularity === 'hour' ? HOUR : DAY
  const limit = granularity === 'hour' ? MAX_HOURS * HOUR : MAX_DAYS * DAY
  const tooWide = granularity === 'hour' ? `按小时最多查看 ${MAX_HOURS} 小时` : `按天最多查看 ${MAX_DAYS} 天`

  // 切换粒度时重置为「选择开始」，并把日历定位到当前范围所在的月份
  useEffect(() => {
    setTab('start')
    setPendingStart(null)
    setPendingDay(null)
    setMonth(monthOf(value.to - 1))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [granularity])

  // 点击选择器外部时收起
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) close()
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const close = () => {
    setOpen(false)
    setTab('start')
    setPendingStart(null)
    setPendingDay(null)
  }

  const [y, m] = month.split('-').map(Number)
  const lead = (new Date(y, m - 1, 1).getDay() + 6) % 7 // 周一作为第一列
  const daysInMonth = new Date(y, m, 0).getDate()
  const cells: (number | null)[] = [
    ...Array.from({ length: lead }, () => null),
    ...Array.from({ length: daysInMonth }, (_, i) => startOfDay(new Date(y, m - 1, i + 1).getTime())),
  ]
  while (cells.length % 7 !== 0) cells.push(null)

  // 提交一个端点（按天整日、按小时整点）
  const commit = (endpointStart: number, isEnd: boolean) => {
    const from = isEnd ? Math.min(pendingStart ?? endpointStart, endpointStart) : endpointStart
    const to = isEnd
      ? Math.max(pendingStart ?? endpointStart, endpointStart) + step
      : endpointStart + step

    if (!isEnd) {
      setPendingStart(endpointStart)
      setTab('end')
      setPendingDay(null)
      return
    }
    if (to - from > limit) {
      toast('err', tooWide)
      return
    }
    onChange({ from, to })
    close()
  }

  const pick = (dayMs: number, isEnd: boolean) => {
    if (granularity === 'day') {
      commit(dayMs, isEnd)
      return
    }
    setPendingDay(dayMs) // 按小时：先定日期，再点小时
  }

  const rangeFrom = tab === 'end' && pendingStart != null ? pendingStart : value.from
  const rangeTo =
    tab === 'end' && pendingStart != null
      ? Math.max(pendingStart, hoverDay ?? pendingStart)
      : value.to - 1

  const inRange = (dayMs: number) => dayMs >= rangeFrom && dayMs <= rangeTo
  const isEndpoint = (dayMs: number) =>
    dayMs === startOfDay(rangeFrom) || dayMs === startOfDay(rangeTo)

  // 选好开始端点后，结束端点必须落在 [start-(limit-step), start+(limit-step)] 内；
  // 超出的日期 / 小时直接置灰不可点，而不是点完再提示「最多只能看 N 小时 / 天」。
  const allowed =
    tab === 'end' && pendingStart != null
      ? { from: pendingStart - (limit - step), to: pendingStart + (limit - step) }
      : null
  const dayDisabled = (dayMs: number) =>
    allowed != null && (dayMs < startOfDay(allowed.from) || dayMs > startOfDay(allowed.to))
  const pointDisabled = (p: number) => allowed != null && (p < allowed.from || p > allowed.to)

  return (
    <div ref={boxRef} className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex max-w-full items-center gap-2 rounded-lg border border-zinc-300 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 max-sm:py-2.5 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200 dark:hover:bg-zinc-800"
      >
        <svg
          width="15"
          height="15"
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          className="flex-none"
        >
          <rect x="2" y="3.5" width="12" height="11" rx="2.5" />
          <path d="M2 7h12M5.5 2v3M10.5 2v3" />
        </svg>
        <span className="truncate">
          {granularity === 'hour'
            ? `${fmtHour(value.from)} ~ ${fmtHour(value.to - 1)}`
            : `${fmtDay(value.from)} ~ ${fmtDay(value.to - 1)}`}
        </span>
      </button>

      {open ? (
        <>
          {/* 窄屏：日历改为底部弹层，避免浮层超出屏幕 */}
          <div className="fixed inset-0 z-20 bg-black/30 sm:hidden" onClick={close} />
          <div className="absolute right-0 z-20 mt-2 w-72 rounded-xl border border-zinc-200 bg-white p-3 shadow-lg max-sm:fixed max-sm:inset-x-3 max-sm:top-auto max-sm:bottom-[calc(0.75rem+env(safe-area-inset-bottom))] max-sm:mt-0 max-sm:w-auto dark:border-zinc-700 dark:bg-zinc-900">
          <div className="mb-2 flex items-center justify-between">
            <button
              onClick={() => setMonth(shiftMonth(month, -1))}
              aria-label="上个月"
              className="rounded-md px-2 py-0.5 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
            >
              ‹
            </button>
            <span className="text-sm font-medium text-zinc-700 dark:text-zinc-200">
              {y} 年 {m} 月
            </span>
            <button
              onClick={() => setMonth(shiftMonth(month, 1))}
              aria-label="下个月"
              className="rounded-md px-2 py-0.5 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
            >
              ›
            </button>
          </div>

          <p className="mb-1 text-center text-[11px] text-emerald-600 dark:text-emerald-400">
            {tab === 'start'
              ? granularity === 'hour'
                ? '请选择开始日期与小时'
                : '请选择开始日期'
              : granularity === 'hour'
                ? '请选择结束日期与小时'
                : '请选择结束日期'}
          </p>

          <div className="grid grid-cols-7 gap-1 text-center text-[11px] text-zinc-400 dark:text-zinc-500">
            {['一', '二', '三', '四', '五', '六', '日'].map((w) => (
              <span key={w}>{w}</span>
            ))}
          </div>
          <div className="mt-1 grid grid-cols-7 gap-1">
            {cells.map((d, i) =>
              d === null ? (
                <span key={`blank-${i}`} />
              ) : (
                <button
                  key={d}
                  disabled={dayDisabled(d)}
                  onMouseEnter={() => setHoverDay(d)}
                  onClick={() => pick(d, tab === 'end')}
                  className={`h-8 rounded-lg text-xs max-sm:h-10 disabled:cursor-not-allowed ${
                    dayDisabled(d)
                      ? 'text-zinc-300 dark:text-zinc-700'
                      : d === pendingDay
                        ? 'bg-emerald-600 text-white'
                        : inRange(d)
                          ? isEndpoint(d)
                            ? 'bg-emerald-600 text-white'
                            : 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
                          : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800'
                  }`}
                >
                  {Number(new Date(d).getDate())}
                </button>
              ),
            )}
          </div>

          {granularity === 'hour' && pendingDay != null ? (
            <div className="mt-2 border-t border-zinc-100 pt-2 dark:border-zinc-800">
              <p className="mb-1 text-[11px] text-zinc-500 dark:text-zinc-400">
                {fmtDay(pendingDay)} · 选择小时
              </p>
              <div className="grid grid-cols-6 gap-1">
                {Array.from({ length: 24 }, (_, h) => {
                  const p = pendingDay + h * HOUR
                  const off = pointDisabled(p)
                  return (
                    <button
                      key={h}
                      disabled={off}
                      onClick={() => commit(p, tab === 'end')}
                      className={`rounded-md px-1 py-1 text-[11px] max-sm:py-2 disabled:cursor-not-allowed ${
                        off
                          ? 'text-zinc-300 dark:text-zinc-700'
                          : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800'
                      }`}
                    >
                      {String(h).padStart(2, '0')}
                    </button>
                  )
                })}
              </div>
            </div>
          ) : null}

          <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-zinc-100 pt-2 dark:border-zinc-800">
            {presets(granularity).map((p) => (
              <button
                key={p.label}
                onClick={() => {
                  if (p.value.to - p.value.from > limit) {
                    toast('err', tooWide)
                    return
                  }
                  onChange(p.value)
                  close()
                }}
                className="text-xs text-emerald-600 hover:underline dark:text-emerald-400"
              >
                {p.label}
              </button>
            ))}
          </div>
          </div>
        </>
      ) : null}
    </div>
  )
}

// presets 常用范围快捷入口
function presets(granularity: 'day' | 'hour'): { label: string; value: RangeValue }[] {
  const now = Date.now()
  if (granularity === 'hour') {
    const cur = startOfHour(now)
    return [
      { label: '最近 12 小时', value: { from: cur - 11 * HOUR, to: cur + HOUR } },
      { label: '最近 24 小时', value: { from: cur - 23 * HOUR, to: cur + HOUR } },
    ]
  }
  const today = startOfDay(now)
  return [
    { label: '最近 3 天', value: { from: today - 2 * DAY, to: today + DAY } },
    { label: '最近 7 天', value: { from: today - 6 * DAY, to: today + DAY } },
    { label: '最近 30 天', value: { from: today - 29 * DAY, to: today + DAY } },
  ]
}

function monthOf(ms: number): string {
  const d = new Date(ms)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}

function shiftMonth(month: string, delta: number): string {
  const [y, m] = month.split('-').map(Number)
  const d = new Date(y, m - 1 + delta, 1)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
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

function fmtDay(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function fmtHour(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:00`
}