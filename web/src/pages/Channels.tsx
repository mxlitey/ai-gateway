import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type Channel, type ChannelInput } from '../lib/api'
import { bjDateString, bjDayStart } from '../lib/tz'
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

type KeyRow = { key: string; enabled: boolean; label?: string }
type MapRow = { pub: string; upstream: string }
type HeaderRow = { name: string; value: string }

type FormState = {
  id: string | null
  name: string
  base_url: string
  path: string
  keys: KeyRow[]
  modelsText: string
  modelMap: MapRow[]
  headers: HeaderRow[]
  paramMap: HeaderRow[]
  staticParamsText: string
  dropParamsText: string
  imageModelsText: string
  imageMode: '' | 'sync' | 'async'
  imagePath: string
  imagePollPath: string
  imageIntervalSec: string
  imageTimeoutSec: string
  imageSubmitHeaders: HeaderRow[]
  imagePollHeaders: HeaderRow[]
  imageTaskIDField: string
  imageStatusField: string
  imageSuccessStatus: string
  imageFailedStatus: string
  imageImagesField: string
  imageErrorField: string
  imageSyncTimeoutSec: string
  imageFormatMode: string
  imageParamMap: HeaderRow[]
  imageStaticParamsText: string
  imageDropParamsText: string
  imageEditPath: string
  imageRefField: string
  imageRefShape: string
  keyStrategy: string
  enabled: boolean
  isPlugin: boolean
}

const KEY_STRATEGIES = [
  { value: 'rotate', label: '负载均衡' },
  { value: 'ordered', label: '故障转移' },
] as const

const IMAGE_MODES = [
  { value: '', label: '关闭' },
  { value: 'sync', label: '同步' },
  { value: 'async', label: '异步任务' },
] as const

const IMAGE_FORMAT_MODES = [
  { value: 'drop', label: '不传上游' },
  { value: 'pass', label: '原样透传' },
  { value: 'url', label: '强制 url' },
  { value: 'b64_json', label: '强制 b64' },
] as const

const IMAGE_REF_SHAPES = [
  { value: 'url_array', label: '字符串数组' },
  { value: 'single_url', label: '单个字符串' },
  { value: 'url_object_array', label: '[{url}]' },
  { value: 'image_object_array', label: '[{image_url}]' },
] as const

const emptyForm: FormState = {
  id: null,
  name: '',
  base_url: '',
  path: '',
  keys: [{ key: '', enabled: true }],
  modelsText: '',
  modelMap: [],
  headers: [],
  paramMap: [],
  staticParamsText: '',
  dropParamsText: '',
  imageModelsText: '',
  imageMode: '',
  imagePath: '',
  imagePollPath: '',
  imageIntervalSec: '5',
  imageTimeoutSec: '360',
  imageSubmitHeaders: [],
  imagePollHeaders: [],
  imageTaskIDField: '',
  imageStatusField: '',
  imageSuccessStatus: '',
  imageFailedStatus: '',
  imageImagesField: '',
  imageErrorField: '',
  imageSyncTimeoutSec: '',
  imageFormatMode: 'drop',
  imageParamMap: [],
  imageStaticParamsText: '',
  imageDropParamsText: '',
  imageEditPath: '',
  imageRefField: '',
  imageRefShape: 'url_array',
  keyStrategy: 'rotate',
  enabled: true,
  isPlugin: false,
}

function isPluginChannel(ch: Channel): boolean {
  return ch.source.startsWith('plugin:')
}

