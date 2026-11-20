import { useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type PythonSettings, type RuntimeSettings, type StorageSettings } from '../lib/api'
import { toast } from '../lib/toast'
import {
  Badge,
  Button,
  ErrorText,
  Field,
  Input,
  PageHead,
  Panel,
  Segmented,
} from './ui'

export default function V2Settings() {
  const queryClient = useQueryClient()
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [cleanupDays, setCleanupDays] = useState('90')

  const runtime = useQuery({ queryKey: ['runtime'], queryFn: api.runtimeInfo })
  const info = runtime.data

  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const [draft, setDraft] = useState<RuntimeSettings | null>(null)
  if (settingsQuery.data && draft === null) {
    setDraft(settingsQuery.data.settings)
  }
  const defaults = settingsQuery.data?.defaults

  const python = useQuery({ queryKey: ['python-status'], queryFn: api.pythonStatus })
  const pythonSettingsQuery = useQuery({ queryKey: ['python-settings'], queryFn: api.pythonSettings })
  const [pyDraft, setPyDraft] = useState<PythonSettings | null>(null)
  if (pythonSettingsQuery.data && pyDraft === null) {
    setPyDraft(pythonSettingsQuery.data.settings)
  }

  const storageSettingsQuery = useQuery({ queryKey: ['storage-settings'], queryFn: api.storageSettings })
  const [stDraft, setStDraft] = useState<StorageSettings | null>(null)
  if (storageSettingsQuery.data && stDraft === null) {
    setStDraft(storageSettingsQuery.data.settings)
  }
  // 上次保存时后端报告的启用状态；未保存过则用查询结果
  const [storageEnabled, setStorageEnabled] = useState<boolean | null>(null)
  const storageActive = storageEnabled ?? storageSettingsQuery.data?.enabled ?? false

  const adminMCP = useQuery({ queryKey: ['admin-mcp'], queryFn: api.adminMCPState })
  const [mcpTokens, setMcpTokens] = useState<{ readonly: string; writable: string }>({
    readonly: '',
    writable: '',
  })

  const saveAdminMCP = useMutation({
    mutationFn: (patch: { enabled?: boolean }) => {
      const cur = adminMCP.data
      return api.saveAdminMCP(patch.enabled ?? cur?.enabled ?? false)
    },
    onSuccess: () => {
      toast('ok', '管理 MCP 设置已保存')
      void queryClient.invalidateQueries({ queryKey: ['admin-mcp'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  const issueMCPToken = useMutation({
    mutationFn: (kind: 'readonly' | 'writable') => api.issueAdminMCPToken(kind),
    onSuccess: (res) => {
      setMcpTokens((prev) => ({ ...prev, [res.kind]: res.token }))
      toast('ok', res.notice || '令牌已生成')
      void queryClient.invalidateQueries({ queryKey: ['admin-mcp'] })
    },
    onError: (err: Error) => toast('err', err.message),
  })

  async function copyText(text: string, label: string) {
    try {
      await navigator.clipboard.writeText(text)
      toast('ok', `${label}已复制到剪贴板`)
    } catch {
      toast('err', '复制失败，请手动选中复制')
    }
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

  const saveStorageSettings = useMutation({
    mutationFn: (body: StorageSettings) => api.updateStorageSettings(body),
    onSuccess: (res) => {
      setStorageEnabled(res.enabled)
      setStDraft(res.settings)
      toast('ok', res.enabled ? '对象存储设置已保存并生效' : '对象存储设置已保存，但尚未可用')
      void queryClient.invalidateQueries({ queryKey: ['storage-settings'] })
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

  const cleanup = useMutation({
    mutationFn: (keepDays: number) => api.cleanupUsage(keepDays),
    onSuccess: (res) => {
      const scope = res.days === 0 ? '全部记录（含当天）' : `${res.days} 天前的记录`
      toast('ok', `已清理${scope}：用量明细 ${res.removed} 条、错误日志 ${res.removed_errors} 条`)
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
    <div className="stack">
      <PageHead title="设置" />

      <Panel title="修改管理员密码">
        <form onSubmit={submit} style={{ maxWidth: 440, display: 'flex', flexDirection: 'column', gap: 12 }}>
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
          <div>
            <Button type="submit" variant="primary" disabled={change.isPending || !oldPassword || !newPassword}>
              {change.isPending ? '提交中…' : '更新密码'}
            </Button>
          </div>
        </form>
      </Panel>

      <Panel title="运行信息">
        {runtime.isError ? <ErrorText text={(runtime.error as Error).message} /> : null}
        <dl className="kvgrid" style={{ maxWidth: 720, gap: '9px 20px', fontSize: 11.5 }}>
          <dt>访问地址</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>
            {info?.access_url ?? '读取中…'}
          </dd>
          <dt>API 地址</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>
            {info?.api_url ?? '读取中…'}
          </dd>
          <dt>监听地址 / 端口</dt>
          <dd>
            {info ? `${info.listen_host}:${info.port}` : '读取中…'}
            {info ? (
              <span className="tiny faint" style={{ marginLeft: 8 }}>
                {info.lan ? '局域网可访问' : '仅本机可访问'}
              </span>
            ) : null}
          </dd>
          <dt>数据目录</dt>
          <dd className="mono" style={{ wordBreak: 'break-all' }}>
            {info?.data_dir ?? '读取中…'}
          </dd>
        </dl>
        <p
          className="tiny"
          style={{
            margin: '12px 0 0',
            display: 'inline-block',
            border: '1px solid var(--warn)',
            background: 'var(--warn-weak)',
            color: 'var(--warn)',
            borderRadius: 8,
            padding: '5px 9px',
          }}
          title="对外监听前请确认访问控制"
        >
          仅本机监听
        </p>
      </Panel>

      <Panel title="管理 MCP">
        {adminMCP.isError ? <ErrorText text={(adminMCP.error as Error).message} /> : null}
        <div style={{ maxWidth: 720, display: 'flex', flexDirection: 'column', gap: 12 }}>
          <Field label="启用管理 MCP" hint={`端点为 ${adminMCP.data?.access_url ?? '—'}`}>
            <Segmented
              value={adminMCP.data?.enabled ? 'on' : 'off'}
              onChange={(v) => saveAdminMCP.mutate({ enabled: v === 'on' })}
              options={[
                { value: 'on', label: '开启' },
                { value: 'off', label: '关闭' },
              ]}
            />
          </Field>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <span style={{ fontSize: 11.5, fontWeight: 500 }}>令牌</span>
            {(
              [
                ['readonly', '只读令牌'],
                ['writable', '读写令牌'],
              ] as const
            ).map(([kind, label]) => {
              const has =
                kind === 'readonly' ? adminMCP.data?.has_readonly_token : adminMCP.data?.has_writable_token
              const plain = mcpTokens[kind]
              return (
                <div
                  key={kind}
                  style={{
                    border: '1px solid var(--border)',
                    borderRadius: 8,
                    padding: '7px 10px',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: 8,
                  }}
                >
                  <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
                    <span style={{ fontSize: 11.5 }}>{label}</span>
                    <Badge tone={has ? 'ok' : 'off'}>{has ? '已生成' : '未生成'}</Badge>
                    <span style={{ flex: 1 }} />
                    <Button
                      variant={has ? 'ghost' : 'primary'}
                      size="sm"
                      disabled={!adminMCP.data?.enabled || issueMCPToken.isPending}
                      onClick={() => {
                        if (
                          has &&
                          !window.confirm(`重新生成${label}？旧令牌会立即失效，使用它的客户端需要更新。`)
                        ) {
                          return
                        }
                        issueMCPToken.mutate(kind)
                      }}
                    >
                      {has ? '重新生成' : '生成'}
                    </Button>
                  </div>
                  {plain ? (
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <code
                        className="mono truncate"
                        style={{
                          flex: 1,
                          minWidth: 0,
                          background: 'var(--warn-weak)',
                          color: 'var(--warn)',
                          borderRadius: 4,
                          padding: '3px 7px',
                          fontSize: 10.5,
                        }}
                      >
                        {plain}
                      </code>
                      <Button variant="ghost" size="sm" onClick={() => void copyText(plain, label)}>
                        复制
                      </Button>
                    </div>
                  ) : null}
                  {!plain && has ? (
                    <p className="tiny faint" style={{ margin: 0 }} title="明文只在生成时显示一次">
                      已生成
                    </p>
                  ) : null}
                </div>
              )
            })}
          </div>

          <details style={{ border: '1px solid var(--border)', borderRadius: 8, padding: '7px 10px' }}>
            <summary className="small" style={{ cursor: 'pointer' }}>
              客户端示例
            </summary>
            <div style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <pre
                className="mono"
                style={{
                  margin: 0,
                  maxHeight: 256,
                  overflow: 'auto',
                  background: 'var(--surface-2)',
                  borderRadius: 8,
                  padding: 10,
                  fontSize: 11,
                  lineHeight: 1.7,
                }}
              >
                {adminMCPExample(
                  adminMCP.data?.access_url ?? 'http://127.0.0.1:8787/admin/mcp',
                  'amcp_你的令牌',
                )}
              </pre>
              <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() =>
                    void copyText(
                      adminMCPExample(
                        adminMCP.data?.access_url ?? 'http://127.0.0.1:8787/admin/mcp',
                        'amcp_你的令牌',
                      ),
                      '客户端示例',
                    )
                  }
                >
                  复制示例
                </Button>
              </div>
            </div>
          </details>
        </div>
      </Panel>

      <Panel title="运行期设置">
        {settingsQuery.isError ? <ErrorText text={(settingsQuery.error as Error).message} /> : null}
        {draft ? (
          <div style={{ maxWidth: 720, display: 'flex', flexDirection: 'column', gap: 12 }}>
            <Field
              label="日志级别"
              hint={`留空沿用启动参数（当前 ${settingsQuery.data?.env_log_level || 'info'}）`}
            >
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
              label="货币符号"
              hint="价格与费用只显示数字，符号按这里填写的显示（如 ¥ / $ / €）；留空则不显示符号"
            >
              <Input
                value={draft.currency_symbol}
                onChange={(e) => setDraft({ ...draft, currency_symbol: e.target.value })}
                placeholder="¥"
                maxLength={4}
                style={{ maxWidth: 128 }}
              />
            </Field>

            <Field
              label="自动签到间隔（分钟）"
              hint={`默认 ${defaults?.plugin_auto_checkin_minutes ?? 60}`}
            >
              <Input
                type="number"
                min={1}
                max={1440}
                value={draft.plugin_auto_checkin_minutes}
                onChange={(e) => setNum('plugin_auto_checkin_minutes', e.target.value)}
              />
            </Field>

            <div className="v2grid g2">
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

            <Field label="上游密钥失败熔断" hint="失败后暂停使用该密钥，连续失败逐次延长，成功即解除">
              <Segmented
                value={draft.key_cooldown_enabled ? 'on' : 'off'}
                onChange={(v) => setDraft({ ...draft, key_cooldown_enabled: v === 'on' })}
                options={[
                  { value: 'on', label: '开启' },
                  { value: 'off', label: '关闭' },
                ]}
              />
            </Field>

            <div className="v2grid g2">
              <Field
                label="初始熔断时长（秒）"
                hint={`首次失败的熔断时长，默认 ${defaults?.key_cooldown_base_seconds ?? 60}`}
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
                label="熔断时长上限（秒）"
                hint={`单次熔断最长时间，默认 ${defaults?.key_cooldown_max_seconds ?? 1800}`}
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

            <div className="v2grid g2">
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

            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <Button variant="primary" disabled={saveSettings.isPending} onClick={() => saveSettings.mutate(draft)}>
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
          <div className="faint tiny">读取中…</div>
        )}
      </Panel>

      <Panel title="对象存储">
        <div style={{ maxWidth: 720, display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
            <span className="tiny muted">
              网关只签发预签名 URL，文件字节由客户端直传对象存储，不经过网关中转。
            </span>
            <span style={{ flex: 1 }} />
            <Badge tone={storageActive ? 'ok' : 'off'}>{storageActive ? '已启用' : '未启用'}</Badge>
          </div>
          {storageSettingsQuery.isError ? (
            <ErrorText text={(storageSettingsQuery.error as Error).message} />
          ) : null}

          {stDraft ? (
            <>
              <Field label="驱动" hint="仅在面板配置，不支持启动参数；选「S3 兼容」并填齐下面四项必填即可启用">
                <Segmented
                  value={stDraft.driver === 's3' ? 's3' : 'none'}
                  onChange={(v) => setStDraft({ ...stDraft, driver: v })}
                  options={[
                    { value: 'none', label: '不启用' },
                    { value: 's3', label: 'S3 兼容' },
                  ]}
                />
              </Field>

              <Field label="Endpoint" hint="如 https://s3.us-east-1.amazonaws.com 或自建 MinIO 地址（必填）">
                <Input
                  value={stDraft.endpoint}
                  onChange={(e) => setStDraft({ ...stDraft, endpoint: e.target.value })}
                  placeholder="https://s3.example.com"
                />
              </Field>

              <div className="v2grid g2">
                <Field label="Region" hint="签名区域，留空按 us-east-1；R2 填 auto">
                  <Input
                    value={stDraft.region}
                    onChange={(e) => setStDraft({ ...stDraft, region: e.target.value })}
                    placeholder="us-east-1"
                  />
                </Field>
                <Field label="Bucket" hint="目标桶名（必填）">
                  <Input
                    value={stDraft.bucket}
                    onChange={(e) => setStDraft({ ...stDraft, bucket: e.target.value })}
                    placeholder="ai-gateway"
                  />
                </Field>
              </div>

              <div className="v2grid g2">
                <Field label="Access Key" hint="预签名凭证（必填）">
                  <Input
                    value={stDraft.access_key}
                    onChange={(e) => setStDraft({ ...stDraft, access_key: e.target.value })}
                    placeholder="AKIA..."
                  />
                </Field>
                <Field label="Secret Key" hint="读取时脱敏；留空或保留脱敏串表示不修改">
                  <Input
                    type="password"
                    value={stDraft.secret_key}
                    onChange={(e) => setStDraft({ ...stDraft, secret_key: e.target.value })}
                    placeholder="留空表示不修改"
                  />
                </Field>
              </div>

              <div className="v2grid g2">
                <Field label="对象键前缀" hint="留空则直接写在桶根目录下">
                  <Input
                    value={stDraft.prefix}
                    onChange={(e) => setStDraft({ ...stDraft, prefix: e.target.value })}
                    placeholder="aigw"
                  />
                </Field>
                <Field label="预签名有效期（秒）" hint="默认 1800，最大 604800（7 天）；0 表示用默认值">
                  <Input
                    type="number"
                    min={0}
                    max={604800}
                    value={stDraft.presign_ttl_seconds}
                    onChange={(e) =>
                      setStDraft({
                        ...stDraft,
                        presign_ttl_seconds: e.target.value === '' ? 0 : Number(e.target.value),
                      })
                    }
                  />
                </Field>
              </div>

              <Field
                label="公开访问前缀"
                hint="填了则返回公开链接（CDN 或公开桶），留空只用预签名地址"
              >
                <Input
                  value={stDraft.public_base}
                  onChange={(e) => setStDraft({ ...stDraft, public_base: e.target.value })}
                  placeholder="https://cdn.example.com"
                />
              </Field>

              <Field label="Path-style 访问" hint="MinIO、Cloudflare R2 等通常需要开启">
                <Segmented
                  value={stDraft.force_path_style === 'on' ? 'on' : 'off'}
                  onChange={(v) => setStDraft({ ...stDraft, force_path_style: v })}
                  options={[
                    { value: 'off', label: '关闭' },
                    { value: 'on', label: '开启' },
                  ]}
                />
              </Field>

              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <Button
                  variant="primary"
                  disabled={saveStorageSettings.isPending}
                  onClick={() => saveStorageSettings.mutate(stDraft)}
                >
                  {saveStorageSettings.isPending ? '保存中…' : '保存'}
                </Button>
                <Button
                  variant="ghost"
                  disabled={saveStorageSettings.isPending || !storageSettingsQuery.data}
                  onClick={() => {
                    if (storageSettingsQuery.data) {
                      setStDraft(storageSettingsQuery.data.settings)
                      setStorageEnabled(null)
                    }
                  }}
                >
                  重置
                </Button>
              </div>
            </>
          ) : (
            <div className="faint tiny">读取中…</div>
          )}
        </div>
      </Panel>

      <Panel title="Python 插件运行时">
        {python.isError ? <ErrorText text={(python.error as Error).message} /> : null}
        <dl className="kvgrid" style={{ maxWidth: 720, gap: '9px 20px', fontSize: 11.5 }}>
          <dt>当前解释器</dt>
          <dd>
            {python.data ? (
              python.data.found ? (
                <span>
                  {python.data.version}
                  <div className="mono faint" style={{ marginTop: 3, fontSize: 10.5, wordBreak: 'break-all' }}>
                    {python.data.path}
                  </div>
                </span>
              ) : (
                <span style={{ color: 'var(--warn)' }}>未找到 Python 3</span>
              )
            ) : (
              '探测中…'
            )}
          </dd>
          <dt>pip 源顺序</dt>
          <dd className="tiny muted" style={{ wordBreak: 'break-all' }}>
            {(python.data?.index_urls ?? []).join(' → ') || '—'}
          </dd>
        </dl>
        {python.data && !python.data.found ? (
          <p
            className="tiny"
            style={{
              margin: '10px 0 0',
              border: '1px solid var(--warn)',
              background: 'var(--warn-weak)',
              color: 'var(--warn)',
              borderRadius: 8,
              padding: '6px 9px',
            }}
          >
            {python.data.error}
          </p>
        ) : null}

        {pyDraft ? (
          <div style={{ marginTop: 14, maxWidth: 720, display: 'flex', flexDirection: 'column', gap: 11 }}>
            <Field label="pip 源（按顺序退避）" hint="留空使用内置镜像顺序，多条用逗号分隔">
              <Input
                value={pyDraft.pip_index_urls}
                onChange={(e) => setPyDraft({ ...pyDraft, pip_index_urls: e.target.value })}
                placeholder="https://pypi.org/simple, https://pypi.tuna.tsinghua.edu.cn/simple"
              />
            </Field>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
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
      </Panel>

      <Panel title="清理旧记录">
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
          <span className="small muted">保留最近</span>
          <input
            type="number"
            min={0}
            max={3650}
            value={cleanupDays}
            onChange={(e) => setCleanupDays(e.target.value)}
            className="input"
            style={{ width: 80 }}
          />
          <span className="small muted">天</span>
          <Button
            variant="ghost"
            style={{ color: 'var(--danger)' }}
            disabled={cleanup.isPending}
            onClick={() => {
              const n = Number(cleanupDays)
              if (!Number.isInteger(n) || n < 0 || n > 3650) {
                toast('err', '天数需为 0-3650 之间的整数')
                return
              }
              const scope = n === 0 ? '全部用量明细与错误日志（含当天）' : `${n} 天前的用量明细与错误日志`
              if (window.confirm(`确认清理${scope}？此操作不可撤销。`)) cleanup.mutate(n)
            }}
          >
            {cleanup.isPending ? '清理中…' : '清理旧记录'}
          </Button>
        </div>
      </Panel>
    </div>
  )
}

function adminMCPExample(url: string, tokenPlaceholder: string) {
  return JSON.stringify(
    {
      mcpServers: {
        'ai-gateway-admin': {
          type: 'http',
          url,
          headers: { Authorization: `Bearer ${tokenPlaceholder}` },
        },
      },
    },
    null,
    2,
  )
}
