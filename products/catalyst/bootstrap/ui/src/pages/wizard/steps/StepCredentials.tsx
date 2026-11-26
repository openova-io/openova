import { useState } from 'react'
import { Eye, EyeOff, CheckCircle2, XCircle, Loader2, AlertCircle, RotateCw, Copy, KeyRound, Download, Sparkles, ClipboardPaste, ShieldAlert } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { resolveSovereignDomain, isValidSSHPublicKey } from '@/entities/deployment/model'
import { API_BASE } from '@/shared/config/urls'
import { useProfile, credentialMap, type CloudProfile } from '@/lib/providers.api'
import { StepShell, useStepNav } from './_shared'

type ValidationState = 'idle' | 'validating' | 'valid' | 'invalid'

/**
 * Specific failure mode reported by the validator. Used to render a
 * targeted error UI per docs/INVIOLABLE-PRINCIPLES.md #2 ("never compromise
 * from quality") — generic "rejected" messages cost the user a support
 * ticket; a specific reason lets them self-recover.
 *
 * Closes #123.
 */
type FailureKind =
  | 'rejected'      // the cloud refused the credentials (200, valid false)
  | 'invalid-field' // the API names a field that is missing or does not match its pattern (400)
  | 'unreachable'   // the API could not reach the cloud (503)
  | 'network'       // could not reach catalyst-api (CORS, offline, DNS)
  | 'parse'         // backend response was malformed
  | 'http'          // any other non-2xx HTTP status

interface FailureDetail {
  kind: FailureKind
  /** Short human-readable summary (one line). */
  summary: string
  /** Detailed remediation hint (multi-line, may include link). */
  hint: string
  /** Raw backend message verbatim, when available. */
  rawMessage?: string
  /** HTTP status code, when relevant. */
  status?: number
}

const FAILURE_HINTS: Record<FailureKind, { summary: string; hint: string }> = {
  rejected: {
    summary: 'Credentials rejected by the cloud',
    hint:
      'The credentials reached the cloud but do not have the access provisioning needs. ' +
      'Check that they are current and allow read and write access, then validate again.',
  },
  'invalid-field': {
    summary: 'A credential field is missing or malformed',
    hint: 'The provisioning API names the field below. Correct it and validate again.',
  },
  unreachable: {
    summary: 'Could not reach the cloud',
    hint:
      'The catalyst-api could not reach the API of the cloud. ' +
      'This is usually transient — wait a few seconds and retry.',
  },
  network: {
    summary: 'Could not reach the validation service',
    hint:
      'The wizard could not POST to /api/v1/credentials/validate. ' +
      'You may be offline, behind a captive portal, or the catalyst-api is down. ' +
      'Reload the page and try again — the wizard preserves your inputs.',
  },
  parse: {
    summary: 'Validation service returned a malformed response',
    hint:
      "The backend's response could not be parsed as JSON. This is a backend bug — " +
      'open a support ticket with the diagnostic JSON below and the wizard team will investigate.',
  },
  http: {
    summary: 'Validation service returned an unexpected status',
    hint:
      'The validation endpoint returned a non-2xx status that the wizard does not handle. ' +
      'Retry — if it persists, copy the diagnostic and file a support ticket.',
  },
}

