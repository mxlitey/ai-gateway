import {
  useEffect,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type TextareaHTMLAttributes,
} from 'react'
import { onToast, type ToastItem } from '../lib/toast'

/* ---------- Button ---------- */

type ButtonVariant = 'default' | 'primary' | 'danger' | 'ghost'
type ButtonSize = 'md' | 'sm'

const buttonStyles: Record<ButtonVariant, string> = {
  // 与窗口一致：默认按钮是浅灰填充、无边框
  default:
    'bg-zinc-100 text-zinc-800 border border-transparent hover:bg-zinc-200 dark:bg-zinc-800 dark:text-zinc-100 dark:hover:bg-zinc-700',
  primary: 'bg-emerald-600 text-white border border-emerald-600 hover:bg-emerald-500',
  danger: 'bg-red-600 text-white border border-red-600 hover:bg-red-500',
  ghost:
    'bg-transparent text-zinc-600 border border-transparent hover:bg-zinc-100 dark:text-zinc-300 dark:hover:bg-zinc-800',
}

const buttonSizes: Record<ButtonSize, string> = {
  // 窄屏加大点击热区，避免手指点到相邻按钮
  md: 'gap-1.5 rounded-lg px-3 py-1.5 text-sm max-md:py-2.5',
  sm: 'gap-1 rounded-md px-2 py-1 text-xs max-md:py-1.5',
}

export function Button({
  variant = 'default',
  size = 'md',
  className = '',
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: ButtonSize }) {
  return (
    <button
      {...rest}
      className={`inline-flex items-center justify-center font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${buttonSizes[size]} ${buttonStyles[variant]} ${className}`}
    />
  )
}

/* ---------- Segmented（分段控件，样式对齐桌面窗口） ---------- */

export function Segmented<T extends string>({
  value,
  options,
  onChange,
  className = '',
}: {
  value: T
  options: { value: T; label: string }[]
  onChange: (v: T) => void
  className?: string
}) {
  return (
    <div
      className={`inline-flex max-w-full gap-0.5 overflow-x-auto rounded-[10px] bg-zinc-100 p-0.5 dark:bg-zinc-800 ${className}`}
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          onClick={() => onChange(o.value)}
          className={`h-7 flex-none rounded-[8px] px-3 text-xs transition-colors max-md:h-10 ${
            value === o.value
              ? 'bg-white font-semibold text-zinc-800 shadow-sm dark:bg-zinc-700 dark:text-zinc-100'
              : 'text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-100'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

/* ---------- Inputs ---------- */

const fieldClass =
  'w-full rounded-lg border border-zinc-300 bg-white px-3 py-2 text-sm text-zinc-900 outline-none placeholder:text-zinc-400 focus:border-emerald-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100'

export function Input({ className = '', ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...rest} className={`${fieldClass} ${className}`} />
}

export function Textarea({ className = '', ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...rest} className={`${fieldClass} font-mono text-[13px] ${className}`} />
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block space-y-1.5">
      <span className="text-sm font-medium text-zinc-700 dark:text-zinc-300">{label}</span>
      {children}
      {hint ? <span className="block text-xs text-zinc-500 dark:text-zinc-400">{hint}</span> : null}
    </label>
  )
}

/* ---------- Card ---------- */

export function Card({
  title,
  action,
  children,
  className = '',
}: {
  title?: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section
      className={`rounded-xl border border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900 ${className}`}
    >
      {title || action ? (
        <header className="flex flex-wrap items-center justify-between gap-2 border-b border-zinc-200 px-4 py-3 dark:border-zinc-800">
          <h2 className="min-w-0 truncate text-sm font-semibold text-zinc-800 dark:text-zinc-100">
            {title}
          </h2>
          {action}
        </header>
      ) : null}
      <div className="p-4">{children}</div>
    </section>
  )
}

/* ---------- Badge ---------- */

type BadgeTone = 'ok' | 'off' | 'warn' | 'info'

const badgeStyles: Record<BadgeTone, string> = {
  // 成功态沿用窗口的状态绿（#34c759），避免与蓝色主色/信息色混淆
  ok: 'bg-green-50 text-green-700 border-green-200 dark:bg-green-950 dark:text-green-300 dark:border-green-900',
  off: 'bg-zinc-100 text-zinc-600 border-zinc-200 dark:bg-zinc-800 dark:text-zinc-400 dark:border-zinc-700',
  warn: 'bg-amber-50 text-amber-700 border-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:border-amber-900',
  info: 'bg-sky-50 text-sky-700 border-sky-200 dark:bg-sky-950 dark:text-sky-300 dark:border-sky-900',
}

export function Badge({ tone = 'info', children }: { tone?: BadgeTone; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-xs font-medium ${badgeStyles[tone]}`}
    >
      {children}
    </span>
  )
}

/* ---------- Switch（iOS 风格开关，用于卡片上快速启停） ---------- */

