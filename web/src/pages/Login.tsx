import { useState, type FormEvent } from 'react'
import { api } from '../lib/api'
import { useAuth } from '../lib/auth'
import { toast } from '../lib/toast'
import { Button, Card, Field, Input } from '../components/ui'

export default function Login() {
  const { signIn } = useAuth()
  const [credential, setCredential] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!credential.trim()) return
    setBusy(true)
    try {
      const { token } = await api.login(credential.trim())
      signIn(token)
      toast('ok', '登录成功')
    } catch (err) {
      toast('err', err instanceof Error ? err.message : '登录失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-full items-center justify-center bg-zinc-50 p-6 dark:bg-zinc-950">
      <div className="w-full max-w-sm">
        <div className="mb-6 text-center">
          <img src="/favicon.svg" alt="" className="mb-2 inline-block h-12 w-12" />
          <h1 className="text-lg font-semibold text-zinc-800 dark:text-zinc-100">AI Gateway</h1>
          <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">登录管理面板</p>
        </div>
        <Card>
          <form onSubmit={submit} className="space-y-4">
            <Field label="管理员密码">
              <Input
                type="password"
                value={credential}
                onChange={(e) => setCredential(e.target.value)}
                placeholder="请输入密码"
                autoFocus
              />
            </Field>
            <Button type="submit" variant="primary" className="w-full" disabled={busy}>
              {busy ? '登录中…' : '登录'}
            </Button>
          </form>
        </Card>
      </div>
    </div>
  )
}