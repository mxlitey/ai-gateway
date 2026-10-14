import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../lib/api'
import { useAuth } from '../lib/auth'
import { toast } from '../lib/toast'
import { Button, Card, Field, Input } from '../components/ui'

export default function Setup() {
  const { signIn } = useAuth()
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (password.length < 8) {
      toast('err', '密码长度至少 8 位')
      return
    }
    if (password !== confirm) {
      toast('err', '两次输入的密码不一致')
      return
    }
    setBusy(true)
    try {
      const res = await api.setup(password)
      if (res.token) signIn(res.token)
      toast('ok', '初始化完成')
      await queryClient.invalidateQueries({ queryKey: ['setup'] })
    } catch (err) {
      toast('err', err instanceof Error ? err.message : '初始化失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-zinc-50 p-6 dark:bg-zinc-950">
      <div className="w-full max-w-sm">
        <div className="mb-6 text-center">
          <img src="/favicon.svg" alt="" className="mb-2 inline-block h-12 w-12" />
          <h1 className="text-lg font-semibold text-zinc-800 dark:text-zinc-100">首次运行设置</h1>
          <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">
            设置管理面板密码，之后用它登录
          </p>
        </div>
        <Card>
          <form onSubmit={submit} className="space-y-4">
            <Field label="管理员密码" hint="至少 8 位，请妥善保管">
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="设置密码"
                autoFocus
              />
            </Field>
            <Field label="确认密码">
              <Input
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                placeholder="再输入一次"
              />
            </Field>
            <Button type="submit" variant="primary" className="w-full" disabled={busy}>
              {busy ? '提交中…' : '完成初始化'}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  )
}