import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, type UsageBucket } from '../lib/api'
import { Badge, Card, Empty, ErrorText, Loading } from '../components/ui'
import DatePicker, { fmtDate } from '../components/DatePicker'

export default function Dashboard() {
  // 用量分析（按模型 / 按渠道）共用的统计日期，默认当天
  const [date, setDate] = useState(fmtDate(new Date()))

  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })
  const keys = useQuery({ queryKey: ['apikeys'], queryFn: api.apiKeys })
  // 错误分布与「用量分析」共用同一个日期选择器
  const errors = useQuery({ queryKey: ['errors', date], queryFn: () => api.errors(date) })
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.plugins })
  const usage = useQuery({
    queryKey: ['usage-summary', date],
    queryFn: () => api.usageSummary({ days: 1, date }),
  })

  if (channels.isLoading || keys.isLoading) return <Loading />

  const channelList = channels.data ?? []
  const keyList = keys.data ?? []
  const errorGroups = errors.data?.channels ?? []
  const pluginList = plugins.data ?? []

  const availableChannels = channelList.filter(
    (c) => c.enabled && c.keys.some((k) => k.enabled),
  ).length
  const enabledKeys = keyList.filter((k) => k.enabled).length
  const dateErrors = errorGroups.reduce((sum, g) => sum + g.errors.length, 0)
  const runningPlugins = pluginList.filter((p) => p.last_status === 'running').length

  const usageSummary = usage.data?.summary
  const maxModelCost = Math.max(1, ...(usageSummary?.by_model.map((b) => b.cost_usd) ?? [1]))

  const maxErrors = Math.max(1, ...errorGroups.map((g) => g.errors.length))

  return (
    <div className="space-y-6">
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">仪表盘</h1>

      {errors.isError ? <ErrorText text={(errors.error as Error).message} /> : null}

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <Stat label="渠道" value={`${availableChannels} / ${channelList.length}`} hint="可用 / 总数" />
        <Stat label="客户端密钥" value={`${enabledKeys} / ${keyList.length}`} hint="启用 / 总数" />
        <Stat label="错误" value={String(dateErrors)} hint="条" />
      </div>

      <div className="space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="text-sm font-semibold text-zinc-700 dark:text-zinc-200">用量分析</h2>
          <DatePicker value={date} onChange={setDate} />
        </div>

        {usage.isLoading ? (
          <Loading />
        ) : usage.isError ? (
          <ErrorText text={(usage.error as Error).message} />
        ) : !usageSummary || usageSummary.requests === 0 ? (
          <Card title={`${date} 的用量`}>
            <Empty text={`${date} 没有调用记录`} />
          </Card>
        ) : (
          <div className="grid gap-4 lg:grid-cols-2">
            <Card
              title="按模型"
              action={<span className="text-xs text-zinc-500">{usageSummary.by_model.length} 个</span>}
            >
              <ul className="space-y-3">
                {usageSummary.by_model.map((b) => (
                  <li key={b.key} className="space-y-1">
                    <div className="flex items-center justify-between gap-3 text-sm">
                      <span className="truncate font-mono text-xs text-zinc-700 dark:text-zinc-300">
                        {b.key}
                      </span>
                      <span className="shrink-0 font-mono text-xs text-zinc-500">
                        {fmtInt(b.requests)} 次 · {fmtCompact(b.total_tokens)} tok ·{' '}
                        <span className="text-emerald-600 dark:text-emerald-400">
                          {fmtUSD(b.cost_usd)}
                        </span>
                      </span>
                    </div>
                    <div className="h-1.5 rounded-full bg-zinc-100 dark:bg-zinc-800">
                      <div
                        className="h-1.5 rounded-full bg-emerald-500"
                        style={{ width: `${Math.min(100, (b.cost_usd / maxModelCost) * 100)}%` }}
                      />
                    </div>
                  </li>
                ))}
              </ul>
            </Card>

            <Card title="按渠道">
              <BucketTable buckets={usageSummary.by_channel} />
            </Card>
          </div>
        )}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card
          title="错误分布"
          action={
            <Link to="/errors" className="text-xs text-emerald-600 hover:underline dark:text-emerald-400">
              查看错误日志
            </Link>
          }
        >
          {errors.isLoading ? (
            <Loading />
          ) : errorGroups.length === 0 ? (
            <p className="py-6 text-center text-sm text-zinc-500 dark:text-zinc-400">
              {date} 没有错误记录
            </p>
          ) : (
            <ul className="space-y-3">
              {errorGroups
                .slice()
                .sort((a, b) => b.errors.length - a.errors.length)
                .map((g) => (
                  <li key={g.channel_id} className="space-y-1">
                    <div className="flex items-center justify-between text-sm">
                      <span className="truncate text-zinc-700 dark:text-zinc-300">{g.channel_name}</span>
                      <span className="font-mono text-xs text-zinc-500">{g.errors.length}</span>
                    </div>
                    <div className="h-1.5 rounded-full bg-zinc-100 dark:bg-zinc-800">
                      <div
                        className="h-1.5 rounded-full bg-red-500"
                        style={{ width: `${(g.errors.length / maxErrors) * 100}%` }}
                      />
                    </div>
                  </li>
                ))}
            </ul>
          )}
        </Card>

        <Card
          title="插件"
          action={
            <Link to="/plugins" className="text-xs text-emerald-600 hover:underline dark:text-emerald-400">
              管理插件
            </Link>
          }
        >
          {pluginList.length === 0 ? (
            <p className="py-6 text-center text-sm text-zinc-500 dark:text-zinc-400">
              尚未发现插件
            </p>
          ) : (
            <ul className="space-y-2">
              {pluginList.map((p) => (
                <li key={p.id} className="flex items-center justify-between text-sm">
                  <span className="text-zinc-700 dark:text-zinc-300">{p.name}</span>
                  <Badge tone={p.last_status === 'running' ? 'ok' : 'off'}>{p.last_status}</Badge>
                </li>
              ))}
            </ul>
          )}
          <p className="mt-3 text-xs text-zinc-500 dark:text-zinc-500">
            运行中 {runningPlugins} / {pluginList.length}
          </p>
        </Card>
      </div>

      <Card title="快速开始">
        <ol className="space-y-2 text-sm text-zinc-600 dark:text-zinc-400">
          <li>
            1. 到{' '}
            <Link to="/channels" className="text-emerald-600 hover:underline dark:text-emerald-400">
              渠道管理
            </Link>{' '}
            添加一个上游渠道（填 API 主机与密钥）
          </li>
          <li>
            2. 到{' '}
            <Link to="/keys" className="text-emerald-600 hover:underline dark:text-emerald-400">
              API 密钥
            </Link>{' '}
            生成一个客户端密钥
          </li>
          <li>
            3. 客户端 Base URL 填 <code className="text-xs">{window.location.origin}/v1</code>
            ，API Key 填刚生成的密钥
          </li>
        </ol>
      </Card>
    </div>
  )
}

