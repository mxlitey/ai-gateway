import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { clearToken, getToken, setToken, setUnauthorizedHandler } from './api'
import { toast } from './toast'

type AuthState = {
  token: string
  signIn: (token: string) => void
  signOut: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setState] = useState<string>(getToken())

  // 服务端拒绝会话时自动登出：面板的登录判断只看本地有没有 Token，
  // 若不在这里清掉，Token 过期后每个页面都会一直显示 Unauthorized
  useEffect(() => {
    setUnauthorizedHandler(() => {
      clearToken()
      setState('')
      toast('err', '登录已过期，请重新登录')
    })
    return () => setUnauthorizedHandler(null)
  }, [])

  const value: AuthState = {
    token,
    signIn: (next) => {
      setToken(next)
      setState(next)
    },
    signOut: () => {
      clearToken()
      setState('')
    },
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth 必须在 AuthProvider 内使用')
  return ctx
}