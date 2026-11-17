import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type ProbeResult, type RouteGroup } from '../lib/api'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  Dot,
  Empty,
  ErrorText,
  Loading,
  Modal,
  PageHead,
  Segmented,
  Switch,
} from './ui'

export default function V2Routes() {
  const queryClient = useQueryClient()
  const routes = useQuery({ queryKey: ['routes'], queryFn: api.routes })
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

  const copyModel = async (model: string) => {
    if (await writeClipboard(model)) toast('ok', '已复制模型名 ' + model)
    else toast('err', '复制失败，请手动选中复制')
  }

  if (routes.isLoading) {
    return (
      <div className="panel">
        <Loading />
      </div>
    )
  }
  if (routes.isError) return <ErrorText text={(routes.error as Error).message} />

  const groups = routes.data ?? []
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
    <div className="stack">
      <PageHead
        title="模型路由"
        actions={
          <Segmented
            value={tab}
            onChange={setTab}
            options={[
              { value: 'chat', label: '对话模型' },
              { value: 'image', label: '生图模型' },
            ]}
          />
        }
      />

      {groups.length === 0 ? (
        <div className="panel">
          <Empty text="还没有可路由的模型，先去添加渠道并配置模型" />
        </div>
      ) : visibleGroups.length === 0 ? (
        <div className="panel">
          <Empty text={tab === 'chat' ? '还没有可路由的对话模型' : '还没有可路由的生图模型'} />
        </div>
      ) : (
        <div className="stack">
          {visibleGroups.map((group) => (
            <div className="panel" key={group.model}>
              <div className="panel-h">
                <h2 style={{ display: 'flex', alignItems: 'center', gap: 4, minWidth: 0 }}>
                  <code className="truncate" style={{ fontSize: 12 }}>
                    {group.model}
                  </code>
                  <button
                    type="button"
                    onClick={() => copyModel(group.model)}
                    title="复制模型名"
                    aria-label="复制模型名"
                    className="btn ghost sm"
                  >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
                      <rect x="5.5" y="5.5" width="8.5" height="8.5" rx="2.2" />
                      <path d="M10.5 5.5V4.2A2.2 2.2 0 0 0 8.3 2H4.2A2.2 2.2 0 0 0 2 4.2v4.1a2.2 2.2 0 0 0 2.2 2.2h1.3" />
                    </svg>
                  </button>
                </h2>
                <div className="r">
                  <Button variant="ghost" size="sm" onClick={() => openDiag(group.model)}>
                    诊断
                  </Button>
                </div>
              </div>

              <div className="tbl-wrap">
                <table className="tbl route-tbl">
                  <colgroup>
                    <col style={{ width: '28%' }} />
                    <col />
                    <col style={{ width: '22%' }} />
                    <col style={{ width: 64 }} />
                  </colgroup>
                  <thead>
                    <tr>
                      <th>渠道</th>
                      <th>上游模型</th>
                      <th>状态</th>
                      <th style={{ textAlign: 'left' }}>顺序</th>
                    </tr>
                  </thead>
                  <tbody>
                    {group.routes.map((route, index) => {
                      const last = group.routes.length - 1
                      return (
                        <tr key={route.channel_id}>
                          <td>
                            <div className="ch-name-cell">
                              {/* 渠道名最多 6 个汉字，超出截断不换行 */}
                              <span className="ch-name">{route.channel_name}</span>
                            </div>
                          </td>
                          <td>
                            {/* 模型名可截断，徽章固定在右侧 */}
                            <div className="up-cell">
                              <code className="up-name mono" style={{ fontSize: 11 }}>
                                {route.upstream_model}
                              </code>
                              {route.direct ? (
                                <Badge tone="info">透传</Badge>
                              ) : (
                                <Badge tone="warn">映射</Badge>
                              )}
                            </div>
                          </td>
                          <td>
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
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
                                <span className="truncate tiny faint" style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                                  <Dot tone="warn" />
                                  渠道不可用
                                </span>
                              ) : null}
                            </div>
                          </td>
                          <td>
                            {/* 只保留上下移箭头，不显示优先级数值；两箭头上下堆叠 */}
                            <div className="order-cell">
                              <button
                                type="button"
                                className="btn ghost order-btn"
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
                              >
                                ▲
                              </button>
                              <button
                                type="button"
                                className="btn ghost order-btn"
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
                              >
                                ▼
                              </button>
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            </div>
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
          <div className="stack">
            {diagGroup && diagGroup.routes.length > 0 ? (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <label className="tiny muted" style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                  <input type="checkbox" checked={allSelected} onChange={(e) => toggleAll(e.target.checked)} />
                  全选
                </label>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                  {diagGroup.routes.map((r) => (
                    <label
                      key={r.channel_id}
                      style={{
                        display: 'flex',
                        flexWrap: 'wrap',
                        alignItems: 'center',
                        gap: 8,
                        border: '1px solid var(--border)',
                        borderRadius: 8,
                        padding: '6px 10px',
                        fontSize: 11.5,
                      }}
                    >
                      <input
                        type="checkbox"
                        checked={!!diag.targets[r.channel_id]}
                        onChange={(e) => toggleTarget(r.channel_id, e.target.checked)}
                      />
                      <span>{r.channel_name}</span>
                      <code className="mono faint" style={{ marginLeft: 'auto', fontSize: 10.5 }}>
                        {r.upstream_model}
                      </code>
                    </label>
                  ))}
                </div>
              </div>
            ) : (
              <div className="faint tiny">没有可诊断的目标</div>
            )}

            {probeRows ? (
              probeRows.length === 0 ? (
                <div className="faint tiny">没有可诊断的目标</div>
              ) : (
                <div className="tbl-wrap">
                  <p className="small muted" style={{ margin: '0 0 6px' }}>
                    诊断结果（{okCount} / {probeRows.length}）
                  </p>
                  <table className="tbl">
                    <thead>
                      <tr>
                        <th>渠道</th>
                        <th>密钥</th>
                        <th>状态</th>
                        <th>耗时</th>
                        <th>结果</th>
                      </tr>
                    </thead>
                    <tbody>
                      {probeRows.map((r, i) => {
                        const ok = r.status === 200 && !r.error
                        return (
                          <tr key={i}>
                            <td className="tiny">{r.channel}</td>
                            <td className="mono tiny faint">{r.key_hint}</td>
                            <td>
                              <Badge tone={ok ? 'ok' : r.status === 429 ? 'warn' : 'off'}>
                                {r.status === 0 ? '网络错误' : r.status}
                              </Badge>
                            </td>
                            <td className="mono tiny">{r.duration_ms}ms</td>
                            <td className="tiny faint" style={{ wordBreak: 'break-all' }}>
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
