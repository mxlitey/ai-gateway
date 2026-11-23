import { Fragment, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  api,
  type ModelMeta,
  type ProbeResult,
  type RouteGroup,
} from '../lib/api'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  Dot,
  Empty,
  ErrorText,
  Field,
  Input,
  Loading,
  Modal,
  PageHead,
  Segmented,
  Switch,
  Textarea,
} from '../components/AdminUI'

/** 元数据表单草稿：数字全部用字符串，留空即「未声明」。 */
type MetaDraft = {
  description: string
  reference: boolean
  negative_prompt: boolean
  seed: boolean
  chinese_prompt: boolean
  chinese_text: boolean
  max_ref_count: string
  max_n: string
  max_n_ref: string
  output_formats: string[]
  min_width_px: string
  min_height_px: string
  max_width_px: string
  max_height_px: string
  size_multiple: string
}

const META_FORMATS: { value: string; label: string }[] = [
  { value: 'png', label: 'PNG' },
  { value: 'jpeg', label: 'JPEG' },
  { value: 'webp', label: 'WebP' },
]

function emptyMetaDraft(): MetaDraft {
  return {
    description: '',
    reference: false,
    negative_prompt: false,
    seed: false,
    chinese_prompt: false,
    chinese_text: false,
    max_ref_count: '',
    max_n: '',
    max_n_ref: '',
    output_formats: [],
    min_width_px: '',
    min_height_px: '',
    max_width_px: '',
    max_height_px: '',
    size_multiple: '',
  }
}

function metaDraftOf(m?: ModelMeta): MetaDraft {
  if (!m) return emptyMetaDraft()
  const str = (n: number) => (n > 0 ? String(n) : '')
  return {
    description: m.description,
    reference: m.reference,
    negative_prompt: m.negative_prompt,
    seed: m.seed,
    chinese_prompt: m.chinese_prompt,
    chinese_text: m.chinese_text,
    max_ref_count: str(m.max_ref_count),
    max_n: str(m.max_n),
    max_n_ref: str(m.max_n_ref),
    output_formats: m.output_formats ?? [],
    min_width_px: str(m.min_width_px),
    min_height_px: str(m.min_height_px),
    max_width_px: str(m.max_width_px),
    max_height_px: str(m.max_height_px),
    size_multiple: str(m.size_multiple),
  }
}

/** 草稿转提交体；数字留空按 0（未声明）处理。 */
function metaPayloadOf(model: string, d: MetaDraft) {
  return {
    model,
    description: d.description.trim(),
    reference: d.reference,
    negative_prompt: d.negative_prompt,
    seed: d.seed,
    chinese_prompt: d.chinese_prompt,
    chinese_text: d.chinese_text,
    max_ref_count: num(d.max_ref_count),
    max_n: num(d.max_n),
    max_n_ref: num(d.max_n_ref),
    output_formats: d.output_formats,
    min_width_px: num(d.min_width_px),
    min_height_px: num(d.min_height_px),
    max_width_px: num(d.max_width_px),
    max_height_px: num(d.max_height_px),
    size_multiple: num(d.size_multiple),
  }
}