function Stat({ label, value, hint }: { label: string; value: string; hint: string }) {
  return (
    <div className="rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <p className="text-xs text-zinc-500 dark:text-zinc-400">{label}</p>
      <p className="mt-1 font-mono text-xl font-semibold text-zinc-800 dark:text-zinc-100">{value}</p>
      <p className="mt-0.5 text-xs text-zinc-400 dark:text-zinc-500">{hint}</p>
    </div>
  )
}

// BucketTable 按渠道 / 客户端密钥的分组统计表
function BucketTable({ buckets }: { buckets: UsageBucket[] }) {
  return (
    <table className="w-full table-fixed text-sm">
      <colgroup>
        <col className="w-[40%]" />
        <col className="w-[20%]" />
        <col className="w-[20%]" />
        <col className="w-[20%]" />
      </colgroup>
      <thead>
        <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
          <th className="py-2 pr-3 font-medium">名称</th>
          <th className="py-2 pr-3 text-right font-medium">请求</th>
          <th className="py-2 pr-3 text-right font-medium">token</th>
          <th className="py-2 text-right font-medium">等价费用</th>
        </tr>
      </thead>
      <tbody>
        {buckets.map((b) => (
          <tr key={b.key} className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
            <td className="py-2 pr-3 text-xs text-zinc-700 dark:text-zinc-300">
              <span className="block truncate">{b.key}</span>
            </td>
            <td className="py-2 pr-3 text-right font-mono text-xs">{fmtInt(b.requests)}</td>
            <td className="py-2 pr-3 text-right font-mono text-xs">{fmtCompact(b.total_tokens)}</td>
            <td className="py-2 text-right font-mono text-xs text-emerald-600 dark:text-emerald-400">
              {fmtUSD(b.cost_usd)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
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