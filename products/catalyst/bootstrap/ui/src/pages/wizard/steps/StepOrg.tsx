import { useState } from 'react'
import { useWizardStore } from '@/entities/deployment/store'
import { ORG_DEFAULTS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

/**
 * StepOrg — captures the organisation profile.
 *
 * Scope on this step is intentionally narrow: name, industry, size,
 * headquarters, and compliance frameworks. These five inputs are the
 * profile signal the rest of the wizard reads when proposing topology
 * sizing and component defaults.
 *
 * What is NOT captured here:
 *   - Sovereign DNS surface (pool / BYO) — lives in StepDomain.
 *   - Admin contact email — was moved to StepDomain in #169 and then
 *     decommissioned from the wizard entirely in #762. The
 *     catalyst-api stamps orgEmail from session.email at submit time
 *     (PR #759 enforces `req.OrgEmail == session.email`), so the
 *     wizard never asks for it again.
 *   - "Org domain" free-text field — removed entirely. The Sovereign FQDN
 *     captured in StepDomain replaces it; the orgDomain store slot is
 *     preserved for backwards-compat with persisted state but is no
 *     longer rendered.
 *
 * #169 promoted the Sovereign-domain capture into a dedicated step so
 * the three-mode (pool / byo-manual / byo-api) UX can render at full
 * width; this revision goes the rest of the way and strips the residual
 * domain + email inputs from the org page.
 */

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
  // An EMPTY default is not a default — #5401 emptied ORG_DEFAULTS, and the
  // badge kept firing because `'' === ''`. A blank required field wearing a
  // DEFAULT chip tells the operator a value is already there.
  const isDefault = defaultValue !== '' && value === defaultValue
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

/**
 * SelectField — an optional profile dropdown.
 *
 * The BLANK first option is load-bearing, not decoration. #5401 emptied
 * ORG_DEFAULTS so `store.orgIndustry` starts as `''`, but this select's
 * option list held no `''` entry, so the HTML "ask for a reset" algorithm
 * (no option selected + display size 1 ⇒ select the first non-disabled
 * option) put INDUSTRIES[0] — `Financial Services` — in the field. That is
 * verbatim `RETIRED_ORG_DEFAULTS_5401.orgIndustry`: the fabricated company's
 * industry, back on step 1 as a real DOM value (`select.value` reads
 * `'Financial Services'`, length 18), while the store still holds `''`.
 * Measured live on hw296 2026-08-14, UAT row W1.
 *
 * Two harms, both closed by the blank option: the operator is shown a
 * profile value nobody chose, and the field they see disagrees with the
 * value the wizard would submit.
 */
function SelectField({ label, value, options, onChange, placeholder = 'Select…' }: {
  label: string; value: string; options: string[]; onChange: (v: string) => void
  placeholder?: string
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
        <option value="" style={{ background: 'var(--wiz-deep-bg)' }}>{placeholder}</option>
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
      description="We use this profile to recommend the right topology and component defaults. Nothing is pre-filled — tell us who the Organisation is, and leave the optional fields blank if they do not apply."
      onNext={next}
    >
      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? col1 : col2, gap: 14 }}>
        <SmartField required label="Organisation name" defaultValue={ORG_DEFAULTS.name}         value={store.orgName}         onChange={store.setOrgName} />
        <SmartField          label="Headquarters"      defaultValue={ORG_DEFAULTS.headquarters} value={store.orgHeadquarters} onChange={store.setOrgHeadquarters} />
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? col1 : col2, gap: 14 }}>
        <SelectField label="Industry"          value={store.orgIndustry} options={INDUSTRIES} onChange={store.setOrgIndustry} />
        <SelectField label="Organisation size" value={store.orgSize}     options={SIZES}      onChange={store.setOrgSize} />
      </div>

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

      <p style={{ fontSize: 11, color: 'var(--wiz-text-hint)', margin: 0, lineHeight: 1.6 }}>
        Only <span style={{ color: 'var(--wiz-accent)' }}>Organisation name</span> identifies the
        Organisation you are provisioning — everything else on this step is optional and only
        shapes what the next steps recommend.
      </p>
    </StepShell>
  )
}
