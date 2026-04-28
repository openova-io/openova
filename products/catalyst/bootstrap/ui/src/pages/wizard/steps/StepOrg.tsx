import { useState } from 'react'
import { CheckCircle2, AlertCircle, Loader2 } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { ORG_DEFAULTS, SOVEREIGN_POOL_DOMAINS, isValidSubdomain, isValidDomain, resolveSovereignDomain } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { useSubdomainAvailability } from '@/shared/lib/useSubdomainAvailability'
import { StepShell, useStepNav } from './_shared'

const INDUSTRIES = [
  'Financial Services', 'Banking', 'Insurance', 'Healthcare',
  'Telecommunications', 'Energy & Utilities', 'Retail', 'Manufacturing',
  'Government', 'Technology', 'Other',
]

const SIZES = ['Under 100', '100–500', '500–2,000', '2,000–10,000', '10,000+']

const COMPLIANCE_OPTIONS = ['PCI DSS', 'ISO 27001', 'SOC 2', 'GDPR', 'HIPAA', 'DORA', 'NIS2', 'FedRAMP']

function SmartField({
  label, defaultValue, value, onChange, type = 'text', required = false,
}: {
  label: string; defaultValue: string; value: string
  onChange: (v: string) => void; type?: string; required?: boolean
}) {
  const isDefault = value === defaultValue
  const [focused, setFocused] = useState(false)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: 'var(--wiz-text-lo)' }}>
          {label}
          {!required && <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', marginLeft: 6 }}>optional</span>}
        </span>
        {isDefault && !focused && (
          <span style={{
            fontSize: 9, fontWeight: 700, letterSpacing: '0.06em', textTransform: 'uppercase',
            color: 'rgba(56,189,248,0.5)', background: 'rgba(56,189,248,0.08)',
            border: '1px solid rgba(56,189,248,0.15)', borderRadius: 4, padding: '1px 6px',
          }}>default</span>
        )}
      </div>
      <input
        type={type}
        value={value}
        onFocus={e => { setFocused(true); e.target.select() }}
        onBlur={e => { setFocused(false); if (!e.target.value.trim()) onChange(defaultValue) }}
        onChange={e => onChange(e.target.value)}
        style={{
          height: 40, borderRadius: 8,
          border: `1.5px solid ${focused ? 'rgba(56,189,248,0.45)' : 'var(--wiz-border)'}`,
          background: 'var(--wiz-bg-input)',
          color: isDefault && !focused ? 'var(--wiz-text-sub)' : 'var(--wiz-text-hi)',
          fontSize: 13, padding: '0 12px', outline: 'none',
          boxShadow: focused ? '0 0 0 3px rgba(56,189,248,0.08)' : 'none',
          transition: 'all 0.15s', fontFamily: 'Inter, sans-serif',
        }}
      />
    </div>
  )
}

function SelectField({ label, value, options, onChange }: {
  label: string; value: string; options: string[]; onChange: (v: string) => void
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
      <span style={{ fontSize: 12, fontWeight: 500, color: 'var(--wiz-text-lo)' }}>
        {label}<span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', marginLeft: 6 }}>optional</span>
      </span>
      <select
        value={value}
        onChange={e => onChange(e.target.value)}
        style={{
          height: 40, borderRadius: 8,
          border: '1.5px solid var(--wiz-border)',
          background: 'var(--wiz-bg-input)',
          color: 'var(--wiz-text-md)', fontSize: 13,
          paddingLeft: 12, paddingRight: 32, outline: 'none',
          fontFamily: 'Inter, sans-serif', cursor: 'pointer',
          appearance: 'none' as const,
          backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%2364748b' stroke-width='2'%3E%3Cpath d='M6 9l6 6 6-6'/%3E%3C/svg%3E")`,
          backgroundRepeat: 'no-repeat', backgroundPosition: 'right 12px center',
        }}
      >
        {options.map(o => <option key={o} value={o} style={{ background: 'var(--wiz-deep-bg)' }}>{o}</option>)}
      </select>
    </div>
  )
}

