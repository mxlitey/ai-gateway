import { useEffect, useState, type ReactNode } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '../lib/api'
import { useAuth } from '../lib/auth'

/** 侧栏导航：路径即面板根路径下的页面。 */
const navItems = [
  { to: '/', label: '仪表盘', icon: 'dash', end: true },
  { to: '/channels', label: '渠道管理', icon: 'channel' },
  { to: '/routes', label: '模型路由', icon: 'route' },
  { to: '/keys', label: 'API 密钥', icon: 'key' },
  { to: '/mcp-servers', label: 'MCP 服务器', icon: 'mcp' },
  { to: '/plugins', label: '插件', icon: 'plugin' },
  { to: '/usage', label: '用量', icon: 'usage' },
  { to: '/prices', label: '价格表', icon: 'price' },
  { to: '/errors', label: '错误日志', icon: 'error' },
  { to: '/settings', label: '设置', icon: 'settings' },
]

const navGroups: { title: string; items: typeof navItems }[] = [
  { title: '概览', items: navItems.slice(0, 2) },
  { title: '接入', items: navItems.slice(2, 6) },
  { title: '运营', items: navItems.slice(6, 9) },
  { title: '系统', items: navItems.slice(9) },
]

export default function Layout({ children }: { children: ReactNode }) {
  const { signOut } = useAuth()
  const runtime = useQuery({ queryKey: ['runtime'], queryFn: api.runtimeInfo })
  const [drawerOpen, setDrawerOpen] = useState(false)
  const location = useLocation()

  useEffect(() => {
    setDrawerOpen(false)
  }, [location.pathname])

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

  const version = runtime.data?.version ? `v${runtime.data.version}` : '—'

  const nav = (
    <nav className="nav">
      {navGroups.map((g) => (
        <div key={g.title}>
          <div className="grp">{g.title}</div>
          {g.items.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={({ isActive }) => (isActive ? 'on' : '')}
            >
              <NavIcon name={item.icon} />
              <span className="label">{item.label}</span>
            </NavLink>
          ))}
        </div>
      ))}
    </nav>
  )

  const footer = (
    <div className="side-foot">
      <div className="row">
        <span>版本</span>
        <span className="mono">{version}</span>
      </div>
      <button type="button" onClick={signOut}>
        退出登录
      </button>
    </div>
  )

  return (
    <div className="admin">
      <div className="app">
        {/* 侧栏：>=941 常驻带文字；641~940 收成 54px 图标条 */}
        <aside className="side">
          <div className="brand">
            <img className="logo" src="/favicon.svg" alt="" />
            <b>AI Gateway</b>
          </div>
          {nav}
          {footer}
        </aside>

        <div className="main">
          <div className="topbar">
            <button
              type="button"
              className="icon-btn only-narrow"
              aria-label="打开菜单"
              onClick={() => setDrawerOpen(true)}
            >
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
                <path d="M4 7h16M4 12h16M4 17h16" />
              </svg>
            </button>
            {/* 页面标题统一由内容区的 PageHead 呈现，顶栏不重复显示，
                否则每页都会出现两个同样的标题 */}
            <div className="sp" />
            <span className="sub hide-xs">{version}</span>
          </div>

          <div className="content">{children}</div>
        </div>
      </div>

      {/* 窄屏抽屉 */}
      <div className={`drawer-mask${drawerOpen ? ' open' : ''}`} onClick={() => setDrawerOpen(false)} />
      <aside className={`drawer${drawerOpen ? ' open' : ''}`}>
        <div className="drawer-h">
          <img src="/favicon.svg" alt="" style={{ width: 19, height: 19, borderRadius: 6, flex: 'none' }} />
          <b style={{ fontSize: 12.5 }}>AI Gateway</b>
          <div className="sp" />
          <button type="button" className="icon-btn" onClick={() => setDrawerOpen(false)} aria-label="关闭菜单">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </div>
        {nav}
        {footer}
      </aside>
    </div>
  )
}

function NavIcon({ name }: { name: string }) {
  const paths: Record<string, ReactNode> = {
    dash: <path d="M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z" />,
    channel: <path d="M4 6h16M4 12h16M4 18h10" />,
    route: <path d="M4 18V6h6l3 4h7M17 10l3-2-3-2" />,
    key: <><circle cx="8" cy="12" r="4" /><path d="M12 12h9M18 12v3M15 12v2" /></>,
    mcp: <><rect x="3" y="4" width="18" height="6" rx="2" /><rect x="3" y="14" width="18" height="6" rx="2" /><path d="M7 7h.01M7 17h.01" /></>,
    plugin: <path d="M10 3v4M14 3v4M5 7h14v7a4 4 0 0 1-4 4H9a4 4 0 0 1-4-4z" />,
    usage: <><path d="M4 20V10M10 20V4M16 20v-7M22 20H2" /></>,
    price: <><circle cx="12" cy="12" r="9" /><path d="M12 7v10M9 10h4.5a2 2 0 0 1 0 4H9" /></>,
    error: <><path d="M12 3l9 16H3z" /><path d="M12 9v5M12 17h.01" /></>,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3M12 19v3M2 12h3M19 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2" /></>,
  }
  return (
    <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" style={{ flex: 'none' }}>
      {paths[name] ?? null}
    </svg>
  )
}
