import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type APIKey, type Channel } from '../lib/api'
import { toast } from '../lib/toast'
import { Button, Card, Empty, ErrorText, Field, Input, Loading, Modal } from '../components/ui'

export default function Keys() {
  const queryClient = useQueryClient()
  const keys = useQuery({ queryKey: ['apikeys'], queryFn: api.apiKeys })
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.channels })

  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [channelIds, setChannelIds] = useState<string[]>([])
  const [created, setCreated] = useState<APIKey | null>(null)
  const [editing, setEditing] = useState<APIKey | null>(null)
  const [editName, setEditName] = useState('')
  const [editChannelIds, setEditChannelIds] = useState<string[]>([])
  // 翻转卡片：记录当前翻到背面的密钥 id
  const [flippedId, setFlippedId] = useState<string | null>(null)

  const copyKey = (text: string) => {
    void navigator.clipboard.writeText(text)
    toast('ok', '已复制到剪贴板')
  }

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['apikeys'] })

  const create = useMutation({
    mutationFn: () => api.createApiKey({ name: name.trim() || 'Unnamed', channel_ids: channelIds }),
    onSuccess: async (key) => {
      setCreated(key)
      setCreating(false)
      setName('')
      setChannelIds([])
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
        channel_ids: editChannelIds,
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
    setEditChannelIds(key.channel_ids)
  }

  if (keys.isLoading) return <Loading />
  if (keys.isError) return <ErrorText text={(keys.error as Error).message} />

  const list = keys.data ?? []
  const channelList = channels.data ?? []
  const channelName = (id: string) => channelList.find((c) => c.id === id)?.name ?? id

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
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
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {list.map((key) => {
              const flipped = flippedId === key.id
              const scope =
                key.channel_ids.length === 0
                  ? '全部渠道'
                  : key.channel_ids.map(channelName).join('、')
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
                      <KeyCardHead k={key} onCopy={() => copyKey(key.key)} />
                      <p className="truncate text-xs text-zinc-500 dark:text-zinc-400" title={scope}>
                        可用渠道{' '}
                        <span className="text-zinc-700 dark:text-zinc-200">{scope}</span>
                      </p>
                    </div>

                    {/* 背面：头部与正面一致，只把「可用渠道」换成操作按钮 */}
                    <div className="flip-face flip-face-back flex flex-col justify-between rounded-xl border border-zinc-200 bg-zinc-50 p-4 dark:border-zinc-800 dark:bg-zinc-950">
                      <KeyCardHead k={key} onCopy={() => copyKey(key.key)} />
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
            <Field label="限定可用渠道" hint="不勾选表示可使用全部渠道">
              <div className="max-h-52 space-y-1.5 overflow-y-auto rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
                <ChannelPicker channels={channelList} value={channelIds} onChange={setChannelIds} />
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
            <div className="flex items-center gap-2">
              <Input readOnly value={created.key} className="font-mono" />
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
            <Field label="限定可用渠道" hint="不勾选表示可使用全部渠道">
              <div className="max-h-52 space-y-1.5 overflow-y-auto rounded-lg border border-zinc-200 p-3 dark:border-zinc-800">
                <ChannelPicker
                  channels={channelList}
                  value={editChannelIds}
                  onChange={setEditChannelIds}
                />
              </div>
            </Field>
          </div>
        </Modal>
      ) : null}
    </div>
  )
}

// KeyCardHead 翻转卡片正反两面共用的头部：名称 / 脱敏密钥（可复制）/ 状态圆点。
function KeyCardHead({ k, onCopy }: { k: APIKey; onCopy: () => void }) {
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
      <span
        className={`mt-1.5 size-2 flex-none rounded-full ${
          k.enabled ? 'bg-[#34c759]' : 'bg-[#c7c7cc] dark:bg-zinc-600'
        }`}
        title={k.enabled ? '启用' : '停用'}
      />
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

// maskKey 只用于展示层：超长密钥打码为 前7位...后4位。
function maskKey(key: string): string {
  return key.length > 12 ? `${key.slice(0, 7)}...${key.slice(-4)}` : key
}