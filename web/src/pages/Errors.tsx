import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type ErrorGroup, type ErrorLog } from '../lib/api'
import { toast } from '../lib/toast'
import { Button, Card, Empty, ErrorText, Loading } from '../components/ui'
import DatePicker, { fmtDate } from '../components/DatePicker'

export default function Errors() {
  // 默认看今天（北京时区），与仪表盘的「今日错误分布」口径一致
  const [date, setDate] = useState(fmtDate(new Date()))

  const errors = useQuery({ queryKey: ['errors', date], queryFn: () => api.errors(date) })

  const groups = errors.data?.channels ?? []
  const total = groups.reduce((sum, g) => sum + g.errors.length, 0)

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">错误日志</h1>
        <div className="flex flex-wrap items-center gap-2">
          <DatePicker value={date} onChange={setDate} />
          <Button onClick={() => errors.refetch()}>刷新</Button>
        </div>
      </div>

      {errors.isLoading ? (
        <Loading />
      ) : errors.isError ? (
        <ErrorText text={(errors.error as Error).message} />
      ) : groups.length === 0 ? (
        <Card title={`${date} 的错误日志`}>
          <Empty text={`${date} 没有错误记录`} />
        </Card>
      ) : (
        <>
          <p className="text-xs text-zinc-500 dark:text-zinc-400">
            {date} 共 {total} 条错误，来自 {groups.length} 个渠道
          </p>
          {groups.map((g) => (
            <ChannelErrors key={g.channel_id} group={g} />
          ))}
        </>
      )}
    </div>
  )
}

// ChannelErrors 一个渠道的错误列表：表格行 + 点击展开详情 + 复制整条记录
function ChannelErrors({ group }: { group: ErrorGroup }) {
  const [openId, setOpenId] = useState<number | null>(null)

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <span className="size-2 flex-none rounded-full bg-red-500" />
          {group.channel_name}
          <span className="text-xs font-normal text-zinc-500 dark:text-zinc-400">
            {group.errors.length} 条
          </span>
        </span>
      }
    >
      <div className="grid grid-cols-[1rem_8.5rem_minmax(0,1.2fr)_4.5rem_minmax(0,1.8fr)_1.75rem] items-center gap-3 border-b border-zinc-200 pb-2 text-xs font-medium text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
        <span />
        <span>时间</span>
        <span>模型</span>
        <span>状态</span>
        <span>错误信息</span>
        <span />
      </div>
      {group.errors.map((e) => (
        <ErrorRow
          key={e.id}
          e={e}
          open={openId === e.id}
          onToggle={() => setOpenId(openId === e.id ? null : e.id)}
        />
      ))}
    </Card>
  )
}

function ErrorRow({ e, open, onToggle }: { e: ErrorLog; open: boolean; onToggle: () => void }) {
  const statusText = e.status > 0 ? String(e.status) : 'ERR'

  return (
    <div className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
      <div
        onClick={onToggle}
        title="点击查看完整详情"
        className="grid cursor-pointer grid-cols-[1rem_8.5rem_minmax(0,1.2fr)_4.5rem_minmax(0,1.8fr)_1.75rem] items-start gap-3 rounded-lg py-2.5 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
      >
        <span
          className={`mt-0.5 text-[10px] text-zinc-400 transition-transform ${open ? 'rotate-90' : ''}`}
        >
          ▶
        </span>
        <span className="text-xs text-zinc-500 dark:text-zinc-400">{fmtTime(e.created_at)}</span>
        <span className="min-w-0">
          <span className="block truncate font-mono text-xs text-zinc-700 dark:text-zinc-300">
            {e.model || '-'}
          </span>
          <span className="mt-0.5 block truncate text-[11px] text-zinc-400 dark:text-zinc-500">
            → {e.upstream_model || '-'}
            {e.key_hint ? ` @ ${e.key_hint}` : ''}
          </span>
        </span>
        <span>
          <StatusBadge status={e.status} text={statusText} />
        </span>
        <span className="min-w-0 truncate text-xs text-zinc-600 dark:text-zinc-400">
          {e.message || '-'}
        </span>
        <button
          type="button"
          title="复制整条记录"
          aria-label="复制整条记录"
          onClick={(ev) => {
            ev.stopPropagation()
            copyRecord(e)
          }}
          className="mt-0.5 rounded-md p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
        >
          <svg
            width="14"
            height="14"
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinejoin="round"
          >
            <rect x="5.5" y="5.5" width="8.5" height="8.5" rx="2.2" />
            <path d="M10.5 5.5V4.2A2.2 2.2 0 0 0 8.3 2H4.2A2.2 2.2 0 0 0 2 4.2v4.1a2.2 2.2 0 0 0 2.2 2.2h1.3" />
          </svg>
        </button>
      </div>

      {open ? (
        <div className="grid gap-1 pb-3 pl-7">
          {[
            ['时间', fmtTime(e.created_at)],
            ['渠道', `${e.channel_name}（${e.channel_id}）`],
            ['上游地址', e.base_url || '-'],
            ['模型', e.model || '-'],
            ['上游模型', e.upstream_model || '-'],
            ['密钥提示', e.key_hint || '-'],
            ['状态码', statusText],
            ['错误信息', e.message || '-'],
          ].map(([label, value]) => (
            <div key={label} className="grid grid-cols-[5rem_minmax(0,1fr)] gap-2 py-0.5 text-xs">
              <span className="text-zinc-400 dark:text-zinc-500">{label}</span>
              <span className="min-w-0 whitespace-pre-wrap break-all font-mono text-zinc-600 dark:text-zinc-300">
                {value}
              </span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}

// StatusBadge 状态码着色：网络错误/5xx 红、404 黄、其它蓝
function StatusBadge({ status, text }: { status: number; text: string }) {
  const tone =
    status === 0 || status >= 500
      ? 'border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300'
      : status === 404
        ? 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300'
        : 'border-sky-200 bg-sky-50 text-sky-700 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-300'
  return (
    <span
      className={`inline-flex items-center rounded-md border px-1.5 py-0.5 font-mono text-xs font-medium ${tone}`}
    >
      {text}
    </span>
  )
}

// copyRecord 复制整条错误记录（时间 / 渠道 / 上游地址 / 模型 / 上游模型 / 密钥 / 状态码 / 错误信息）
async function copyRecord(e: ErrorLog) {
  const text = [
    `时间: ${fmtTime(e.created_at)}`,
    `渠道: ${e.channel_name}（${e.channel_id}）`,
    `上游地址: ${e.base_url || '-'}`,
    `模型: ${e.model || '-'}`,
    `上游模型: ${e.upstream_model || '-'}`,
    `密钥提示: ${e.key_hint || '-'}`,
    `状态码: ${e.status > 0 ? e.status : 'ERR'}`,
    `错误信息: ${e.message || '-'}`,
  ].join('\n')

  try {
    await navigator.clipboard.writeText(text)
    toast('ok', '已复制整条记录')
  } catch {
    // 剪贴板 API 不可用时回退
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    try {
      document.execCommand('copy')
      toast('ok', '已复制整条记录')
    } catch {
      toast('err', '复制失败')
    }
    document.body.removeChild(ta)
  }
}

// fmtTime 按本地时区输出 YYYY-MM-DD HH:mm:ss
function fmtTime(ms: number): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}