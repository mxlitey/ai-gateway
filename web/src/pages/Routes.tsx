import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api, type ProbeResult } from '../lib/api'
import { toast } from '../lib/toast'
import { Badge, Button, Card, Empty, ErrorText, Input, Loading, Modal, Switch } from '../components/ui'

export default function ModelRoutes() {
  const queryClient = useQueryClient()
  const routes = useQuery({ queryKey: ['routes'], queryFn: api.routes })
  // key = `${model}::${channelId}` -> 输入框中的字符串
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [diag, setDiag] = useState<{ model: string; targets: Record<string, boolean> } | null>(null)
  const [probeRows, setProbeRows] = useState<ProbeResult[] | null>(null)

  const save = useMutation({
    mutationFn: (p: { channelId: string; model: string; priority: number }) =>
      api.setPriority(p.channelId, p.model, p.priority),
    onSuccess: async () => {
      toast('ok', '优先级已保存')
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

  if (routes.isLoading) return <Loading />
  if (routes.isError) return <ErrorText text={(routes.error as Error).message} />

  const groups = routes.data ?? []
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
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">模型路由</h1>

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
      ) : (
        <div className="space-y-3">
          {groups.map((group) => (
            <Card
              key={group.model}
              title={<code className="text-sm">{group.model}</code>}
              action={
                <Button variant="ghost" className="px-2 py-0.5 text-xs" onClick={() => openDiag(group.model)}>
                  诊断
                </Button>
              }
            >
              <div className="overflow-x-auto">
                <table className="w-full min-w-[34rem] table-fixed text-sm">
                  <colgroup>
                    <col className="w-[26%]" />
                    <col className="w-[30%]" />
                    <col className="w-[13%]" />
                    <col className="w-[17%]" />
                    <col className="w-[14%]" />
                  </colgroup>
                  <thead>
                    <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                      <th className="py-2 pr-3 font-medium">渠道</th>
                      <th className="py-2 pr-3 font-medium">上游模型</th>
                      <th className="py-2 pr-3 font-medium">状态</th>
                      <th className="py-2 pr-3 font-medium">优先级</th>
                      <th className="py-2 font-medium">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {group.routes.map((route) => {
                      const key = `${group.model}::${route.channel_id}`
                      const value = draft[key] ?? String(route.priority)
                      const changed = value !== String(route.priority)
                      return (
                        <tr
                          key={route.channel_id}
                          className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60"
                        >
                          <td className="py-2 pr-3 text-zinc-800 dark:text-zinc-200">
                            <span className="block truncate">{route.channel_name}</span>
                          </td>
                          <td className="py-2 pr-3">
                            <div className="flex items-center gap-1.5">
                              <code className="min-w-0 truncate font-mono text-xs text-zinc-600 dark:text-zinc-400">
                                {route.upstream_model}
                              </code>
                              {route.direct ? <Badge tone="info">透传</Badge> : null}
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
                                <span className="text-xs text-zinc-400 dark:text-zinc-500">
                                  渠道不可用
                                </span>
                              ) : null}
                            </div>
                          </td>
                          <td className="py-2 pr-3">
                            <Input
                              className="w-full"
                              value={value}
                              onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
                            />
                          </td>
                          <td className="py-2">
                            <Button
                              variant={changed ? 'primary' : 'ghost'}
                              disabled={!changed || save.isPending}
                              onClick={() =>
                                save.mutate({
                                  channelId: route.channel_id,
                                  model: group.model,
                                  priority: Number(value) || 0,
                                })
                              }
                            >
                              保存
                            </Button>
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
                <p className="text-xs text-zinc-500 dark:text-zinc-500">
                  将对选中渠道的每个已启用密钥各发起一次真实对话请求。
                </p>
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