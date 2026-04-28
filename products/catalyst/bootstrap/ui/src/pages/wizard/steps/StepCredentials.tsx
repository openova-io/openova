import { useState } from 'react'
import { Eye, EyeOff, CheckCircle2, XCircle, Loader2, ExternalLink, AlertCircle, RotateCw, Copy } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { API_BASE } from '@/shared/config/urls'
import { StepShell, useStepNav } from './_shared'

type ValidationState = 'idle' | 'validating' | 'valid' | 'invalid'

/**
 * Specific failure mode reported by the validator. Used to render a
 * targeted error UI per docs/INVIOLABLE-PRINCIPLES.md #2 ("never compromise
 * from quality") — generic "rejected" messages cost the user a support
 * ticket; a specific reason lets them self-recover.
 *
 * Closes #123 ([I] ux: error handling — what happens if Hetzner API rejects token).
 */
type FailureKind =
  | 'rejected'      // backend confirmed token is wrong (401/403 path)
  | 'too-short'     // client- or server-side length validation
  | 'unreachable'   // could not reach the cloud provider's API (503 path)
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
    summary: 'Token rejected by Hetzner Cloud',
    hint:
      'The token authenticated but does not have the permissions OpenTofu needs. ' +
      'Generate a new token in Hetzner Cloud Console → Security → API Tokens, ' +
      'pick the same project, and select "Read & Write" — never "Read only".',
  },
  'too-short': {
    summary: 'Token is too short',
    hint:
      'Hetzner API tokens are at least 64 characters long. ' +
      'Make sure you copied the full token from the Hetzner Cloud Console — ' +
      "the token is only shown once at creation time, so if you've lost it, generate a new one.",
  },
  unreachable: {
    summary: 'Could not reach Hetzner Cloud',
    hint:
      'The catalyst-api could not establish a TLS connection to api.hetzner.cloud. ' +
      'This is usually transient — wait a few seconds and retry. ' +
      'If it persists, check the Hetzner status page at status.hetzner.com.',
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

const PROVIDER_NAMES: Record<CloudProvider, string> = {
  hetzner: 'Hetzner Cloud',
  huawei:  'Huawei Cloud',
  oci:     'Oracle Cloud (OCI)',
  aws:     'Amazon Web Services',
  azure:   'Microsoft Azure',
}

const PROVIDER_TOKEN_HINT: Record<CloudProvider, string> = {
  hetzner: 'Read & Write API token (64+ chars)',
  huawei:  'Access Key ID — format: AK + secret',
  oci:     'API private key or session token',
  aws:     'Access Key ID + secret (paste as JSON or combined)',
  azure:   'Service principal client ID + secret',
}

/* ── Token section — one per provider ───────────────────────────────── */
function TokenSection({
  provider,
  regionIndices,
}: {
  provider: CloudProvider
  regionIndices: number[]
}) {
  const store = useWizardStore()
  const [token, setToken] = useState(store.providerTokens[provider] ?? '')
  const [show, setShow] = useState(false)
  const [state, setState] = useState<ValidationState>(
    store.providerValidated[provider] ? 'valid' : 'idle'
  )
  /**
   * Specific failure detail — populated when validate() determines the
   * token is invalid OR validation itself failed. Displays a targeted
   * error card with remediation steps + retry button.
   */
  const [failure, setFailure] = useState<FailureDetail | null>(null)
  const [focused, setFocused] = useState(false)

  function handleChange(v: string) {
    setToken(v)
    setFailure(null)
    setState('idle')
    store.setProviderValidated(provider, false)
  }

  /**
   * validate() — POSTs to /api/v1/credentials/validate and surfaces the
   * exact failure mode the backend reports. Replaces the previous
   * silently-swallow-on-error behaviour: per docs/INVIOLABLE-PRINCIPLES.md
   * #1, ANY validation error is a hard "invalid" — the user explicitly
   * needs to know what went wrong, not be told everything is fine when
   * the backend returned 503.
   *
   * Closes #123.
   */
  async function validate() {
    if (token.trim().length < 64) {
      setFailure({
        kind: 'too-short',
        summary: FAILURE_HINTS['too-short'].summary,
        hint: FAILURE_HINTS['too-short'].hint,
      })
      setState('invalid')
      return
    }
    setState('validating')
    setFailure(null)
    store.setProviderToken(provider, token)

    let res: Response
    try {
      res = await fetch(`${API_BASE}/v1/credentials/validate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, provider }),
      })
    } catch (err) {
      setFailure({
        kind: 'network',
        summary: FAILURE_HINTS.network.summary,
        hint: FAILURE_HINTS.network.hint,
        rawMessage: String(err),
      })
      setState('invalid')
      store.setProviderValidated(provider, false)
      return
    }

    let data: { valid?: boolean; message?: string } | null = null
    try {
      data = (await res.json()) as { valid?: boolean; message?: string }
    } catch (err) {
      setFailure({
        kind: 'parse',
        summary: FAILURE_HINTS.parse.summary,
        hint: FAILURE_HINTS.parse.hint,
        rawMessage: String(err),
        status: res.status,
      })
      setState('invalid')
      store.setProviderValidated(provider, false)
      return
    }

    // Backend wire format (handler/credentials.go):
    //   200 + valid=true   → token good, set state=valid
    //   200 + valid=false  → token rejected by Hetzner
    //   400 + valid=false  → too-short (server-side check)
    //   503 + valid=false  → Hetzner API unreachable
    //   anything else      → unhandled
    if (res.ok && data?.valid === true) {
      setState('valid')
      store.setProviderValidated(provider, true)
      if (provider === 'hetzner') {
        store.setHetznerToken(token)
        store.setCredentialValidated(true)
      }
      return
    }

    let kind: FailureKind = 'rejected'
    if (res.status === 503) kind = 'unreachable'
    else if (res.status === 400) kind = 'too-short'
    else if (!res.ok) kind = 'http'

    setFailure({
      kind,
      summary: FAILURE_HINTS[kind].summary,
      hint: FAILURE_HINTS[kind].hint,
      rawMessage: data?.message,
      status: res.status,
    })
    setState('invalid')
    store.setProviderValidated(provider, false)
  }

  function skipDemo() {
    const demoToken = `demo-mode-${provider}-` + 'x'.repeat(50)
    setToken(demoToken)
    setState('valid')
    store.setProviderToken(provider, demoToken)
    store.setProviderValidated(provider, true)
    if (provider === 'hetzner') {
      store.setHetznerToken(demoToken)
      store.setCredentialValidated(true)
    }
  }

  return (
    <div style={{
      borderRadius: 12, overflow: 'hidden',
      border: state === 'valid'
        ? '1.5px solid rgba(74,222,128,0.3)'
        : '1.5px solid var(--wiz-border-sub)',
      background: state === 'valid' ? 'rgba(74,222,128,0.03)' : 'var(--wiz-bg-xs)',
      transition: 'all 0.2s',
    }}>
      {/* Provider header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', borderBottom: '1px solid var(--wiz-border-sub)' }}>
        <div style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>{PROVIDER_NAMES[provider]}</span>
            {state === 'valid' && <CheckCircle2 size={13} style={{ color: '#4ADE80' }} />}
          </div>
          <div style={{ fontSize: 11, color: 'var(--wiz-text-sub)', marginTop: 2 }}>
            Region{regionIndices.length > 1 ? 's' : ''} {regionIndices.map(i => i + 1).join(', ')}
          </div>
        </div>
      </div>

      {/* Token form */}
      <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span style={{ fontSize: 11, fontWeight: 500, color: 'var(--wiz-text-sub)' }}>
          API credential · {PROVIDER_TOKEN_HINT[provider]}
        </span>

        {/* Input + validate row */}
        <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <div style={{ flex: 1, position: 'relative' }}>
            <input
              type={show ? 'text' : 'password'}
              value={token}
              onFocus={() => setFocused(true)}
              onBlur={() => setFocused(false)}
              onChange={e => handleChange(e.target.value)}
              placeholder="Paste your credential here…"
              aria-invalid={state === 'invalid'}
              aria-describedby={failure ? `${provider}-validation-err` : undefined}
              style={{
                width: '100%', height: 38, borderRadius: 7,
                border: `1.5px solid ${failure ? 'rgba(248,113,113,0.5)' : focused ? 'rgba(56,189,248,0.45)' : 'var(--wiz-border)'}`,
                background: 'var(--wiz-bg-input)',
                color: 'var(--wiz-text-hi)', fontSize: 13, paddingLeft: 10, paddingRight: 38,
                outline: 'none', fontFamily: 'Inter, monospace',
                boxShadow: focused ? `0 0 0 3px ${failure ? 'rgba(248,113,113,0.07)' : 'rgba(56,189,248,0.07)'}` : 'none',
                transition: 'all 0.15s',
              }}
            />
            <button
              type="button"
              onClick={() => setShow(v => !v)}
              style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', color: 'var(--wiz-text-sub)', cursor: 'pointer', padding: 0, display: 'flex' }}
            >
              {show ? <EyeOff size={14} /> : <Eye size={14} />}
            </button>
          </div>

          {/* Validate button */}
          <button
            type="button"
            onClick={validate}
            disabled={state === 'validating' || state === 'valid'}
            style={{
              height: 38, padding: '0 14px', borderRadius: 7, flexShrink: 0,
              border: state === 'valid' ? '1.5px solid rgba(74,222,128,0.35)' : '1.5px solid var(--wiz-border)',
              background: state === 'valid' ? 'rgba(74,222,128,0.07)' : 'var(--wiz-bg-input)',
              color: state === 'valid' ? '#4ADE80' : 'var(--wiz-text-sub)',
              fontSize: 12, fontWeight: 600, cursor: state === 'validating' || state === 'valid' ? 'default' : 'pointer',
              display: 'flex', alignItems: 'center', gap: 5,
              fontFamily: 'Inter, sans-serif', transition: 'all 0.15s',
              whiteSpace: 'nowrap',
            }}
          >
            {state === 'validating' && <Loader2 size={12} className="animate-spin" />}
            {state === 'valid'      && <CheckCircle2 size={12} />}
            {state === 'idle'       ? 'Validate' :
             state === 'invalid'    ? 'Retry' :
             state === 'validating' ? 'Checking…' : 'Validated'}
          </button>
        </div>

        {/* Feedback */}
        {state === 'valid' && (
          <div style={{ display: 'flex', gap: 6, fontSize: 11, color: '#4ADE80', alignItems: 'center' }}>
            <CheckCircle2 size={12} /> Token validated — access confirmed
          </div>
        )}
        {failure && state === 'invalid' && (
          <ValidationErrorCard
            id={`${provider}-validation-err`}
            failure={failure}
            onRetry={validate}
            disabled={state !== 'invalid' || token.trim().length === 0}
          />
        )}

        {/* Hetzner project ID — required for resource attribution. The Hetzner Cloud API token
            is project-scoped, but we still ask for the project ID explicitly so the wizard can
            display it back during the Review step and so the provisioner can write it into
            tofu.auto.tfvars.json before terraform apply runs. */}
        {provider === 'hetzner' && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5, marginTop: 6, paddingTop: 10, borderTop: '1px dashed var(--wiz-border-sub)' }}>
            <span style={{ fontSize: 11, fontWeight: 500, color: 'var(--wiz-text-sub)' }}>
              Hetzner Project ID <span style={{ color: 'var(--wiz-text-hint)', marginLeft: 6 }}>required · used for resource attribution + audit log</span>
            </span>
            <input
              type="text"
              value={store.hetznerProjectId}
              onChange={e => store.setHetznerProjectId(e.target.value)}
              placeholder="e.g. proj_abc123 (find it in Hetzner Cloud Console → Project settings)"
              style={{
                height: 36, borderRadius: 7,
                border: '1.5px solid var(--wiz-border)',
                background: 'var(--wiz-bg-input)', color: 'var(--wiz-text-hi)',
                fontSize: 12, padding: '0 10px', outline: 'none',
                fontFamily: 'JetBrains Mono, monospace',
              }}
            />
          </div>
        )}

        {/* Demo bypass */}
        {state !== 'valid' && (
          <button
            type="button"
            onClick={skipDemo}
            style={{ alignSelf: 'flex-start', fontSize: 11, fontWeight: 500, color: 'var(--wiz-accent)', background: 'none', border: 'none', cursor: 'pointer', padding: 0, textDecoration: 'underline', textUnderlineOffset: 3, fontFamily: 'Inter, sans-serif' }}
          >
            No token yet? Skip — explore in demo mode →
          </button>
        )}
      </div>
    </div>
  )
}

export function StepCredentials() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const bp = useBreakpoint()

  const uniqueProviders = [...new Set(Object.values(store.regionProviders))] as CloudProvider[]
  const providers: CloudProvider[] = uniqueProviders.length > 0
    ? uniqueProviders
    : store.provider ? [store.provider] : ['hetzner']

  const allValidated = providers.every(p => store.providerValidated[p])

  const regionIndicesFor = (p: CloudProvider) =>
    Object.entries(store.regionProviders)
      .filter(([, v]) => v === p)
      .map(([k]) => Number(k))

  /* 2-col when 2+ providers AND not mobile */
  const cols = providers.length >= 2 && bp !== 'mobile' ? '1fr 1fr' : '1fr'

  return (
    <StepShell
      title="Cloud credentials"
      description={providers.length > 1
        ? `You selected ${providers.length} different cloud providers. Provide one API credential per provider.`
        : 'Provide a read/write API credential. Credentials are used only during provisioning and never persisted on our servers.'}
      onNext={() => { if (allValidated) next() }}
      onBack={back}
      nextDisabled={!allValidated}
    >
      {/* Credential sections */}
      <div style={{ display: 'grid', gridTemplateColumns: cols, gap: 14 }}>
        {providers.map(p => (
          <TokenSection
            key={p}
            provider={p}
            regionIndices={regionIndicesFor(p).length > 0 ? regionIndicesFor(p) : [0]}
          />
        ))}
      </div>

      {/* How-to for Hetzner */}
      {providers.includes('hetzner') && !allValidated && (
        <div style={{ borderRadius: 10, border: '1px solid var(--wiz-border-sub)', background: 'var(--wiz-bg-xs)', padding: '12px 14px' }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'var(--wiz-text-sub)', margin: '0 0 8px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>
            How to create a Hetzner API token
          </p>
          <ol style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 4 }}>
            {[
              'Open Hetzner Cloud Console',
              'Select your project',
              'Go to Security \u2192 API Tokens',
              'Click Generate API Token',
              'Choose Read & Write permissions',
              'Copy the token \u2014 shown only once',
            ].map((s, i) => (
              <li key={i} style={{ display: 'flex', gap: 8, fontSize: 11, color: 'var(--wiz-text-sub)', alignItems: 'flex-start' }}>
                <span style={{ width: 15, height: 15, borderRadius: '50%', background: 'var(--wiz-bg-input)', border: '1px solid var(--wiz-border-sub)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 8, fontWeight: 700, flexShrink: 0 }}>{i + 1}</span>
                {s}
              </li>
            ))}
          </ol>
          <a href="https://console.hetzner.cloud" target="_blank" rel="noopener noreferrer" style={{ display: 'inline-flex', alignItems: 'center', gap: 4, marginTop: 8, fontSize: 11, color: 'var(--wiz-accent)', textDecoration: 'none' }}>
            Open Hetzner Cloud Console <ExternalLink size={10} />
          </a>
        </div>
      )}
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
 * Closes #123 ([I] ux: error handling — what happens if Hetzner API
 * rejects token).
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
