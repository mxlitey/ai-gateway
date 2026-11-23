import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type APIKey, type Channel, type MCPServer } from '../lib/api'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  Empty,
  ErrorText,
  Field,
  Input,
  Loading,
  Modal,
  PageHead,
  Segmented,
  Switch,
} from '../components/AdminUI'

type Scope = APIKey['channel_scope']
type StorageScope = APIKey['storage_scope']

function isPluginChannel(ch: Channel): boolean {
  return ch.source.startsWith('plugin:')
}

export default function Keys() {
  const queryClient = useQueryClient()
  const keys = useQuery({ queryKey: ['apikeys'], queryFn: api.apiKeys })
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })
  const mcpServers = useQuery({ queryKey: ['mcp-servers'], queryFn: api.mcpServers })
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.plugins })

  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [channelScope, setChannelScope] = useState<Scope>('all')
  const [channelIds, setChannelIds] = useState<string[]>([])
  const [mcpScope, setMcpScope] = useState<Scope>('all')
  const [mcpServerIds, setMcpServerIds] = useState<string[]>([])
  const [storageScope, setStorageScope] = useState<StorageScope>('all')
  const [created, setCreated] = useState<APIKey | null>(null)
  const [editing, setEditing] = useState<APIKey | null>(null)
  const [editName, setEditName] = useState('')
  const [editChannelScope, setEditChannelScope] = useState<Scope>('all')
  const [editChannelIds, setEditChannelIds] = useState<string[]>([])
  const [editMcpScope, setEditMcpScope] = useState<Scope>('all')
  const [editMcpServerIds, setEditMcpServerIds] = useState<string[]>([])
  const [editStorageScope, setEditStorageScope] = useState<StorageScope>('all')
  const [flippedId, setFlippedId] = useState<string | null>(null)

  const copyKey = async (text: string) => {
    if (await writeClipboard(text)) toast('ok', '已复制到剪贴板')
    else toast('err', '复制失败，请手动选择复制')
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
        storage_scope: storageScope,
      }),
    onSuccess: async (key) => {
      setCreated(key)
      setCreating(false)
      setName('')
      setChannelScope('all')
      setChannelIds([])
      setMcpScope('all')
      setMcpServerIds([])
      setStorageScope('all')
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
        storage_scope: editStorageScope,
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
    setEditStorageScope(key.storage_scope ?? 'all')
  }

  if (keys.isLoading || channels.isLoading || plugins.isLoading || mcpServers.isLoading) {
    return (
      <div className="panel">
        <Loading />
      </div>
    )
  }
  if (keys.isError) return <ErrorText text={(keys.error as Error).message} />

  const list = keys.data ?? []
  const allChannels = channels.data ?? []
  const allMcpServers = mcpServers.data ?? []
  const runningPlugins = new Set(
    (plugins.data ?? []).filter((p) => p.last_status === 'running').map((p) => p.id),
  )
  const channelList = allChannels.filter(
    (ch) => !isPluginChannel(ch) || runningPlugins.has(ch.source.slice('plugin:'.length)),
  )
  const channelName = (id: string) => allChannels.find((c) => c.id === id)?.name ?? id
  const mcpName = (id: string) => allMcpServers.find((s) => s.id === id)?.name ?? id

  return (
    <div className="stack">
      <PageHead
        title="API 密钥"
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            生成密钥
          </Button>
        }
      />

      {list.length === 0 ? (
        <div className="panel">
          <Empty text="还没有客户端密钥" />
        </div>
      ) : (
        <div className="ch-grid">
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
            const storageScopeText = key.storage_scope === 'none' ? '未授权' : '允许'
            const toggleFlip = () => setFlippedId(flipped ? null : key.id)
            return (
              <div
                key={key.id}
                role="button"
                tabIndex={0}
                title={flipped ? '点击返回' : '点击查看操作'}
                className={`flip-card${flipped ? ' flipped' : ''}`}
                onClick={toggleFlip}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    toggleFlip()
                  }
                }}
              >
                <div className="flip-card-inner">
                  {/* 正面：概览 */}
                  <div className="flip-face">
                    <KeyHead k={key} onCopy={() => copyKey(key.key)} onToggle={() => toggle.mutate(key)} />
                    <div className="ch-foot-info">
                      <span title={channelScopeText}>
                        可用渠道 <b>{channelScopeText}</b>
                      </span>
                      <span title={mcpScopeText}>
                        可用 MCP <b>{mcpScopeText}</b>
                      </span>
                      <span title={storageScopeText}>
                        对象存储 <b>{storageScopeText}</b>
                      </span>
                    </div>
                  </div>

                  {/* 背面：头部一致，底部换成操作 */}
                  <div className="flip-face fl-back">
                    <KeyHead k={key} onCopy={() => copyKey(key.key)} onToggle={() => toggle.mutate(key)} />
                    <div className="ch-foot-btns">
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
          <div className="stack">
            <Field label="名称">
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="如 我的笔记本"
                autoFocus
              />
            </Field>
            <Field label="渠道权限">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <ScopePicker value={channelScope} onChange={setChannelScope} />
                {channelScope === 'selected' ? (
                  <div
                    style={{
                      maxHeight: 208,
                      overflowY: 'auto',
                      border: '1px solid var(--border)',
                      borderRadius: 8,
                      padding: 10,
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 6,
                    }}
                  >
                    <ChannelPicker channels={channelList} value={channelIds} onChange={setChannelIds} />
                  </div>
                ) : null}
              </div>
            </Field>
            <Field label="MCP 权限">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <ScopePicker value={mcpScope} onChange={setMcpScope} />
                {mcpScope === 'selected' ? (
                  <div
                    style={{
                      maxHeight: 208,
                      overflowY: 'auto',
                      border: '1px solid var(--border)',
                      borderRadius: 8,
                      padding: 10,
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 6,
                    }}
                  >
                    <McpPicker servers={allMcpServers} value={mcpServerIds} onChange={setMcpServerIds} />
                  </div>
                ) : null}
              </div>
            </Field>
            <Field label="对象存储权限">
              <StorageScopePicker value={storageScope} onChange={setStorageScope} />
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
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Input readOnly value={created.key} className="mono" />
            <Button onClick={() => copyKey(created.key)}>复制</Button>
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
          <div className="stack">
            <Field label="名称">
              <Input value={editName} onChange={(e) => setEditName(e.target.value)} autoFocus />
            </Field>
            <Field label="渠道权限">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <ScopePicker value={editChannelScope} onChange={setEditChannelScope} />
                {editChannelScope === 'selected' ? (
                  <div
                    style={{
                      maxHeight: 208,
                      overflowY: 'auto',
                      border: '1px solid var(--border)',
                      borderRadius: 8,
                      padding: 10,
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 6,
                    }}
                  >
                    <ChannelPicker
                      channels={channelList}
                      value={editChannelIds}
                      onChange={setEditChannelIds}
                    />
                  </div>
                ) : null}
              </div>
            </Field>
            <Field label="MCP 权限">
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                <ScopePicker value={editMcpScope} onChange={setEditMcpScope} />
                {editMcpScope === 'selected' ? (
                  <div
                    style={{
                      maxHeight: 208,
                      overflowY: 'auto',
                      border: '1px solid var(--border)',
                      borderRadius: 8,
                      padding: 10,
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 6,
                    }}
                  >
                    <McpPicker
                      servers={allMcpServers}
                      value={editMcpServerIds}
                      onChange={setEditMcpServerIds}
                    />
                  </div>
                ) : null}
              </div>
            </Field>
            <Field label="对象存储权限">
              <StorageScopePicker value={editStorageScope} onChange={setEditStorageScope} />
            </Field>
          </div>
        </Modal>
      ) : null}
    </div>
  )
}

