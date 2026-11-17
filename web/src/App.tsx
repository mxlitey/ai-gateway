import { Navigate, Route, Routes } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from './lib/api'
import { useAuth } from './lib/auth'
import { setCurrencySymbol } from './lib/money'
import { Loading, Toaster } from './components/ui'
import Login from './pages/Login'
import Setup from './pages/Setup'
import V2Layout from './v2/V2Layout'
import V2Dashboard from './v2/Dashboard'
import V2Usage from './v2/Usage'
import V2Channels from './v2/Channels'
import V2Routes from './v2/Routes'
import V2Keys from './v2/Keys'
import V2Mcp from './v2/Mcp'
import V2Plugins from './v2/Plugins'
import V2Prices from './v2/Prices'
import V2Errors from './v2/Errors'
import V2Settings from './v2/Settings'
import './v2/v2.css'

export default function App() {
  const { token } = useAuth()
  const setupState = useQuery({ queryKey: ['setup'], queryFn: api.setupState })
  // 货币符号只在展示层生效：读到运行期设置后同步给格式化函数
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: api.settings,
    enabled: Boolean(token) && Boolean(setupState.data?.initialized),
  })
  if (settings.data) setCurrencySymbol(settings.data.settings.currency_symbol)

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
      <Routes>
        <Route
          path="/*"
          element={
            <V2Layout>
              <Routes>
                <Route index element={<V2Dashboard />} />
                <Route path="/usage" element={<V2Usage />} />
                <Route path="/channels" element={<V2Channels />} />
                <Route path="/routes" element={<V2Routes />} />
                <Route path="/keys" element={<V2Keys />} />
                {/* /mcp 是 MCP 协议端点，管理页必须用别的路径，否则刷新会被协议处理器拦截 */}
                <Route path="/mcp-servers" element={<V2Mcp />} />
                <Route path="/plugins" element={<V2Plugins />} />
                <Route path="/prices" element={<V2Prices />} />
                <Route path="/errors" element={<V2Errors />} />
                <Route path="/settings" element={<V2Settings />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </V2Layout>
          }
        />
      </Routes>
      <Toaster />
    </>
  )
}