export function Switch({
  checked,
  onChange,
  disabled = false,
  title,
  className = '',
}: {
  checked: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
  title?: string
  className?: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      title={title}
      onClick={(e) => {
        e.stopPropagation()
        onChange(!checked)
      }}
      onKeyDown={(e) => e.stopPropagation()}
      className={`relative inline-flex h-5 w-9 flex-none items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
        checked ? 'bg-[#34c759]' : 'bg-zinc-300 dark:bg-zinc-600'
      } ${className}`}
    >
      <span
        className={`inline-block size-4 rounded-full bg-white shadow-sm transition-transform ${
          checked ? 'translate-x-[18px]' : 'translate-x-0.5'
        }`}
      />
    </button>
  )
}

/* ---------- 图标按钮与常用图标 ---------- */

export function IconButton({
  title,
  className = '',
  children,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { title: string }) {
  return (
    <button
      {...rest}
      type="button"
      title={title}
      aria-label={title}
      className={`inline-flex flex-none items-center justify-center rounded-md p-1.5 text-zinc-500 transition-colors hover:bg-zinc-100 hover:text-zinc-700 disabled:cursor-not-allowed disabled:opacity-40 max-md:min-h-10 max-md:min-w-10 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-200 ${className}`}
    >
      {children}
    </button>
  )
}

// Icon 统一 15×15 线性图标，描边随文字色。
function Icon({ children }: { children: ReactNode }) {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      {children}
    </svg>
  )
}

export function RefreshIcon() {
  return (
    <Icon>
      <path d="M13.5 8a5.5 5.5 0 1 1-1.7-3.95" />
      <path d="M13.5 2.5V5H11" />
    </Icon>
  )
}

export function CrossIcon() {
  return (
    <Icon>
      <path d="M4 4l8 8M12 4l-8 8" />
    </Icon>
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
  // 弹层打开时锁定背景滚动（手机端避免背后的长页面跟着滑动）
  useEffect(() => {
    if (!open) return
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [open])

  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center overflow-y-auto bg-black/40 p-0 sm:items-start sm:p-6">
      <div
        className={`flex max-h-[92dvh] w-full flex-col rounded-t-2xl border border-zinc-200 bg-white shadow-xl sm:mt-10 sm:max-h-[calc(100dvh-5rem)] sm:rounded-xl dark:border-zinc-800 dark:bg-zinc-900 ${
          wide ? 'sm:max-w-3xl' : 'sm:max-w-lg'
        }`}
      >
        <header className="flex shrink-0 items-center justify-between gap-3 border-b border-zinc-200 px-4 py-3 dark:border-zinc-800">
          <h3 className="min-w-0 truncate text-sm font-semibold text-zinc-800 dark:text-zinc-100">
            {title}
          </h3>
          <button
            onClick={onClose}
            className="-mr-1 flex-none rounded-md px-2 py-1 text-zinc-500 hover:bg-zinc-100 active:bg-zinc-100 max-sm:min-h-10 max-sm:min-w-10 sm:py-0.5 dark:hover:bg-zinc-800"
            aria-label="关闭"
          >
            ✕
          </button>
        </header>
        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-4">{children}</div>
        {footer ? (
          <footer className="flex shrink-0 flex-wrap justify-end gap-2 border-t border-zinc-200 px-4 py-3 pb-[calc(0.75rem+env(safe-area-inset-bottom))] sm:pb-3 dark:border-zinc-800">
            {footer}
          </footer>
        ) : null}
      </div>
    </div>
  )
}

/* ---------- 状态占位 ---------- */

export function Empty({ text, action }: { text: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 py-12 text-center">
      <p className="text-sm text-zinc-500 dark:text-zinc-400">{text}</p>
      {action}
    </div>
  )
}

export function Loading({ text = '加载中…' }: { text?: string }) {
  return <p className="py-8 text-center text-sm text-zinc-500 dark:text-zinc-400">{text}</p>
}

export function ErrorText({ text }: { text: string }) {
  return (
    <p className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
      {text}
    </p>
  )
}

/* ---------- Toaster ---------- */

export function Toaster() {
  const [items, setItems] = useState<ToastItem[]>([])

  useEffect(() => {
    return onToast((item) => {
      setItems((prev) => [...prev, item])
      window.setTimeout(() => {
        setItems((prev) => prev.filter((i) => i.id !== item.id))
      }, 3200)
    })
  }, [])

  return (
    <div className="pointer-events-none fixed inset-x-4 bottom-[calc(1rem+env(safe-area-inset-bottom))] z-[60] flex flex-col items-stretch gap-2 sm:inset-x-auto sm:right-4 sm:w-80 sm:items-end">
      {items.map((item) => (
        <div
          key={item.id}
          className={`pointer-events-auto rounded-lg border px-3 py-2 text-sm shadow-lg ${
            item.kind === 'ok'
              ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200'
              : 'border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200'
          }`}
        >
          {item.text}
        </div>
      ))}
    </div>
  )
}