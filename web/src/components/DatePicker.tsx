import { useEffect, useRef, useState } from 'react'
import { bjDateStart, bjDateString, bjDaysInMonth, bjMonthString, bjWeekday } from '../lib/tz'

// DatePicker 单日期的日历选择器：点击按钮展开月历，选中某天后自动收起（默认当天）。
export default function DatePicker({
  value,
  onChange,
}: {
  value: string
  onChange: (d: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [month, setMonth] = useState(value.slice(0, 7)) // 当前展示的月份，YYYY-MM
  const boxRef = useRef<HTMLDivElement>(null)

  // 点击选择器外部时收起
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const [y, m] = month.split('-').map(Number)
  // 日历全部按北京时间排布，与后端的日界（+8 小时）保持一致
  const lead = (bjWeekday(y, m, 1) + 6) % 7 // 让周一作为一周的第一列
  const daysInMonth = bjDaysInMonth(y, m)
  const today = bjDateString()

  const cells: (string | null)[] = [
    ...Array.from({ length: lead }, () => null),
    ...Array.from({ length: daysInMonth }, (_, i) => `${month}-${String(i + 1).padStart(2, '0')}`),
  ]
  while (cells.length % 7 !== 0) cells.push(null)

  const shiftMonth = (delta: number) => {
    setMonth(bjMonthString(bjDateStart(y, m + delta, 1)))
  }

  return (
    <div ref={boxRef} className="relative">
      <button
        onClick={() => setOpen((v) => !v)}
        className="dp-btn flex items-center gap-2 rounded-lg border border-zinc-300 bg-white px-3 py-1.5 text-sm text-zinc-700 hover:bg-zinc-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-200 dark:hover:bg-zinc-800"
      >
        <svg
          width="15"
          height="15"
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
        >
          <rect x="2" y="3.5" width="12" height="11" rx="2.5" />
          <path d="M2 7h12M5.5 2v3M10.5 2v3" />
        </svg>
        {value}
      </button>

      {open ? (
        <>
          {/* 窄屏：日历改为底部弹层，避免浮层超出屏幕 */}
          <div className="fixed inset-0 z-20 bg-black/30 sm:hidden" onClick={() => setOpen(false)} />
          <div className="dp-pop absolute right-0 z-20 mt-2 w-64 rounded-xl border border-zinc-200 bg-white p-3 shadow-lg max-sm:fixed max-sm:inset-x-3 max-sm:top-auto max-sm:bottom-[calc(0.75rem+env(safe-area-inset-bottom))] max-sm:mt-0 max-sm:w-auto dark:border-zinc-700 dark:bg-zinc-900">
            <div className="mb-2 flex items-center justify-between">
              <button
                onClick={() => shiftMonth(-1)}
                aria-label="上个月"
                className="dp-nav rounded-md px-2 py-0.5 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
              >
                ‹
              </button>
              <span className="dp-title text-sm font-medium text-zinc-700 dark:text-zinc-200">
                {y} 年 {m} 月
              </span>
              <button
                onClick={() => shiftMonth(1)}
                aria-label="下个月"
                className="dp-nav rounded-md px-2 py-0.5 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800"
              >
                ›
              </button>
            </div>
            <div className="dp-week grid grid-cols-7 gap-1 text-center text-[11px] text-zinc-400 dark:text-zinc-500">
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
                    onClick={() => {
                      onChange(d)
                      setOpen(false)
                    }}
                    className={`dp-day h-8 rounded-lg text-xs max-sm:h-10 ${
                      d === value
                        ? 'is-on bg-emerald-600 text-white'
                        : d === today
                          ? 'is-today text-emerald-600 hover:bg-zinc-100 dark:text-emerald-400 dark:hover:bg-zinc-800'
                          : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800'
                    }`}
                  >
                    {Number(d.slice(8))}
                  </button>
                ),
              )}
            </div>
            <div className="mt-2 border-t border-zinc-100 pt-2 text-right dark:border-zinc-800">
              <button
                onClick={() => {
                  onChange(today)
                  setOpen(false)
                }}
                className="dp-preset text-xs text-emerald-600 hover:underline dark:text-emerald-400"
              >
                今天
              </button>
            </div>
          </div>
        </>
      ) : null}
    </div>
  )
}

// fmtDate 按北京时间输出 YYYY-MM-DD（与后端日界一致）
export function fmtDate(d: Date): string {
  return bjDateString(d.getTime())
}