/* ── The credential fields of the cloud profile ─────────────────────── */
function CredentialsSection({ profile, optional }: { profile: CloudProfile; optional: boolean }) {
  const store = useWizardStore()
  const [state, setState] = useState<ValidationState>(store.credentialValidated ? 'valid' : 'idle')
  /**
   * Specific failure detail — populated when validate() determines the
   * credentials are invalid OR validation itself failed. Displays a targeted
   * error card with remediation steps + retry button.
   */
  const [failure, setFailure] = useState<FailureDetail | null>(null)
  const [shown, setShown] = useState<Record<string, boolean>>({})
  const fields = profile.credentials.fields
  const missing = fields.some((f) => f.required && !(store.credentials[f.key] ?? '').trim())

  function change(key: string, value: string) {
    store.setCredential(key, value)
    setFailure(null)
    setState('idle')
  }

  function fail(kind: FailureKind, rawMessage?: string, status?: number) {
    setFailure({ kind, ...FAILURE_HINTS[kind], rawMessage, status })
    setState('invalid')
    store.setCredentialValidated(false)
  }

  /**
   * validate() — POSTs the credential map to /api/v1/credentials/validate
   * ({provider, installation, credentials}) and surfaces the exact failure
   * mode the backend reports: per docs/INVIOLABLE-PRINCIPLES.md #1, ANY
   * validation error is a hard "invalid".
   *
   * Backend wire format (handler/credentials.go):
   *   200 + valid=true   → the cloud accepted the credentials
   *   200 + valid=false  → the cloud refused them
   *   400 + field        → a field is missing or does not match its pattern
   *   503                → the cloud is unreachable
   *   anything else      → unhandled
   */
  async function validate() {
    setState('validating')
    setFailure(null)
    const s = useWizardStore.getState()
    let res: Response
    try {
      res = await fetch(`${API_BASE}/v1/credentials/validate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider: s.provider, installation: s.installation, credentials: credentialMap(s.credentials) }),
      })
    } catch (err) {
      fail('network', String(err))
      return
    }
    let data: { valid?: boolean; field?: string; message?: string }
    try {
      data = (await res.json()) as { valid?: boolean; field?: string; message?: string }
    } catch (err) {
      fail('parse', String(err), res.status)
      return
    }
    if (res.ok && data.valid === true) {
      setState('valid')
      store.setCredentialValidated(true)
      return
    }
    const kind: FailureKind = res.status === 503 ? 'unreachable' : res.status === 400 ? 'invalid-field' : res.ok ? 'rejected' : 'http'
    fail(kind, data.field ? `${data.field}: ${data.message ?? ''}` : data.message, res.status)
  }

  return (
    <div data-testid="credentials-section" style={{
      borderRadius: 12, overflow: 'hidden',
      border: state === 'valid'
        ? '1.5px solid rgba(74,222,128,0.3)'
        : '1.5px solid var(--wiz-border-sub)',
      background: state === 'valid' ? 'rgba(74,222,128,0.03)' : 'var(--wiz-bg-xs)',
      transition: 'all 0.2s',
    }}>
      {/* Cloud header */}
      <div style={{ padding: '10px 14px', borderBottom: '1px solid var(--wiz-border-sub)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>{profile.displayName}</span>
          {state === 'valid' && <CheckCircle2 size={13} style={{ color: '#4ADE80' }} />}
        </div>
        <div style={{ fontSize: 11, color: 'var(--wiz-text-sub)', marginTop: 2 }}>
          {optional
            ? "Optional: leave every field empty to provision with the operator's credentials."
            : 'The credentials of your cloud account, used only during provisioning.'}
        </div>
      </div>

      <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}>
        {fields.map((f) => (
          <label key={f.key} style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <span style={{ fontSize: 11, fontWeight: 500, color: 'var(--wiz-text-sub)' }}>
              {f.label} <span style={{ color: 'var(--wiz-text-hint)', marginLeft: 6 }}>{f.required ? 'required' : 'optional'}</span>
            </span>
            <div style={{ position: 'relative' }}>
              <input
                data-testid={`credential-${f.key}`}
                type={f.secret && !shown[f.key] ? 'password' : 'text'}
                autoComplete="off"
                value={store.credentials[f.key] ?? ''}
                onChange={(e) => change(f.key, e.target.value)}
                aria-invalid={state === 'invalid'}
                style={{
                  width: '100%', height: 38, borderRadius: 7,
                  border: `1.5px solid ${failure ? 'rgba(248,113,113,0.5)' : 'var(--wiz-border)'}`,
                  background: 'var(--wiz-bg-input)',
                  color: 'var(--wiz-text-hi)', fontSize: 13, paddingLeft: 10, paddingRight: 38,
                  outline: 'none', fontFamily: 'Inter, monospace',
                }}
              />
              {f.secret && (
                <button
                  type="button"
                  aria-label={`Show ${f.label}`}
                  onClick={() => setShown((m) => ({ ...m, [f.key]: !m[f.key] }))}
                  style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', color: 'var(--wiz-text-sub)', cursor: 'pointer', padding: 0, display: 'flex' }}
                >
                  {shown[f.key] ? <EyeOff size={14} /> : <Eye size={14} />}
                </button>
              )}
            </div>
          </label>
        ))}

        {/* Validate button */}
        <button
          type="button"
          data-testid="credentials-validate"
          onClick={validate}
          disabled={missing || state === 'validating' || state === 'valid'}
          style={{
            alignSelf: 'flex-start', height: 34, padding: '0 14px', borderRadius: 7,
            border: state === 'valid' ? '1.5px solid rgba(74,222,128,0.35)' : '1.5px solid var(--wiz-border)',
            background: state === 'valid' ? 'rgba(74,222,128,0.07)' : 'var(--wiz-bg-input)',
            color: state === 'valid' ? '#4ADE80' : 'var(--wiz-text-sub)',
            fontSize: 12, fontWeight: 600, cursor: missing || state === 'validating' || state === 'valid' ? 'default' : 'pointer',
            display: 'flex', alignItems: 'center', gap: 5, fontFamily: 'Inter, sans-serif',
          }}
        >
          {state === 'validating' && <Loader2 size={12} className="animate-spin" />}
          {state === 'valid'      && <CheckCircle2 size={12} />}
          {state === 'idle'       ? 'Validate' :
           state === 'invalid'    ? 'Retry' :
           state === 'validating' ? 'Checking…' : 'Validated'}
        </button>

        {/* Feedback */}
        {state === 'valid' && (
          <div style={{ display: 'flex', gap: 6, fontSize: 11, color: '#4ADE80', alignItems: 'center' }}>
            <CheckCircle2 size={12} /> Credentials validated — access confirmed
          </div>
        )}
        {failure && state === 'invalid' && (
          <ValidationErrorCard id="credentials-validation-err" failure={failure} onRetry={validate} disabled={missing} />
        )}
      </div>
    </div>
  )
}

export function StepCredentials() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const loaded = useProfile(store.provider, store.installation)
  const profile = loaded?.state === 'ready' ? loaded.value : null
  const source = profile?.credentials.source

  // A cloud whose profile takes the operator's credentials asks for none;
  // one that takes either needs them validated only when some are typed.
  const typed = Object.keys(credentialMap(store.credentials)).length > 0
  const credentialsOk = source === 'operator' || (source === 'either' && !typed) || (!!source && store.credentialValidated)
  // Security floor (#160): the OpenTofu module rejects empty/invalid keys;
  // gate the wizard's Next button on the same regex so the user can't reach
  // StepReview with a payload the backend will refuse.
  const sshKeyOk = isValidSSHPublicKey(store.sshPublicKey)
  const canProceed = credentialsOk && sshKeyOk

  return (
    <StepShell
      title="Cloud credentials"
      description="Provide the credentials the profile of your cloud asks for, plus an SSH public key for break-glass access. Credentials are used only during provisioning and never persisted on our servers."
      onNext={() => { if (canProceed) next() }}
      onBack={back}
      nextDisabled={!canProceed}
    >
      {!store.provider && (
        <div role="alert" style={{ fontSize: 12, color: '#F59E0B' }}>Choose a cloud on the provider step first.</div>
      )}
      {loaded?.state === 'loading' && <div style={{ fontSize: 12, color: 'var(--wiz-text-sub)' }}>Loading the profile of the cloud…</div>}
      {loaded?.state === 'error' && (
        <div role="alert" data-testid="credentials-profile-error" style={{ fontSize: 12, color: '#F87171' }}>
          The profile of the cloud cannot be read: {loaded.error.message}
        </div>
      )}
      {profile && source === 'operator' && (
        <div data-testid="credentials-operator" style={{ fontSize: 12, color: 'var(--wiz-text-sub)' }}>
          {profile.displayName} is provisioned with the operator's credentials. There is nothing to enter.
        </div>
      )}
      {profile && source !== 'operator' && <CredentialsSection profile={profile} optional={source === 'either'} />}

      {/* SSH keypair — attached to every server for sovereign-admin
          break-glass access. Closes #160. */}
      <SSHKeySection />
    </StepShell>
  )
}

/**
 * ValidationErrorCard — inline error banner shown beneath the credential
 * input when validate() determines the token is invalid OR validation
 * itself failed.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 ("never compromise from quality")
 * we surface the exact failure mode + remediation hint + the raw backend
 * message verbatim so the operator can self-recover without filing a
 * support ticket.
 *
 * Closes #123 ([I] ux: error handling — what happens if the cloud
 * rejects the credentials).
 */
function ValidationErrorCard({
  id,
  failure,
  onRetry,
  disabled,
}: {
  id: string
  failure: FailureDetail
  onRetry: () => void | Promise<void>
  disabled: boolean
}) {
  const [retrying, setRetrying] = useState(false)
  const [copied, setCopied] = useState(false)

  async function retry() {
    setRetrying(true)
    try {
      await onRetry()
    } finally {
      setRetrying(false)
    }
  }

  async function copyDiagnostic() {
    const blob = JSON.stringify(
      {
        kind: failure.kind,
        summary: failure.summary,
        status: failure.status,
        rawMessage: failure.rawMessage,
      },
      null,
      2,
    )
    await navigator.clipboard.writeText(blob)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <div
      id={id}
      role="alert"
      style={{
        borderRadius: 8,
        border: '1px solid rgba(248,113,113,0.35)',
        background: 'rgba(248,113,113,0.05)',
        padding: '10px 12px',
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
        <AlertCircle size={14} style={{ color: '#F87171', flexShrink: 0, marginTop: 1 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 12, fontWeight: 700, color: '#F87171' }}>{failure.summary}</span>
            {failure.status !== undefined && (
              <span
                style={{
                  fontSize: 9,
                  fontWeight: 700,
                  fontFamily: 'JetBrains Mono, monospace',
                  color: '#F87171',
                  background: 'rgba(248,113,113,0.12)',
                  padding: '1px 6px',
                  borderRadius: 3,
                }}
              >
                HTTP {failure.status}
              </span>
            )}
          </div>
          <p style={{ margin: '4px 0 0', fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.5 }}>
            {failure.hint}
          </p>
          {failure.rawMessage && (
            <pre
              style={{
                margin: '6px 0 0',
                padding: '6px 8px',
                fontSize: 10.5,
                fontFamily: 'JetBrains Mono, monospace',
                color: 'var(--wiz-text-md)',
                background: 'rgba(0,0,0,0.25)',
                borderRadius: 5,
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
              }}
            >
              {failure.rawMessage}
            </pre>
          )}
        </div>
      </div>

      <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
        <button
          type="button"
          onClick={retry}
          disabled={disabled || retrying}
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 5,
            height: 28,
            padding: '0 10px',
            borderRadius: 6,
            border: '1px solid rgba(56,189,248,0.4)',
            background: 'rgba(56,189,248,0.1)',
            color: 'var(--wiz-accent)',
            fontSize: 11,
            fontWeight: 600,
            cursor: disabled || retrying ? 'default' : 'pointer',
            opacity: disabled ? 0.5 : 1,
            fontFamily: 'Inter, sans-serif',
          }}
        >
          {retrying ? <Loader2 size={11} className="animate-spin" /> : <RotateCw size={11} />}
          Retry validation
        </button>

        <button
          type="button"
          onClick={copyDiagnostic}
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 5,
            height: 28,
            padding: '0 10px',
            borderRadius: 6,
            border: '1px solid var(--wiz-border)',
            background: 'transparent',
            color: 'var(--wiz-text-md)',
            fontSize: 11,
            fontWeight: 500,
            cursor: 'pointer',
            fontFamily: 'Inter, sans-serif',
          }}
        >
          <Copy size={11} />
          {copied ? 'Copied' : 'Copy diagnostic'}
        </button>

        <span style={{ fontSize: 10, color: 'var(--wiz-text-hint)', marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
          <XCircle size={10} /> Token will not be persisted on our servers either way
        </span>
      </div>
    </div>
  )
}

/* ── SSH keypair section (issue #160) ─────────────────────────────────
 *
 * Two-mode UX:
 *
 *   Mode A — "Generate keypair"
 *     Single button → POST /api/v1/sshkey/generate → catalyst-api emits an
 *     Ed25519 keypair → wizard stores the public key + downloads the
 *     private key as `<sovereign-fqdn>.pem` (or `catalyst.pem` if the FQDN
 *     isn't filled in yet) → renders the public key inline (read-only) and
 *     a one-time "save your private key now" warning.
 *
 *   Mode B — "Paste existing public key"
 *     Textarea, RFC validation regex (isValidSSHPublicKey).
 *     Sets store.sshPublicKey directly; clears any private blob held over
 *     from a Mode-A generation in the same session.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   • #4 (never hardcode): the FQDN sent to the backend is computed from
 *     wizard state, never a literal "omantel.omani.works".
 *   • #10 (credential hygiene): the private key is held only long enough
 *     to trigger the download. partialize() in the store strips it from
 *     the persisted payload regardless.
 */
function SSHKeySection() {
  const store = useWizardStore()
  const initialMode: 'generate' | 'paste' = store.sshPublicKey && !store.sshKeyGeneratedThisSession ? 'paste' : 'generate'
  const [mode, setMode] = useState<'generate' | 'paste'>(initialMode)
  const [generating, setGenerating] = useState(false)
  const [pasted, setPasted] = useState(store.sshPublicKey)
  const [error, setError] = useState<string | null>(null)

  const sovereignFQDN = resolveSovereignDomain(store)
  const downloadFilename = `${sovereignFQDN || 'catalyst'}.pem`

  async function generate() {
    setError(null)
    setGenerating(true)
    let res: Response
    try {
      res = await fetch(`${API_BASE}/v1/sshkey/generate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ fqdn: sovereignFQDN }),
      })
    } catch (err) {
      setError(`Could not reach the keypair generator (${String(err)}). Reload and retry.`)
      setGenerating(false)
      return
    }
    if (!res.ok) {
      setError(`Generator returned HTTP ${res.status}. Reload and retry.`)
      setGenerating(false)
      return
    }
    let data: { publicKey?: string; privateKey?: string; fingerprint?: string } | null = null
    try {
      data = (await res.json()) as { publicKey?: string; privateKey?: string; fingerprint?: string }
    } catch (err) {
      setError(`Generator response was malformed (${String(err)}).`)
      setGenerating(false)
      return
    }
    if (!data?.publicKey || !data?.privateKey) {
      setError('Generator response was missing publicKey or privateKey.')
      setGenerating(false)
      return
    }
    store.setSshGenerated(data.publicKey, data.privateKey, data.fingerprint ?? '')
    triggerPrivateKeyDownload(data.privateKey, downloadFilename)
    setGenerating(false)
  }

  function handlePaste(value: string) {
    setPasted(value)
    setError(null)
    if (value.trim() === '') {
      // Clear store immediately on empty so the wizard can't keep an old key in scope.
      store.setSshPublicKey('')
      return
    }
    if (!isValidSSHPublicKey(value)) {
      // Don't write into the store yet — leave the previous valid value (if any)
      // intact, but surface the error so the user can correct it.
      setError(
        'Public key did not parse. Paste a single line beginning with ' +
          '"ssh-ed25519", "ssh-rsa", or "ecdsa-sha2-nistp256/384/521" followed by a base64 blob.',
      )
      return
    }
    store.setSshPublicKey(value.trim())
  }

  function reDownload() {
    if (!store.sshPrivateKeyOnce) return
    triggerPrivateKeyDownload(store.sshPrivateKeyOnce, downloadFilename)
  }

  const sshKeyOk = isValidSSHPublicKey(store.sshPublicKey)

  return (
    <div
      data-testid="ssh-key-section"
      style={{
        borderRadius: 12,
        overflow: 'hidden',
        border: sshKeyOk ? '1.5px solid rgba(74,222,128,0.3)' : '1.5px solid var(--wiz-border-sub)',
        background: sshKeyOk ? 'rgba(74,222,128,0.03)' : 'var(--wiz-bg-xs)',
        transition: 'all 0.2s',
      }}
    >
      {/* Header */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 14px',
          borderBottom: '1px solid var(--wiz-border-sub)',
        }}
      >
        <KeyRound size={14} style={{ color: 'var(--wiz-text-md)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>
              SSH public key
            </span>
            {sshKeyOk && <CheckCircle2 size={13} style={{ color: '#4ADE80' }} />}
            <span
              style={{
                fontSize: 9,
                fontWeight: 700,
                fontFamily: 'JetBrains Mono, monospace',
                color: '#FBBF24',
                background: 'rgba(251,191,36,0.12)',
                padding: '1px 6px',
                borderRadius: 3,
              }}
            >
              REQUIRED
            </span>
          </div>
          <div style={{ fontSize: 11, color: 'var(--wiz-text-sub)', marginTop: 2 }}>
            Break-glass access if k3s + Flux reconcile fails post-bootstrap. The cloud attaches this to every server.
          </div>
        </div>
      </div>

      {/* Mode tabs */}
      <div
        role="tablist"
        aria-label="SSH key input mode"
        style={{ display: 'flex', borderBottom: '1px solid var(--wiz-border-sub)' }}
      >
        <ModeTab
          testid="ssh-mode-generate"
          active={mode === 'generate'}
          onClick={() => setMode('generate')}
          icon={<Sparkles size={12} />}
          label="Generate keypair"
          hint="Recommended"
        />
        <ModeTab
          testid="ssh-mode-paste"
          active={mode === 'paste'}
          onClick={() => setMode('paste')}
          icon={<ClipboardPaste size={12} />}
          label="Paste existing public key"
          hint="Advanced"
        />
      </div>

      {/* Mode body */}
      <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        {mode === 'generate' && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {!store.sshKeyGeneratedThisSession && (
              <button
                type="button"
                data-testid="ssh-generate-button"
                onClick={generate}
                disabled={generating}
                style={{
                  alignSelf: 'flex-start',
                  height: 36,
                  padding: '0 14px',
                  borderRadius: 7,
                  border: '1.5px solid rgba(56,189,248,0.4)',
                  background: 'rgba(56,189,248,0.1)',
                  color: 'var(--wiz-accent)',
                  fontSize: 12,
                  fontWeight: 600,
                  cursor: generating ? 'default' : 'pointer',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 6,
                  fontFamily: 'Inter, sans-serif',
                }}
              >
                {generating ? <Loader2 size={12} className="animate-spin" /> : <Sparkles size={12} />}
                {generating ? 'Generating Ed25519 keypair…' : 'Generate Ed25519 keypair'}
              </button>
            )}

            {store.sshKeyGeneratedThisSession && (
              <>
                {/* One-time warning banner — closes the part of the spec
                    that says "Private key shown once. Save it now or you
                    lose access." */}
                <div
                  data-testid="ssh-private-key-warning"
                  role="alert"
                  style={{
                    borderRadius: 8,
                    border: '1px solid rgba(251,191,36,0.45)',
                    background: 'rgba(251,191,36,0.07)',
                    padding: '10px 12px',
                    display: 'flex',
                    alignItems: 'flex-start',
                    gap: 8,
                  }}
                >
                  <ShieldAlert size={14} style={{ color: '#FBBF24', flexShrink: 0, marginTop: 1 }} />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ fontSize: 12, fontWeight: 700, color: '#FBBF24' }}>
                      Private key shown once. Save it now or you lose access.
                    </div>
                    <p style={{ margin: '4px 0 0', fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.55 }}>
                      Your browser downloaded <code>{downloadFilename}</code>. Move it to <code>~/.ssh/</code> and run{' '}
                      <code>chmod 600 ~/.ssh/{downloadFilename}</code>. The catalyst-api has discarded its copy.
                    </p>
                    <div style={{ display: 'flex', gap: 6, marginTop: 8, flexWrap: 'wrap' }}>
                      <button
                        type="button"
                        data-testid="ssh-redownload"
                        onClick={reDownload}
                        disabled={!store.sshPrivateKeyOnce}
                        style={{
                          height: 26,
                          padding: '0 9px',
                          borderRadius: 6,
                          border: '1px solid rgba(251,191,36,0.45)',
                          background: 'rgba(251,191,36,0.1)',
                          color: '#FBBF24',
                          fontSize: 11,
                          fontWeight: 600,
                          cursor: store.sshPrivateKeyOnce ? 'pointer' : 'default',
                          display: 'inline-flex',
                          alignItems: 'center',
                          gap: 4,
                          fontFamily: 'Inter, sans-serif',
                          opacity: store.sshPrivateKeyOnce ? 1 : 0.5,
                        }}
                      >
                        <Download size={11} />
                        {store.sshPrivateKeyOnce ? 'Download again' : 'Already downloaded'}
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          // Re-generate path — discards the prior public key
                          // and emits a fresh keypair. Useful if the user
                          // accidentally lost the .pem and wants a new one
                          // without leaving the wizard.
                          store.clearSshPrivateKey()
                          generate()
                        }}
                        style={{
                          height: 26,
                          padding: '0 9px',
                          borderRadius: 6,
                          border: '1px solid var(--wiz-border)',
                          background: 'transparent',
                          color: 'var(--wiz-text-md)',
                          fontSize: 11,
                          fontWeight: 500,
                          cursor: 'pointer',
                          display: 'inline-flex',
                          alignItems: 'center',
                          gap: 4,
                          fontFamily: 'Inter, sans-serif',
                        }}
                      >
                        <RotateCw size={11} /> Re-generate
                      </button>
                    </div>
                  </div>
                </div>

                {/* Read-only public-key preview */}
                <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
                  <span style={{ fontSize: 11, fontWeight: 500, color: 'var(--wiz-text-sub)' }}>
                    Public key — embedded in tofu apply
                  </span>
                  <textarea
                    data-testid="ssh-public-key-preview"
                    value={store.sshPublicKey}
                    readOnly
                    rows={3}
                    style={{
                      width: '100%',
                      borderRadius: 7,
                      border: '1.5px solid var(--wiz-border)',
                      background: 'var(--wiz-bg-input)',
                      color: 'var(--wiz-text-hi)',
                      fontSize: 11.5,
                      padding: '8px 10px',
                      outline: 'none',
                      fontFamily: 'JetBrains Mono, monospace',
                      resize: 'vertical',
                      wordBreak: 'break-all',
                    }}
                  />
                  {store.sshFingerprint && (
                    <span
                      data-testid="ssh-fingerprint"
                      style={{ fontSize: 10.5, color: 'var(--wiz-text-hint)', fontFamily: 'JetBrains Mono, monospace' }}
                    >
                      Fingerprint: {store.sshFingerprint}
                    </span>
                  )}
                </div>
              </>
            )}

            {!store.sshKeyGeneratedThisSession && (
              <p style={{ margin: 0, fontSize: 11, color: 'var(--wiz-text-sub)', lineHeight: 1.55 }}>
                The catalyst-api will emit a fresh Ed25519 keypair, ship the private half to your browser as a one-time
                download, and forget it. The public half is stored only as long as the wizard stays open.
              </p>
            )}
          </div>
        )}

        {mode === 'paste' && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 11, fontWeight: 500, color: 'var(--wiz-text-sub)' }}>
              Public key (single line, OpenSSH authorized_keys format)
            </span>
            <textarea
              data-testid="ssh-paste-input"
              value={pasted}
              onChange={(e) => handlePaste(e.target.value)}
              placeholder="ssh-ed25519 AAAA… user@host"
              rows={3}
              aria-invalid={error !== null}
              style={{
                width: '100%',
                borderRadius: 7,
                border: error
                  ? '1.5px solid rgba(248,113,113,0.5)'
                  : sshKeyOk
                    ? '1.5px solid rgba(74,222,128,0.45)'
                    : '1.5px solid var(--wiz-border)',
                background: 'var(--wiz-bg-input)',
                color: 'var(--wiz-text-hi)',
                fontSize: 11.5,
                padding: '8px 10px',
                outline: 'none',
                fontFamily: 'JetBrains Mono, monospace',
                resize: 'vertical',
              }}
            />
            {error && (
              <div
                data-testid="ssh-paste-error"
                role="alert"
                style={{
                  display: 'flex',
                  gap: 6,
                  fontSize: 11,
                  color: '#F87171',
                  alignItems: 'flex-start',
                  lineHeight: 1.45,
                }}
              >
                <AlertCircle size={12} style={{ flexShrink: 0, marginTop: 1 }} />
                <span>{error}</span>
              </div>
            )}
            {!error && sshKeyOk && (
              <div style={{ display: 'flex', gap: 6, fontSize: 11, color: '#4ADE80', alignItems: 'center' }}>
                <CheckCircle2 size={12} /> Looks like a valid OpenSSH public key.
              </div>
            )}
            <span style={{ fontSize: 10.5, color: 'var(--wiz-text-hint)', lineHeight: 1.5 }}>
              Accepted algorithms: <code>ssh-ed25519</code>, <code>ssh-rsa</code>,{' '}
              <code>ecdsa-sha2-nistp256/384/521</code>. The provisioner runs the same regex server-side — empty or
              malformed keys are rejected (security floor — never an empty SSH key).
            </span>
          </div>
        )}
      </div>
    </div>
  )
}

