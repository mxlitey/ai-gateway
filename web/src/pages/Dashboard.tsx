import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type KeyCooldown, type UsageBucket } from '../lib/api'
import { Badge, Card, Empty, ErrorText, Loading } from '../components/ui'
import { toast } from '../lib/toast'
import { fmtDate } from '../components/DatePicker'

export default function Dashboard() {
  // 仪表盘统计固定为当天
  const date = fmtDate(new Date())

  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })
  const keys = useQuery({ queryKey: ['apikeys'], queryFn: api.apiKeys })
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
  const cacheHitRate =
    usageSummary && usageSummary.prompt_tokens > 0
      ? `${((usageSummary.cached_tokens / usageSummary.prompt_tokens) * 100).toFixed(1)}%`
      : '—'

  const maxModelCost = Math.max(1, ...(usageSummary?.by_model.map((b) => b.cost_usd) ?? [1]))
  const maxErrors = Math.max(1, ...errorGroups.map((g) => g.errors.length))

  return (
    <div className="space-y-6">
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">仪表盘</h1>

      {errors.isError ? <ErrorText text={(errors.error as Error).message} /> : null}

      {/* 用量与运行状态一览（用量均为当天） */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 sm:gap-4 xl:grid-cols-7">
        <Stat label="请求数" value={fmtInt(usageSummary?.requests ?? 0)} />
        <Stat label="总 token" value={fmtCompact(usageSummary?.total_tokens ?? 0)} />
        <Stat label="缓存命中率" value={cacheHitRate} />
        <Stat label="市场价" value={fmtUSD(usageSummary?.cost_usd ?? 0)} />
        <Stat label="渠道" value={`${availableChannels} / ${channelList.length}`} />
        <Stat label="客户端密钥" value={`${enabledKeys} / ${keyList.length}`} />
        <Stat label="错误" value={String(dateErrors)} />
      </div>

      {/* 当天用量：按模型 / 按渠道 */}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card
          title="按模型"
          action={
            <span className="text-xs text-zinc-500">{usageSummary?.by_model.length ?? 0} 个</span>
          }
        >
          {usage.isLoading ? (
            <Loading />
          ) : usage.isError ? (
            <ErrorText text={(usage.error as Error).message} />
          ) : !usageSummary || usageSummary.by_model.length === 0 ? (
            <Empty text="当天没有调用记录" />
          ) : (
            <ul className="space-y-3">
              {usageSummary.by_model.map((b) => (
                <li key={b.key} className="space-y-1">
                  <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 text-sm">
                    <span className="min-w-0 flex-1 truncate font-mono text-xs text-zinc-700 dark:text-zinc-300">
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
          )}
        </Card>

        <Card title="按渠道">
          {usage.isLoading ? (
            <Loading />
          ) : !usageSummary || usageSummary.by_channel.length === 0 ? (
            <Empty text="当天没有调用记录" />
          ) : (
            <BucketTable buckets={usageSummary.by_channel} />
          )}
        </Card>
      </div>

      {/* 左：错误分布 + 插件；右：临时禁用 + 快速开始 */}
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="space-y-4">
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

        <div className="space-y-4">
          <KeyCooldownCard />

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
      </div>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-zinc-200 bg-white p-3 sm:p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <p className="text-xs text-zinc-500 dark:text-zinc-400">{label}</p>
      <p className="mt-1 truncate font-mono text-lg font-semibold text-zinc-800 sm:text-xl dark:text-zinc-100">
        {value}
      </p>
    </div>
  )
}

// BucketTable 按渠道的分组统计表
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

// KeyCooldownCard 展示当前被临时禁用的上游密钥，倒计时并支持手动恢复。
function KeyCooldownCard() {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['key-cooldowns'],
    queryFn: api.keyCooldowns,
    // 冷却条目会自然到期，定期重取保持列表准确
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

  // 每秒刷新一次本地倒计时
  const [, setTick] = useState(0)
  useEffect(() => {
    const timer = setInterval(() => setTick((n) => n + 1), 1000)
    return () => clearInterval(timer)
  }, [])

  const items = query.data?.items ?? []
  const now = Date.now()

  return (
    <Card
      title="临时禁用"
      action={
        items.length > 0 ? (
          <span className="text-xs text-zinc-500">{items.length} 个</span>
        ) : undefined
      }
    >
      {query.isError ? (
        <ErrorText text={(query.error as Error).message} />
      ) : items.length === 0 ? (
        <p className="py-2 text-sm text-zinc-500 dark:text-zinc-400">当前没有被临时禁用的密钥</p>
      ) : (
        <ul className="space-y-1.5">
          {items.map((c) => {
            const remain = Math.max(0, Math.ceil((c.cooldown_until - now) / 1000))
            return (
              <li
                key={`${c.channel_id}:${c.key_hint}`}
                className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs"
              >
                <span className="min-w-0 truncate text-zinc-700 dark:text-zinc-300">
                  {c.channel_name || c.channel_id}
                  <span className="ml-1.5 font-mono text-[11px] text-zinc-500">{c.key_hint}</span>
                </span>
                <span className="flex shrink-0 items-center gap-2.5">
                  <Badge tone="warn">连续失败 {c.fail_streak} 次</Badge>
                  <span className="font-mono text-[11px] text-zinc-500 tabular-nums">
                    {fmtCountdown(remain)}
                  </span>
                  <button
                    type="button"
                    title="立即恢复"
                    disabled={reset.isPending}
                    onClick={() => reset.mutate(c)}
                    className="rounded-md p-1 text-zinc-400 transition-colors hover:bg-zinc-100 hover:text-zinc-600 disabled:opacity-50 dark:hover:bg-zinc-800 dark:hover:text-zinc-300"
                  >
                    <ResetIcon />
                  </button>
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}

// fmtCountdown 把剩余秒数格式化为 mm:ss 或 h:mm:ss。
function fmtCountdown(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = s % 60
  const mm = String(m).padStart(2, '0')
  const sss = String(ss).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${sss}` : `${mm}:${sss}`
}

// ResetIcon 简洁的重置（旋转箭头）图标。
function ResetIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 12a9 9 0 1 0 3-6.7" />
      <path d="M3 4v5h5" />
    </svg>
  )
}