export function StepOrg() {
  const store = useWizardStore()
  const { next } = useStepNav()
  const bp = useBreakpoint()

  const col2 = '1fr 1fr'
  const col1 = '1fr'

  // Hoist the availability hook so the Next button can react to it.
  // Only meaningful in pool mode; in byo mode we pass empty subdomain to
  // keep the hook idle. Closes #124.
  const pool = SOVEREIGN_POOL_DOMAINS.find(p => p.id === store.sovereignPoolDomain) ?? SOVEREIGN_POOL_DOMAINS[0]!
  const availability = useSubdomainAvailability(
    store.sovereignDomainMode === 'pool' ? store.sovereignSubdomain : '',
    pool.domain,
  )

  // Block Next while the subdomain is being checked, when it's taken,
  // when it's invalid, or when the check itself failed (operator must
  // resolve the issue or switch to BYO mode).
  const nextBlocked =
    store.sovereignDomainMode === 'pool' &&
    (availability.status === 'taken' ||
      availability.status === 'invalid' ||
      availability.status === 'checking' ||
      availability.status === 'error')

  function toggleCompliance(tag: string) {
    store.setOrgCompliance(
      store.orgCompliance.includes(tag)
        ? store.orgCompliance.filter(t => t !== tag)
        : [...store.orgCompliance, tag]
    )
  }

  return (
    <StepShell
      title="Tell us about your organisation"
      description="We use this profile to recommend the right topology and component defaults. All fields are pre-filled — proceed without changing anything or override what you need."
      onNext={next}
      nextDisabled={nextBlocked}
    >
      {/* Row 1: Name · Domain (always 2-col on tablet/desktop, 1-col on mobile) */}
      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? col1 : col2, gap: 14 }}>
        <SmartField required label="Organisation name" defaultValue={ORG_DEFAULTS.name}   value={store.orgName}   onChange={store.setOrgName} />
        <SmartField         label="Domain"             defaultValue={ORG_DEFAULTS.domain} value={store.orgDomain} onChange={store.setOrgDomain} />
      </div>

      {/* Row 2: Email · HQ (2-col on tablet/desktop, 1-col mobile stacked) */}
      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? col1 : col2, gap: 14 }}>
        <SmartField label="Platform team email" defaultValue={ORG_DEFAULTS.email}         value={store.orgEmail}         onChange={store.setOrgEmail} type="email" />
        <SmartField label="Headquarters"         defaultValue={ORG_DEFAULTS.headquarters} value={store.orgHeadquarters} onChange={store.setOrgHeadquarters} />
      </div>

      {/* Row 3: Industry · Size */}
      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? col1 : col2, gap: 14 }}>
        <SelectField label="Industry"          value={store.orgIndustry} options={INDUSTRIES} onChange={store.setOrgIndustry} />
        <SelectField label="Organisation size" value={store.orgSize}     options={SIZES}      onChange={store.setOrgSize} />
      </div>

      {/* Compliance */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: 'var(--wiz-text-lo)' }}>
          Compliance frameworks <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>optional · shapes component defaults</span>
        </span>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
          {COMPLIANCE_OPTIONS.map(tag => {
            const active = store.orgCompliance.includes(tag)
            return (
              <button
                key={tag}
                type="button"
                onClick={() => toggleCompliance(tag)}
                style={{
                  height: 28, padding: '0 12px', borderRadius: 6,
                  border: `1.5px solid ${active ? 'rgba(56,189,248,0.45)' : 'var(--wiz-border)'}`,
                  background: active ? 'rgba(56,189,248,0.1)' : 'var(--wiz-bg-sub)',
                  color: active ? '#38BDF8' : 'var(--wiz-text-sub)',
                  fontSize: 11, fontWeight: active ? 600 : 400,
                  cursor: 'pointer', transition: 'all 0.15s', fontFamily: 'Inter, sans-serif',
                }}
              >
                {tag}
              </button>
            )
          })}
        </div>
      </div>

      {/* Sovereign domain — pool subdomain or BYO. Required to proceed. */}
      <SovereignDomainSection availability={availability} />

      <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
        Fields marked <span style={{ color: 'var(--wiz-accent)' }}>default</span> are pre-filled.
        Click to focus — all text is selected so you can type a replacement immediately.
      </p>
    </StepShell>
  )
}

