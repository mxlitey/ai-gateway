import type { ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { useAuth } from '../lib/auth'

const navItems = [
  { to: '/', label: '仪表盘', end: true },
  { to: '/channels', label: '渠道管理' },
  { to: '/routes', label: '模型路由' },
  { to: '/keys', label: 'API 密钥' },
  { to: '/plugins', label: '插件' },
  { to: '/usage', label: '用量' },
  { to: '/prices', label: '价格表' },
  { to: '/errors', label: '错误日志' },
  { to: '/settings', label: '设置' },
]

export default function Layout({ children }: { children: ReactNode }) {
  const { signOut } = useAuth()
  // 与「设置 → 运行信息」同一来源，保证左下角展示的 API 地址一致
  const runtime = useQuery({ queryKey: ['runtime'], queryFn: api.runtimeInfo })

  return (
    <div className="flex min-h-full bg-zinc-50 dark:bg-zinc-950">
      {/* 侧边栏贴合视口高度并吸顶，保证底部的「API 地址 / 退出登录」在长页面下也始终可见 */}
      <aside className="sticky top-0 flex h-screen w-56 shrink-0 flex-col border-r border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <div className="flex h-14 items-center gap-2 border-b border-zinc-200 px-4 dark:border-zinc-800">
          <img src="/favicon.svg" alt="" className="h-5 w-5" />
          <span className="text-sm font-semibold text-zinc-800 dark:text-zinc-100">AI Gateway</span>
        </div>
        <nav className="flex-1 space-y-0.5 overflow-y-auto p-2">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                `block rounded-lg px-3 py-2 text-sm transition-colors ${
                  isActive
                    ? 'bg-emerald-50 font-medium text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
                    : 'text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800'
                }`
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-zinc-200 p-3 dark:border-zinc-800">
          <p className="mb-2 text-xs leading-relaxed text-zinc-500 dark:text-zinc-500">
            API 地址
            <br />
            <code className="text-[11px] break-all text-zinc-600 dark:text-zinc-400">
              {runtime.data?.api_url ?? `${window.location.origin}/v1`}
            </code>
          </p>
          <button
            onClick={signOut}
            className="w-full rounded-lg border border-zinc-300 px-3 py-1.5 text-sm text-zinc-600 hover:bg-zinc-50 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800"
          >
            退出登录
          </button>
        </div>
      </aside>

      <main className="min-w-0 flex-1">
        <div className="mx-auto max-w-6xl p-6">{children}</div>
      </main>
    </div>
  )
}