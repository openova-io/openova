import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { api, errorText } from '../api/client'
import { homeFor, useSession } from '../auth/session'
import { Field, Notice } from '../components/ui'

export function SignIn() {
  const { me, loading, refresh } = useSession()
  const nav = useNavigate()
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [sent, setSent] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  if (!loading && me) return <Navigate to={homeFor(me)} replace />

  const request = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.post('/auth/pin/request', { email: email.trim().toLowerCase() })
      setSent(true)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const verify = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.post('/auth/pin/verify', { email: email.trim().toLowerCase(), code: code.trim() })
      const m = await refresh()
      nav(homeFor(m), { replace: true })
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="single">
      <h1>Chargeback</h1>
      <div className="card">
        {!sent ? (
          <form onSubmit={request}>
            <Field label="Email">
              <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus required />
            </Field>
            {error ? <Notice kind="bad">{error}</Notice> : null}
            <button className="primary" disabled={busy}>
              Send sign-in PIN
            </button>
          </form>
        ) : (
          <form onSubmit={verify}>
            <p className="muted">A PIN was sent to {email}.</p>
            <Field label="PIN">
              <input value={code} onChange={(e) => setCode(e.target.value)} inputMode="numeric" autoFocus required />
            </Field>
            {error ? <Notice kind="bad">{error}</Notice> : null}
            <div className="row">
              <button className="primary" disabled={busy}>
                Sign in
              </button>
              <button type="button" onClick={() => setSent(false)}>
                Use another address
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  )
}
