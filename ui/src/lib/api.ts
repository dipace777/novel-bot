export interface User {
  id: string
  client_id: string
  name: string
  email: string
  created_at: string
}

export interface Login {
  access_token: string
  expires_at: string
  user: User
}

export interface APIKey {
  id: string
  name: string
  created_at: string
  expires_at: string
  revoked_at?: string
}

export interface IssuedKey {
  key: APIKey
  api_key: string
}

export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message)
    this.name = 'APIError'
  }
}

async function request<T>(
  path: string,
  options: {
    method?: string
    token?: string
    body?: unknown
    signal?: AbortSignal
  } = {},
): Promise<T> {
  let response: Response
  try {
    response = await fetch(`/v1${path}`, {
      method: options.method ?? 'GET',
      headers: {
        Accept: 'application/json',
        ...(options.body ? { 'Content-Type': 'application/json' } : {}),
        ...(options.token ? { Authorization: `Bearer ${options.token}` } : {}),
      },
      body: options.body ? JSON.stringify(options.body) : undefined,
      signal: options.signal ?? AbortSignal.timeout(15_000),
      cache: 'no-store',
      credentials: 'omit',
    })
  } catch (error) {
    if (options.signal?.aborted) throw error
    throw new APIError(
      'Could not reach the API. Check your connection and try again.',
      0,
    )
  }
  if (response.status === 204) return undefined as T
  const data = await response.json().catch(() => null)
  if (!response.ok) {
    const retry =
      response.status === 429 ? response.headers.get('Retry-After') : null
    throw new APIError(
      `${data?.error?.message ?? 'The request failed.'}${retry ? ` Try again in ${retry} seconds.` : ''}`,
      response.status,
    )
  }
  if (data === null)
    throw new APIError(
      'The API returned an unexpected response.',
      response.status,
    )
  return data as T
}

export const api = {
  register: (name: string, email: string, password: string) =>
    request<{ user: User }>('/auth/register', {
      method: 'POST',
      body: { name, email, password },
    }),
  login: (email: string, password: string) =>
    request<Login>('/auth/login', {
      method: 'POST',
      body: { email, password },
    }),
  me: (token: string, signal?: AbortSignal) =>
    request<{ user: User }>('/auth/me', { token, signal }),
  logout: (token: string) =>
    request<void>('/auth/logout', { method: 'POST', token }),
  keys: (token: string, signal?: AbortSignal) =>
    request<{ keys: APIKey[] }>('/api-keys', { token, signal }),
  issue: (token: string, name: string, ttl: number) =>
    request<IssuedKey>('/api-keys', {
      method: 'POST',
      token,
      body: { name, ttl_seconds: ttl },
    }),
  revoke: (token: string, id: string) =>
    request<void>(`/api-keys/${encodeURIComponent(id)}`, {
      method: 'DELETE',
      token,
    }),
}

export function errorMessage(error: unknown) {
  return error instanceof Error
    ? error.message
    : 'Something went wrong. Please try again.'
}
