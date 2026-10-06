import { createFileRoute } from '@tanstack/react-router'
import { APIKeysScreen } from '../features/api-keys/api-keys-screen'
export const Route = createFileRoute('/api-keys')({ component: APIKeysScreen })