export default function Channels() {
  const queryClient = useQueryClient()
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.plugins })
  // 今日用量：渠道卡片上的请求占比、成功率、首字耗时与输出速度都来自这里。
  // 用 from/to 走北京自然日，与仪表盘、用量页的日界保持一致（days: 1 是滚动 24 小时）
  const usage = useQuery({
    queryKey: ['usage-summary', 'channels-today', bjDateString()],
    queryFn: () => {
      const from = bjDayStart()
      return api.usageSummary({ from, to: from + 24 * 60 * 60 * 1000 })
    },
  })

  const [form, setForm] = useState<FormState | null>(null)
  const [flippedId, setFlippedId] = useState<string | null>(null)
  const [fetched, setFetched] = useState<{ models: string[]; picked: Record<string, boolean> } | null>(
    null,
  )

  const invalidateChannels = () =>
    queryClient
      .invalidateQueries({ queryKey: ['channels'] })
      .then(() => queryClient.invalidateQueries({ queryKey: ['routes'] }))

  const save = useMutation({
    mutationFn: async (state: FormState) => {
      const body: ChannelInput = {
        name: state.name.trim(),
        base_url: state.base_url.trim(),
        path: state.path.trim(),
        keys: state.keys
          .map((k) => ({ key: k.key.trim(), enabled: k.enabled, label: k.label }))
          .filter((k) => k.key !== ''),
        models: splitLines(state.modelsText),
        model_map: Object.fromEntries(
          state.modelMap
            .map((r) => [r.pub.trim(), r.upstream.trim()] as const)
            .filter(([pub]) => pub !== ''),
        ),
        headers: toHeaders(state.headers),
        param_map: Object.fromEntries(
          state.paramMap
            .map((r) => [r.name.trim(), r.value.trim()] as const)
            .filter(([from, to]) => from !== '' && to !== ''),
        ),
        static_params: parseJSONObject(state.staticParamsText),
        drop_params: splitLines(state.dropParamsText),
        image_models: splitLines(state.imageModelsText),
        image_generation: {
          mode: state.imageMode,
          path: state.imagePath.trim(),
          poll_path: state.imagePollPath.trim(),
          submit_headers: toHeaders(state.imageSubmitHeaders),
          poll_headers: toHeaders(state.imagePollHeaders),
          poll_interval_ms: secondsToMS(state.imageIntervalSec),
          timeout_ms: secondsToMS(state.imageTimeoutSec),
          task_id_field: state.imageTaskIDField.trim(),
          status_field: state.imageStatusField.trim(),
          success_status: state.imageSuccessStatus.trim(),
          failed_status: state.imageFailedStatus.trim(),
          images_field: state.imageImagesField.trim(),
          error_field: state.imageErrorField.trim(),
          sync_timeout_ms: secondsToMS(state.imageSyncTimeoutSec),
          response_format_mode: state.imageFormatMode,
          param_map: Object.fromEntries(
            state.imageParamMap
              .map((r) => [r.name.trim(), r.value.trim()] as const)
              .filter(([from, to]) => from !== '' && to !== ''),
          ),
          static_params: parseJSONObject(state.imageStaticParamsText),
          drop_params: splitLines(state.imageDropParamsText),
          edit_path: state.imageEditPath.trim(),
          reference_field: state.imageRefField.trim(),
          reference_shape: state.imageRefShape,
        },
        key_strategy: state.keyStrategy,
        enabled: state.enabled,
      }
      return state.id ? api.updateChannel(state.id, body) : api.createChannel(body)
    },
    onSuccess: async () => {
      toast('ok', '渠道已保存')
      setFetched(null)
      setForm(null)
      await invalidateChannels()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const toggle = useMutation({
    mutationFn: (id: string) => api.toggleChannel(id),
    onSuccess: invalidateChannels,
    onError: (err: Error) => toast('err', err.message),
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteChannel(id),
    onSuccess: async () => {
      toast('ok', '渠道已删除')
      await invalidateChannels()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const fetchModels = useMutation({
    mutationFn: async (state: FormState) => {
      const res = await api.fetchModels({
        channel_id: state.id ?? undefined,
        base_url: state.base_url.trim(),
        keys: state.keys.map((k) => k.key.trim()).filter(Boolean),
      })
      return { models: res.models, existing: splitLines(state.modelsText) }
    },
    onSuccess: ({ models, existing }) => {
      if (models.length === 0) {
        toast('err', '上游没有返回模型')
        return
      }
      const have = new Set(existing)
      const picked: Record<string, boolean> = {}
      for (const m of models) picked[m] = !have.has(m)
      setFetched({ models, picked })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const togglePicked = (model: string) =>
    setFetched((prev) =>
      prev ? { ...prev, picked: { ...prev.picked, [model]: !prev.picked[model] } } : prev,
    )

  const setAllPicked = (on: boolean) =>
    setFetched((prev) => {
      if (!prev) return prev
      const picked: Record<string, boolean> = {}
      for (const m of prev.models) picked[m] = on
      return { ...prev, picked }
    })

  const applyPicked = () => {
    if (!fetched) return
    const chosen = fetched.models.filter((m) => fetched.picked[m])
    if (chosen.length === 0) return
    setForm((prev) => {
      if (!prev) return prev
      const lines = splitLines(prev.modelsText)
      const have = new Set(lines)
      for (const m of chosen) {
        if (!have.has(m)) {
          lines.push(m)
          have.add(m)
        }
      }
      return { ...prev, modelsText: lines.join('\n') }
    })
    setFetched(null)
    toast('ok', `已添加 ${chosen.length} 个模型`)
  }

  const openNew = () => {
    setFetched(null)
    setForm({ ...emptyForm })
  }

  const openEditChannel = (ch: Channel) => {
    setFetched(null)
    openEdit(ch, setForm)
  }

  const closeForm = () => {
    setFetched(null)
    setForm(null)
  }

  // 今日每渠道用量：用于卡片双色环
  const channelUsage = useMemo(() => {
    const map = new Map<
      string,
      { requests: number; success: number; ttft: number; speed: number }
    >()
    for (const b of usage.data?.summary.by_channel ?? []) {
      map.set(b.key, {
        requests: b.requests,
        success: b.success_requests,
        ttft: b.ttft_ms,
        speed: b.tokens_per_sec,
      })
    }
    return map
  }, [usage.data])
  const totalRequests = usage.data?.summary.requests ?? 0

  if (channels.isLoading || plugins.isLoading) {
    return (
      <div className="panel">
        <Loading />
      </div>
    )
  }
  if (channels.isError) return <ErrorText text={(channels.error as Error).message} />

  const runningPlugins = new Set(
    (plugins.data ?? []).filter((p) => p.last_status === 'running').map((p) => p.id),
  )
  const list = (channels.data ?? [])
    .filter((ch) => !isPluginChannel(ch) || runningPlugins.has(ch.source.slice('plugin:'.length)))
    .sort((a, b) => Number(isPluginChannel(b)) - Number(isPluginChannel(a)))

  return (
    <div className="stack">
      <PageHead
        title="渠道管理"
        actions={
          <Button variant="primary" onClick={openNew}>
            添加渠道
          </Button>
        }
      />

      {list.length === 0 ? (
        <div className="panel">
          <Empty text="还没有渠道，先添加一个上游服务" />
        </div>
      ) : (
        <div className="ch-grid">
          {list.map((ch) => {
            const enabledKeys = ch.keys.filter((k) => k.enabled).length
            const modelCount =
              Object.keys(ch.model_map).length + ch.models.length + ch.image_models.length
            const flipped = flippedId === ch.id
            const u = channelUsage.get(ch.name)
            const share = totalRequests > 0 && u ? (u.requests / totalRequests) * 100 : 0
            const successRate = u && u.requests > 0 ? (u.success / u.requests) * 100 : 100
            const successTone = successRate >= 99 ? 'ok' : successRate >= 95 ? 'warn' : 'bad'
            // 状态圆点：停用=灰、无可用密钥=琥珀、启用且有密钥=绿
            const dotTone = !ch.enabled ? 'off' : enabledKeys === 0 ? 'warn' : 'ok'
            // 首字耗时不到 1 秒时用毫秒展示，避免 0.42s 这种小数字看不出差别
            const ttft =
              u && u.ttft > 0 ? (u.ttft < 1000 ? `${u.ttft}` : (u.ttft / 1000).toFixed(2)) : '—'
            const ttftUnit = ttft === '—' ? undefined : u!.ttft < 1000 ? 'ms' : 's'
            const speed = u && u.speed > 0 ? u.speed.toFixed(1) : '—'

            return (
              <div
                key={ch.id}
                role="button"
                tabIndex={0}
                title={flipped ? '点击返回' : '点击查看操作'}
                className={`flip-card${flipped ? ' flipped' : ''}`}
                onClick={() => setFlippedId(flipped ? null : ch.id)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    setFlippedId(flipped ? null : ch.id)
                  }
                }}
              >
                <div className="flip-card-inner">
                  {/* 正面：概览 */}
                  <div className="flip-face">
                    <ChannelHead
                      ch={ch}
                      dotTone={dotTone}
                      onToggle={() => toggle.mutate(ch.id)}
                    />
                    <div className="ch-foot">
                      <div className="ch-foot-info">
                        <span>
                          今日请求 <b className="num">{u?.requests ?? 0}</b> 次
                          {totalRequests > 0 ? (
                            <span className="faint"> · {share.toFixed(0)}%</span>
                          ) : null}
                        </span>
                        <span>
                          成功率{' '}
                          <b
                            className="num"
                            style={{
                              color:
                                successTone === 'ok'
                                  ? 'var(--ok)'
                                  : successTone === 'warn'
                                    ? 'var(--warn)'
                                    : 'var(--danger)',
                            }}
                          >
                            {u && u.requests > 0 ? `${successRate.toFixed(1)}%` : '—'}
                          </b>
                        </span>
                        <span>
                          模型 <b className="num">{modelCount}</b> · 密钥{' '}
                          <b className="num">
                            {enabledKeys} / {ch.keys.length}
                          </b>
                        </span>
                        <span>
                          首字 <b className="num">{ttft}</b>
                          {ttftUnit ? <span className="faint"> {ttftUnit}</span> : null} · 输出{' '}
                          <b className="num">{speed}</b>
                          {speed === '—' ? null : <span className="faint"> tok/s</span>}
                        </span>
                      </div>
                    </div>
                  </div>

                  {/* 背面：只把底部换成操作按钮，其余保持与正面一致 */}
                  <div className="flip-face fl-back">
                    <ChannelHead
                      ch={ch}
                      dotTone={dotTone}
                      onToggle={() => toggle.mutate(ch.id)}
                    />
                    <div className="ch-foot-btns">
                      <Button
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          setFlippedId(null)
                          openEditChannel(ch)
                        }}
                      >
                        编辑
                      </Button>
                      <Button
                        size="sm"
                        onClick={(e) => {
                          e.stopPropagation()
                          setFlippedId(null)
                          toggle.mutate(ch.id)
                        }}
                      >
                        {ch.enabled ? '停用' : '启用'}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={(e) => {
                          e.stopPropagation()
                          setFlippedId(null)
                          if (window.confirm(`确认删除渠道「${ch.name}」？`)) remove.mutate(ch.id)
                        }}
                      >
                        删除
                      </Button>
                    </div>
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {form ? (
        <Modal
          open
          wide
          title={form.id ? '编辑渠道' : '添加渠道'}
          onClose={closeForm}
          footer={
            <>
              <Button onClick={closeForm}>取消</Button>
              <Button
                variant="primary"
                disabled={save.isPending || !form.name.trim() || !form.base_url.trim()}
                onClick={() => save.mutate(form)}
              >
                {save.isPending ? '保存中…' : '保存'}
              </Button>
            </>
          }
        >
          <div className="stack">
            <div className="admingrid g2">
              <Field label="名称">
                <Input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="如 NVIDIA NIM"
                />
              </Field>
              <Field
                label="API 主机"
                hint="默认拼接 /chat/completions；如上游端点不同，把完整地址写在这里（如 https://api.xxx.com/v1/messages）"
              >
                <Input
                  value={form.base_url}
                  onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                  placeholder="https://integrate.api.nvidia.com/v1"
                />
              </Field>
            </div>

            <Field label="API 密钥" hint={form.isPlugin ? '密钥由插件同步，可启停与排序' : undefined}>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span className="tiny fancy">密钥选用策略</span>
                  <Segmented
                    value={form.keyStrategy}
                    onChange={(v) => setForm({ ...form, keyStrategy: v })}
                    options={KEY_STRATEGIES.map((s) => ({ value: s.value, label: s.label }))}
                  />
                </div>
                {form.keys.map((row, i) => (
                  <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <Switch
                      checked={row.enabled}
                      title={row.enabled ? '已启用，点击停用' : '已停用，点击启用'}
                      onChange={(v) => {
                        const keys = [...form.keys]
                        keys[i] = { ...keys[i], enabled: v }
                        setForm({ ...form, keys })
                      }}
                    />
                    {form.isPlugin ? (
                      <div
                        style={{
                          flex: 1,
                          minWidth: 0,
                          border: '1px solid var(--border)',
                          borderRadius: 7,
                          padding: '5px 8px',
                          display: 'flex',
                          alignItems: 'center',
                          gap: 6,
                          fontSize: 11.5,
                        }}
                      >
                        <span>{row.label || row.key}</span>
                        {row.label ? (
                          <span className="mono faint" style={{ marginLeft: 6, fontSize: 10 }}>
                            {row.key}
                          </span>
                        ) : null}
                      </div>
                    ) : (
                      <Input
                        value={row.key}
                        onChange={(e) => {
                          const keys = [...form.keys]
                          keys[i] = { ...keys[i], key: e.target.value }
                          setForm({ ...form, keys })
                        }}
                        placeholder="sk-..."
                      />
                    )}
                    {form.isPlugin ? (
                      <>
                        <Button
                          variant="ghost"
                          disabled={i === 0}
                          onClick={() => {
                            const keys = [...form.keys]
                            ;[keys[i - 1], keys[i]] = [keys[i], keys[i - 1]]
                            setForm({ ...form, keys })
                          }}
                        >
                          上移
                        </Button>
                        <Button
                          variant="ghost"
                          disabled={i === form.keys.length - 1}
                          onClick={() => {
                            const keys = [...form.keys]
                            ;[keys[i + 1], keys[i]] = [keys[i], keys[i + 1]]
                            setForm({ ...form, keys })
                          }}
                        >
                          下移
                        </Button>
                      </>
                    ) : (
                      <Button
                        variant="ghost"
                        title="移除密钥"
                        onClick={() =>
                          setForm({ ...form, keys: form.keys.filter((_, idx) => idx !== i) })
                        }
                      >
                        ✕
                      </Button>
                    )}
                  </div>
                ))}
                {form.isPlugin ? null : (
                  <div>
                    <Button
                      onClick={() =>
                        setForm({ ...form, keys: [...form.keys, { key: '', enabled: true }] })
                      }
                    >
                      添加密钥
                    </Button>
                  </div>
                )}
              </div>
            </Field>

            <Field label="模型列表" hint="每行一个，支持从上游拉取后勾选">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <Textarea
                  rows={5}
                  value={form.modelsText}
                  onChange={(e) => setForm({ ...form, modelsText: e.target.value })}
                  placeholder={'meta/llama-3.1-405b-instruct\nqwen/qwen2.5-72b-instruct'}
                />
                <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
                  <Button
                    disabled={fetchModels.isPending || !form.base_url.trim()}
                    onClick={() => fetchModels.mutate(form)}
                  >
                    {fetchModels.isPending ? '拉取中…' : '获取上游模型'}
                  </Button>
                  {fetched ? (
                    <>
                      <Button
                        variant="primary"
                        disabled={!fetched.models.some((m) => fetched.picked[m])}
                        onClick={applyPicked}
                      >
                        添加选中
                      </Button>
                      <Button variant="ghost" onClick={() => setFetched(null)}>
                        取消
                      </Button>
                    </>
                  ) : null}
                </div>
                {fetched ? (
                  <div style={{ border: '1px solid var(--border)', borderRadius: 8 }}>
                    <div
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 8,
                        padding: '7px 10px',
                        borderBottom: '1px solid var(--hairline)',
                        fontSize: 11,
                      }}
                    >
                      <span className="faint">上游返回 {fetched.models.length} 个模型</span>
                      <span style={{ display: 'flex', gap: 8 }}>
                        <button
                          type="button"
                          className="btn ghost sm"
                          onClick={() => setAllPicked(true)}
                        >
                          全选
                        </button>
                        <button
                          type="button"
                          className="btn ghost sm"
                          onClick={() => setAllPicked(false)}
                        >
                          全不选
                        </button>
                      </span>
                    </div>
                    <div style={{ maxHeight: 208, overflowY: 'auto', padding: 8 }}>
                      {fetched.models.map((m) => (
                        <label
                          key={m}
                          style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: 8,
                            padding: '3px 6px',
                            borderRadius: 5,
                            cursor: 'pointer',
                            fontSize: 11.5,
                          }}
                        >
                          <input
                            type="checkbox"
                            checked={!!fetched.picked[m]}
                            onChange={() => togglePicked(m)}
                          />
                          <span className="mono" style={{ fontSize: 11 }}>
                            {m}
                          </span>
                          {splitLines(form.modelsText).includes(m) ? (
                            <Badge tone="off">已在列表</Badge>
                          ) : null}
                        </label>
                      ))}
                    </div>
                  </div>
                ) : null}
              </div>
            </Field>

            <Field label="生图模型" hint="每行一个，仅用于 /v1/images/generations">
              <Textarea
                rows={3}
                value={form.imageModelsText}
                onChange={(e) => setForm({ ...form, imageModelsText: e.target.value })}
                placeholder={'Qwen/Qwen-Image-2.1-Turbo'}
              />
            </Field>

            <ImageSection form={form} setForm={setForm} />

            <Field label="模型映射" hint="公开模型名 → 上游模型名，优先于模型列表">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {form.modelMap.map((row, i) => (
                  <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <Input
                      value={row.pub}
                      onChange={(e) => {
                        const modelMap = [...form.modelMap]
                        modelMap[i] = { ...modelMap[i], pub: e.target.value }
                        setForm({ ...form, modelMap })
                      }}
                      placeholder="公开模型名"
                    />
                    <span className="faint">→</span>
                    <Input
                      value={row.upstream}
                      onChange={(e) => {
                        const modelMap = [...form.modelMap]
                        modelMap[i] = { ...modelMap[i], upstream: e.target.value }
                        setForm({ ...form, modelMap })
                      }}
                      placeholder="上游模型名"
                    />
                    <Button
                      variant="ghost"
                      title="移除映射"
                      onClick={() =>
                        setForm({ ...form, modelMap: form.modelMap.filter((_, idx) => idx !== i) })
                      }
                    >
                      ✕
                    </Button>
                  </div>
                ))}
                <div>
                  <Button
                    onClick={() =>
                      setForm({ ...form, modelMap: [...form.modelMap, { pub: '', upstream: '' }] })
                    }
                  >
                    添加映射
                  </Button>
                </div>
              </div>
            </Field>

            <Field
              label="自定义请求头"
              hint="支持 {{请求头名}}、{{a | b}}、{{[常量]}}、{{uuid}}、{{timestamp}}"
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {form.headers.map((row, i) => (
                  <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <Input
                      value={row.name}
                      onChange={(e) => {
                        const headers = [...form.headers]
                        headers[i] = { ...headers[i], name: e.target.value }
                        setForm({ ...form, headers })
                      }}
                      placeholder="x-session-id"
                    />
                    <Input
                      value={row.value}
                      onChange={(e) => {
                        const headers = [...form.headers]
                        headers[i] = { ...headers[i], value: e.target.value }
                        setForm({ ...form, headers })
                      }}
                      placeholder="{{x-session-id | [gw-session-001]}}"
                    />
                    <Button
                      variant="ghost"
                      title="移除请求头"
                      onClick={() =>
                        setForm({ ...form, headers: form.headers.filter((_, idx) => idx !== i) })
                      }
                    >
                      ✕
                    </Button>
                  </div>
                ))}
                <div>
                  <Button
                    onClick={() =>
                      setForm({ ...form, headers: [...form.headers, { name: '', value: '' }] })
                    }
                  >
                    添加请求头
                  </Button>
                </div>
              </div>
            </Field>

            <details>
              <summary className="tiny faint" style={{ cursor: 'pointer' }}>
                高级：请求体参数适配
              </summary>
              <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 9 }}>
                <Field label="参数映射（客户端参数 → 上游参数）">
                  <HeaderRows
                    rows={form.paramMap}
                    onChange={(rows) => setForm({ ...form, paramMap: rows })}
                    namePlaceholder="max_tokens"
                    valuePlaceholder="max_completion_tokens"
                    addLabel="添加映射"
                  />
                </Field>
                <div className="admingrid g2">
                  <Field label="参数改写" hint="客户端未传时补充这些字段">
                    <Textarea
                      value={form.staticParamsText}
                      onChange={(e) => setForm({ ...form, staticParamsText: e.target.value })}
                      placeholder='{"thinking": {"type": "disabled"}}'
                    />
                  </Field>
                  <Field label="参数剥离" hint="每行一个，发往该渠道前删除">
                    <Textarea
                      value={form.dropParamsText}
                      onChange={(e) => setForm({ ...form, dropParamsText: e.target.value })}
                      placeholder="thinking"
                    />
                  </Field>
                </div>
              </div>
            </details>
          </div>
        </Modal>
      ) : null}
    </div>
  )
}

/* 卡片头部：正反面共用，含状态圆点、名称、徽章、地址、开关 */
function ChannelHead({
  ch,
  dotTone,
  onToggle,
}: {
  ch: Channel
  dotTone: 'ok' | 'warn' | 'off'
  onToggle: () => void
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8 }}>
      <div style={{ minWidth: 0 }}>
        <div className="ch-name-cell">
          <Dot tone={dotTone} />
          <span className="ch-name" style={{ fontSize: 13, fontWeight: 600 }}>
            {ch.name}
          </span>
          {isPluginChannel(ch) ? <Badge tone="info">插件</Badge> : null}
          {ch.image_generation?.mode ? <Badge tone="ok">生图</Badge> : null}
        </div>
        <div
          className="mono truncate"
          style={{ marginTop: 5, fontSize: 10.5, color: 'var(--text-3)' }}
          title={ch.base_url}
        >
          {ch.base_url}
        </div>
      </div>
      <Switch
        checked={ch.enabled}
        onChange={onToggle}
        title={ch.enabled ? '已启用，点击停用' : '已停用，点击启用'}
      />
    </div>
  )
}

/* 生图配置区：按模式显示不同字段，高级项折叠 */
function ImageSection({
  form,
  setForm,
}: {
  form: FormState
  setForm: (f: FormState) => void
}) {
  const show = form.imageMode !== ''
  return (
    <div
      style={{
        border: '1px solid var(--border)',
        borderRadius: 8,
        padding: 11,
        display: 'flex',
        flexDirection: 'column',
        gap: 9,
      }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
        <span style={{ fontSize: 11.5, fontWeight: 500 }}>生图调用配置</span>
        <Segmented
          value={form.imageMode}
          onChange={(v) => setForm({ ...form, imageMode: v as FormState['imageMode'] })}
          options={IMAGE_MODES.map((m) => ({ value: m.value, label: m.label }))}
        />
      </div>

      {show ? (
        <>
          <div className="admingrid g2">
            <Field label="提交路径" hint="留空默认 /images/generations">
              <Input
                value={form.imagePath}
                onChange={(e) => setForm({ ...form, imagePath: e.target.value })}
                placeholder="/images/generations"
              />
            </Field>
            {form.imageMode === 'async' ? (
              <>
                <Field label="轮询路径" hint="留空默认 /tasks/{task_id}">
                  <Input
                    value={form.imagePollPath}
                    onChange={(e) => setForm({ ...form, imagePollPath: e.target.value })}
                    placeholder="/tasks/{task_id}"
                  />
                </Field>
                <Field label="轮询间隔（秒）" hint="默认 5 秒">
                  <Input
                    type="number"
                    value={form.imageIntervalSec}
                    onChange={(e) => setForm({ ...form, imageIntervalSec: e.target.value })}
                  />
                </Field>
              </>
            ) : null}
            <Field label="任务超时（秒）" hint="默认 360 秒">
              <Input
                type="number"
                value={form.imageTimeoutSec}
                onChange={(e) => setForm({ ...form, imageTimeoutSec: e.target.value })}
              />
            </Field>
          </div>

          <details>
            <summary className="tiny faint" style={{ cursor: 'pointer' }}>
              高级：通用适配
            </summary>
            <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 9 }}>
              <div className="admingrid g2">
                <Field label="同步响应超时（秒）">
                  <Input
                    type="number"
                    value={form.imageSyncTimeoutSec}
                    onChange={(e) => setForm({ ...form, imageSyncTimeoutSec: e.target.value })}
                    placeholder="沿用任务超时"
                  />
                </Field>
                <Field label="编辑路径（可选）">
                  <Input
                    value={form.imageEditPath}
                    onChange={(e) => setForm({ ...form, imageEditPath: e.target.value })}
                    placeholder="/images/edits"
                  />
                </Field>
                <Field label="参考图字段名">
                  <Input
                    value={form.imageRefField}
                    onChange={(e) => setForm({ ...form, imageRefField: e.target.value })}
                    placeholder="image_url"
                  />
                </Field>
              </div>

              <Field label="结果格式（response_format）">
                <Segmented
                  value={form.imageFormatMode}
                  onChange={(v) => setForm({ ...form, imageFormatMode: v })}
                  options={IMAGE_FORMAT_MODES.map((m) => ({ value: m.value, label: m.label }))}
                />
              </Field>

              <Field label="参考图形态">
                <Segmented
                  value={form.imageRefShape}
                  onChange={(v) => setForm({ ...form, imageRefShape: v })}
                  options={IMAGE_REF_SHAPES.map((m) => ({ value: m.value, label: m.label }))}
                />
              </Field>

              <Field label="参数映射（客户端参数 → 上游参数）">
                <HeaderRows
                  rows={form.imageParamMap}
                  onChange={(rows) => setForm({ ...form, imageParamMap: rows })}
                  namePlaceholder="size"
                  valuePlaceholder="image_size"
                  addLabel="添加映射"
                />
              </Field>

              <div className="admingrid g2">
                <Field label="参数改写">
                  <Textarea
                    value={form.imageStaticParamsText}
                    onChange={(e) => setForm({ ...form, imageStaticParamsText: e.target.value })}
                    placeholder='{"watermark": false}'
                  />
                </Field>
                <Field label="参数剥离">
                  <Textarea
                    value={form.imageDropParamsText}
                    onChange={(e) => setForm({ ...form, imageDropParamsText: e.target.value })}
                    placeholder="response_format"
                  />
                </Field>
              </div>
            </div>
          </details>

          <Field label="提交附加请求头">
            <HeaderRows
              rows={form.imageSubmitHeaders}
              onChange={(rows) => setForm({ ...form, imageSubmitHeaders: rows })}
            />
          </Field>

          {form.imageMode === 'async' ? (
            <>
              <Field label="轮询附加请求头">
                <HeaderRows
                  rows={form.imagePollHeaders}
                  onChange={(rows) => setForm({ ...form, imagePollHeaders: rows })}
                />
              </Field>

              <details>
                <summary className="tiny faint" style={{ cursor: 'pointer' }}>
                  高级：任务字段映射
                </summary>
                <div className="admingrid g2" style={{ marginTop: 8 }}>
                  <Field label="任务 ID 字段">
                    <Input
                      value={form.imageTaskIDField}
                      placeholder="task_id"
                      onChange={(e) => setForm({ ...form, imageTaskIDField: e.target.value })}
                    />
                  </Field>
                  <Field label="状态字段">
                    <Input
                      value={form.imageStatusField}
                      placeholder="task_status"
                      onChange={(e) => setForm({ ...form, imageStatusField: e.target.value })}
                    />
                  </Field>
                  <Field label="成功状态值">
                    <Input
                      value={form.imageSuccessStatus}
                      placeholder="SUCCEED"
                      onChange={(e) => setForm({ ...form, imageSuccessStatus: e.target.value })}
                    />
                  </Field>
                  <Field label="失败状态值">
                    <Input
                      value={form.imageFailedStatus}
                      placeholder="FAILED"
                      onChange={(e) => setForm({ ...form, imageFailedStatus: e.target.value })}
                    />
                  </Field>
                  <Field label="图片列表字段">
                    <Input
                      value={form.imageImagesField}
                      placeholder="output_images"
                      onChange={(e) => setForm({ ...form, imageImagesField: e.target.value })}
                    />
                  </Field>
                  <Field label="失败原因字段">
                    <Input
                      value={form.imageErrorField}
                      placeholder="error"
                      onChange={(e) => setForm({ ...form, imageErrorField: e.target.value })}
                    />
                  </Field>
                </div>
              </details>
            </>
          ) : null}
        </>
      ) : null}
    </div>
  )
}

/* 键值对行编辑器（生图请求头 / 参数映射复用） */
function HeaderRows({
  rows,
  onChange,
  namePlaceholder = 'X-ModelScope-Async-Mode',
  valuePlaceholder = 'true',
  addLabel = '添加请求头',
}: {
  rows: HeaderRow[]
  onChange: (rows: HeaderRow[]) => void
  namePlaceholder?: string
  valuePlaceholder?: string
  addLabel?: string
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {rows.map((row, i) => (
        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Input
            value={row.name}
            onChange={(e) => {
              const next = [...rows]
              next[i] = { ...next[i], name: e.target.value }
              onChange(next)
            }}
            placeholder={namePlaceholder}
          />
          <Input
            value={row.value}
            onChange={(e) => {
              const next = [...rows]
              next[i] = { ...next[i], value: e.target.value }
              onChange(next)
            }}
            placeholder={valuePlaceholder}
          />
          <Button variant="ghost" title="移除" onClick={() => onChange(rows.filter((_, idx) => idx !== i))}>
            ✕
          </Button>
        </div>
      ))}
      <div>
        <Button onClick={() => onChange([...rows, { name: '', value: '' }])}>{addLabel}</Button>
      </div>
    </div>
  )
}

function openEdit(ch: Channel, setForm: (f: FormState) => void) {
  setForm({
    id: ch.id,
    name: ch.name,
    base_url: ch.base_url,
    path: ch.path,
    keys:
      ch.keys.length > 0
        ? ch.keys.map((k) => ({ key: k.key, enabled: k.enabled, label: k.label }))
        : [{ key: '', enabled: true }],
    modelsText: ch.models.join('\n'),
    modelMap: Object.entries(ch.model_map).map(([pub, upstream]) => ({ pub, upstream })),
    headers: ch.headers.map((h) => ({ name: h.name, value: h.value })),
    paramMap: Object.entries(ch.param_map ?? {}).map(([name, value]) => ({ name, value })),
    staticParamsText:
      ch.static_params && Object.keys(ch.static_params).length > 0
        ? JSON.stringify(ch.static_params, null, 2)
        : '',
    dropParamsText: (ch.drop_params ?? []).join('\n'),
    imageModelsText: (ch.image_models ?? []).join('\n'),
    imageMode: (ch.image_generation?.mode ?? '') as FormState['imageMode'],
    imagePath: ch.image_generation?.path ?? '',
    imagePollPath: ch.image_generation?.poll_path ?? '',
    imageIntervalSec: String(
      ch.image_generation?.poll_interval_ms ? ch.image_generation.poll_interval_ms / 1000 : 5,
    ),
    imageTimeoutSec: String(
      ch.image_generation?.timeout_ms ? ch.image_generation.timeout_ms / 1000 : 360,
    ),
    imageSubmitHeaders: (ch.image_generation?.submit_headers ?? []).map((h) => ({
      name: h.name,
      value: h.value,
    })),
    imagePollHeaders: (ch.image_generation?.poll_headers ?? []).map((h) => ({
      name: h.name,
      value: h.value,
    })),
    imageTaskIDField: ch.image_generation?.task_id_field ?? '',
    imageStatusField: ch.image_generation?.status_field ?? '',
    imageSuccessStatus: ch.image_generation?.success_status ?? '',
    imageFailedStatus: ch.image_generation?.failed_status ?? '',
    imageImagesField: ch.image_generation?.images_field ?? '',
    imageErrorField: ch.image_generation?.error_field ?? '',
    imageSyncTimeoutSec: ch.image_generation?.sync_timeout_ms
      ? String(ch.image_generation.sync_timeout_ms / 1000)
      : '',
    imageFormatMode: ch.image_generation?.response_format_mode || 'drop',
    imageParamMap: Object.entries(ch.image_generation?.param_map ?? {}).map(([name, value]) => ({
      name,
      value,
    })),
    imageStaticParamsText:
      ch.image_generation?.static_params && Object.keys(ch.image_generation.static_params).length > 0
        ? JSON.stringify(ch.image_generation.static_params, null, 2)
        : '',
    imageDropParamsText: (ch.image_generation?.drop_params ?? []).join('\n'),
    imageEditPath: ch.image_generation?.edit_path ?? '',
    imageRefField: ch.image_generation?.reference_field ?? '',
    imageRefShape: ch.image_generation?.reference_shape || 'url_array',
    keyStrategy: ch.key_strategy || 'rotate',
    enabled: ch.enabled,
    isPlugin: isPluginChannel(ch),
  })
}

function toHeaders(rows: HeaderRow[]) {
  return rows.map((h) => ({ name: h.name.trim(), value: h.value })).filter((h) => h.name !== '')
}

function secondsToMS(text: string): number {
  const n = Number(text)
  if (!Number.isFinite(n) || n <= 0) return 0
  return Math.round(n * 1000)
}

function splitLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

function parseJSONObject(text: string): Record<string, unknown> {
  const raw = text.trim()
  if (!raw) return {}
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    throw new Error('参数改写不是合法 JSON')
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('参数改写必须是 JSON 对象，例如 {"watermark": false}')
  }
  return value as Record<string, unknown>
}
