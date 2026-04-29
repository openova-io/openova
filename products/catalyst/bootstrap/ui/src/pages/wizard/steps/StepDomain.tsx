/**
 * StepDomain — sovereign-domain capture, three-mode (pool / byo-manual /
 * byo-api), plus the admin-contact email. Closes #169 ([I] wizard:
 * StepDomain — Bring Your Own Domain).
 *
 * The wizard's previous "domain" UX lived as a section inside StepOrg. With
 * BYO bringing two delegation flows (manual NS edit, registrar-API NS flip)
 * the section grew past what fits beneath the org-profile fields, so #169
 * promotes it to its own step.
 *
 * The admin-contact email also lives on this step. It used to live on
 * StepOrg next to the org name, which made the opening screen feel like a
 * sign-up form and asked for personal contact data before the operator
 * had any idea what they were configuring. Pairing the email with the
 * Sovereign FQDN matches the way it's actually used downstream — Let's
 * Encrypt registration, deployment-completion notifications, and the
 * console's "platform owner" badge are all keyed off this address.
 *
 * All three modes end at the SAME outcome: a per-Sovereign zone exists in
 * OpenOva PowerDNS so cert-manager DNS-01 + the sovereign LB can resolve.
 *
 *   pool        ─ existing PDM /reserve flow (unchanged behaviour vs. #163)
 *   byo-manual  ─ customer types their domain; wizard shows the OpenOva
 *                 nameservers and tells them to paste those into their
 *                 registrar's UI; catalyst-api polls until propagation.
 *   byo-api     ─ customer types their domain + picks registrar + pastes
 *                 token; wizard validates the token via PDM /validate
 *                 (read-only) BEFORE letting them continue.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4  no hardcoded URLs — API_BASE drives all fetch calls; nameservers
 *       come from OPENOVA_NAMESERVERS so a future runtime endpoint can
 *       replace the constant without touching this file
 *   #10 credential hygiene — registrarToken is in-memory only; the store's
 *       partialize() omits it from localStorage. Same posture as the SSH
 *       break-glass private key.
 */

import { useState } from 'react'
import {
  AlertCircle,
  CheckCircle2,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Loader2,
  RefreshCw,
  Sparkles,
} from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  isValidDomain,
  isValidSubdomain,
  OPENOVA_NAMESERVERS,
  REGISTRAR_OPTIONS,
  resolveSovereignDomain,
  SOVEREIGN_POOL_DOMAINS,
  type DomainMode,
  type RegistrarType,
} from '@/entities/deployment/model'
import { useSubdomainAvailability } from '@/shared/lib/useSubdomainAvailability'
import { API_BASE } from '@/shared/config/urls'
import { StepShell, useStepNav } from './_shared'

const MODE_OPTIONS: { id: DomainMode; label: string; sub: string }[] = [
  { id: 'pool',       label: 'OpenOva pool domain',  sub: 'recommended for first deployment' },
  { id: 'byo-manual', label: "I'll change NS at my registrar manually",
                                                     sub: 'we show you the records to paste' },
  { id: 'byo-api',    label: 'Let OpenOva flip NS via my registrar API',
                                                     sub: 'paste a token, we do the rest' },
]

