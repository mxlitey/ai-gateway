import {
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from 'react'
import { useNavigate } from 'react-router-dom'

/* ---------- Button ---------- */

type ButtonVariant = 'default' | 'primary' | 'ghost' | 'danger'

export function Button({
  variant = 'default',
  size,
  className = '',
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant
  size?: 'sm'
}) {
  const cls = [
    'btn',
    variant !== 'default' ? variant : '',
    size === 'sm' ? 'sm' : '',
    className,
  ]
    .filter(Boolean)
    .join(' ')
  return <button {...rest} type={rest.type ?? 'button'} className={cls} />
}

/* ---------- Inputs ---------- */

export function Input({ className = '', ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={`input ${className}`} />
}

export function Textarea({ className = '', ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...rest} className={`input ${className}`} />
}

export function Select({ className = '', ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...rest} className={`input ${className}`} />
}

/** hint 走 title，不占版面。 */
export function Field({
  label,
  hint,
  children,
  className = '',
}: {
  label: string
  hint?: string
  children: ReactNode
  className?: string
}) {
  return (
    <label className={`field ${className}`} title={hint}>
      <span>{label}</span>
      {children}
    </label>
  )
}

/* ---------- Segmented ---------- */

export function Segmented<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T
  options: { value: T; label: string }[]
  onChange: (v: T) => void
}) {
  return (
    <div className="seg">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          className={value === o.value ? 'on' : ''}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/* ---------- Panel ---------- */

export function Panel({
  title,
  action,
  children,
  className = '',
  bodyClass = 'panel-b',
}: {
  title?: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
  bodyClass?: string
}) {
  return (
    <section className={`panel ${className}`}>
      {title || action ? (
        <header className="panel-h">
          <h2>{title}</h2>
          {action ? <div className="r">{action}</div> : null}
        </header>
      ) : null}
      <div className={bodyClass}>{children}</div>
    </section>
  )
}

/* ---------- Badge / Chip / Dot / Switch ---------- */

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'off'

export function Badge({ tone = 'info', children }: { tone?: Tone; children: ReactNode }) {
  return <span className={`badge ${tone}`}>{children}</span>
}

export function Chip({
  children,
  mono = false,
  onClick,
  title,
}: {
  children: ReactNode
  mono?: boolean
  onClick?: () => void
  title?: string
}) {
  return (
    <span
      className={`chip${mono ? ' mono' : ''}${onClick ? ' x' : ''}`}
      title={title}
      onClick={onClick}
      role={onClick ? 'button' : undefined}
    >
      {children}
    </span>
  )
}

export function Dot({ tone = 'off' }: { tone?: Tone }) {
  return <i className={`dot ${tone}`} />
}

export function Switch({
  checked,
  onChange,
  disabled = false,
  title,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
  title?: string
}) {
  return (
    <button
      type="button"
      className="sw"
      role="switch"
      aria-checked={checked}
      aria-label={title}
      title={title}
      disabled={disabled}
      onClick={(e) => {
        e.stopPropagation()
        onChange(!checked)
      }}
      onKeyDown={(e) => e.stopPropagation()}
    />
  )
}

/* ---------- 页头 ---------- */

export function PageHead({
  title,
  desc,
  actions,
}: {
  title: string
  desc?: ReactNode
  actions?: ReactNode
}) {
  return (
    <div className="page-head">
      <div className="t">
        <h1>{title}</h1>
        {desc ? <div className="desc">{desc}</div> : null}
      </div>
      {actions ? <div className="act">{actions}</div> : null}
    </div>
  )
}

/* ---------- 状态占位 ---------- */

export function Empty({ text }: { text: string }) {
  return <div className="empty">{text}</div>
}

export function Loading({ text = '加载中…' }: { text?: string }) {
  return <div className="empty">{text}</div>
}

export function ErrorText({ text }: { text: string }) {
  return (
    <div
      style={{
        border: '1px solid var(--danger)',
        background: 'var(--danger-weak)',
        color: 'var(--danger)',
        borderRadius: 8,
        padding: '7px 10px',
        fontSize: 11.5,
        marginBottom: 12,
      }}
    >
      {text}
    </div>
  )
}

/* ---------- Modal ---------- */

export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  wide = false,
}: {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
}) {
  useEffect(() => {
    if (!open) return
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      document.body.style.overflow = prev
      window.removeEventListener('keydown', onKey)
    }
  }, [open, onClose])

  if (!open) return null
  return (
    <div className="modal-mask" onClick={onClose} role="presentation">
      <div
        className={`modal${wide ? ' wide' : ''}`}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <header className="modal-h">
          <b>{title}</b>
          <button type="button" className="icon-btn" onClick={onClose} aria-label="关闭">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </header>
        <div className="modal-b">{children}</div>
        {footer ? <footer className="modal-f">{footer}</footer> : null}
      </div>
    </div>
  )
}

