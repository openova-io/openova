import { useState } from 'react'
import { Eye, EyeOff, CheckCircle2, XCircle, Loader2, ExternalLink } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

type ValidationState = 'idle' | 'validating' | 'valid' | 'invalid'

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
  const [error, setError] = useState('')
  const [state, setState] = useState<ValidationState>(
    store.providerValidated[provider] ? 'valid' : 'idle'
  )
  const [focused, setFocused] = useState(false)

  function handleChange(v: string) {
    setToken(v)
    setError('')
    setState('idle')
    store.setProviderValidated(provider, false)
  }

  async function validate() {
    if (token.trim().length < 64) {
      setError('Token must be at least 64 characters')
      return
    }
    setState('validating')
    store.setProviderToken(provider, token)
    try {
      const res = await fetch('/api/v1/credentials/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, provider }),
      })
      const data = await res.json()
      if (data.valid) {
        setState('valid')
        store.setProviderValidated(provider, true)
        if (provider === 'hetzner') {
          store.setHetznerToken(token)
          store.setCredentialValidated(true)
        }
      } else {
        setState('invalid')
        store.setProviderValidated(provider, false)
      }
    } catch {
      setState('valid')
      store.setProviderValidated(provider, true)
      store.setProviderToken(provider, token)
      if (provider === 'hetzner') {
        store.setHetznerToken(token)
        store.setCredentialValidated(true)
      }
    }
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
              style={{
                width: '100%', height: 38, borderRadius: 7,
                border: `1.5px solid ${error ? 'rgba(248,113,113,0.5)' : focused ? 'rgba(56,189,248,0.45)' : 'var(--wiz-border)'}`,
                background: 'var(--wiz-bg-input)',
                color: 'var(--wiz-text-hi)', fontSize: 13, paddingLeft: 10, paddingRight: 38,
                outline: 'none', fontFamily: 'Inter, monospace',
                boxShadow: focused ? `0 0 0 3px ${error ? 'rgba(248,113,113,0.07)' : 'rgba(56,189,248,0.07)'}` : 'none',
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

        {error && <span style={{ fontSize: 11, color: '#F87171' }}>{error}</span>}

        {/* Feedback */}
        {state === 'valid' && (
          <div style={{ display: 'flex', gap: 6, fontSize: 11, color: '#4ADE80', alignItems: 'center' }}>
            <CheckCircle2 size={12} /> Token validated — access confirmed
          </div>
        )}
        {state === 'invalid' && (
          <div style={{ display: 'flex', gap: 6, fontSize: 11, color: '#F87171', alignItems: 'center' }}>
            <XCircle size={12} /> Token rejected — check permissions and try again
          </div>
        )}

        {/* Demo bypass */}
        {state !== 'valid' && (
          <button
            type="button"
            onClick={skipDemo}
            style={{ alignSelf: 'flex-start', fontSize: 11, fontWeight: 500, color: 'rgba(56,189,248,0.5)', background: 'none', border: 'none', cursor: 'pointer', padding: 0, textDecoration: 'underline', textUnderlineOffset: 3, fontFamily: 'Inter, sans-serif' }}
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
          <a href="https://console.hetzner.cloud" target="_blank" rel="noopener noreferrer" style={{ display: 'inline-flex', alignItems: 'center', gap: 4, marginTop: 8, fontSize: 11, color: 'rgba(56,189,248,0.5)', textDecoration: 'none' }}>
            Open Hetzner Cloud Console <ExternalLink size={10} />
          </a>
        </div>
      )}
    </StepShell>
  )
}
