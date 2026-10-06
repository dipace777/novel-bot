import { createFileRoute, Navigate } from '@tanstack/react-router'
import { useAuth } from '../features/auth/auth-provider'
import { LoadingScreen } from '../components/icon'

export const Route = createFileRoute('/')({ component: Home })

function Home() {
  const auth = useAuth()
  if (auth.loading) return <LoadingScreen />
  return <Navigate to={auth.session ? '/api-keys' : '/login'} replace />
}
