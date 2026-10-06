import { useState } from 'react'
import type { FormEvent } from 'react'
import { api, APIError, errorMessage } from '../../lib/api'
import type { IssuedKey } from '../../lib/api'
import { Icon } from '../../components/icon'
import { Dialog } from '../../components/dialog'

export function CreateKey({
  token,
  onClose,
  onIssued,
  onUnauthorized,
}: {
  token: string
  onClose: () => void
  onIssued: (key: IssuedKey) => void
  onUnauthorized: () => void
}) {
  const [name, setName] = useState('')
  const [days, setDays] = useState('30')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    if (!name.trim()) {
      setError('Give your key a name.')
      return
    }
    setBusy(true)
    setError('')
    try {
      onIssued(await api.issue(token, name.trim(), Number(days) * 86400))
    } catch (error) {
      if (error instanceof APIError && error.status === 401) onUnauthorized()
      else setError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog title="Create API key" busy={busy} onClose={onClose}>
      <p className="dialog-description">
        Give this key a name you’ll recognize. It grants access to your
        workspace’s browser sessions.
      </p>
      <form className="auth-form" onSubmit={submit}>
        {error && (
          <div className="alert error" role="alert">
            {error}
          </div>
        )}
        <label>
          Key name
          <input
            autoFocus
            required
            maxLength={100}
            placeholder="e.g. Production scraper"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={busy}
          />
        </label>
        <label>
          Expires after
          <select
            value={days}
            onChange={(e) => setDays(e.target.value)}
            disabled={busy}
          >
            <option value="1">1 day</option>
            <option value="7">7 days</option>
            <option value="30">30 days</option>
            <option value="90">90 days</option>
          </select>
        </label>
        <div className="dialog-actions">
          <button
            type="button"
            className="button secondary"
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </button>
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? <span className="spinner" /> : <Icon name="plus" />}
            {busy ? 'Creating…' : 'Create key'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

export function KeySecret({
  issued,
  onClose,
}: {
  issued: IssuedKey
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  async function copy() {
    try {
      await navigator.clipboard.writeText(issued.api_key)
      setCopied(true)
      setError('')
    } catch {
      setError(
        'Clipboard access is unavailable. Select and copy the key below.',
      )
    }
  }
  return (
    <Dialog title="Your key is ready." onClose={onClose}>
      <div className="success-icon">
        <Icon name="check" size={25} />
      </div>
      <p className="dialog-description">
        Copy <strong>{issued.key.name}</strong> and save it somewhere secure.
        You won’t be able to see this secret again.
      </p>
      <label className="secret-label">
        API key
        <textarea
          readOnly
          value={issued.api_key}
          aria-label="Generated API key"
          spellCheck={false}
          onFocus={(e) => e.target.select()}
        />
      </label>
      <button className="button secondary copy-key" onClick={() => void copy()}>
        <Icon name={copied ? 'check' : 'copy'} />
        {copied ? 'Copied to clipboard' : 'Copy API key'}
      </button>
      <p className="secret-expiry">
        Expires{' '}
        {new Date(issued.key.expires_at).toLocaleDateString(undefined, {
          day: 'numeric',
          month: 'short',
          year: 'numeric',
        })}
        . Use it as a Bearer token in your app.
      </p>
      {error && (
        <div className="alert error" role="alert">
          {error}
        </div>
      )}
      <div className="dialog-actions">
        <button className="button primary" onClick={onClose}>
          I’ve saved my key <Icon name="check" />
        </button>
      </div>
      <span className="sr-only" role="status">
        {copied ? 'API key copied to clipboard.' : ''}
      </span>
    </Dialog>
  )
}
