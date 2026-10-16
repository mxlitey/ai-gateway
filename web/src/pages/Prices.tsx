import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api, type PricesResponse } from '../lib/api'
import { toast } from '../lib/toast'
import { Badge, Button, Card, Empty, Field, IconButton, Input, Loading, RefreshIcon } from '../components/ui'

export default function Prices() {
  const prices = useQuery({ queryKey: ['prices'], queryFn: api.prices })

  const refreshPrices = useMutation({
    mutationFn: () => api.refreshPrices(),
    onSuccess: async (res) => {
      toast('ok', `已更新 ${res.models} 条模型价格`)
      await prices.refetch()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const savePrice = useMutation({
    mutationFn: (body: {
      model: string
      prompt_price: number
      completion_price: number
      cache_read_price?: number
    }) => api.upsertPrice(body),
    onSuccess: async () => {
      toast('ok', '价格已保存')
      await prices.refetch()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const deletePrice = useMutation({
    mutationFn: (name: string) => api.deletePrice(name),
    onSuccess: async () => {
      toast('ok', '价格已删除')
      await prices.refetch()
    },
    onError: (err: Error) => toast('err', err.message),
  })

  return (
    <div className="space-y-4">
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">价格表</h1>

      <PriceSection
        data={prices.data}
        loading={prices.isLoading}
        refreshing={refreshPrices.isPending}
        onRefresh={() => refreshPrices.mutate()}
        onSave={(body) => savePrice.mutate(body)}
        onDelete={(name) => deletePrice.mutate(name)}
      />
    </div>
  )
}

function PriceSection({
  data,
  loading,
  refreshing,
  onRefresh,
  onSave,
  onDelete,
}: {
  data?: PricesResponse
  loading: boolean
  refreshing: boolean
  onRefresh: () => void
  onSave: (body: {
    model: string
    prompt_price: number
    completion_price: number
    cache_read_price?: number
  }) => void
  onDelete: (name: string) => void
}) {
  const [form, setForm] = useState({ model: '', prompt: '', completion: '', cache: '' })
  const [query, setQuery] = useState('')

  const submit = () => {
    if (!form.model.trim()) {
      toast('err', '请填写模型名')
      return
    }
    onSave({
      model: form.model.trim(),
      prompt_price: Number(form.prompt) || 0,
      completion_price: Number(form.completion) || 0,
      cache_read_price: Number(form.cache) || 0,
    })
    setForm({ model: '', prompt: '', completion: '', cache: '' })
  }

  // 手工价排在最前，其余按模型名升序；再按搜索词实时过滤
  const q = query.trim().toLowerCase()
  const sorted = (data?.items ?? [])
    .slice()
    .sort(
      (a, b) =>
        (a.source === 'manual' ? 0 : 1) - (b.source === 'manual' ? 0 : 1) ||
        a.model.localeCompare(b.model),
    )
  const shown = q ? sorted.filter((p) => p.model.toLowerCase().includes(q)) : sorted

  return (
    <Card
      title="价格表"
      action={
        <div className="flex items-center gap-2">
          <span className="text-xs text-zinc-500">
            {data ? `${data.count} 条 · 单位 ${data.unit}` : ''}
          </span>
          <IconButton
            title={refreshing ? '拉取中…' : '刷新价格'}
            disabled={refreshing}
            onClick={onRefresh}
          >
            <RefreshIcon />
          </IconButton>
        </div>
      }
    >
      <p className="mb-3 text-xs text-zinc-500 dark:text-zinc-400">
        来源：<code>{data?.endpoint ?? ''}</code>
        {data?.refreshed_at ? (
          <> ；上次更新 {new Date(Number(data.refreshed_at)).toLocaleString()}</>
        ) : (
          <> ；尚未成功拉取过，可手工添加价格</>
        )}
      </p>

      <div className="mb-4 grid gap-2 sm:grid-cols-5">
        <Field label="模型名">
          <Input
            value={form.model}
            onChange={(e) => setForm({ ...form, model: e.target.value })}
            placeholder="claude-sonnet-4"
          />
        </Field>
        <Field label="输入价 / 百万">
          <Input
            value={form.prompt}
            onChange={(e) => setForm({ ...form, prompt: e.target.value })}
            placeholder="3"
          />
        </Field>
        <Field label="输出价 / 百万">
          <Input
            value={form.completion}
            onChange={(e) => setForm({ ...form, completion: e.target.value })}
            placeholder="15"
          />
        </Field>
        <Field label="缓存价 / 百万">
          <Input
            value={form.cache}
            onChange={(e) => setForm({ ...form, cache: e.target.value })}
            placeholder="0.3"
          />
        </Field>
        <div className="flex items-end">
          <Button variant="primary" className="w-full" onClick={submit}>
            添加 / 覆盖
          </Button>
        </div>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Input
          className="w-56 py-1 text-xs"
          placeholder="搜索模型名"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <span className="text-xs text-zinc-500 dark:text-zinc-400">
          共 {sorted.length} 条{q ? ` · 匹配 ${shown.length} 条` : ''}
        </span>
      </div>

      {loading ? (
        <Loading text="读取价格表…" />
      ) : sorted.length === 0 ? (
        <Empty text="价格表为空：点「刷新价格」从 OpenRouter 拉取，或在上方手工添加" />
      ) : shown.length === 0 ? (
        <Empty text={`没有匹配「${query.trim()}」的模型`} />
      ) : (
        <div className="max-h-72 overflow-y-auto">
          <table className="w-full table-fixed text-sm">
            <colgroup>
              <col className="w-[32%]" />
              <col className="w-[14%]" />
              <col className="w-[14%]" />
              <col className="w-[14%]" />
              <col className="w-[14%]" />
              <col className="w-[12%]" />
            </colgroup>
            <thead>
              <tr className="border-b border-zinc-200 text-left text-xs text-zinc-500 dark:border-zinc-800">
                <th className="py-2 pr-3 font-medium">模型</th>
                <th className="py-2 pr-3 font-medium">来源</th>
                <th className="py-2 pr-3 text-right font-medium">输入</th>
                <th className="py-2 pr-3 text-right font-medium">输出</th>
                <th className="py-2 pr-3 text-right font-medium">缓存</th>
                <th className="py-2 font-medium" />
              </tr>
            </thead>
            <tbody>
              {shown.map((p) => (
                <tr
                  key={p.model}
                  className="border-b border-zinc-100 last:border-0 dark:border-zinc-800/60"
                >
                  <td className="py-1.5 pr-3 font-mono text-xs text-zinc-700 dark:text-zinc-300">
                    <span className="block truncate">{p.model}</span>
                  </td>
                  <td className="py-1.5 pr-3">
                    <Badge tone={p.source === 'manual' ? 'info' : 'off'}>{p.source}</Badge>
                  </td>
                  <td className="py-1.5 pr-3 text-right font-mono text-xs">{fmtPrice(p.prompt_price)}</td>
                  <td className="py-1.5 pr-3 text-right font-mono text-xs">{fmtPrice(p.completion_price)}</td>
                  <td className="py-1.5 pr-3 text-right font-mono text-xs">{fmtPrice(p.cache_read_price)}</td>
                  <td className="py-1.5 text-right">
                    <button
                      className="text-xs text-red-600 hover:underline dark:text-red-400"
                      onClick={() => onDelete(p.model)}
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

// fmtPrice 规整价格数值：去掉浮点误差尾巴与多余的 0（如 0.7999999999999999 → 0.8）。
function fmtPrice(n: number): string {
  if (!Number.isFinite(n)) return '—'
  return String(Number(n.toFixed(6)))
}