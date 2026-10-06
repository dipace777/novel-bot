import { useEffect, useState } from 'react'
import { Link, Navigate } from '@tanstack/react-router'
import { api, APIError, errorMessage } from '../../lib/api'
import type { APIKey, IssuedKey } from '../../lib/api'
import { useAuth } from '../auth/auth-provider'
import { Brand, Icon, LoadingScreen } from '../../components/icon'
import { Dialog } from '../../components/dialog'
import { CreateKey, KeySecret } from './key-dialogs'

function keyStatus(key: APIKey, now: number) {
  return key.revoked_at
    ? 'Revoked'
    : Date.parse(key.expires_at) <= now
      ? 'Expired'
      : 'Active'
}
function date(value: string) {
  return new Intl.DateTimeFormat(undefined, {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
  }).format(new Date(value))
}

export function APIKeysScreen() {
  const auth = useAuth()
  const token = auth.session?.access_token
  const [keys, setKeys] = useState<APIKey[]>([])
  const [loading, setLoading] = useState(true)
  const [reload, setReload] = useState(0)
  const [error, setError] = useState('')
  const [search, setSearch] = useState('')
  const [create, setCreate] = useState(false)
  const [issued, setIssued] = useState<IssuedKey | null>(null)
  const [revoking, setRevoking] = useState<APIKey | null>(null)
  const [busy, setBusy] = useState(false)
  const [signingOut, setSigningOut] = useState(false)
  const [now, setNow] = useState(Date.now())

  function failure(error: unknown) {
    if (error instanceof APIError && error.status === 401) {
      setIssued(null)
      auth.endSession('Your session expired. Sign in to continue.')
    } else setError(errorMessage(error))
  }

  useEffect(() => {
    if (!token) return
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 15_000)
    let disposed = false
    setLoading(true)
    setError('')
    api
      .keys(token, controller.signal)
      .then((result) => {
        if (!disposed) setKeys(result.keys ?? [])
      })
      .catch((error) => {
        if (!disposed) failure(error)
      })
      .finally(() => {
        clearTimeout(timeout)
        if (!disposed) setLoading(false)
      })
    return () => {
      disposed = true
      clearTimeout(timeout)
      controller.abort()
    }
  }, [token, reload])

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(timer)
  }, [])

  if (auth.loading) return <LoadingScreen />
  if (!auth.session) return <Navigate to="/login" replace />
  const { user } = auth.session
  const active = keys.filter((key) => keyStatus(key, now) === 'Active').length
  const visible = keys.filter((key) =>
    `${key.name} ${key.id}`.toLowerCase().includes(search.toLowerCase()),
  )
  const initials = user.name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part[0])
    .join('')
    .toUpperCase()

  async function signOut() {
    setSigningOut(true)
    try {
      await auth.logout()
    } catch {
      /* Local credentials have been cleared by the provider. */
    } finally {
      setSigningOut(false)
    }
  }

  async function revoke() {
    if (!revoking || !token || busy) return
    setBusy(true)
    setError('')
    try {
      await api.revoke(token, revoking.id)
      setKeys((current) =>
        current.map((key) =>
          key.id === revoking.id
            ? { ...key, revoked_at: new Date().toISOString() }
            : key,
        ),
      )
      setRevoking(null)
    } catch (error) {
      failure(error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="console-layout">
      <aside className="sidebar">
        <Link to="/" className="sidebar-brand">
          <Brand />
        </Link>
        <div className="workspace-label">DEVELOPER WORKSPACE</div>
        <nav aria-label="Workspace navigation">
          <Link
            className="nav-item selected"
            to="/api-keys"
            aria-current="page"
          >
            <Icon name="key" /> API keys <span className="nav-indicator" />
          </Link>
          <a
            className="nav-item"
            href="/docs/"
            target="_blank"
            rel="noreferrer"
          >
            <Icon name="book" /> API reference{' '}
            <Icon name="external" size={13} />
          </a>
        </nav>
        <div className="sidebar-note">
          <span className="live-dot" />
          <span>
            Your gateway to the web.<small>Build something novel.</small>
          </span>
        </div>
        <div className="account">
          <span className="avatar">{initials}</span>
          <div>
            <strong title={user.name}>{user.name}</strong>
            <small title={user.email}>{user.email}</small>
          </div>
          <button
            className="icon-button"
            aria-label="Sign out"
            title="Sign out"
            disabled={signingOut}
            onClick={() => void signOut()}
          >
            {signingOut ? <span className="spinner" /> : <Icon name="logout" />}
          </button>
        </div>
      </aside>
      <main className="console-main">
        <div className="topbar">
          <span>
            Workspace <span className="breadcrumb-slash">/</span>{' '}
            <strong>API keys</strong>
          </span>
          <a href="/docs/" target="_blank" rel="noreferrer">
            Documentation <Icon name="external" size={13} />
          </a>
        </div>
        <div className="console-content">
          <div className="page-heading">
            <div>
              <div className="eyebrow">CONNECT YOUR APPLICATIONS</div>
              <h1>API keys</h1>
              <p>A small key. A world of possibilities.</p>
            </div>
            <button
              className="button primary"
              onClick={() => {
                setError('')
                setCreate(true)
              }}
            >
              <Icon name="plus" /> Create API key
            </button>
          </div>
          <div className="stats">
            <div className="stat">
              <span className="stat-icon">
                <Icon name="key" size={21} />
              </span>
              <div>
                <small>Active keys</small>
                <strong>
                  {loading ? '—' : active}
                  <span>ready to connect</span>
                </strong>
              </div>
            </div>
            <div className="stat">
              <span className="stat-icon neutral">
                <Icon name="shield" size={21} />
              </span>
              <div>
                <small>Access control</small>
                <strong>
                  Tenant scoped<span>isolated to your workspace</span>
                </strong>
              </div>
            </div>
            <div className="stat">
              <span className="stat-icon neutral">
                <Icon name="lock" size={21} />
              </span>
              <div>
                <small>Secret visibility</small>
                <strong>
                  One-time only<span>save each key securely</span>
                </strong>
              </div>
            </div>
          </div>
          {error && !create && !revoking && (
            <div className="alert error list-error" role="alert">
              {error}
              <button
                className="text-button"
                onClick={() => setReload((value) => value + 1)}
              >
                Try again
              </button>
            </div>
          )}
          <section className="keys-card" aria-labelledby="keys-heading">
            <div className="card-heading">
              <div>
                <h2 id="keys-heading">
                  Your keys{' '}
                  <span className="count">{loading ? '…' : keys.length}</span>
                </h2>
                <p>Manage credentials for your apps and integrations.</p>
              </div>
              <div className="search-field">
                <Icon name="search" size={16} />
                <input
                  aria-label="Search API keys"
                  placeholder="Search keys…"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
              </div>
            </div>
            {loading ? (
              <div className="empty-state" role="status">
                <span className="spinner" />
                <p>Loading your keys…</p>
              </div>
            ) : keys.length === 0 && !error ? (
              <div className="empty-state">
                <span className="empty-icon">
                  <Icon name="key" size={28} />
                </span>
                <h3>Make your first connection.</h3>
                <p>
                  Create an API key to start using Novel Bot
                  <br />
                  in your applications.
                </p>
                <button
                  className="button secondary"
                  onClick={() => setCreate(true)}
                >
                  <Icon name="plus" size={16} /> Create your first key
                </button>
              </div>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Name / key ID</th>
                      <th>Status</th>
                      <th>Created</th>
                      <th>Expires</th>
                      <th>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((key) => {
                      const status = keyStatus(key, now)
                      return (
                        <tr key={key.id}>
                          <td>
                            <div className="key-name">
                              <span className="key-icon">
                                <Icon name="key" size={17} />
                              </span>
                              <div>
                                <strong>{key.name}</strong>
                                <code title={key.id}>
                                  nb_key_{key.id.slice(0, 8)}…
                                </code>
                              </div>
                            </div>
                          </td>
                          <td>
                            <span className={`badge ${status.toLowerCase()}`}>
                              <span />
                              {status}
                            </span>
                          </td>
                          <td>{date(key.created_at)}</td>
                          <td>{date(key.expires_at)}</td>
                          <td>
                            {status !== 'Revoked' && (
                              <button
                                className="text-button revoke-button"
                                onClick={() => {
                                  setError('')
                                  setRevoking(key)
                                }}
                              >
                                Revoke
                              </button>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
                {visible.length === 0 && (
                  <p className="no-results">
                    {error
                      ? 'Keys could not be loaded. Try again above.'
                      : 'No keys match your search.'}
                  </p>
                )}
              </div>
            )}
            <div className="card-footer">
              <Icon name="lock" size={13} /> Full keys are shown only when you
              create them.
            </div>
          </section>
          <section className="quickstart">
            <div className="quickstart-heading">
              <span className="terminal-icon">
                <Icon name="terminal" size={23} />
              </span>
              <div>
                <h2>From key to first request.</h2>
                <p>
                  Set your key in <code>NOVELBOT_API_KEY</code>, then verify
                  access.
                </p>
              </div>
              <a href="/docs/" target="_blank" rel="noreferrer">
                Explore the API <Icon name="arrow" size={16} />
              </a>
            </div>
            <pre>
              <code>
                <span className="code-comment">
                  # Use the public API address in your environment
                </span>
                {'\n'}curl http://localhost:8080/v1/whoami \{'\n'} -H{' '}
                <span className="code-green">
                  "Authorization: Bearer $NOVELBOT_API_KEY"
                </span>
              </code>
            </pre>
          </section>
          <footer className="console-footer">
            <span>Built for what comes next.</span>
            <span>
              Novel Bot <span className="footer-dot">·</span> Developer console
            </span>
          </footer>
        </div>
      </main>
      {create && (
        <CreateKey
          token={token!}
          onClose={() => {
            setCreate(false)
            setError('')
          }}
          onIssued={(value) => {
            setKeys((current) => [value.key, ...current])
            setIssued(value)
            setCreate(false)
          }}
          onUnauthorized={() =>
            auth.endSession('Your session expired. Sign in to continue.')
          }
        />
      )}
      {issued && <KeySecret issued={issued} onClose={() => setIssued(null)} />}
      {revoking && (
        <Dialog
          title="Revoke this key?"
          busy={busy}
          onClose={() => {
            setRevoking(null)
            setError('')
          }}
        >
          <p className="dialog-description">
            Apps using <strong>{revoking.name}</strong> will lose access
            immediately. This cannot be undone.
          </p>
          {error && (
            <div className="alert error" role="alert">
              {error}
            </div>
          )}
          <div className="dialog-actions">
            <button
              className="button secondary"
              disabled={busy}
              onClick={() => {
                setRevoking(null)
                setError('')
              }}
            >
              Keep key
            </button>
            <button
              className="button danger"
              disabled={busy}
              onClick={() => void revoke()}
            >
              {busy && <span className="spinner" />}
              {busy ? 'Revoking…' : 'Revoke key'}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  )
}
