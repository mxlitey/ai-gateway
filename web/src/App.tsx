import { Navigate, Route, Routes } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from './lib/api'
import { useAuth } from './lib/auth'
import Layout from './components/Layout'
import { Loading, Toaster } from './components/ui'
import Login from './pages/Login'
import Setup from './pages/Setup'
import Dashboard from './pages/Dashboard'
import Channels from './pages/Channels'
import ModelRoutes from './pages/Routes'
import Keys from './pages/Keys'
import Usage from './pages/Usage'
import Prices from './pages/Prices'
import Errors from './pages/Errors'
import Plugins from './pages/Plugins'
import Mcp from './pages/Mcp'
import Settings from './pages/Settings'

export default function App() {
  const { token } = useAuth()
  const setupState = useQuery({ queryKey: ['setup'], queryFn: api.setupState })

  if (setupState.isLoading) {
    return (
      <div className="min-h-full bg-zinc-50 dark:bg-zinc-950">
        <Loading />
      </div>
    )
  }

  // 未完成首次初始化：只暴露向导页
  if (!setupState.data?.initialized) {
    return (
      <>
        <Setup />
        <Toaster />
      </>
    )
  }

  // 未登录：只暴露登录页
  if (!token) {
    return (
      <>
        <Login />
        <Toaster />
      </>
    )
  }

  return (
    <>
      <Layout>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/channels" element={<Channels />} />
          <Route path="/routes" element={<ModelRoutes />} />
          <Route path="/keys" element={<Keys />} />
          {/* /mcp 是 MCP 协议端点，管理页必须用别的路径，否则刷新会被协议处理器拦截 */}
          <Route path="/mcp-servers" element={<Mcp />} />
          <Route path="/usage" element={<Usage />} />
          <Route path="/prices" element={<Prices />} />
          <Route path="/errors" element={<Errors />} />
          <Route path="/plugins" element={<Plugins />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </Layout>
      <Toaster />
    </>
  )
}