export function StepDomain() {
  const store = useWizardStore()
  const { next } = useStepNav()

  const pool = SOVEREIGN_POOL_DOMAINS.find(p => p.id === store.sovereignPoolDomain) ?? SOVEREIGN_POOL_DOMAINS[0]!
  const availability = useSubdomainAvailability(
    store.sovereignDomainMode === 'pool' ? store.sovereignSubdomain : '',
    pool.domain,
  )

  const resolved = resolveSovereignDomain(store)
  const nextDisabled = computeNextDisabled(store, availability.status)

  return (
    <StepShell
      title="Where will your Sovereign live in DNS?"
      description="Pool gives you a working URL in seconds. BYO lets you keep the domain you already own — pick the manual flow if your registrar isn't on the API list, or the API flow if you'd rather we flip the NS records for you."
      onNext={next}
      nextDisabled={nextDisabled}
    >
      {resolved && (
        <div
          data-testid="domain-preview"
          style={{
            display: 'inline-flex', gap: 8, alignItems: 'center',
            padding: '6px 10px', borderRadius: 8,
            border: '1px solid rgba(56,189,248,0.25)',
            background: 'rgba(56,189,248,0.06)',
            alignSelf: 'flex-start',
          }}
        >
          <span style={{ fontSize: 11, color: 'var(--wiz-text-sub)' }}>Sovereign URL:</span>
          <code style={{ fontSize: 12, color: '#38BDF8', fontFamily: 'JetBrains Mono, monospace' }}>
            console.{resolved}
          </code>
        </div>
      )}

      <fieldset style={{ border: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <legend style={{ fontSize: 12, fontWeight: 500, color: 'var(--wiz-text-lo)', marginBottom: 4 }}>
          Choose how to set up your domain <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>required</span>
        </legend>
        {MODE_OPTIONS.map(opt => (
          <ModeCard
            key={opt.id}
            id={opt.id}
            label={opt.label}
            sub={opt.sub}
            active={store.sovereignDomainMode === opt.id}
            onSelect={() => store.setSovereignDomainMode(opt.id)}
          />
        ))}
      </fieldset>

      {store.sovereignDomainMode === 'pool' && <PoolModeBody availability={availability} />}
      {store.sovereignDomainMode === 'byo-manual' && <ByoManualBody />}
      {store.sovereignDomainMode === 'byo-api' && <ByoApiBody />}

      <AdminEmailField />
    </StepShell>
  )
}

function computeNextDisabled(
  s: ReturnType<typeof useWizardStore.getState>,
  availabilityStatus: import('@/shared/lib/useSubdomainAvailability').AvailabilityStatus,
): boolean {
  // Admin email is required regardless of which domain mode the operator
  // picked. cert-manager registers it as the Let's Encrypt account email,
  // and the catalyst-api uses it for the deployment-completion notification.
  if (!isValidAdminEmail(s.orgEmail)) return true
  if (s.sovereignDomainMode === 'pool') {
    if (!s.sovereignSubdomain) return true
    return availabilityStatus !== 'available'
  }
  if (s.sovereignDomainMode === 'byo-manual') {
    return !isValidDomain(s.sovereignByoDomain)
  }
  if (s.sovereignDomainMode === 'byo-api') {
    if (!isValidDomain(s.sovereignByoDomain)) return true
    if (!s.registrarType) return true
    if (!s.registrarToken) return true
    return !s.registrarTokenValidated
  }
  return true
}

/**
 * Minimal RFC-5321-ish email validator. Accepts the common case
 * (local@domain.tld) without trying to chase the full RFC. Empty / blank
 * strings fail; the wizard's "default" placeholder ('platform@acme.io')
 * passes on purpose so the operator can proceed without retyping when the
 * pre-filled value matches their setup.
 */
function isValidAdminEmail(value: string): boolean {
  const v = value.trim()
  if (!v) return false
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v)
}

function AdminEmailField() {
  const store = useWizardStore()
  const valid = !store.orgEmail || isValidAdminEmail(store.orgEmail)
  return (
    <fieldset
      style={{
        border: 'none',
        padding: 0,
        margin: 0,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        paddingTop: 4,
      }}
    >
      <legend style={{ fontSize: 12, fontWeight: 500, color: 'var(--wiz-text-lo)', marginBottom: 4 }}>
        Admin contact email <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>required</span>
      </legend>
      <input
        type="email"
        data-testid="admin-email-input"
        placeholder="platform@acme.io"
        value={store.orgEmail}
        onChange={e => store.setOrgEmail(e.target.value)}
        aria-invalid={!valid}
        autoComplete="email"
        spellCheck={false}
        style={inputStyle(valid ? 'idle' : 'error')}
      />
      <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', lineHeight: 1.5 }}>
        Used as the Let's Encrypt account email for TLS issuance, and as the
        deployment-completion notification address. We do not send marketing
        from this address.
      </span>
    </fieldset>
  )
}

function ModeCard({
  id, label, sub, active, onSelect,
}: { id: string; label: string; sub: string; active: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      data-testid={`domain-mode-${id}`}
      onClick={onSelect}
      style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '12px 14px', borderRadius: 10,
        border: `1.5px solid ${active ? 'rgba(56,189,248,0.5)' : 'var(--wiz-border)'}`,
        background: active ? 'rgba(56,189,248,0.08)' : 'var(--wiz-bg-sub)',
        cursor: 'pointer', textAlign: 'left',
        transition: 'all 0.15s', fontFamily: 'Inter, sans-serif',
      }}
    >
      <span
        aria-hidden
        style={{
          width: 16, height: 16, borderRadius: '50%',
          border: `1.5px solid ${active ? '#38BDF8' : 'var(--wiz-border)'}`,
          display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
        }}
      >
        {active && <span style={{ width: 8, height: 8, borderRadius: '50%', background: '#38BDF8' }} />}
      </span>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
        <span style={{ fontSize: 13, fontWeight: active ? 600 : 500, color: active ? '#38BDF8' : 'var(--wiz-text-hi)' }}>
          {label}
        </span>
        <span style={{ fontSize: 11, color: 'var(--wiz-text-sub)' }}>{sub}</span>
      </div>
    </button>
  )
}