/* ---------- 空心双色环 ----------
   外环：本渠道今日请求数占全部请求数的比例
   内环：成功率（按状态着色）
   环心：P95 数值 */

export function Donut({
  share,
  shareTone = 'brand',
  successRate,
  successTone,
  center,
  centerUnit,
  label,
  title,
}: {
  share: number
  shareTone?: 'brand' | 'warn' | 'danger'
  successRate: number
  successTone: Tone
  center: string
  centerUnit?: string
  label?: string
  title?: string
}) {
  const size = 72
  const c = size / 2
  const rOuter = 31
  const rInner = 23
  const strokeOuter = 4
  const strokeInner = 4
  const shareColor =
    shareTone === 'warn' ? 'var(--warn)' : shareTone === 'danger' ? 'var(--danger)' : 'var(--brand)'
  const innerColor =
    successTone === 'ok'
      ? 'var(--ok)'
      : successTone === 'warn'
        ? 'var(--warn)'
        : successTone === 'bad'
          ? 'var(--danger)'
          : 'var(--text-3)'

  const ring = (r: number, pct: number, color: string, width: number) => {
    const circumference = 2 * Math.PI * r
    const dash = Math.max(0, Math.min(100, pct)) / 100 * circumference
    return (
      <>
        <circle cx={c} cy={c} r={r} fill="none" stroke="var(--surface-3)" strokeWidth={width} />
        <circle
          cx={c}
          cy={c}
          r={r}
          fill="none"
          stroke={color}
          strokeWidth={width}
          strokeLinecap="round"
          strokeDasharray={`${dash} ${circumference}`}
          transform={`rotate(-90 ${c} ${c})`}
        />
      </>
    )
  }

  return (
    <div className="donut" title={title}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
        {ring(rOuter, share, shareColor, strokeOuter)}
        {ring(rInner, successRate, innerColor, strokeInner)}
      </svg>
      <div className="donut-c">
        <b>
          {center}
          {centerUnit ? <i>{centerUnit}</i> : null}
        </b>
        {label ? <span>{label}</span> : null}
      </div>
    </div>
  )
}

/* ---------- 图表通用类型 ---------- */

export type SeriesRow = { key: string; label: string; color: string }

/* ---------- 小倍数图（每行一个指标，各自独立缩放） ----------
   所有行共用同一组时间横坐标，因此可以跨指标对照同一小时。
   同 stack 的序列在同一行内堆叠（例如请求数的成功/失败）。
   交互：鼠标移入某列即高亮整列；触屏点按选中。 */

export type MiniSeries = {
  key: string
  label: string
  color: string
  /** 该行的数值格式化（token / 次数 / 金额各不相同） */
  format: (n: number) => string
  /** 同 stack 的序列在同一行堆叠；不填则独占一行 */
  stack?: string
  /** 该行左侧显示的名称（同一 stack 取第一个出现的） */
  rowLabel?: string
}

