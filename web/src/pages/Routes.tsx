import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type ProbeResult, type RouteGroup } from '../lib/api'
import { toast } from '../lib/toast'
import { Badge, Button, Card, Empty, ErrorText, Loading, Modal, Segmented, Switch } from '../components/ui'

export default function ModelRoutes() {
  const queryClient = useQueryClient()
  const routes = useQuery({ queryKey: ['routes'], queryFn: api.routes })
  // 对话模型与生图模型分开展示
  const [tab, setTab] = useState<'chat' | 'image'>('chat')
  const [diag, setDiag] = useState<{ model: string; targets: Record<string, boolean> } | null>(null)
  const [probeRows, setProbeRows] = useState<ProbeResult[] | null>(null)

  // 顺序即优先级：上移/下移后把整组重写成 n-1..0，只提交真正变化的行
  const reorder = useMutation({
    mutationFn: async (p: { model: string; routes: RouteGroup['routes']; from: number; to: number }) => {
      const next = [...p.routes]
      const [moved] = next.splice(p.from, 1)
      next.splice(p.to, 0, moved)
      const changes = next
        .map((route, i) => ({ route, priority: next.length - 1 - i }))
        .filter(({ route, priority }) => route.priority !== priority)
      await Promise.all(
        changes.map(({ route, priority }) => api.setPriority(route.channel_id, p.model, priority)),
      )
    },
    onSuccess: async () => {
      toast('ok', '优先级已更新')
      await queryClient.invalidateQueries({ queryKey: ['routes'] })
      await queryClient.invalidateQueries({ queryKey: ['channels'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const runProbe = useMutation({
    mutationFn: (tasks: { channel_id: string; model: string }[]) => api.testUpstream({ tasks }),
    onSuccess: (res) => setProbeRows(res.results),
    onError: (err: Error) => toast('err', err.message),
  })

  // 按模型维度启停：只影响「本渠道上的这个模型」，不动渠道与其他模型
  const saveModel = useMutation({
    mutationFn: (p: { channelId: string; model: string; enabled: boolean }) =>
      api.setModelEnabled(p.channelId, p.model, p.enabled),
    onSuccess: async () => {
      toast('ok', '已更新模型状态')
      await queryClient.invalidateQueries({ queryKey: ['routes'] })
      await queryClient.invalidateQueries({ queryKey: ['channels'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  // 复制对外模型名，便于直接填到客户端配置里
  const copyModel = async (model: string) => {
    if (await writeClipboard(model)) toast('ok', '已复制模型名 ' + model)
    else toast('err', '复制失败，请手动选中复制')
  }

  if (routes.isLoading) return <Loading />
  if (routes.isError) return <ErrorText text={(routes.error as Error).message} />

  const groups = routes.data ?? []
  // 任一渠道把它标记为生图模型，就归入生图分类
  const isImageGroup = (g: RouteGroup) => g.routes.some((r) => r.image)
  const visibleGroups = groups.filter((g) => (tab === 'image' ? isImageGroup(g) : !isImageGroup(g)))
  const diagGroup = diag ? groups.find((g) => g.model === diag.model) : undefined
  const selectedCount = diagGroup
    ? diagGroup.routes.filter((r) => diag?.targets[r.channel_id]).length
    : 0
  const allSelected = diagGroup ? selectedCount === diagGroup.routes.length && selectedCount > 0 : false

  const openDiag = (model: string) => {
    const group = groups.find((g) => g.model === model)
    const targets: Record<string, boolean> = {}
    for (const r of group?.routes ?? []) targets[r.channel_id] = true
    setDiag({ model, targets })
    setProbeRows(null)
  }

  const toggleTarget = (channelId: string, on: boolean) =>
    setDiag((prev) => (prev ? { ...prev, targets: { ...prev.targets, [channelId]: on } } : prev))

  const toggleAll = (on: boolean) =>
    setDiag((prev) => {
      if (!prev) return prev
      const group = groups.find((g) => g.model === prev.model)
      const targets: Record<string, boolean> = {}
      for (const r of group?.routes ?? []) targets[r.channel_id] = on
      return { ...prev, targets }
    })

  const runDiag = () => {
    if (!diag || !diagGroup) return
    const tasks = diagGroup.routes
      .filter((r) => diag.targets[r.channel_id])
      .map((r) => ({ channel_id: r.channel_id, model: diag.model }))
    runProbe.mutate(tasks)
  }

  const okCount = probeRows ? probeRows.filter((r) => r.status === 200 && !r.error).length : 0

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">模型路由</h1>
        <Segmented
          value={tab}
          onChange={setTab}
          options={[
            { value: 'chat', label: '对话模型' },
            { value: 'image', label: '生图模型' },
          ]}
        />
      </div>

      {groups.length === 0 ? (
        <Card>
          <Empty
            text="还没有可路由的模型，先去添加渠道并配置模型"
            action={
              <Link to="/channels">
                <Button variant="primary">前往渠道管理</Button>
              </Link>
            }
          />
        </Card>
      ) : visibleGroups.length === 0 ? (
        <Card>
          <Empty text={tab === 'chat' ? '还没有可路由的对话模型' : '还没有可路由的生图模型'} />
        </Card>
      ) : (
        <div className="space-y-3">
          {visibleGroups.map((group) => (
            <Card
              key={group.model}
              title={
                <span className="flex min-w-0 items-center gap-1">
                  <code className="truncate text-sm">{group.model}</code>
                  <button
                    type="button"
                    onClick={() => copyModel(group.model)}
                    title="复制模型名"
                    aria-label="复制模型名"
                    className="flex-none rounded-md p-1 text-zinc-400 hover:bg-zinc-100 hover:text-zinc-600 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
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
                </span>
              }
              action={
                <Button variant="ghost" className="px-2 py-0.5 text-xs" onClick={() => openDiag(group.model)}>
                  诊断
                </Button>
              }
            >
              <div className="overflow-x-auto">
                <table className="w-full min-w-[32rem] table-fixed text-sm">
                  <colgroup>
                    <col className="w-[7rem]" />
                    <col />
                    <col className="w-[7.5rem]" />
                    <col className="w-[7rem]" />
                  </colgroup>
                  <thead>
                    <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                      <th className="py-2 pr-3 font-medium">渠道</th>
                      <th className="py-2 pr-3 font-medium">上游模型</th>
                      <th className="py-2 pr-3 font-medium">状态</th>
                      <th className="py-2 font-medium">优先级</th>
                    </tr>
                  </thead>
                  <tbody>
                    {group.routes.map((route, index) => {
                      const last = group.routes.length - 1
                      return (
                        <tr
                          key={route.channel_id}
                          className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60"
                        >
                          <td className="py-2 pr-3 text-zinc-800 dark:text-zinc-200">
                            <span className="block truncate">{route.channel_name}</span>
                          </td>
                          <td className="py-2 pr-3">
                            {/* 徽章靠右对齐：模型名过长时截断，不挤压徽章 */}
                            <div className="flex items-center justify-between gap-1.5">
                              <code className="min-w-0 truncate font-mono text-xs text-zinc-600 dark:text-zinc-400">
                                {route.upstream_model}
                              </code>
                              {route.direct ? (
                                <Badge tone="info">透传</Badge>
                              ) : (
                                <Badge tone="warn">映射</Badge>
                              )}
                            </div>
                          </td>
                          <td className="py-2 pr-3">
                            <div className="flex items-center gap-2">
                              <Switch
                                checked={!route.model_disabled}
                                disabled={saveModel.isPending}
                                title={
                                  route.model_disabled
                                    ? '该模型在此渠道已停用，点击启用'
                                    : '该模型在此渠道已启用，点击停用'
                                }
                                onChange={(v) =>
                                  saveModel.mutate({
                                    channelId: route.channel_id,
                                    model: group.model,
                                    enabled: v,
                                  })
                                }
                              />
                              {/* 模型本身启用、但渠道停用或无可用密钥时，模型实际不可用 */}
                              {!route.model_disabled && !route.enabled ? (
                                <span className="truncate text-xs text-zinc-400 dark:text-zinc-500">
                                  渠道不可用
                                </span>
                              ) : null}
                            </div>
                          </td>
                          <td className="py-2">
                            <div className="flex items-center gap-1.5">
                              <span className="w-4 text-right text-xs tabular-nums text-zinc-400 dark:text-zinc-500">
                                {index + 1}
                              </span>
                              <button
                                type="button"
                                title="上移，提升优先级"
                                aria-label="上移，提升优先级"
                                disabled={index === 0 || reorder.isPending}
                                onClick={() =>
                                  reorder.mutate({
                                    model: group.model,
                                    routes: group.routes,
                                    from: index,
                                    to: index - 1,
                                  })
                                }
                                className="rounded-md border border-zinc-200 p-1 text-zinc-500 transition-colors hover:bg-zinc-100 hover:text-zinc-700 disabled:opacity-40 disabled:hover:bg-transparent dark:border-zinc-700 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
                              >
                                <svg
                                  width="12"
                                  height="12"
                                  viewBox="0 0 16 16"
                                  fill="none"
                                  stroke="currentColor"
                                  strokeWidth="1.8"
                                  strokeLinecap="round"
                                  strokeLinejoin="round"
                                >
                                  <path d="M4 10l4-4 4 4" />
                                </svg>
                              </button>
                              <button
                                type="button"
                                title="下移，降低优先级"
                                aria-label="下移，降低优先级"
                                disabled={index === last || reorder.isPending}
                                onClick={() =>
                                  reorder.mutate({
                                    model: group.model,
                                    routes: group.routes,
                                    from: index,
                                    to: index + 1,
                                  })
                                }
                                className="rounded-md border border-zinc-200 p-1 text-zinc-500 transition-colors hover:bg-zinc-100 hover:text-zinc-700 disabled:opacity-40 disabled:hover:bg-transparent dark:border-zinc-700 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
                              >
                                <svg
                                  width="12"
                                  height="12"
                                  viewBox="0 0 16 16"
                                  fill="none"
                                  stroke="currentColor"
                                  strokeWidth="1.8"
                                  strokeLinecap="round"
                                  strokeLinejoin="round"
                                >
                                  <path d="M4 6l4 4 4-4" />
                                </svg>
                              </button>
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            </Card>
          ))}
        </div>
      )}

      {diag ? (
        <Modal
          open
          wide
          title={`连通性诊断 · ${diag.model}`}
          onClose={() => setDiag(null)}
          footer={
            <>
              <Button onClick={() => setDiag(null)}>关闭</Button>
              <Button
                variant="primary"
                disabled={selectedCount === 0 || runProbe.isPending}
                onClick={runDiag}
              >
                {runProbe.isPending ? '诊断中…' : '运行诊断'}
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            {diagGroup && diagGroup.routes.length > 0 ? (
              <div className="space-y-2">
                <label className="inline-flex items-center gap-2 text-sm text-zinc-600 dark:text-zinc-400">
                  <input
                    type="checkbox"
                    checked={allSelected}
                    onChange={(e) => toggleAll(e.target.checked)}
                  />
                  全选
                </label>
                <div className="space-y-1">
                  {diagGroup.routes.map((r) => (
                    <label
                      key={r.channel_id}
                      className="flex flex-wrap items-center gap-2 rounded-lg border border-zinc-200 px-3 py-2 text-sm dark:border-zinc-800"
                    >
                      <input
                        type="checkbox"
                        checked={!!diag.targets[r.channel_id]}
                        onChange={(e) => toggleTarget(r.channel_id, e.target.checked)}
                      />
                      <span className="text-zinc-700 dark:text-zinc-300">{r.channel_name}</span>
                      <code className="ml-auto min-w-0 truncate font-mono text-xs text-zinc-500 dark:text-zinc-400">
                        {r.upstream_model}
                      </code>
                    </label>
                  ))}
                </div>
              </div>
            ) : (
              <p className="text-sm text-zinc-500">没有可诊断的目标</p>
            )}

            {probeRows ? (
              probeRows.length === 0 ? (
                <p className="text-sm text-zinc-500">没有可诊断的目标</p>
              ) : (
                <div className="overflow-x-auto">
                  <p className="mb-2 text-sm font-medium text-zinc-700 dark:text-zinc-300">
                    诊断结果（{okCount} / {probeRows.length}）
                  </p>
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                        <th className="py-2 pr-3 font-medium">渠道</th>
                        <th className="py-2 pr-3 font-medium">密钥</th>
                        <th className="py-2 pr-3 font-medium">状态</th>
                        <th className="py-2 pr-3 font-medium">耗时</th>
                        <th className="py-2 font-medium">结果</th>
                      </tr>
                    </thead>
                    <tbody>
                      {probeRows.map((r, i) => {
                        const ok = r.status === 200 && !r.error
                        return (
                          <tr key={i} className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60">
                            <td className="py-2 pr-3 text-zinc-700 dark:text-zinc-300">{r.channel}</td>
                            <td className="py-2 pr-3 font-mono text-xs">{r.key_hint}</td>
                            <td className="py-2 pr-3">
                              <Badge tone={ok ? 'ok' : r.status === 429 ? 'warn' : 'off'}>
                                {r.status === 0 ? '网络错误' : r.status}
                              </Badge>
                            </td>
                            <td className="py-2 pr-3 font-mono text-xs">{r.duration_ms}ms</td>
                            <td className="py-2 text-xs break-all text-zinc-600 dark:text-zinc-400">
                              {r.error ? r.error : (r.body ?? '').slice(0, 160)}
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              )
            ) : null}
          </div>
        </Modal>
      ) : null}
    </div>
  )
}

// writeClipboard 优先用剪贴板 API，失败（如局域网 IP 经 http 访问时非安全上下文）再回退 execCommand。
async function writeClipboard(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 继续尝试回退方案
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.cssText = 'position:fixed;top:-1000px;opacity:0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}