/**
 * SovereignDomainSection — captures where the new Sovereign will live in DNS.
 *
 * Two modes:
 * - 'pool': customer picks a subdomain under one of OpenOva's pool domains
 *           (default omani.works). Sovereign URL becomes <subdomain>.<pool-domain>.
 *           The provisioner backend writes A/CNAME records via Dynadot's API.
 * - 'byo':  customer brings their own domain (e.g. sovereign.acme-bank.com).
 *           They are responsible for pointing the apex/CNAME at the Sovereign LB.
 *
 * Validation:
 * - subdomain must be a valid DNS label (RFC 1035), 1-63 chars
 * - BYO domain must be a syntactically valid public domain (>= 2 labels)
 * - Cannot proceed to Next until at least one mode resolves to a non-empty domain
 */
function SovereignDomainSection({ availability }: { availability: import('@/shared/lib/useSubdomainAvailability').AvailabilityResult }) {
  const store = useWizardStore()
  const resolved = resolveSovereignDomain(store)
  const subdomainValid = !store.sovereignSubdomain || isValidSubdomain(store.sovereignSubdomain)
  const byoValid = !store.sovereignByoDomain || isValidDomain(store.sovereignByoDomain)
  const pool = SOVEREIGN_POOL_DOMAINS.find(p => p.id === store.sovereignPoolDomain) ?? SOVEREIGN_POOL_DOMAINS[0]!

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, paddingTop: 14, borderTop: '1px solid var(--wiz-border)', marginTop: 6 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: 'var(--wiz-text-lo)' }}>
          Sovereign domain <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', marginLeft: 6 }}>required · how end-users reach this Sovereign</span>
        </span>
        {resolved && (
          <code style={{ fontSize: 11, color: '#38BDF8', fontFamily: 'JetBrains Mono, monospace' }}>
            console.{resolved}
          </code>
        )}
      </div>

      {/* Mode toggle */}
      <div style={{ display: 'inline-flex', gap: 0, border: '1.5px solid var(--wiz-border)', borderRadius: 8, padding: 2, alignSelf: 'flex-start' }}>
        {(['pool', 'byo'] as const).map(mode => {
          const active = store.sovereignDomainMode === mode
          return (
            <button
              key={mode}
              type="button"
              onClick={() => store.setSovereignDomainMode(mode)}
              style={{
                height: 30, padding: '0 14px', borderRadius: 6, border: 'none', cursor: 'pointer',
                background: active ? 'rgba(56,189,248,0.12)' : 'transparent',
                color: active ? '#38BDF8' : 'var(--wiz-text-sub)',
                fontSize: 12, fontWeight: active ? 600 : 400, fontFamily: 'Inter, sans-serif',
                transition: 'all 0.15s',
              }}
            >
              {mode === 'pool' ? 'OpenOva pool subdomain' : 'Use my own domain'}
            </button>
          )
        })}
      </div>

      {/* Mode body */}
      {store.sovereignDomainMode === 'pool' ? (
        <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
          {/* Subdomain input + availability status */}
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 5 }}>
            <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6 }}>
              <span>Subdomain</span>
              <SubdomainAvailabilityIndicator status={availability.status} />
            </span>
            <input
              type="text"
              placeholder="e.g. omantel"
              value={store.sovereignSubdomain}
              onChange={e => store.setSovereignSubdomain(e.target.value)}
              aria-invalid={availability.status === 'taken' || availability.status === 'invalid' || !subdomainValid}
              aria-describedby={availability.status === 'taken' || availability.status === 'invalid' || availability.status === 'error' ? 'sovereign-subdomain-err' : undefined}
              style={{
                height: 40, borderRadius: 8,
                border: `1.5px solid ${
                  availability.status === 'taken' || availability.status === 'invalid' || !subdomainValid
                    ? 'rgba(239,68,68,0.5)'
                  : availability.status === 'available'
                    ? 'rgba(74,222,128,0.5)'
                  : 'var(--wiz-border)'
                }`,
                background: 'var(--wiz-bg-input)', color: 'var(--wiz-text-hi)',
                fontSize: 13, padding: '0 12px', outline: 'none',
                fontFamily: 'JetBrains Mono, monospace',
              }}
            />
          </div>
          <span style={{ fontSize: 14, color: 'var(--wiz-text-md)', fontFamily: 'JetBrains Mono, monospace', paddingBottom: 12 }}>.</span>
          {/* Pool dropdown */}
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 5 }}>
            <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>Pool domain</span>
            <select
              value={store.sovereignPoolDomain}
              onChange={e => store.setSovereignPoolDomain(e.target.value)}
              style={{
                height: 40, borderRadius: 8, border: '1.5px solid var(--wiz-border)',
                background: 'var(--wiz-bg-input)', color: 'var(--wiz-text-md)',
                fontSize: 13, padding: '0 12px', outline: 'none',
                fontFamily: 'JetBrains Mono, monospace', cursor: 'pointer',
              }}
            >
              {SOVEREIGN_POOL_DOMAINS.map(p => (
                <option key={p.id} value={p.id} style={{ background: 'var(--wiz-deep-bg)' }}>{p.domain}</option>
              ))}
            </select>
          </div>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>Your domain (you'll need to point a CNAME or A record at the Sovereign load balancer after provisioning)</span>
          <input
            type="text"
            placeholder="e.g. sovereign.acme-bank.com"
            value={store.sovereignByoDomain}
            onChange={e => store.setSovereignByoDomain(e.target.value)}
            style={{
              height: 40, borderRadius: 8,
              border: `1.5px solid ${byoValid ? 'var(--wiz-border)' : 'rgba(239,68,68,0.5)'}`,
              background: 'var(--wiz-bg-input)', color: 'var(--wiz-text-hi)',
              fontSize: 13, padding: '0 12px', outline: 'none',
              fontFamily: 'JetBrains Mono, monospace',
            }}
          />
        </div>
      )}

      {/* Inline availability error — pool mode only.
          Closes #124: surface a specific reason ("reserved", "exists", etc.)
          before the user reaches Submit, with a one-line remediation hint
          straight from the backend's SubdomainCheckResponse.detail field. */}
      {store.sovereignDomainMode === 'pool' && (availability.status === 'taken' || availability.status === 'invalid' || availability.status === 'error') && (
        <div
          id="sovereign-subdomain-err"
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
              {availability.status === 'taken' && availability.reason === 'exists' && 'Subdomain is already taken'}
              {availability.status === 'taken' && availability.reason === 'reserved' && 'Subdomain is reserved'}
              {availability.status === 'taken' && availability.reason === 'unsupported-pool' && 'Pool domain not supported'}
              {availability.status === 'taken' && availability.reason === 'lookup-error' && 'DNS lookup failed'}
              {availability.status === 'invalid' && 'Invalid subdomain format'}
              {availability.status === 'error' && 'Availability check unavailable'}
              {availability.fqdn && (
                <code style={{ fontSize: 10, fontFamily: 'JetBrains Mono, monospace', background: 'rgba(248,113,113,0.12)', padding: '1px 5px', borderRadius: 3 }}>
                  {availability.fqdn}
                </code>
              )}
            </div>
            <p style={{ margin: '3px 0 0', fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.5 }}>
              {availability.detail ?? 'Pick a different subdomain to continue.'}
            </p>
          </div>
        </div>
      )}

      {/* Helper text */}
      {store.sovereignDomainMode === 'pool' && (
        <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
          {pool.description} TLS certificates issued automatically via Let's Encrypt DNS-01.
        </p>
      )}
      {store.sovereignDomainMode === 'byo' && (
        <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
          After provisioning, point an A record (apex) or CNAME (subdomain) at the Sovereign's load balancer IP — shown in the success screen.
          TLS issued via Let's Encrypt HTTP-01 once DNS resolves.
        </p>
      )}
    </div>
  )
}

/**
 * SubdomainAvailabilityIndicator — small status pill that updates live as
 * useSubdomainAvailability runs. Renders next to the subdomain input label.
 *
 * Closes #124. Visual states match useSubdomainAvailability.AvailabilityStatus:
 *   idle      → nothing shown
 *   checking  → spinner + "checking…"
 *   available → green check + "available"
 *   taken     → red ring + "taken"
 *   invalid   → red ring + "invalid"
 *   error     → red ring + "check failed"
 */
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