export type MiniRow = {
  label: string
  values: Record<string, number>
}

/** 从「2026-11-17 15:00」取出小时部分「15」，时间轴只显示小时 */
export function hourLabel(key: string): string {
  const m = key.match(/(\d{2}):00$/)
  return m ? m[1] : key.slice(-5, -3)
}

/** 小时桶转时间段文案："2026-11-17 15:00" → "15-16"，末段为 "23-24"。 */
export function hourRange(key: string): string {
  const m = key.match(/(\d{2}):00$/)
  if (!m) return key
  const start = Number(m[1])
  return `${m[1]}-${String(start + 1).padStart(2, '0')}`
}

export function SmallMultiples({
  rows,
  series,
  active,
  onActivate,
  onHover,
  maxIndex,
}: {
  rows: MiniRow[]
  series: MiniSeries[]
  /** 已点选并固定的小时下标；用于高亮与底部统计 */
  active: number | null
  /** 点选某小时：传 null 取消。悬停不调用此函数 */
  onActivate: (index: number | null) => void
  /** 悬停的小时变化时回调（含离开的 null），供上层同步读数 */
  onHover?: (index: number | null) => void
  /** 只绘制到该下标（含）；用于隐藏今天尚未到达的小时 */
  maxIndex?: number
}) {
  // 悬停只做预览，点选才固定。两者分开存，避免「第一次点击被悬停状态吃掉」。
  // 有固定选中时优先显示选中列，鼠标悬停只在该列之外提供 tooltip，不再抢读数。
  const [hover, setHover] = useState<number | null>(null)
  const shownIndex = active ?? hover
  const rootRef = useRef<HTMLDivElement>(null)

  // 悬停状态变化时通知上层，让主图外的读数与底部统计跟着走
  useEffect(() => {
    onHover?.(hover)
  }, [hover, onHover])

  // 点击图表以外的区域取消选中；图表内部由柱子自己处理。
  // 同时清掉悬停：触屏点过柱子后浏览器会补一次 mouseenter，若不清，
  // 读数会一直停在那个小时，看起来像「选中了取消不掉」。
  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (rootRef.current?.contains(e.target as Node)) return
      setHover(null)
      if (active !== null) onActivate(null)
    }
    document.addEventListener('pointerdown', onDown)
    return () => document.removeEventListener('pointerdown', onDown)
  }, [active, onActivate])

  // 按 stack 分行：没有 stack 的序列各自独占一行
  const groups: { key: string; label: string; items: MiniSeries[] }[] = []
  for (const s of series) {
    const gk = s.stack ?? s.key
    const hit = groups.find((g) => g.key === gk)
    if (hit) hit.items.push(s)
    else groups.push({ key: gk, label: s.rowLabel ?? s.label, items: [s] })
  }

  const shown = maxIndex === undefined ? rows.length : Math.min(rows.length, maxIndex + 1)

  return (
    <div
      ref={rootRef}
      className={`sm${shownIndex !== null ? ' has-active' : ''}`}
      onMouseLeave={() => setHover(null)}
    >
      {groups.map((g) => {
        // 同一行的多段堆叠共用一个纵轴上限，才能看出构成比例
        const max = Math.max(
          1,
          ...rows.slice(0, shown).map((r) => g.items.reduce((sum, s) => sum + (r.values[s.key] ?? 0), 0)),
        )
        return (
          <div className="sm-row" key={g.key}>
            <span className="sm-lbl">
              {g.items.map((s) => (
                <i key={s.key} style={{ background: s.color }} title={s.label} />
              ))}
              {g.label}
            </span>
            <div className="sm-plot">
              {rows.map((r, i) => {
                const total = g.items.reduce((sum, s) => sum + (r.values[s.key] ?? 0), 0)
                const title =
                  `${r.label} ${g.label} ${g.items.map((s) => s.format(r.values[s.key] ?? 0)).join(' / ')}`
                return (
                  <div
                    key={i}
                    className={`sb-slot${i === shownIndex ? ' on' : ''}${i === active ? ' pin' : ''}`}
                    title={i < shown ? title : ''}
                    onMouseEnter={() => i < shown && setHover(i)}
                    onClick={() => {
                      // 未来时段没有数据，不参与选中
                      if (i >= shown) return
                      onActivate(i === active ? null : i)
                    }}
                  >
                    {i < shown
                      ? g.items.map((s) => {
                          const v = r.values[s.key] ?? 0
                          if (v <= 0) return null
                          return (
                            <div
                              key={s.key}
                              className="sb"
                              style={{ height: `${(v / max) * 100}%`, background: s.color }}
                            />
                          )
                        })
                      : null}
                    {/* 未来时段留空，但保留占位以便点击与对齐 */}
                    {i < shown && total <= 0 ? <div className="sb sb-zero" /> : null}
                  </div>
                )
              })}
            </div>
            <span className="sm-max">{g.items[0].format(max)}</span>
          </div>
        )
      })}
      <div className="sm-axis">
        {rows.map((r, i) => (
          <span
            key={i}
            className={`${i === shownIndex ? 'on' : ''}${i === active ? ' pin' : ''}`}
            onClick={() => {
              if (i >= shown) return
              onActivate(i === active ? null : i)
            }}
            title={i < shown ? `选中 ${hourLabel(r.label)} 点` : ''}
          >
            {i % 4 === 0 || i === shownIndex ? hourLabel(r.label) : ''}
          </span>
        ))}
      </div>
    </div>
  )
}