/* 卡片头部：名称 / 脱敏密钥（可复制）/ 启用开关，正反面共用 */
function KeyHead({
  k,
  onCopy,
  onToggle,
}: {
  k: APIKey
  onCopy: () => void
  onToggle: () => void
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8 }}>
      <div style={{ minWidth: 0 }}>
        <span className="truncate" style={{ display: 'block', fontSize: 13, fontWeight: 600 }}>
          {k.name}
        </span>
        <div style={{ display: 'flex', alignItems: 'center', gap: 4, marginTop: 4 }}>
          <span className="mono faint truncate" style={{ fontSize: 10.5 }}>
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
            className="btn ghost sm"
            style={{ flex: 'none', padding: '1px 4px' }}
          >
            <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round">
              <rect x="5.5" y="5.5" width="8.5" height="8.5" rx="2.2" />
              <path d="M10.5 5.5V4.2A2.2 2.2 0 0 0 8.3 2H4.2A2.2 2.2 0 0 0 2 4.2v4.1a2.2 2.2 0 0 0 2.2 2.2h1.3" />
            </svg>
          </button>
          {!k.enabled ? <Badge tone="off">已停用</Badge> : null}
        </div>
      </div>
      <Switch
        checked={k.enabled}
        onChange={onToggle}
        title={k.enabled ? '已启用，点击停用' : '已停用，点击启用'}
      />
    </div>
  )
}

function ScopePicker({ value, onChange }: { value: Scope; onChange: (v: Scope) => void }) {
  return (
    <Segmented
      value={value}
      onChange={onChange}
      options={[
        { value: 'all', label: '全部' },
        { value: 'selected', label: '仅勾选' },
        { value: 'none', label: '禁用' },
      ]}
    />
  )
}

function StorageScopePicker({
  value,
  onChange,
}: {
  value: StorageScope
  onChange: (v: StorageScope) => void
}) {
  return (
    <Segmented
      value={value}
      onChange={onChange}
      options={[
        { value: 'all', label: '启用' },
        { value: 'none', label: '禁用' },
      ]}
    />
  )
}

function ChannelPicker({
  channels,
  value,
  onChange,
}: {
  channels: Channel[]
  value: string[]
  onChange: (ids: string[]) => void
}) {
  if (channels.length === 0) return <div className="faint tiny">还没有渠道</div>
  return (
    <>
      {channels.map((c) => (
        <label key={c.id} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11.5 }}>
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

function McpPicker({
  servers,
  value,
  onChange,
}: {
  servers: MCPServer[]
  value: string[]
  onChange: (ids: string[]) => void
}) {
  if (servers.length === 0) return <div className="faint tiny">还没有 MCP 服务器</div>
  return (
    <>
      {servers.map((s) => (
        <label key={s.id} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11.5 }}>
          <input
            type="checkbox"
            checked={value.includes(s.id)}
            onChange={(e) =>
              onChange(e.target.checked ? [...value, s.id] : value.filter((id) => id !== s.id))
            }
          />
          {s.name}
          <span className="mono faint" style={{ fontSize: 10 }}>
            {s.alias}
          </span>
        </label>
      ))}
    </>
  )
}

function maskKey(key: string): string {
  return key.length > 12 ? `${key.slice(0, 7)}...${key.slice(-4)}` : key
}

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
