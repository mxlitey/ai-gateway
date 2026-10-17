import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type PythonSettings, type RuntimeSettings } from '../lib/api'
import { toast } from '../lib/toast'
import { Button, Card, ErrorText, Field, Input, Segmented } from '../components/ui'

export default function Settings() {
  const queryClient = useQueryClient()
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  // 清理旧记录：保留最近多少天（0 = 连当天一起清理）
  const [cleanupDays, setCleanupDays] = useState('90')
  // 外部访问地址（反代 / 隧道场景下插件登录回调使用的基址）
  const [externalBase, setExternalBase] = useState('')

  const runtime = useQuery({ queryKey: ['runtime'], queryFn: api.runtimeInfo })
  const info = runtime.data
  // 首次拉到配置后回填输入框；之后用户编辑不受影响
  const [externalBaseLoaded, setExternalBaseLoaded] = useState(false)
  if (info && !externalBaseLoaded) {
    setExternalBase(info.external_base ?? '')
    setExternalBaseLoaded(true)
  }

  // 运行期设置（日志级别 / 价格源 / 插件监管策略）
  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const [draft, setDraft] = useState<RuntimeSettings | null>(null)
  if (settingsQuery.data && draft === null) {
    setDraft(settingsQuery.data.settings)
  }
  const defaults = settingsQuery.data?.defaults

  // Python 插件运行时：探测结果 + 可选覆盖项（便携目录 / 下载镜像 / pip 源）
  const python = useQuery({ queryKey: ['python-status'], queryFn: api.pythonStatus })
  const pythonSettingsQuery = useQuery({ queryKey: ['python-settings'], queryFn: api.pythonSettings })
  const [pyDraft, setPyDraft] = useState<PythonSettings | null>(null)
  if (pythonSettingsQuery.data && pyDraft === null) {
    setPyDraft(pythonSettingsQuery.data.settings)
  }

  const savePythonSettings = useMutation({
    mutationFn: (body: PythonSettings) => api.updatePythonSettings(body),
    onSuccess: () => {
      toast('ok', 'Python 运行时设置已保存')
      void queryClient.invalidateQueries({ queryKey: ['python-status'] })
      void queryClient.invalidateQueries({ queryKey: ['python-settings'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const downloadPython = useMutation({
    mutationFn: () => api.downloadPython(''),
    onSuccess: (res) => {
      toast('ok', `便携 Python 已就绪：${res.path}`)
      void queryClient.invalidateQueries({ queryKey: ['python-status'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const saveSettings = useMutation({
    mutationFn: (body: RuntimeSettings) => api.updateSettings(body),
    onSuccess: () => {
      toast('ok', '运行期设置已保存并生效')
      void queryClient.invalidateQueries({ queryKey: ['settings'] })
      void queryClient.invalidateQueries({ queryKey: ['prices'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  // 数值输入：空串按 0 处理，由后端校验区间
  function setNum(key: keyof RuntimeSettings, raw: string) {
    if (!draft) return
    setDraft({ ...draft, [key]: raw === '' ? 0 : Number(raw) })
  }

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

  const saveExternalBase = useMutation({
    mutationFn: () => api.setExternalBase(externalBase.trim()),
    onSuccess: (res) => {
      toast('ok', res.external_base ? '外部访问地址已保存' : '已清除外部访问地址')
      void queryClient.invalidateQueries({ queryKey: ['runtime'] })
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

      <Card title="外部访问地址">
        <p className="mb-3 text-xs text-zinc-500 dark:text-zinc-400">
          用反向代理 / 隧道（nginx、frp、Cloudflare Tunnel 等）把网关暴露到公网时，填这里的外部域名。
          插件登录授权完成后，浏览器会跳回这个地址下的统一回调入口；留空则按监听地址推导（仅本机 / 局域网可用）。
        </p>
        <div className="max-w-md space-y-4">
          <Field
            label="外部访问地址"
            hint="只填到域名（可带端口），如 https://gw.example.com；不要带路径。反代需把路径原样透传到网关根路径。"
          >
            <Input
              value={externalBase}
              onChange={(e) => setExternalBase(e.target.value)}
              placeholder="https://gw.example.com"
            />
          </Field>
          <Button
            variant="primary"
            disabled={saveExternalBase.isPending || externalBase.trim() === (info?.external_base ?? '')}
            onClick={() => saveExternalBase.mutate()}
          >
            {saveExternalBase.isPending ? '保存中…' : '保存'}
          </Button>
        </div>
      </Card>

      <Card title="运行期设置">
        {settingsQuery.isError ? (
          <div className="mb-3">
            <ErrorText text={(settingsQuery.error as Error).message} />
          </div>
        ) : null}
        <p className="mb-3 text-xs text-zinc-500 dark:text-zinc-400">
          这些设置保存在数据库里，保存后立即生效，无需重启。留空的文本项表示沿用启动参数 / 环境变量。
        </p>
        {draft ? (
          <div className="max-w-2xl space-y-4">
            <Field label="日志级别" hint={`留空沿用启动参数（当前 ${settingsQuery.data?.env_log_level || 'info'}）`}>
              <Segmented
                value={draft.log_level}
                onChange={(v) => setDraft({ ...draft, log_level: v })}
                options={[
                  { value: '', label: '跟随启动参数' },
                  { value: 'debug', label: 'debug' },
                  { value: 'info', label: 'info' },
                  { value: 'warn', label: 'warn' },
                  { value: 'error', label: 'error' },
                ]}
              />
            </Field>

            <Field
              label="价格源地址"
              hint={`留空沿用环境变量 / 内置默认（当前生效：${settingsQuery.data?.effective_price_source ?? '—'}）`}
            >
              <Input
                value={draft.price_endpoint}
                onChange={(e) => setDraft({ ...draft, price_endpoint: e.target.value })}
                placeholder="https://openrouter.ai/api/v1/models"
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="价格刷新间隔（分钟）" hint={`默认 ${defaults?.price_refresh_minutes ?? 60}`}>
                <Input
                  type="number"
                  min={1}
                  max={1440}
                  value={draft.price_refresh_minutes}
                  onChange={(e) => setNum('price_refresh_minutes', e.target.value)}
                />
              </Field>
              <Field label="自动签到间隔（分钟）" hint={`默认 ${defaults?.plugin_auto_checkin_minutes ?? 60}`}>
                <Input
                  type="number"
                  min={1}
                  max={1440}
                  value={draft.plugin_auto_checkin_minutes}
                  onChange={(e) => setNum('plugin_auto_checkin_minutes', e.target.value)}
                />
              </Field>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="插件探活间隔（秒）" hint={`默认 ${defaults?.plugin_probe_interval_seconds ?? 2}`}>
                <Input
                  type="number"
                  min={1}
                  max={3600}
                  value={draft.plugin_probe_interval_seconds}
                  onChange={(e) => setNum('plugin_probe_interval_seconds', e.target.value)}
                />
              </Field>
              <Field label="插件探活超时（秒）" hint={`默认 ${defaults?.plugin_probe_timeout_seconds ?? 2}`}>
                <Input
                  type="number"
                  min={1}
                  max={3600}
                  value={draft.plugin_probe_timeout_seconds}
                  onChange={(e) => setNum('plugin_probe_timeout_seconds', e.target.value)}
                />
              </Field>
            </div>

            <Field
              label="上游密钥失败冷却"
              hint="开启后，某密钥请求上游失败即冷却该密钥，冷却期内不再使用；连续失败按倍数递增冷却时长，任意一次成功即重置。"
            >
              <Segmented
                value={draft.key_cooldown_enabled ? 'on' : 'off'}
                onChange={(v) => setDraft({ ...draft, key_cooldown_enabled: v === 'on' })}
                options={[
                  { value: 'on', label: '开启' },
                  { value: 'off', label: '关闭' },
                ]}
              />
            </Field>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="初始冷却（秒）"
                hint={`首次失败的冷却时长，默认 ${defaults?.key_cooldown_base_seconds ?? 60}`}
              >
                <Input
                  type="number"
                  min={1}
                  max={86400}
                  value={draft.key_cooldown_base_seconds}
                  onChange={(e) => setNum('key_cooldown_base_seconds', e.target.value)}
                />
              </Field>
              <Field
                label="冷却上限（秒）"
                hint={`单次冷却最长时间，默认 ${defaults?.key_cooldown_max_seconds ?? 1800}`}
              >
                <Input
                  type="number"
                  min={1}
                  max={604800}
                  value={draft.key_cooldown_max_seconds}
                  onChange={(e) => setNum('key_cooldown_max_seconds', e.target.value)}
                />
              </Field>
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="不健康阈值（次）"
                hint={`连续探活失败达到该次数即重启，默认 ${defaults?.plugin_unhealthy_threshold ?? 3}`}
              >
                <Input
                  type="number"
                  min={1}
                  max={1000}
                  value={draft.plugin_unhealthy_threshold}
                  onChange={(e) => setNum('plugin_unhealthy_threshold', e.target.value)}
                />
              </Field>
              <Field
                label="最大失败次数（次）"
                hint={`连续失败达到该次数后放弃自动重启，默认 ${defaults?.plugin_max_failures ?? 5}`}
              >
                <Input
                  type="number"
                  min={1}
                  max={1000}
                  value={draft.plugin_max_failures}
                  onChange={(e) => setNum('plugin_max_failures', e.target.value)}
                />
              </Field>
            </div>

            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                disabled={saveSettings.isPending}
                onClick={() => saveSettings.mutate(draft)}
              >
                {saveSettings.isPending ? '保存中…' : '保存'}
              </Button>
              <Button
                variant="ghost"
                disabled={saveSettings.isPending || !settingsQuery.data}
                onClick={() => {
                  if (settingsQuery.data) setDraft(settingsQuery.data.settings)
                }}
              >
                重置
              </Button>
            </div>
          </div>
        ) : (
          <p className="text-sm text-zinc-500 dark:text-zinc-400">读取中…</p>
        )}
      </Card>

      <Card title="Python 插件运行时">
        <p className="mb-3 text-xs text-zinc-500 dark:text-zinc-400">
          插件一律是 Python 源码（入口为 <code className="text-xs">.py</code> 脚本），
          同一份源码可在
          Windows / macOS / Linux 通用。宿主优先使用系统 Python；没有时可用这里的便携 Python（下载到数据目录，不随安装包分发）。
        </p>
        {python.isError ? (
          <div className="mb-3">
            <ErrorText text={(python.error as Error).message} />
          </div>
        ) : null}
        <dl className="grid max-w-2xl gap-x-6 gap-y-3 text-sm sm:grid-cols-[8rem_1fr]">
          <dt className="text-zinc-500 dark:text-zinc-400">当前解释器</dt>
          <dd className="text-zinc-700 dark:text-zinc-300">
            {python.data ? (
              python.data.found ? (
                <span>
                  {python.data.version}
                  <span className="ml-2 text-xs text-zinc-500 dark:text-zinc-400">
                    {python.data.portable ? '便携' : '系统'}
                  </span>
                  <div className="mt-1 font-mono text-xs break-all text-zinc-500">{python.data.path}</div>
                </span>
              ) : (
                <span className="text-amber-600 dark:text-amber-400">未找到 Python 3</span>
              )
            ) : (
              '探测中…'
            )}
          </dd>
          <dt className="text-zinc-500 dark:text-zinc-400">便携目录</dt>
          <dd className="font-mono text-xs break-all text-zinc-700 dark:text-zinc-300">
            {python.data?.runtime_dir ?? '—'}
          </dd>
          <dt className="text-zinc-500 dark:text-zinc-400">pip 源顺序</dt>
          <dd className="text-xs break-all text-zinc-600 dark:text-zinc-300">
            {(python.data?.index_urls ?? []).join(' → ') || '—'}
          </dd>
        </dl>
        {python.data && !python.data.found ? (
          <p className="mt-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-300">
            {python.data.error}
          </p>
        ) : null}
        <div className="mt-4 flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            disabled={downloadPython.isPending}
            onClick={() => downloadPython.mutate()}
          >
            {downloadPython.isPending ? '下载中…（可能需要数分钟）' : '下载便携 Python'}
          </Button>
          <span className="text-xs text-zinc-500 dark:text-zinc-400">
            下载后会自动解压到便携目录；重复点击不会重复下载已就绪的版本。
          </span>
        </div>

        {pyDraft ? (
          <div className="mt-5 max-w-2xl space-y-4">
            <Field
              label="便携 Python 下载地址（镜像前缀）"
              hint="留空使用官方发布地址；内网/国内加速时可填自建镜像前缀，文件名保持与官方一致。"
            >
              <Input
                value={pyDraft.python_mirror}
                onChange={(e) => setPyDraft({ ...pyDraft, python_mirror: e.target.value })}
                placeholder="https://your-mirror.example.com/python-build-standalone"
              />
            </Field>
            <Field
              label="便携 Python 目录"
              hint="留空使用数据目录下的 runtime/python；也可以把已解压的 Python 目录手动放到这里。"
            >
              <Input
                value={pyDraft.python_runtime_dir}
                onChange={(e) => setPyDraft({ ...pyDraft, python_runtime_dir: e.target.value })}
                placeholder={python.data?.runtime_dir ?? ''}
              />
            </Field>
            <Field
              label="pip 源（按顺序退避）"
              hint="留空使用内置顺序：官方源优先，失败后依次尝试清华、阿里云、腾讯云、中科大镜像。多条用逗号分隔。"
            >
              <Input
                value={pyDraft.pip_index_urls}
                onChange={(e) => setPyDraft({ ...pyDraft, pip_index_urls: e.target.value })}
                placeholder="https://pypi.org/simple, https://pypi.tuna.tsinghua.edu.cn/simple"
              />
            </Field>
            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                disabled={savePythonSettings.isPending}
                onClick={() => savePythonSettings.mutate(pyDraft)}
              >
                {savePythonSettings.isPending ? '保存中…' : '保存'}
              </Button>
              <Button
                variant="ghost"
                disabled={savePythonSettings.isPending || !pythonSettingsQuery.data}
                onClick={() => {
                  if (pythonSettingsQuery.data) setPyDraft(pythonSettingsQuery.data.settings)
                }}
              >
                重置
              </Button>
            </div>
          </div>
        ) : null}
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