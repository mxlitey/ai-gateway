import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type ErrorLog } from '../lib/api'
import { bjDateString, bjFmtDateTime } from '../lib/tz'
import { toast } from '../lib/toast'
import DatePicker from '../components/DatePicker'
import { Button, Empty, ErrorText, Loading, PageHead, Segmented } from './ui'

// 明细行采用「表格行 + 点击展开详情」；窄屏由 CSS 切换为堆叠行

type SourceFilter = 'all' | 'gateway' | 'upstream'

export default function V2Errors() {
  const [date, setDate] = useState(bjDateString())
  const [openId, setOpenId] = useState<number | null>(null)
  const [source, setSource] = useState<SourceFilter>('all')

  const errors = useQuery({ queryKey: ['errors', date], queryFn: () => api.errors(date) })

  const groups = errors.data?.channels ?? []
  const all = groups.flatMap((g) => g.errors).sort((a, b) => b.created_at - a.created_at)
  const gatewayItems = all.filter((e) => e.source === 'gateway')
  const upstreamItems = all.filter((e) => e.source === 'upstream')
  const items = source === 'all' ? all : source === 'gateway' ? gatewayItems : upstreamItems
  const channelCount = new Set(items.filter((e) => e.channel_id).map((e) => e.channel_id)).size

  const summary =
    source === 'gateway'
      ? `${date} ${items.length} 条，均在转发前被网关拒绝，未请求上游`
      : source === 'upstream'
        ? `${date} ${items.length} 条，来自 ${channelCount} 个渠道`
        : `${date} 共 ${all.length} 条：客户端→网关 ${gatewayItems.length} 条，网关→上游 ${upstreamItems.length} 条`

  return (
    <div className="stack">
      <PageHead
        title="错误日志"
        actions={
          <>
            <DatePicker value={date} onChange={setDate} />
            <Button variant="ghost" onClick={() => errors.refetch()}>
              刷新
            </Button>
          </>
        }
      />

      {errors.isLoading ? (
        <div className="panel">
          <Loading />
        </div>
      ) : errors.isError ? (
        <ErrorText text={(errors.error as Error).message} />
      ) : (
        <>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10 }}>
            <Segmented
              value={source}
              onChange={setSource}
              options={[
                { value: 'all', label: `全部 ${all.length}` },
                { value: 'gateway', label: `客户端→网关 ${gatewayItems.length}` },
                { value: 'upstream', label: `网关→上游 ${upstreamItems.length}` },
              ]}
            />
            <span className="tiny faint">{summary}</span>
          </div>

          {items.length === 0 ? (
            <div className="panel">
              <Empty text={`${date} 没有该来源的错误记录`} />
            </div>
          ) : (
            <div className="panel">
              {/* 宽屏表头 */}
              <div className="err-head">
                <span />
                <span>时间</span>
                <span>渠道</span>
                <span>模型</span>
                <span>状态</span>
                <span>错误信息</span>
                <span />
              </div>
              {items.map((e) => (
                <ErrorRow
                  key={e.id}
                  e={e}
                  open={openId === e.id}
                  onToggle={() => setOpenId(openId === e.id ? null : e.id)}
                />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}

function ErrorRow({ e, open, onToggle }: { e: ErrorLog; open: boolean; onToggle: () => void }) {
  const statusText = e.status > 0 ? String(e.status) : 'ERR'

  return (
    <div className={`err-row${open ? ' open' : ''}`}>
      {/* 宽屏行 */}
      <div className="err-line" onClick={onToggle} title="点击查看完整详情">
        <span className="err-chev">▶</span>
        <span className="err-t">{fmtTime(e.created_at)}</span>
        <span className="err-ch truncate" title={e.channel_name}>
          {e.channel_name || '-'}
        </span>
        <span style={{ minWidth: 0 }}>
          <span className="mono truncate" style={{ display: 'block', fontSize: 11 }}>
            {e.model || '-'}
          </span>
          <span className="faint truncate" style={{ display: 'block', fontSize: 10 }}>
            → {e.upstream_model || '-'}
            {e.key_hint ? ` @ ${e.key_hint}` : ''}
          </span>
        </span>
        <span>
          <StatusBadge status={e.status} text={statusText} />
        </span>
        <span className="err-msg truncate">{e.message || '-'}</span>
        <button
          type="button"
          title="复制整条记录"
          aria-label="复制整条记录"
          onClick={(ev) => {
            ev.stopPropagation()
            void copyRecord(e)
          }}
          className="btn ghost sm"
          style={{ padding: '1px 4px' }}
        >
          <CopyIcon />
        </button>
      </div>

      {/* 窄屏堆叠行：内容区可点展开，复制按钮独立在最右 */}
      <div className="err-mobile">
        <div
          onClick={onToggle}
          style={{
            display: 'flex',
            alignItems: 'flex-start',
            gap: 8,
            cursor: 'pointer',
          }}
        >
          <div style={{ minWidth: 0, flex: 1 }}>
            <div className="mono truncate" style={{ fontSize: 11 }}>
              {e.model || '-'}
            </div>
            <div className="faint" style={{ fontSize: 10 }}>
              {fmtTime(e.created_at)}
              {e.channel_name ? ` · ${e.channel_name}` : ''}
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 3 }}>
              <StatusBadge status={e.status} text={statusText} />
              <span className="faint truncate" style={{ fontSize: 10 }}>
                {e.message || '-'}
              </span>
            </div>
          </div>
          <button
            type="button"
            title="复制整条记录"
            aria-label="复制整条记录"
            onClick={(ev) => {
              ev.stopPropagation()
              void copyRecord(e)
            }}
            className="btn ghost sm"
            style={{ padding: '2px 5px', flex: 'none' }}
          >
            <CopyIcon />
          </button>
        </div>
      </div>

      {open ? (
        <div className="err-detail">
          {(
            [
              ['时间', fmtTime(e.created_at)],
              ['来源', sourceText(e)],
              ['渠道', e.channel_id ? `${e.channel_name}（${e.channel_id}）` : e.channel_name || '-'],
              ['上游地址', e.base_url || '-'],
              ['模型', e.model || '-'],
              ['上游模型', e.upstream_model || '-'],
              ['上游密钥', e.key_hint || '-'],
              ['状态码', statusText],
              ['错误信息', e.message || '-'],
            ] as [string, string][]
          ).map(([label, value]) => (
            <div key={label} className="err-kv">
              <span>{label}</span>
              <span className="mono">{value}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function CopyIcon() {
  return (
    <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
      <rect x="5.5" y="5.5" width="8.5" height="8.5" rx="2.2" />
      <path d="M10.5 5.5V4.2A2.2 2.2 0 0 0 8.3 2H4.2A2.2 2.2 0 0 0 2 4.2v4.1a2.2 2.2 0 0 0 2.2 2.2h1.3" />
    </svg>
  )
}

/** 状态码着色：网络错误/5xx 红、404 黄、其它蓝 */
function StatusBadge({ status, text }: { status: number; text: string }) {
  const tone = status === 0 || status >= 500 ? 'bad' : status === 404 ? 'warn' : 'info'
  return <span className={`badge ${tone} mono`}>{text}</span>
}

async function copyRecord(e: ErrorLog) {
  const text = [
    `时间: ${fmtTime(e.created_at)}`,
    `来源: ${sourceText(e)}`,
    `渠道: ${e.channel_id ? `${e.channel_name}（${e.channel_id}）` : e.channel_name || '-'}`,
    `上游地址: ${e.base_url || '-'}`,
    `模型: ${e.model || '-'}`,
    `上游模型: ${e.upstream_model || '-'}`,
    `上游密钥: ${e.key_hint || '-'}`,
    `状态码: ${e.status > 0 ? e.status : 'ERR'}`,
    `错误信息: ${e.message || '-'}`,
  ].join('\n')

  try {
    await navigator.clipboard.writeText(text)
    toast('ok', '已复制整条记录')
  } catch {
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

function fmtTime(ms: number): string {
  return bjFmtDateTime(ms)
}

function sourceText(e: ErrorLog): string {
  return e.source === 'gateway' ? '客户端 → 网关' : '网关 → 上游'
}
