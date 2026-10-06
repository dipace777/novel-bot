import { createFileRoute } from '@tanstack/react-router'
import { AuthScreen } from '../features/auth/auth-screen'
export const Route = createFileRoute('/login')({
  component: () => <AuthScreen />,
})