function PoolModeBody({ availability }: { availability: import('@/shared/lib/useSubdomainAvailability').AvailabilityResult }) {
  const store = useWizardStore()
  const subdomainValid = !store.sovereignSubdomain || isValidSubdomain(store.sovereignSubdomain)
  const pool = SOVEREIGN_POOL_DOMAINS.find(p => p.id === store.sovereignPoolDomain) ?? SOVEREIGN_POOL_DOMAINS[0]!
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, paddingTop: 10 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
            <span>Subdomain</span>
            <SubdomainAvailabilityIndicator status={availability.status} />
          </span>
          <input
            type="text"
            data-testid="pool-subdomain-input"
            placeholder="e.g. omantel-prod"
            value={store.sovereignSubdomain}
            onChange={e => store.setSovereignSubdomain(e.target.value)}
            aria-invalid={availability.status === 'taken' || availability.status === 'invalid' || !subdomainValid}
            style={inputStyle(
              availability.status === 'taken' || availability.status === 'invalid' || !subdomainValid
                ? 'error'
                : availability.status === 'available'
                  ? 'success'
                  : 'idle',
            )}
          />
        </div>
        <span style={{ fontSize: 14, color: 'var(--wiz-text-md)', fontFamily: 'JetBrains Mono, monospace', paddingBottom: 12 }}>.</span>
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>Pool domain</span>
          <select
            data-testid="pool-domain-select"
            value={store.sovereignPoolDomain}
            onChange={e => store.setSovereignPoolDomain(e.target.value)}
            style={{ ...inputStyle('idle'), cursor: 'pointer' }}
          >
            {SOVEREIGN_POOL_DOMAINS.map(p => (
              <option key={p.id} value={p.id} style={{ background: 'var(--wiz-deep-bg)' }}>{p.domain}</option>
            ))}
          </select>
        </div>
      </div>
      {(availability.status === 'taken' || availability.status === 'invalid' || availability.status === 'error') && (
        <ErrorCard
          title={
            availability.status === 'taken' && availability.reason === 'exists' ? 'Subdomain is already taken' :
            availability.status === 'taken' && availability.reason === 'reserved' ? 'Subdomain is reserved' :
            availability.status === 'taken' && availability.reason === 'unsupported-pool' ? 'Pool domain not supported' :
            availability.status === 'taken' && availability.reason === 'lookup-error' ? 'DNS lookup failed' :
            availability.status === 'invalid' ? 'Invalid subdomain format' :
            'Availability check unavailable'
          }
          fqdn={availability.fqdn}
          detail={availability.detail ?? 'Pick a different subdomain to continue.'}
        />
      )}
      <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
        {pool.description} TLS certificates issued automatically via Let's Encrypt DNS-01.
      </p>
    </div>
  )
}