/* ---------- 环形图例（可点击隐藏） ---------- */

export function Legend({
  series,
  hidden,
  onToggle,
}: {
  series: SeriesRow[]
  hidden: Record<string, boolean>
  onToggle: (key: string) => void
}) {
  return (
    <div className="legend">
      {series.map((s) => (
        <span
          key={s.key}
          className={hidden[s.key] ? 'off' : ''}
          onClick={() => onToggle(s.key)}
          title={hidden[s.key] ? '点击显示' : '点击隐藏'}
        >
          <i style={{ background: s.color }} />
          {s.label}
        </span>
      ))}
    </div>
  )
}

/* ---------- 迷你柱（近 7 日） ---------- */

export function MiniBars({ values }: { values: number[] }) {
  const max = Math.max(1, ...values)
  return (
    <span className="spark-cell">
      {values.map((v, i) => (
        <span key={i} className="mb" title={fmtInt(v)}>
          <i style={{ height: `${Math.max(2, (v / max) * 16)}px` }} />
        </span>
      ))}
    </span>
  )
}

/* ---------- 回退入口（v2 → 经典版） ---------- */

export function ClassicLink() {
  const navigate = useNavigate()
  return (
    <button type="button" className="btn ghost" onClick={() => navigate('/')} title="回到经典版界面">
      经典版
    </button>
  )
}

/* ---------- 数值格式化 ---------- */

export function fmtInt(n: number | undefined): string {
  return (n ?? 0).toLocaleString('en-US')
}

export function fmtCompact(n: number | undefined): string {
  const v = n ?? 0
  if (v < 1000) return String(v)
  if (v < 1_000_000) return `${(v / 1000).toFixed(1)}K`
  return `${(v / 1_000_000).toFixed(2)}M`
}

export function fmtTime(ts: number): string {
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

export function fmtCountdown(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = s % 60
  const mm = String(m).padStart(2, '0')
  const sss = String(ss).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${sss}` : `${mm}:${sss}`
}

/** useTick 每秒触发一次重渲染，用于倒计时。 */
export function useTick(intervalMs = 1000): number {
  const [, setTick] = useState(0)
  useEffect(() => {
    const timer = setInterval(() => setTick((n) => n + 1), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return Date.now()
}
