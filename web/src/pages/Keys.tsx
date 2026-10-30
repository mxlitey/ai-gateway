import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type APIKey, type Channel, type MCPServer } from '../lib/api'
import { toast } from '../lib/toast'
import { Button, Card, Empty, ErrorText, Field, Input, Loading, Modal, Switch } from '../components/ui'

// Scope 密钥在某类资源上的权限范围：全部 / 仅勾选 / 禁用。
type Scope = APIKey['channel_scope']

// isPluginChannel 判断渠道是否由插件注册（source 形如 plugin:<插件id>）。
function isPluginChannel(ch: Channel): boolean {
  return ch.source.startsWith('plugin:')
}

export default function Keys() {
  const queryClient = useQueryClient()
  const keys = useQuery({ queryKey: ['apikeys'], queryFn: api.apiKeys })
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })
  // MCP 权限：密钥可访问哪些 MCP 服务器（不选表示全部）
  const mcpServers = useQuery({ queryKey: ['mcp-servers'], queryFn: api.mcpServers })
  // 插件渠道由插件自动注册，需要知道哪些插件正在运行才能决定是否可选
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.plugins })

  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [channelScope, setChannelScope] = useState<Scope>('all')
  const [channelIds, setChannelIds] = useState<string[]>([])
  const [mcpScope, setMcpScope] = useState<Scope>('all')
  const [mcpServerIds, setMcpServerIds] = useState<string[]>([])
  const [created, setCreated] = useState<APIKey | null>(null)
  const [editing, setEditing] = useState<APIKey | null>(null)
  const [editName, setEditName] = useState('')
  const [editChannelScope, setEditChannelScope] = useState<Scope>('all')
  const [editChannelIds, setEditChannelIds] = useState<string[]>([])
  const [editMcpScope, setEditMcpScope] = useState<Scope>('all')
  const [editMcpServerIds, setEditMcpServerIds] = useState<string[]>([])
  // 翻转卡片：记录当前翻到背面的密钥 id
  const [flippedId, setFlippedId] = useState<string | null>(null)

  // 剪贴板 API 只在安全上下文（HTTPS / localhost）可用；
  // 通过局域网 IP 用 http 访问时 navigator.clipboard 不可用，需回退到 execCommand
  const copyKey = async (text: string) => {
    if (await writeClipboard(text)) {
      toast('ok', '已复制到剪贴板')
    } else {
      toast('err', '复制失败，请手动选择复制')
    }
  }

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['apikeys'] })

  const create = useMutation({
    mutationFn: () =>
      api.createApiKey({
        name: name.trim() || 'Unnamed',
        channel_scope: channelScope,
        channel_ids: channelScope === 'selected' ? channelIds : [],
        mcp_scope: mcpScope,
        mcp_server_ids: mcpScope === 'selected' ? mcpServerIds : [],
      }),
    onSuccess: async (key) => {
      setCreated(key)
      setCreating(false)
      setName('')
      setChannelScope('all')
      setChannelIds([])
      setMcpScope('all')
      setMcpServerIds([])
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const toggle = useMutation({
    mutationFn: (key: APIKey) => api.updateApiKey(key.id, { enabled: !key.enabled }),
    onSuccess: invalidate,
    onError: (err: Error) => toast('err', err.message),
  })

  const saveEdit = useMutation({
    mutationFn: () =>
      api.updateApiKey(editing!.id, {
        name: editName.trim() || 'Unnamed',
        channel_scope: editChannelScope,
        channel_ids: editChannelScope === 'selected' ? editChannelIds : [],
        mcp_scope: editMcpScope,
        mcp_server_ids: editMcpScope === 'selected' ? editMcpServerIds : [],
      }),
    onSuccess: async () => {
      toast('ok', '密钥已更新')
      setEditing(null)
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteApiKey(id),
    onSuccess: async () => {
      toast('ok', '密钥已删除')
      await invalidate()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const openEdit = (key: APIKey) => {
    setEditing(key)
    setEditName(key.name)
    setEditChannelScope(key.channel_scope ?? 'all')
    setEditChannelIds(key.channel_ids)
    setEditMcpScope(key.mcp_scope ?? 'all')
    setEditMcpServerIds(key.mcp_server_ids ?? [])
  }

  if (keys.isLoading || channels.isLoading || plugins.isLoading || mcpServers.isLoading) return <Loading />
  if (keys.isError) return <ErrorText text={(keys.error as Error).message} />

  const list = keys.data ?? []
  const allChannels = channels.data ?? []
  const allMcpServers = mcpServers.data ?? []
  // 没有启动的插件不进入可选渠道，避免把密钥绑到实际不存在的上游上
  const runningPlugins = new Set(
    (plugins.data ?? []).filter((p) => p.last_status === 'running').map((p) => p.id),
  )
  const channelList = allChannels.filter(
    (ch) => !isPluginChannel(ch) || runningPlugins.has(ch.source.slice('plugin:'.length)),
  )
  // 名称解析仍用全量渠道，否则插件停用后已绑定的渠道会显示成裸 id
  const channelName = (id: string) => allChannels.find((c) => c.id === id)?.name ?? id
  const mcpName = (id: string) => allMcpServers.find((s) => s.id === id)?.name ?? id

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">API 密钥</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>
          生成密钥
        </Button>
      </div>

      <Card>
        {list.length === 0 ? (
          <Empty
            text="还没有客户端密钥"
            action={
              <Button variant="primary" onClick={() => setCreating(true)}>
                生成密钥
              </Button>
            }
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 sm:gap-4 xl:grid-cols-3">
            {list.map((key) => {
              const flipped = flippedId === key.id
              const channelScopeText =
                key.channel_scope === 'none'
                  ? '未授权'
                  : key.channel_scope === 'selected'
                    ? key.channel_ids.map(channelName).join('、')
                    : '全部渠道'
              const mcpScopeText =
                key.mcp_scope === 'none'
                  ? '未授权'
                  : key.mcp_scope === 'selected'
                    ? (key.mcp_server_ids ?? []).map(mcpName).join('、')
                    : '全部 MCP 服务器'
              const toggleFlip = () => setFlippedId(flipped ? null : key.id)
              return (
                <div
                  key={key.id}
                  role="button"
                  tabIndex={0}
                  title={flipped ? '点击返回' : '点击查看操作'}
                  onClick={toggleFlip}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      toggleFlip()
                    }
                  }}
                  className={`flip-card h-40 cursor-pointer ${flipped ? 'flipped' : ''}`}
                >
                  <div className="flip-card-inner">
                    {/* 正面：概览 */}
                    <div className="flip-face flex flex-col justify-between rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
                      <KeyCardHead
                        k={key}
                        onCopy={() => copyKey(key.key)}
                        onToggle={() => toggle.mutate(key)}
                      />
                      <div className="space-y-0.5 text-xs text-zinc-500 dark:text-zinc-400">
                        <p className="truncate" title={channelScopeText}>
                          可用渠道{' '}
                          <span className="text-zinc-700 dark:text-zinc-200">{channelScopeText}</span>
                        </p>
                        <p className="truncate" title={mcpScopeText}>
                          可用 MCP{' '}
                          <span className="text-zinc-700 dark:text-zinc-200">{mcpScopeText}</span>
                        </p>
                      </div>
                    </div>

                    {/* 背面：头部与正面一致，只把「可用渠道」换成操作按钮 */}
                    <div className="flip-face flip-face-back flex flex-col justify-between rounded-xl border border-zinc-200 bg-zinc-50 p-4 dark:border-zinc-800 dark:bg-zinc-950">
                      <KeyCardHead
                        k={key}
                        onCopy={() => copyKey(key.key)}
                        onToggle={() => toggle.mutate(key)}
                      />
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Button
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            openEdit(key)
                          }}
                        >
                          编辑
                        </Button>
                        <Button
                          size="sm"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            toggle.mutate(key)
                          }}
                        >
                          {key.enabled ? '停用' : '启用'}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-red-600 dark:text-red-400"
                          onClick={(e) => {
                            e.stopPropagation()
                            setFlippedId(null)
                            if (window.confirm(`确认删除密钥「${key.name}」？`)) remove.mutate(key.id)
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
      </Card>

      {creating ? (
        <Modal
          open
          title="生成 API 密钥"
          onClose={() => setCreating(false)}
          footer={
            <>
              <Button onClick={() => setCreating(false)}>取消</Button>
              <Button variant="primary" disabled={create.isPending} onClick={() => create.mutate()}>
                {create.isPending ? '生成中…' : '生成'}
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <Field label="名称">
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="如 我的笔记本"
                autoFocus
              />
            </Field>
            <Field label="渠道权限" hint="「全部」含之后新增的渠道；「禁用」表示这把密钥不能调用 /v1 接口">
              <div className="space-y-2">
                <ScopePicker value={channelScope} onChange={setChannelScope} />
                {channelScope === 'selected' ? (
                  <div className="max-h-52 space-y-1.5 overflow-y-auto rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
                    <ChannelPicker
                      channels={channelList}
                      value={channelIds}
                      onChange={setChannelIds}
                    />
                  </div>
                ) : null}
              </div>
            </Field>
            <Field
              label="MCP 权限"
              hint="「全部」含之后新增的服务器；「禁用」表示这把密钥不能调用 /mcp 接口"
            >
              <div className="space-y-2">
                <ScopePicker value={mcpScope} onChange={setMcpScope} />
                {mcpScope === 'selected' ? (
                  <div className="max-h-52 space-y-1.5 overflow-y-auto rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
                    <McpPicker
                      servers={allMcpServers}
                      value={mcpServerIds}
                      onChange={setMcpServerIds}
                    />
                  </div>
                ) : null}
              </div>
            </Field>
          </div>
        </Modal>
      ) : null}

      {created ? (
        <Modal
          open
          title="密钥已生成"
          onClose={() => setCreated(null)}
          footer={
            <Button variant="primary" onClick={() => setCreated(null)}>
              我已保存
            </Button>
          }
        >
          <div className="space-y-3">
            <p className="text-sm text-zinc-500 dark:text-zinc-400">
              密钥已保存，之后在列表里可以随时点「复制」取用。
            </p>
            <div className="flex flex-wrap items-center gap-2">
              <Input readOnly value={created.key} className="min-w-0 flex-1 basis-48 font-mono" />
              <Button onClick={() => copyKey(created.key)}>复制</Button>
            </div>
          </div>
        </Modal>
      ) : null}

      {editing ? (
        <Modal
          open
          title="编辑 API 密钥"
          onClose={() => setEditing(null)}
          footer={
            <>
              <Button onClick={() => setEditing(null)}>取消</Button>
              <Button variant="primary" disabled={saveEdit.isPending} onClick={() => saveEdit.mutate()}>
                {saveEdit.isPending ? '保存中…' : '保存'}
              </Button>
            </>
          }
        >
          <div className="space-y-4">
            <Field label="名称">
              <Input value={editName} onChange={(e) => setEditName(e.target.value)} autoFocus />
            </Field>
            <Field label="渠道权限" hint="「全部」含之后新增的渠道；「禁用」表示这把密钥不能调用 /v1 接口">
              <div className="space-y-2">
                <ScopePicker value={editChannelScope} onChange={setEditChannelScope} />
                {editChannelScope === 'selected' ? (
                  <div className="max-h-52 space-y-1.5 overflow-y-auto rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
                    <ChannelPicker
                      channels={channelList}
                      value={editChannelIds}
                      onChange={setEditChannelIds}
                    />
                  </div>
                ) : null}
              </div>
            </Field>
            <Field
              label="MCP 权限"
              hint="「全部」含之后新增的服务器；「禁用」表示这把密钥不能调用 /mcp 接口"
            >
              <div className="space-y-2">
                <ScopePicker value={editMcpScope} onChange={setEditMcpScope} />
                {editMcpScope === 'selected' ? (
                  <div className="max-h-52 space-y-1.5 overflow-y-auto rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
                    <McpPicker
                      servers={allMcpServers}
                      value={editMcpServerIds}
                      onChange={setEditMcpServerIds}
                    />
                  </div>
                ) : null}
              </div>
            </Field>
          </div>
        </Modal>
      ) : null}
    </div>
  )
}

// KeyCardHead 翻转卡片正反两面共用的头部：名称 / 脱敏密钥（可复制）/ 启用开关。
function KeyCardHead({
  k,
  onCopy,
  onToggle,
}: {
  k: APIKey
  onCopy: () => void
  onToggle: () => void
}) {
  return (
    <div className="flex items-start justify-between gap-2">
      <div className="min-w-0">
        <span className="block truncate text-sm font-semibold text-zinc-800 dark:text-zinc-100">
          {k.name}
        </span>
        <div className="mt-1 flex items-center gap-1">
          <span className="truncate font-mono text-xs text-zinc-500 dark:text-zinc-400">
            {maskKey(k.key)}
          </span>
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onCopy()
            }}
            title="复制密钥"
            aria-label="复制密钥"
            className="flex-none rounded-md p-1 text-zinc-500 hover:bg-zinc-100 hover:text-zinc-700 dark:hover:bg-zinc-800 dark:hover:text-zinc-200"
          >
            <svg
              width="15"
              height="15"
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
      </div>
      <Switch
        checked={k.enabled}
        onChange={onToggle}
        title={k.enabled ? '已启用，点击停用' : '已停用，点击启用'}
        className="mt-1"
      />
    </div>
  )
}

// ScopePicker 渲染权限范围三态：全部 / 仅勾选 / 禁用。
// 用显式范围代替「空列表即全部」，才能真正表达「一个都不授权」。
function ScopePicker({ value, onChange }: { value: Scope; onChange: (v: Scope) => void }) {
  const options: { value: Scope; label: string }[] = [
    { value: 'all', label: '全部' },
    { value: 'selected', label: '仅勾选' },
    { value: 'none', label: '禁用' },
  ]
  return (
    <div className="flex gap-1.5">
      {options.map((o) => {
        const active = value === o.value
        return (
          <button
            key={o.value}
            type="button"
            onClick={() => onChange(o.value)}
            className={`rounded-md border px-2.5 py-1 text-xs transition-colors ${
              active
                ? 'border-zinc-900 bg-zinc-900 text-white dark:border-zinc-100 dark:bg-zinc-100 dark:text-zinc-900'
                : 'border-zinc-200 text-zinc-600 hover:bg-zinc-100 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800'
            }`}
          >
            {o.label}
          </button>
        )
      })}
    </div>
  )
}

// ChannelPicker 渲染「限定可用渠道」的多选列表（生成与编辑共用）。
function ChannelPicker({
  channels,
  value,
  onChange,
}: {
  channels: Channel[]
  value: string[]
  onChange: (ids: string[]) => void
}) {
  if (channels.length === 0) {
    return <p className="text-sm text-zinc-500">还没有渠道</p>
  }
  return (
    <>
      {channels.map((c) => (
        <label
          key={c.id}
          className="flex items-center gap-2 text-sm text-zinc-700 dark:text-zinc-300"
        >
          <input
            type="checkbox"
            checked={value.includes(c.id)}
            onChange={(e) =>
              onChange(e.target.checked ? [...value, c.id] : value.filter((id) => id !== c.id))
            }
          />
          {c.name}
        </label>
      ))}
    </>
  )
}

// McpPicker 渲染「限定可用 MCP 服务器」的多选列表（生成与编辑共用）。
function McpPicker({
  servers,
  value,
  onChange,
}: {
  servers: MCPServer[]
  value: string[]
  onChange: (ids: string[]) => void
}) {
  if (servers.length === 0) {
    return <p className="text-sm text-zinc-500">还没有 MCP 服务器</p>
  }
  return (
    <>
      {servers.map((s) => (
        <label
          key={s.id}
          className="flex items-center gap-2 text-sm text-zinc-700 dark:text-zinc-300"
        >
          <input
            type="checkbox"
            checked={value.includes(s.id)}
            onChange={(e) =>
              onChange(e.target.checked ? [...value, s.id] : value.filter((id) => id !== s.id))
            }
          />
          {s.name}
          <span className="font-mono text-xs text-zinc-400">{s.alias}</span>
        </label>
      ))}
    </>
  )
}

// maskKey 只用于展示层：超长密钥打码为 前7位...后4位。
function maskKey(key: string): string {
  return key.length > 12 ? `${key.slice(0, 7)}...${key.slice(-4)}` : key
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