function ModeTab({
  testid,
  active,
  onClick,
  icon,
  label,
  hint,
}: {
  testid: string
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
  hint: string
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      data-testid={testid}
      onClick={onClick}
      style={{
        flex: 1,
        height: 38,
        background: active ? 'rgba(56,189,248,0.07)' : 'transparent',
        border: 'none',
        borderBottom: active ? '2px solid var(--wiz-accent)' : '2px solid transparent',
        color: active ? 'var(--wiz-text-hi)' : 'var(--wiz-text-sub)',
        fontSize: 12,
        fontWeight: active ? 600 : 500,
        cursor: 'pointer',
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 6,
        fontFamily: 'Inter, sans-serif',
        transition: 'all 0.15s',
      }}
    >
      {icon}
      {label}
      <span style={{ fontSize: 10, color: 'var(--wiz-text-hint)', fontWeight: 500 }}>· {hint}</span>
    </button>
  )
}

/**
 * triggerPrivateKeyDownload — creates a one-shot Blob URL for the private
 * key blob and clicks a hidden anchor to make the browser save it. The URL
 * is revoked immediately afterwards so the blob is GC'd as soon as the
 * download dialog has copied the bytes.
 *
 * Per credential hygiene (#10), this is the ONLY moment the private key
 * touches the DOM; nothing renders it inline.
 */
function triggerPrivateKeyDownload(privateKey: string, filename: string) {
  // jsdom in vitest doesn't implement Blob URLs the same way browsers do;
  // guard so the test environment doesn't crash when the helper runs.
  if (typeof window === 'undefined' || typeof document === 'undefined') return
  try {
    const blob = new Blob([privateKey], { type: 'application/x-pem-file' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.style.display = 'none'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  } catch {
    // Swallow — the user can still click "Download again" from the warning
    // banner, which calls back into this helper. We log nothing so the
    // private key never appears in console.error stack traces.
  }
}
