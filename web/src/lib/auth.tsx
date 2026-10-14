import { createContext, useContext, useState, type ReactNode } from 'react'
import { clearToken, getToken, setToken } from './api'

type AuthState = {
  token: string
  signIn: (token: string) => void
  signOut: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setState] = useState<string>(getToken())

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