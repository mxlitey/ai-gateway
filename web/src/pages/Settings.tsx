import { useState, type FormEvent } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { toast } from '../lib/toast'
import { Button, Card, ErrorText, Field, Input } from '../components/ui'

export default function Settings() {
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  // 清理旧记录：保留最近多少天（0 = 连当天一起清理）
  const [cleanupDays, setCleanupDays] = useState('90')

  const runtime = useQuery({ queryKey: ['runtime'], queryFn: api.runtimeInfo })
  const info = runtime.data

  const change = useMutation({
    mutationFn: () => api.changePassword(oldPassword, newPassword),
    onSuccess: () => {
      toast('ok', '密码已更新')
      setOldPassword('')
      setNewPassword('')
      setConfirm('')
    },
    onError: (err: Error) => toast('err', err.message),
  })

  // 按「保留最近 N 天」清理用量明细与错误日志，days 为 0 时连当天一起清理
  const cleanup = useMutation({
    mutationFn: (keepDays: number) => api.cleanupUsage(keepDays),
    onSuccess: (res) => {
      const scope = res.days === 0 ? '全部记录（含当天）' : `${res.days} 天前的记录`
      toast(
        'ok',
        `已清理${scope}：用量明细 ${res.removed} 条、错误日志 ${res.removed_errors} 条`,
      )
    },
    onError: (err: Error) => toast('err', err.message),
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    if (newPassword.length < 8) {
      toast('err', '新密码长度至少 8 位')
      return
    }
    if (newPassword !== confirm) {
      toast('err', '两次输入的新密码不一致')
      return
    }
    change.mutate()
  }

  return (
    <div className="space-y-4">
      <h1 className="text-base font-semibold text-zinc-800 dark:text-zinc-100">设置</h1>

      <Card title="修改管理员密码">
        <form onSubmit={submit} className="max-w-md space-y-4">
          <Field label="当前密码">
            <Input
              type="password"
              value={oldPassword}
              onChange={(e) => setOldPassword(e.target.value)}
              placeholder="当前密码"
            />
          </Field>
          <Field label="新密码" hint="至少 8 位">
            <Input
              type="password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              placeholder="新密码"
            />
          </Field>
          <Field label="确认新密码">
            <Input
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              placeholder="再输入一次"
            />
          </Field>
          <Button
            type="submit"
            variant="primary"
            disabled={change.isPending || !oldPassword || !newPassword}
          >
            {change.isPending ? '提交中…' : '更新密码'}
          </Button>
        </form>
      </Card>

      <Card title="运行信息">
        {runtime.isError ? (
          <div className="mb-3">
            <ErrorText text={(runtime.error as Error).message} />
          </div>
        ) : null}
        <dl className="grid max-w-2xl gap-x-6 gap-y-3 text-sm sm:grid-cols-[8rem_1fr]">
          <dt className="text-zinc-500 dark:text-zinc-400">访问地址</dt>
          <dd className="font-mono text-xs break-all text-zinc-700 dark:text-zinc-300">
            {info?.access_url ?? '读取中…'}
          </dd>
          <dt className="text-zinc-500 dark:text-zinc-400">API 地址</dt>
          <dd className="font-mono text-xs break-all text-zinc-700 dark:text-zinc-300">
            {info?.api_url ?? '读取中…'}
          </dd>
          <dt className="text-zinc-500 dark:text-zinc-400">监听地址 / 端口</dt>
          <dd className="text-zinc-700 dark:text-zinc-300">
            {info ? `${info.listen_host}:${info.port}` : '读取中…'}
            {info ? (
              <span className="ml-2 text-xs text-zinc-500 dark:text-zinc-400">
                {info.lan ? '局域网可访问' : '仅本机可访问'}
              </span>
            ) : null}
          </dd>
          <dt className="text-zinc-500 dark:text-zinc-400">数据目录</dt>
          <dd className="font-mono text-xs break-all text-zinc-700 dark:text-zinc-300">
            {info?.data_dir ?? '读取中…'}
          </dd>
        </dl>
        <p className="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300">
          安全提示：默认仅监听本机回环地址。若改为对外监听，请自行确认网络与访问控制。
        </p>
      </Card>

      <Card title="清理旧记录">
        <p className="mb-3 text-xs text-zinc-500 dark:text-zinc-400">
          按「保留最近 N 天」同时清理用量明细与错误日志；填 0 表示连当天一起清理。此操作不可撤销。
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm text-zinc-600 dark:text-zinc-300">保留最近</span>
          <input
            type="number"
            min={0}
            max={3650}
            value={cleanupDays}
            onChange={(e) => setCleanupDays(e.target.value)}
            className="w-20 rounded-lg border border-zinc-300 bg-white px-2 py-1.5 text-sm text-zinc-900 outline-none focus:border-emerald-500 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
          />
          <span className="text-sm text-zinc-600 dark:text-zinc-300">天</span>
          <Button
            variant="danger"
            disabled={cleanup.isPending}
            onClick={() => {
              const n = Number(cleanupDays)
              if (!Number.isInteger(n) || n < 0 || n > 3650) {
                toast('err', '天数需为 0-3650 之间的整数')
                return
              }
              const scope =
                n === 0 ? '全部用量明细与错误日志（含当天）' : `${n} 天前的用量明细与错误日志`
              if (window.confirm(`确认清理${scope}？此操作不可撤销。`)) cleanup.mutate(n)
            }}
          >
            {cleanup.isPending ? '清理中…' : '清理旧记录'}
          </Button>
        </div>
      </Card>
    </div>
  )
}