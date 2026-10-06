import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, Navigate } from '@tanstack/react-router'
import { api, errorMessage } from '../../lib/api'
import { Brand, Icon, LoadingScreen } from '../../components/icon'
import { useAuth } from './auth-provider'

export function AuthScreen({ register = false }: { register?: boolean }) {
  const auth = useAuth()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [registered, setRegistered] = useState(false)
  if (auth.loading) return <LoadingScreen />
  if (auth.session) return <Navigate to="/api-keys" replace />

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    const bytes = new TextEncoder().encode(password).length
    if (register && !registered && (bytes < 12 || bytes > 72)) {
      setError('Use a password between 12 and 72 bytes.')
      return
    }
    setBusy(true)
    setError('')
    try {
      if (register && !registered) {
        await api.register(name.trim(), email.trim(), password)
        setRegistered(true)
      }
      auth.accept(await api.login(email.trim(), password))
      setPassword('')
    } catch (error) {
      setError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="auth-layout">
      <aside className="auth-story">
        <Link to="/">
          <Brand />
        </Link>
        <div className="story-content">
          <div className="eyebrow">
            <span className="live-dot" /> THE WEB, AT YOUR COMMAND
          </div>
          <h1>
            Your next idea.
            <br />
            <span>A browser away.</span>
          </h1>
          <p>
            Give your applications a browser. We take care of the
            infrastructure, so you can focus on what comes next.
          </p>
          <div className="browser-art" aria-hidden="true">
            <div className="browser-shadow" />
            <div className="browser-window">
              <div className="browser-bar">
                <span />
                <span />
                <span />
                <div>
                  <Icon name="lock" size={11} /> novelbot / your next idea
                </div>
              </div>
              <div className="browser-code">
                <span className="code-comment">
                  // a little code. a lot of possibility.
                </span>
                <br />
                <br />
                <span className="code-purple">const</span> browser ={' '}
                <span className="code-purple">await</span>
                <br />
                &nbsp; chromium.
                <span className="code-green">connectOverCDP</span>(<br />
                &nbsp;&nbsp;&nbsp; session.cdp_url
                <br />
                &nbsp; );
                <br />
                <br />
                <span className="code-comment">
                  // you're connected. go build.
                </span>
              </div>
              <div className="browser-status">
                <span className="live-dot" /> Browser connected <span>CDP</span>
              </div>
            </div>
            <div className="ready-tag">
              <Icon name="check" size={14} /> Ready to automate
            </div>
          </div>
          <div className="story-benefits">
            <span>
              <Icon name="shield" /> Secure API access
            </span>
            <span>
              <Icon name="terminal" /> Built for developers
            </span>
          </div>
        </div>
        <span className="story-footer">
          © {new Date().getFullYear()} Novel Bot
        </span>
      </aside>
      <main className="auth-main">
        <div className="auth-top">
          <span>
            {register ? 'Already have an account?' : 'New around here?'}
          </span>
          <Link to={register ? '/login' : '/register'}>
            {register ? 'Sign in' : 'Create an account'}{' '}
            <Icon name="arrow" size={15} />
          </Link>
        </div>
        <div className="auth-form-wrap">
          <div className="form-emblem">
            <Icon name={register ? 'terminal' : 'key'} size={24} />
          </div>
          <h2>{register ? 'Start building.' : 'Welcome back.'}</h2>
          <p className="auth-subtitle">
            {register
              ? 'Your browser infrastructure starts here.'
              : 'Sign in to your developer workspace.'}
          </p>
          <form onSubmit={submit} className="auth-form">
            {error && (
              <div className="alert error" role="alert">
                {registered ? 'Account created. Sign-in failed: ' : ''}
                {error}
              </div>
            )}
            {!error && auth.notice && (
              <div className="alert" role="status">
                {auth.notice}
              </div>
            )}
            {register && (
              <label>
                Full name
                <input
                  required
                  name="name"
                  autoComplete="name"
                  maxLength={100}
                  placeholder="Ada Lovelace"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  disabled={busy || registered}
                />
              </label>
            )}
            <label>
              Email address
              <input
                required
                type="email"
                name="email"
                autoComplete="username"
                maxLength={254}
                placeholder="you@company.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                disabled={busy || registered}
              />
            </label>
            <label>
              Password
              <div className="password-field">
                <input
                  required
                  type={showPassword ? 'text' : 'password'}
                  name="password"
                  aria-label="Password"
                  aria-describedby={register ? 'password-help' : undefined}
                  autoComplete={register ? 'new-password' : 'current-password'}
                  placeholder={
                    register
                      ? 'Create a strong password'
                      : 'Enter your password'
                  }
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  disabled={busy}
                />
                <button
                  type="button"
                  className="icon-button"
                  aria-label={showPassword ? 'Hide password' : 'Show password'}
                  aria-pressed={showPassword}
                  onClick={() => setShowPassword(!showPassword)}
                >
                  <Icon name="eye" />
                </button>
              </div>
              {register && (
                <small id="password-help">
                  12–72 UTF-8 bytes (12–72 ASCII characters).
                </small>
              )}
            </label>
            <button
              className="button primary auth-submit"
              disabled={busy}
              type="submit"
            >
              {busy ? <span className="spinner" /> : null}
              {busy
                ? 'Connecting…'
                : register && !registered
                  ? 'Create account'
                  : 'Sign in'}
              {!busy && <Icon name="arrow" />}
            </button>
          </form>
          <p className="auth-reassurance">
            <Icon name="lock" size={13} /> Your keys. Your workspace. Your
            control.
          </p>
          <div className="auth-docs">
            <span>Here to explore?</span>
            <a href="/docs/" target="_blank" rel="noreferrer">
              Read the API docs <Icon name="external" size={13} />
            </a>
          </div>
        </div>
        <p className="auth-bottom">
          Browser automation, without the infrastructure work.
        </p>
      </main>
    </div>
  )
}
