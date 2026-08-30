import { useEffect, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, errorText } from '../api/client'
import { useSession } from '../auth/session'
import type { ActivateResult, Invite } from '../api/types'
import { Badge, Field, Notice, Steps } from '../components/ui'
import { hasErrors, parseProjectIds, validateActivation, type ActivationErrors } from '../lib/activation'

const STEPS = ['PIN', 'Projects and key', 'Verify', 'Done']

/**
 * Invite activation (spec §5 customer flow): the invite link opens here;
 * the invitee proves the mailbox with a PIN, enters region + project ids +
 * AK/SK, the server verifies each project against the cloud (spec §3
 * "Verification"), and on success the customer is active and the session
 * belongs to the admin. The secret key is posted once and never shown.
 */
export function Activate() {
  const { token = '' } = useParams()
  const { me, refresh } = useSession()
  const [invite, setInvite] = useState<Invite | null>(null)
  const [loadError, setLoadError] = useState('')
  const [step, setStep] = useState(0)

  // step 0 — PIN
  const [email, setEmail] = useState('')
  const [pinSent, setPinSent] = useState(false)
  const [code, setCode] = useState('')
  const [pinError, setPinError] = useState('')

  // step 1 — form
  const [region, setRegion] = useState('')
  const [projectIds, setProjectIds] = useState('')
  const [accessKey, setAccessKey] = useState('')
  const [secretKey, setSecretKey] = useState('')
  const [errors, setErrors] = useState<ActivationErrors>({})
  const [submitError, setSubmitError] = useState('')
  const [busy, setBusy] = useState(false)

  // step 2/3 — result
  const [result, setResult] = useState<ActivateResult | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .get<Invite>(`/invites/${token}`)
      .then((inv) => {
        if (cancelled) return
        setInvite(inv)
        setEmail(inv.email ?? '')
        setRegion(inv.region ?? '')
        setProjectIds((inv.project_ids ?? []).join('\n'))
      })
      .catch((e) => !cancelled && setLoadError(errorText(e)))
    return () => {
      cancelled = true
    }
  }, [token])

  // An already signed-in invitee skips the PIN step.
  useEffect(() => {
    if (me && step === 0) setStep(1)
  }, [me, step])

  const requestPin = async (e: FormEvent) => {
    e.preventDefault()
    setPinError('')
    try {
      await api.post('/auth/pin/request', { email: email.trim().toLowerCase() })
      setPinSent(true)
    } catch (err) {
      setPinError(errorText(err))
    }
  }

  const verifyPin = async (e: FormEvent) => {
    e.preventDefault()
    setPinError('')
    try {
      await api.post('/auth/pin/verify', { email: email.trim().toLowerCase(), code: code.trim() })
      await refresh()
      setStep(1)
    } catch (err) {
      setPinError(errorText(err))
    }
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateActivation({ region, projectIds, accessKey, secretKey })
    setErrors(errs)
    if (hasErrors(errs)) return
    setBusy(true)
    setSubmitError('')
    setStep(2)
    try {
      const res = await api.post<ActivateResult>(`/invites/${token}/activate`, {
        region: region.trim(),
        project_ids: parseProjectIds(projectIds),
        access_key: accessKey.trim(),
        secret_key: secretKey,
      })
      setSecretKey('')
      setResult(res)
      await refresh()
      setStep(3)
    } catch (err) {
      setSubmitError(errorText(err))
      setStep(1)
    } finally {
      setBusy(false)
    }
  }

  if (loadError) {
    return (
      <div className="single">
        <h1>Activate</h1>
        <Notice kind="bad">This invite link is not valid: {loadError}</Notice>
      </div>
    )
  }
  if (!invite) return <div className="single muted">Loading invite…</div>

  return (
    <div className="single" style={{ maxWidth: 640 }}>
      <h1>Activate {invite.customer_name}</h1>
      <Steps steps={STEPS} at={step} />

      {step === 0 ? (
        <div className="card">
          <p className="muted">Confirm the address this invite was sent to.</p>
          {!pinSent ? (
            <form onSubmit={requestPin}>
              <Field label="Email">
                <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
              </Field>
              {pinError ? <Notice kind="bad">{pinError}</Notice> : null}
              <button className="primary">Send PIN</button>
            </form>
          ) : (
            <form onSubmit={verifyPin}>
              <Field label={`PIN sent to ${email}`}>
                <input value={code} onChange={(e) => setCode(e.target.value)} inputMode="numeric" autoFocus required />
              </Field>
              {pinError ? <Notice kind="bad">{pinError}</Notice> : null}
              <button className="primary">Continue</button>
            </form>
          )}
        </div>
      ) : null}

      {step === 1 ? (
        <form className="card" onSubmit={submit}>
          <p className="muted">
            Add a read-only access key for the cloud projects to be metered. Each project is verified with one signed
            call before anything is collected.
          </p>
          <Field label="Region" error={errors.region}>
            <input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="om-east-1" />
          </Field>
          <Field label="Project ids (one per line)" error={errors.projectIds}>
            <textarea value={projectIds} onChange={(e) => setProjectIds(e.target.value)} />
          </Field>
          <div className="grid2">
            <Field label="Access key (AK)" error={errors.accessKey}>
              <input value={accessKey} onChange={(e) => setAccessKey(e.target.value)} autoComplete="off" />
            </Field>
            <Field label="Secret key (SK) — write-only" error={errors.secretKey}>
              <input type="password" value={secretKey} onChange={(e) => setSecretKey(e.target.value)} autoComplete="new-password" />
            </Field>
          </div>
          {submitError ? <Notice kind="bad">{submitError}</Notice> : null}
          <button className="primary" disabled={busy}>
            Verify and activate
          </button>
        </form>
      ) : null}

      {step === 2 ? (
        <div className="card">
          <p>Verifying {parseProjectIds(projectIds).length} project(s)…</p>
        </div>
      ) : null}

      {step === 3 && result ? (
        <div className="card stack">
          <Notice kind="ok">{invite.customer_name} is active.</Notice>
          {result.sources && result.sources.length > 0 ? (
            <table>
              <thead>
                <tr>
                  <th>Project</th>
                  <th>Region</th>
                  <th>Status</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {result.sources.map((s) => (
                  <tr key={s.id ?? s.project_id}>
                    <td className="mono">{s.project_id}</td>
                    <td>{s.region}</td>
                    <td>
                      <Badge status={s.status} />
                    </td>
                    <td className="small">{s.last_error ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : null}
          <p>
            <Link to="/my/usage">Open my usage</Link>
          </p>
        </div>
      ) : null}
    </div>
  )
}
