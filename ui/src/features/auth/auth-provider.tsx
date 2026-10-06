import { createContext, useContext, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { api, APIError } from '../../lib/api'
import type { Login } from '../../lib/api'

const storageKey = 'novelbot.account-session'
type Auth = {
  session: Login | null
  loading: boolean
  notice: string
  accept: (session: Login) => void
  endSession: (notice?: string) => void
  logout: () => Promise<void>
}
const AuthContext = createContext<Auth | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Login | null>(null)
  const [loading, setLoading] = useState(true)
  const [notice, setNotice] = useState('')

  function endSession(message = '') {
    setSession(null)
    setNotice(message)
    try {
      sessionStorage.removeItem(storageKey)
    } catch {
      /* memory-only session */
    }
  }

  function accept(value: Login) {
    setSession(value)
    setNotice('')
    try {
      sessionStorage.setItem(
        storageKey,
        JSON.stringify({
          token: value.access_token,
          expiresAt: value.expires_at,
        }),
      )
    } catch {
      /* Browsers that disable storage can still sign in. */
    }
  }

  useEffect(() => {
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 15_000)
    let disposed = false
    async function restore() {
      try {
        const saved = JSON.parse(sessionStorage.getItem(storageKey) ?? 'null')
        if (!saved) return
        if (
          typeof saved.token !== 'string' ||
          typeof saved.expiresAt !== 'string' ||
          !(Date.parse(saved.expiresAt) > Date.now())
        ) {
          endSession('Your session expired. Sign in to continue.')
          return
        }
        const { user } = await api.me(saved.token, controller.signal)
        if (!disposed)
          setSession({
            access_token: saved.token,
            expires_at: saved.expiresAt,
            user,
          })
      } catch (error) {
        if (!disposed)
          endSession(
            error instanceof APIError && error.status === 401
              ? 'Your session expired. Sign in to continue.'
              : 'Could not restore your session. Please sign in again.',
          )
      } finally {
        clearTimeout(timeout)
        if (!disposed) setLoading(false)
      }
    }
    void restore()
    return () => {
      disposed = true
      clearTimeout(timeout)
      controller.abort()
    }
  }, [])

  useEffect(() => {
    if (!session) return
    let timer: ReturnType<typeof setTimeout>
    function schedule() {
      const remaining = Date.parse(session!.expires_at) - Date.now()
      if (remaining <= 0)
        endSession('Your session expired. Sign in to continue.')
      else timer = setTimeout(schedule, Math.min(remaining, 2_147_483_647))
    }
    schedule()
    return () => clearTimeout(timer)
  }, [session])

  async function logout() {
    try {
      if (session) await api.logout(session.access_token)
    } finally {
      endSession()
    }
  }

  return (
    <AuthContext.Provider
      value={{ session, loading, notice, accept, endSession, logout }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const auth = useContext(AuthContext)
  if (!auth) throw new Error('useAuth requires AuthProvider')
  return auth
}
