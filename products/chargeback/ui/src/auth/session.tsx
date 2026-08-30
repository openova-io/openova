import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from '../api/client'
import type { Me } from '../api/types'

interface SessionState {
  me: Me | null
  loading: boolean
  refresh: () => Promise<Me | null>
  logout: () => Promise<void>
}

const Ctx = createContext<SessionState>({
  me: null,
  loading: true,
  refresh: async () => null,
  logout: async () => {},
})

export function SessionProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const m = await api.get<Me>('/auth/me')
      setMe(m)
      return m
    } catch (e) {
      // 401 = no session; anything else is still "signed out" for routing.
      if (!(e instanceof ApiError) || e.status !== 401) console.warn('auth/me', e)
      setMe(null)
      return null
    } finally {
      setLoading(false)
    }
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.post('/auth/logout')
    } finally {
      setMe(null)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const value = useMemo(() => ({ me, loading, refresh, logout }), [me, loading, refresh, logout])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useSession() {
  return useContext(Ctx)
}

export function homeFor(me: Me | null): string {
  if (!me) return '/signin'
  return me.role === 'operator' ? '/overview' : '/my/usage'
}