function num(s: string): number {
  const n = Number(s.trim())
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

export default function ModelRoutes() {
  const queryClient = useQueryClient()
  const routes = useQuery({ queryKey: ['routes'], queryFn: api.routes })
  const metaQuery = useQuery({ queryKey: ['model-meta'], queryFn: api.modelMeta })
  const [tab, setTab] = useState<'chat' | 'image'>('chat')
  const [diag, setDiag] = useState<{ model: string; targets: Record<string, boolean> } | null>(null)
  const [probeRows, setProbeRows] = useState<ProbeResult[] | null>(null)
  const [editing, setEditing] = useState<{ model: string; draft: MetaDraft } | null>(null)

  // 元数据按模型名索引，卡片上标记「已声明」
  const metaByModel = useMemo(() => {
    const m = new Map<string, ModelMeta>()
    for (const it of metaQuery.data?.items ?? []) m.set(it.model, it)
    for (const it of metaQuery.data?.orphans ?? []) m.set(it.model, it)
    return m
  }, [metaQuery.data])

  // 顺序即优先级：拖动落位后把整组重写成 n-1..0，只提交真正变化的行
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

  // 拖动状态只记「哪个模型的哪一行正被拖到第几行」，跨模型不生效
  const [drag, setDrag] = useState<{ model: string; from: number; over: number } | null>(null)
  const dropAt = (model: string, routes: RouteGroup['routes'], index: number) => {
    const from = drag && drag.model === model ? drag.from : -1
    setDrag(null)
    if (from >= 0 && from !== index) {
      reorder.mutate({ model, routes, from, to: index })
    }
  }

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

  // 元数据保存 / 删除
  const saveMeta = useMutation({
    mutationFn: (p: { model: string; draft: MetaDraft }) =>
      api.upsertModelMeta(metaPayloadOf(p.model, p.draft)),
    onSuccess: async () => {
      toast('ok', '元数据已保存')
      setEditing(null)
      await queryClient.invalidateQueries({ queryKey: ['model-meta'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const removeMeta = useMutation({
    mutationFn: (model: string) => api.deleteModelMeta(model),
    onSuccess: async () => {
      toast('ok', '元数据已删除')
      await queryClient.invalidateQueries({ queryKey: ['model-meta'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const openMeta = (model: string) =>
    setEditing({ model, draft: metaDraftOf(metaByModel.get(model)) })

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
  // 孤儿只在生图 tab 显示：元数据本来就是生图模型专用
  const orphans = tab === 'image' ? metaQuery.data?.orphans ?? [] : []
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
                  {tab === 'image' ? (
                    <span
                      title={
                        metaByModel.has(group.model)
                          ? '已声明能力与约束，点击修改'
                          : '补充尺寸、参考图上限等约束，帮助 AI 客户端一次调用成功'
                      }
                    >
                      <Button variant="ghost" size="sm" onClick={() => openMeta(group.model)}>
                        {metaByModel.has(group.model) ? '元数据 · 已声明' : '元数据'}
                      </Button>
                    </span>
                  ) : null}
                  <Button variant="ghost" size="sm" onClick={() => openDiag(group.model)}>
                    诊断
                  </Button>
                </div>
              </div>

              <div className="tbl-wrap">
                <table className="tbl route-tbl">
                  <colgroup>
                    {/* 手柄单独一列且无列名；渠道列只放渠道名，
                        宽度写在 CSS 里，窄屏可以把渠道列再收一档 */}
                    <col className="c-grip" />
                    <col className="c-ch" />
                    <col />
                    <col className="c-state" />
                  </colgroup>
                  <thead>
                    <tr>
                      <th className="grip-th" aria-label="拖动排序" />
                      <th>渠道</th>
                      <th>上游模型</th>
                      <th>状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {group.routes.map((route, index) => {
                      const active = drag?.model === group.model
                      const dragging = active && drag.from === index
                      const over = active && drag.from !== index && drag.over === index
                      // 落点方向：向下拖插到目标之后，向上拖插到目标之前，
                      // 与 dropAt 里 splice 后的实际落位保持一致
                      const dropBefore = over && drag.from > index
                      const dropAfter = over && drag.from < index
                      // 虚拟行本身就代表「放在 index 处」，光标落在它上面时保持落点不变
                      const placeholder = (
                        <tr
                          className="row-placeholder"
                          aria-hidden="true"
                          onDragOver={(e) => {
                            if (!active) return
                            e.preventDefault()
                            e.dataTransfer.dropEffect = 'move'
                            if (drag.over !== index) setDrag({ ...drag, over: index })
                          }}
                          onDrop={(e) => {
                            e.preventDefault()
                            dropAt(group.model, group.routes, index)
                          }}
                        >
                          <td colSpan={4}>
                            <div className="ph-slot" />
                          </td>
                        </tr>
                      )
                      return (
                        <Fragment key={route.channel_id}>
                          {dropBefore ? placeholder : null}
                          <tr
                            className={
                              dragging ? 'row-dragging' : over ? 'row-drag-swept' : undefined
                            }
                            onDragOver={(e) => {
                              // 只接受同一模型内部的拖动，跨模型不响应
                              if (!active) return
                              e.preventDefault()
                              e.dataTransfer.dropEffect = 'move'
                              if (drag.over !== index) setDrag({ ...drag, over: index })
                            }}
                            onDrop={(e) => {
                              e.preventDefault()
                              dropAt(group.model, group.routes, index)
                            }}
                          >
                            <td className="grip-cell">
                              <span
                                className="order-grip"
                                role="button"
                                tabIndex={-1}
                                draggable={!reorder.isPending}
                                title="按住拖动可调整优先级，从上到下即优先级由高到低"
                                onDragStart={(e) => {
                                  // Firefox 必须写入数据才会真正开始拖动
                                  e.dataTransfer.effectAllowed = 'move'
                                  e.dataTransfer.setData('text/plain', String(index))
                                  setDrag({ model: group.model, from: index, over: index })
                                }}
                                onDragEnd={() => setDrag(null)}
                              >
                                <svg width="10" height="14" viewBox="0 0 10 14" fill="currentColor">
                                  <circle cx="3" cy="3" r="1.1" />
                                  <circle cx="7" cy="3" r="1.1" />
                                  <circle cx="3" cy="7" r="1.1" />
                                  <circle cx="7" cy="7" r="1.1" />
                                  <circle cx="3" cy="11" r="1.1" />
                                  <circle cx="7" cy="11" r="1.1" />
                                </svg>
                              </span>
                            </td>
                            <td>
                              {/* 渠道名最多 6 个汉字，超出截断不换行 */}
                              <span className="ch-name">{route.channel_name}</span>
                            </td>
                            <td>
                              {/* 模型名尽量占满，超长截断；徽章紧跟其后 */}
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
                              <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
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
                                {/* 模型启用但渠道停用 / 无可用密钥：用一个小圆点提示，
                                    文字说明放 title，避免状态列被撑宽 */}
                                {!route.model_disabled && !route.enabled ? (
                                  <span title="渠道不可用（已停用或无可用密钥）">
                                    <Dot tone="warn" />
                                  </span>
                                ) : null}
                              </div>
                            </td>
                          </tr>
                          {dropAfter ? placeholder : null}
                        </Fragment>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          ))}
        </div>
      )}

      {orphans.length > 0 ? (
        <div className="panel">
          <div className="panel-h">
            <h2>已无渠道的模型</h2>
            <div className="r">
              <Badge tone="warn">{orphans.length}</Badge>
            </div>
          </div>
          <div className="tbl-wrap">
            <table className="tbl">
              <thead>
                <tr>
                  <th>模型</th>
                  <th>说明</th>
                  <th style={{ width: 130 }}>操作</th>
                </tr>
              </thead>
              <tbody>
                {orphans.map((m) => (
                  <tr key={m.model}>
                    <td>
                      <code className="mono" style={{ fontSize: 11 }}>
                        {m.model}
                      </code>
                    </td>
                    <td className="tiny faint">{m.description || '—'}</td>
                    <td>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <Button variant="ghost" size="sm" onClick={() => openMeta(m.model)}>
                          编辑
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={removeMeta.isPending}
                          onClick={() => {
                            if (!window.confirm(`删除「${m.model}」的元数据？只删声明，不影响任何文件或渠道。`))
                              return
                            removeMeta.mutate(m.model)
                          }}
                        >
                          删除
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="tiny faint" style={{ margin: '8px 0 0' }}>
            这些模型曾声明过能力与约束，但当前没有任何渠道使用该模型名。删掉渠道后记录会保留在这里，
            重新添加同名模型时会自动复用。
          </p>
        </div>
      ) : null}

      {editing ? (
        <MetaEditor
          model={editing.model}
          draft={editing.draft}
          saving={saveMeta.isPending}
          onChange={(draft) => setEditing({ model: editing.model, draft })}
          onClose={() => setEditing(null)}
          onSave={() => saveMeta.mutate({ model: editing.model, draft: editing.draft })}
        />
      ) : null}

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

// MetaEditor 生图模型元数据编辑器：左栏能力开关，右栏数值约束。
// 参考图关闭时，依赖它的几项自动置灰，表单本身表达依赖关系。
function MetaEditor({
  model,
  draft,
  saving,
  onChange,
  onClose,
  onSave,
}: {
  model: string
  draft: MetaDraft
  saving: boolean
  onChange: (d: MetaDraft) => void
  onClose: () => void
  onSave: () => void
}) {
  const set = <K extends keyof MetaDraft>(k: K, v: MetaDraft[K]) => onChange({ ...draft, [k]: v })
  const refOff = !draft.reference

  const row = (
    label: string,
    hint: string,
    value: boolean,
    onToggle: (v: boolean) => void,
    disabled = false,
  ) => (
    <div
      title={hint}
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: 10,
        padding: '5px 0',
        opacity: disabled ? 0.45 : 1,
      }}
    >
      <span style={{ fontSize: 11.5 }}>{label}</span>
      <Switch checked={value} disabled={disabled} onChange={onToggle} title={hint} />
    </div>
  )

  const numField = (
    label: string,
    hint: string,
    key: keyof MetaDraft,
    disabled = false,
    width = 90,
  ) => (
    <Field label={label} hint={hint}>
      <Input
        type="number"
        min={0}
        placeholder="未声明"
        disabled={disabled}
        value={draft[key] as string}
        onChange={(e) => set(key, e.target.value as never)}
        style={{ width }}
      />
    </Field>
  )

  // sizeBox 只放一个数字输入，标签固定为宽/高，用于分辨率两两成对展示
  const sizeBox = (key: keyof MetaDraft, ph: string) => (
    <Input
      type="number"
      min={0}
      placeholder={ph}
      value={draft[key] as string}
      onChange={(e) => set(key, e.target.value as never)}
      style={{ width: 76 }}
    />
  )

  return (
    <Modal
      open
      wide
      title={`元数据 · ${model}`}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" disabled={saving} onClick={onSave}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </>
      }
    >
      <div className="stack">
        <Field label="描述" hint="说明这个模型能做什么，会展示给 AI 客户端；支持 Markdown">
          <Textarea
            rows={3}
            placeholder="例如：通用文生图，支持参考图改图；最高 4K"
            value={draft.description}
            onChange={(e) => set('description', e.target.value)}
            maxLength={500}
          />
        </Field>

        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))',
            gap: 16,
          }}
        >
          <div>
            <div className="tiny muted" style={{ marginBottom: 4 }}>
              能力
            </div>
            {row('参考图', '支持传图生成或改图', draft.reference, (v) => set('reference', v))}
            {row('反向提示词', '支持 negative_prompt', draft.negative_prompt, (v) =>
              set('negative_prompt', v),
            )}
            {row('随机种子', '支持 seed，可复现同一张', draft.seed, (v) => set('seed', v))}
            {row('中文提示词', '能理解中文描述', draft.chinese_prompt, (v) =>
              set('chinese_prompt', v),
            )}
            {row('中文文字', '能把中文正确画进图里', draft.chinese_text, (v) =>
              set('chinese_text', v),
            )}
          </div>

          <div>
            <div className="tiny muted" style={{ marginBottom: 4 }}>
              约束
            </div>
            <div style={{ display: 'flex', gap: 10 }}>
              {numField('输入参考图数', '最多可以传几张参考图，留空不声明', 'max_ref_count', refOff, 96)}
              {numField('文生图输出数', '不带参考图时最多出几张', 'max_n', false, 110)}
              {numField('图生图输出数', '带参考图时最多出几张', 'max_n_ref', refOff, 110)}
            </div>

            <div style={{ marginTop: 6 }} title="生图结果可返回的图片格式">
              <div className="tiny muted" style={{ marginBottom: 4 }}>
                输出格式
              </div>
              <div style={{ display: 'flex', gap: 12 }}>
                {META_FORMATS.map((f) => (
                  <label
                    key={f.value}
                    className="tiny"
                    style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}
                  >
                    <input
                      type="checkbox"
                      checked={draft.output_formats.includes(f.value)}
                      onChange={(e) =>
                        set(
                          'output_formats',
                          e.target.checked
                            ? [...draft.output_formats, f.value]
                            : draft.output_formats.filter((x) => x !== f.value),
                        )
                      }
                    />
                    {f.label}
                  </label>
                ))}
              </div>
            </div>

            <div
              style={{
                marginTop: 8,
                display: 'flex',
                gap: 24,
                flexWrap: 'wrap',
                alignItems: 'flex-start',
              }}
            >
              <div title="输出图片的最小与最大宽高，单位像素">
                <div className="tiny muted" style={{ marginBottom: 4 }}>
                  分辨率
                </div>
                <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span className="tiny muted">最小</span>
                    {sizeBox('min_width_px', '宽')}
                    <span className="tiny faint">×</span>
                    {sizeBox('min_height_px', '高')}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span className="tiny muted">最大</span>
                    {sizeBox('max_width_px', '宽')}
                    <span className="tiny faint">×</span>
                    {sizeBox('max_height_px', '高')}
                  </div>
                </div>
              </div>
              <div style={{ width: 140 }}>
                {numField(
                  '尺寸倍数',
                  '宽高必须是该值的正整数倍，常见 16 或 32；留空不声明',
                  'size_multiple',
                )}
              </div>
            </div>
          </div>
        </div>

        <p className="tiny faint" style={{ margin: 0 }}>
          留空表示未声明。声明只用于对外说明能力，网关不据此拒绝请求；客户端遇到未声明的项会按「未知」保守取值。
        </p>
      </div>
    </Modal>
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