function ByoManualBody() {
  const store = useWizardStore()
  const valid = !store.sovereignByoDomain || isValidDomain(store.sovereignByoDomain)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12, paddingTop: 10 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>
          Your domain (apex like <code style={{ fontFamily: 'JetBrains Mono, monospace' }}>acme.com</code> or a delegated subdomain like{' '}
          <code style={{ fontFamily: 'JetBrains Mono, monospace' }}>apps.acme.com</code>)
        </span>
        <input
          type="text"
          data-testid="byo-domain-input"
          placeholder="acme.com"
          value={store.sovereignByoDomain}
          onChange={e => store.setSovereignByoDomain(e.target.value)}
          aria-invalid={!valid}
          style={inputStyle(valid ? 'idle' : 'error')}
        />
      </div>
      <NameserverInstructions />
      <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
        After you save the records at your registrar, click Continue. The wizard will hold here until propagation is detected
        (typically 1–4 hours; up to 48 hours for some TLDs). TLS issued via Let's Encrypt DNS-01 once delegation completes.
      </p>
    </div>
  )
}

function ByoApiBody() {
  const store = useWizardStore()
  const [showToken, setShowToken] = useState(false)
  const [validating, setValidating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const valid = !store.sovereignByoDomain || isValidDomain(store.sovereignByoDomain)
  const reg = REGISTRAR_OPTIONS.find(r => r.id === store.registrarType)

  async function onValidate() {
    setError(null)
    if (!isValidDomain(store.sovereignByoDomain) || !store.registrarType || !store.registrarToken) {
      setError('Domain, registrar, and token are all required before validation.')
      return
    }
    setValidating(true)
    try {
      const res = await fetch(`${API_BASE}/v1/registrar/${store.registrarType}/validate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ domain: store.sovereignByoDomain, token: store.registrarToken }),
      })
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { error?: string; detail?: string }
        const map: Record<string, string> = {
          'invalid-token':         "Token rejected by your registrar — check it's the right token type and not expired.",
          'rate-limited':          'Your registrar rate-limited the validation request — wait a minute and retry.',
          'domain-not-in-account': "The domain isn't visible to that token — check you pasted a token that owns the domain.",
          'api-unavailable':       "Couldn't reach your registrar's API right now — try again in a moment.",
          'unsupported-registrar': "OpenOva doesn't yet have an adapter for that registrar — switch to manual mode.",
        }
        setError(map[body.error ?? ''] ?? body.detail ?? `Validation failed (HTTP ${res.status}).`)
        store.setRegistrarTokenValidated(false)
        return
      }
      store.setRegistrarTokenValidated(true)
    } catch (err) {
      setError(`Network error reaching the validation service: ${String(err)}`)
      store.setRegistrarTokenValidated(false)
    } finally {
      setValidating(false)
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12, paddingTop: 10 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>Your domain</span>
        <input
          type="text"
          data-testid="byo-api-domain-input"
          placeholder="acme.com"
          value={store.sovereignByoDomain}
          onChange={e => store.setSovereignByoDomain(e.target.value)}
          aria-invalid={!valid}
          style={inputStyle(valid ? 'idle' : 'error')}
        />
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>Registrar</span>
        <select
          data-testid="byo-api-registrar-select"
          value={store.registrarType ?? ''}
          onChange={e => store.setRegistrarType((e.target.value || null) as RegistrarType | null)}
          style={{ ...inputStyle('idle'), cursor: 'pointer' }}
        >
          <option value="" style={{ background: 'var(--wiz-deep-bg)' }}>— select registrar —</option>
          {REGISTRAR_OPTIONS.map(r => (
            <option key={r.id} value={r.id} style={{ background: 'var(--wiz-deep-bg)' }}>{r.label}</option>
          ))}
        </select>
        {reg && <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>{reg.tokenHint}</span>}
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <KeyRound size={12} /> API token
          <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)', fontStyle: 'italic' }}>
            held in memory only — not persisted to localStorage
          </span>
        </span>
        <div style={{ display: 'flex', alignItems: 'stretch', gap: 8 }}>
          <input
            data-testid="byo-api-token-input"
            type={showToken ? 'text' : 'password'}
            placeholder="paste registrar API token"
            value={store.registrarToken}
            onChange={e => store.setRegistrarToken(e.target.value)}
            style={{ ...inputStyle('idle'), flex: 1 }}
            autoComplete="off"
            spellCheck={false}
          />
          <button
            type="button"
            onClick={() => setShowToken(s => !s)}
            aria-label={showToken ? 'Hide token' : 'Show token'}
            data-testid="byo-api-token-toggle"
            style={{
              padding: '0 12px', borderRadius: 8,
              border: '1.5px solid var(--wiz-border)', background: 'var(--wiz-bg-sub)',
              color: 'var(--wiz-text-sub)', cursor: 'pointer',
            }}
          >
            {showToken ? <EyeOff size={14} /> : <Eye size={14} />}
          </button>
          <button
            type="button"
            onClick={onValidate}
            disabled={validating || !isValidDomain(store.sovereignByoDomain) || !store.registrarType || !store.registrarToken}
            data-testid="byo-api-validate-button"
            style={{
              padding: '0 14px', borderRadius: 8, border: 'none', cursor: validating ? 'progress' : 'pointer',
              background: store.registrarTokenValidated ? 'rgba(74,222,128,0.15)' : '#38BDF8',
              color: store.registrarTokenValidated ? '#4ADE80' : 'var(--wiz-deep-bg)',
              fontSize: 12, fontWeight: 600,
              display: 'inline-flex', alignItems: 'center', gap: 6,
              opacity: (!isValidDomain(store.sovereignByoDomain) || !store.registrarType || !store.registrarToken) ? 0.5 : 1,
            }}
          >
            {validating ? <Loader2 size={12} className="animate-spin" /> :
              store.registrarTokenValidated ? <CheckCircle2 size={12} /> :
              <Sparkles size={12} />}
            {validating ? 'Validating…' : store.registrarTokenValidated ? 'Validated' : 'Validate token'}
          </button>
        </div>
      </div>

      {error && <ErrorCard title="Validation failed" detail={error} />}

      {store.registrarTokenValidated && !error && (
        <div
          data-testid="byo-api-validated-banner"
          role="status"
          style={{
            display: 'flex', gap: 8, alignItems: 'flex-start',
            border: '1px solid rgba(74,222,128,0.35)', background: 'rgba(74,222,128,0.06)',
            borderRadius: 8, padding: '8px 10px',
          }}
        >
          <CheckCircle2 size={14} style={{ color: '#4ADE80', flexShrink: 0, marginTop: 1 }} />
          <span style={{ fontSize: 11, color: 'var(--wiz-text-md)' }}>
            Credentials confirmed. We'll flip the NS records on your behalf when you click Continue.
          </span>
        </div>
      )}

      <NameserverInstructions readOnly />

      <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
        Token never leaves the browser's memory or the validation request — we don't write it to disk, send it to logs, or save it
        anywhere on the server. After validation we keep it just long enough to make the NS-flip call when you submit.
      </p>
    </div>
  )
}

function NameserverInstructions({ readOnly = false }: { readOnly?: boolean }) {
  const [copied, setCopied] = useState<number | null>(null)
  return (
    <div
      data-testid="byo-ns-instructions"
      style={{
        display: 'flex', flexDirection: 'column', gap: 8,
        border: '1px solid var(--wiz-border)', background: 'var(--wiz-bg-sub)',
        borderRadius: 10, padding: 12,
      }}
    >
      <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--wiz-text-hi)', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <RefreshCw size={11} /> {readOnly
          ? 'OpenOva will set these nameservers at your registrar'
          : 'Set these as the nameservers for your domain at your registrar'}
      </span>
      <table style={{ borderCollapse: 'collapse', fontSize: 11, fontFamily: 'JetBrains Mono, monospace' }}>
        <tbody>
          {OPENOVA_NAMESERVERS.map((ns: string, i: number) => (
            <tr key={ns}>
              <td style={{ padding: '3px 8px 3px 0', color: 'var(--wiz-text-sub)' }}>NS {i + 1}</td>
              <td style={{ padding: 3, color: 'var(--wiz-text-hi)' }}>{ns}</td>
              <td style={{ padding: 3 }}>
                {!readOnly && (
                  <button
                    type="button"
                    aria-label={`Copy ${ns} to clipboard`}
                    data-testid={`byo-ns-copy-${i}`}
                    onClick={() => {
                      void navigator.clipboard?.writeText(ns)
                      setCopied(i)
                      window.setTimeout(() => setCopied(c => (c === i ? null : c)), 1200)
                    }}
                    style={{
                      padding: '2px 6px', borderRadius: 4, border: 'none',
                      background: copied === i ? 'rgba(74,222,128,0.2)' : 'transparent',
                      color: copied === i ? '#4ADE80' : 'var(--wiz-text-sub)',
                      cursor: 'pointer',
                    }}
                  >
                    {copied === i ? <CheckCircle2 size={11} /> : <Copy size={11} />}
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function ErrorCard({ title, detail, fqdn }: { title: string; detail: string; fqdn?: string }) {
  return (
    <div
      role="alert"
      style={{
        display: 'flex', gap: 8, alignItems: 'flex-start',
        borderRadius: 8,
        border: '1px solid rgba(248,113,113,0.35)',
        background: 'rgba(248,113,113,0.05)',
        padding: '8px 10px',
      }}
    >
      <AlertCircle size={13} style={{ color: '#F87171', flexShrink: 0, marginTop: 1 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 11, fontWeight: 700, color: '#F87171', display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          {title}
          {fqdn && (
            <code style={{ fontSize: 10, fontFamily: 'JetBrains Mono, monospace', background: 'rgba(248,113,113,0.12)', padding: '1px 5px', borderRadius: 3 }}>
              {fqdn}
            </code>
          )}
        </div>
        <p style={{ margin: '3px 0 0', fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.5 }}>{detail}</p>
      </div>
    </div>
  )
}

function SubdomainAvailabilityIndicator({ status }: { status: import('@/shared/lib/useSubdomainAvailability').AvailabilityStatus }) {
  if (status === 'idle') return null
  const palette = {
    checking:  { fg: 'var(--wiz-text-sub)',  label: 'checking…',    icon: <Loader2 size={10} className="animate-spin" /> },
    available: { fg: '#4ADE80',              label: 'available',    icon: <CheckCircle2 size={10} /> },
    taken:     { fg: '#F87171',              label: 'taken',        icon: <AlertCircle size={10} /> },
    invalid:   { fg: '#F87171',              label: 'invalid',      icon: <AlertCircle size={10} /> },
    error:     { fg: '#FBBF24',              label: 'check failed', icon: <AlertCircle size={10} /> },
  } as const
  const e = palette[status]
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: e.fg, fontSize: 10, fontWeight: 600, letterSpacing: '0.04em', textTransform: 'uppercase' }}>
      {e.icon}
      {e.label}
    </span>
  )
}

function inputStyle(state: 'idle' | 'success' | 'error'): React.CSSProperties {
  const border =
    state === 'error'   ? 'rgba(239,68,68,0.5)' :
    state === 'success' ? 'rgba(74,222,128,0.5)' :
                          'var(--wiz-border)'
  return {
    height: 40, borderRadius: 8,
    border: `1.5px solid ${border}`,
    background: 'var(--wiz-bg-input)',
    color: 'var(--wiz-text-hi)',
    fontSize: 13, padding: '0 12px', outline: 'none',
    fontFamily: 'JetBrains Mono, monospace',
  }
}
