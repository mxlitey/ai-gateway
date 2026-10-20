import { useEffect, useState, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { useAuth } from '../lib/auth'

const navItems = [
  { to: '/', label: '仪表盘', end: true },
  { to: '/channels', label: '渠道管理' },
  { to: '/routes', label: '模型路由' },
  { to: '/keys', label: 'API 密钥' },
  { to: '/mcp-servers', label: 'MCP 服务器' },
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

  // 窄屏（手机竖屏 / 小平板）把侧边栏收起为抽屉
  const [drawerOpen, setDrawerOpen] = useState(false)
  const location = useLocation()

  // 路由变化后自动收起抽屉
  useEffect(() => {
    setDrawerOpen(false)
  }, [location.pathname])

  // 抽屉打开时锁定背景滚动，并支持 Esc 关闭
  useEffect(() => {
    if (!drawerOpen) return
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setDrawerOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => {
      document.body.style.overflow = prev
      window.removeEventListener('keydown', onKey)
    }
  }, [drawerOpen])

  const apiUrl = runtime.data?.api_url ?? `${window.location.origin}/v1`

  const nav = (
    <nav className="flex-1 space-y-0.5 overflow-y-auto p-2">
      {navItems.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          end={item.end}
          className={({ isActive }) =>
            `block rounded-lg px-3 py-2.5 text-sm transition-colors md:py-2 ${
              isActive
                ? 'bg-emerald-50 font-medium text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
                : 'text-zinc-600 hover:bg-zinc-100 active:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:active:bg-zinc-800'
            }`
          }
        >
          {item.label}
        </NavLink>
      ))}
    </nav>
  )

  const footer = (
    <div className="border-t border-zinc-200 p-3 dark:border-zinc-800">
      <p className="mb-2 text-xs leading-relaxed text-zinc-500 dark:text-zinc-500">
        API 地址
        <br />
        <code className="text-[11px] break-all text-zinc-600 dark:text-zinc-400">{apiUrl}</code>
      </p>
      <button
        onClick={signOut}
        className="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm text-zinc-600 hover:bg-zinc-50 active:bg-zinc-100 md:py-1.5 dark:border-zinc-700 dark:text-zinc-300 dark:hover:bg-zinc-800"
      >
        退出登录
      </button>
    </div>
  )

  return (
    <div className="min-h-full bg-zinc-50 dark:bg-zinc-950">
      {/* 顶栏：仅窄屏显示，提供抽屉入口（padding-top 适配刘海屏安全区） */}
      <header className="sticky top-0 z-30 border-b border-zinc-200 bg-white/90 pt-[env(safe-area-inset-top)] backdrop-blur dark:border-zinc-800 dark:bg-zinc-900/90 md:hidden">
        <div className="flex h-14 items-center gap-2 px-3">
          <button
            type="button"
            aria-label="打开菜单"
            aria-expanded={drawerOpen}
            onClick={() => setDrawerOpen(true)}
            className="-ml-1 rounded-lg p-2.5 text-zinc-600 active:bg-zinc-100 dark:text-zinc-300 dark:active:bg-zinc-800"
          >
            <svg
              width="20"
              height="20"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.8"
              strokeLinecap="round"
            >
              <path d="M4 7h16M4 12h16M4 17h16" />
            </svg>
          </button>
          <img src="/favicon.svg" alt="" className="h-5 w-5" />
          <span className="text-sm font-semibold text-zinc-800 dark:text-zinc-100">AI Gateway</span>
        </div>
      </header>

      <div className="flex">
        {/* 侧边栏：md 以上常驻，贴合视口高度并吸顶 */}
        <aside className="sticky top-0 hidden h-screen w-52 shrink-0 flex-col border-r border-zinc-200 bg-white md:flex lg:w-56 dark:border-zinc-800 dark:bg-zinc-900">
          <div className="flex h-14 items-center gap-2 border-b border-zinc-200 px-4 dark:border-zinc-800">
            <img src="/favicon.svg" alt="" className="h-5 w-5" />
            <span className="text-sm font-semibold text-zinc-800 dark:text-zinc-100">AI Gateway</span>
          </div>
          {nav}
          {footer}
        </aside>

        {/* 抽屉：窄屏下的同款导航 */}
        {drawerOpen ? (
          <div className="fixed inset-0 z-40 md:hidden">
            <div
              className="absolute inset-0 bg-black/40"
              onClick={() => setDrawerOpen(false)}
              aria-hidden="true"
            />
            <aside className="absolute inset-y-0 left-0 flex w-[17rem] max-w-[85vw] flex-col bg-white shadow-xl dark:bg-zinc-900">
              <div className="flex h-14 shrink-0 items-center gap-2 border-b border-zinc-200 px-4 pt-[env(safe-area-inset-top)] dark:border-zinc-800">
                <img src="/favicon.svg" alt="" className="h-5 w-5" />
                <span className="text-sm font-semibold text-zinc-800 dark:text-zinc-100">
                  AI Gateway
                </span>
                <button
                  type="button"
                  aria-label="关闭菜单"
                  onClick={() => setDrawerOpen(false)}
                  className="ml-auto rounded-lg p-1.5 text-zinc-500 active:bg-zinc-100 min-h-10 min-w-10 inline-flex items-center justify-center dark:text-zinc-400 dark:active:bg-zinc-800"
                >
                  <svg
                    width="18"
                    height="18"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.8"
                    strokeLinecap="round"
                  >
                    <path d="M6 6l12 12M18 6L6 18" />
                  </svg>
                </button>
              </div>
              {nav}
              <div className="pb-[env(safe-area-inset-bottom)]">{footer}</div>
            </aside>
          </div>
        ) : null}

        <main className="min-w-0 flex-1">
          <div className="mx-auto max-w-6xl p-3 pb-[calc(1rem+env(safe-area-inset-bottom))] sm:p-4 md:p-6">
            {children}
          </div>
        </main>
      </div>
    </div>
  )
}
