import { useState } from 'react'
import { useWizardStore } from '@/entities/deployment/store'
import { ORG_DEFAULTS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
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
        <span style={{ fontSize: 12, fontWeight: 500, color: 'rgba(var(--wiz-ch),0.5)' }}>
          {label}
          {!required && <span style={{ fontSize: 11, color: 'rgba(var(--wiz-ch),0.2)', marginLeft: 6 }}>optional</span>}
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
          border: `1.5px solid ${focused ? 'rgba(56,189,248,0.45)' : 'rgba(var(--wiz-ch),0.1)'}`,
          background: 'rgba(var(--wiz-ch),0.05)',
          color: isDefault && !focused ? 'rgba(var(--wiz-ch),0.28)' : 'rgba(var(--wiz-ch),0.88)',
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
      <span style={{ fontSize: 12, fontWeight: 500, color: 'rgba(var(--wiz-ch),0.5)' }}>
        {label}<span style={{ fontSize: 11, color: 'rgba(var(--wiz-ch),0.2)', marginLeft: 6 }}>optional</span>
      </span>
      <select
        value={value}
        onChange={e => onChange(e.target.value)}
        style={{
          height: 40, borderRadius: 8,
          border: '1.5px solid rgba(var(--wiz-ch),0.1)',
          background: 'rgba(var(--wiz-ch),0.05)',
          color: 'rgba(var(--wiz-ch),0.7)', fontSize: 13,
          paddingLeft: 12, paddingRight: 32, outline: 'none',
          fontFamily: 'Inter, sans-serif', cursor: 'pointer',
          appearance: 'none' as const,
          backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='rgba(var(--wiz-ch),0.3)' stroke-width='2'%3E%3Cpath d='M6 9l6 6 6-6'/%3E%3C/svg%3E")`,
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
        <span style={{ fontSize: 12, fontWeight: 500, color: 'rgba(var(--wiz-ch),0.5)' }}>
          Compliance frameworks <span style={{ fontSize: 11, color: 'rgba(var(--wiz-ch),0.2)' }}>optional · shapes component defaults</span>
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
                  border: `1.5px solid ${active ? 'rgba(56,189,248,0.45)' : 'rgba(var(--wiz-ch),0.1)'}`,
                  background: active ? 'rgba(56,189,248,0.1)' : 'rgba(var(--wiz-ch),0.03)',
                  color: active ? '#38BDF8' : 'rgba(var(--wiz-ch),0.3)',
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

      <p style={{ fontSize: 11, color: 'rgba(var(--wiz-ch),0.18)', margin: 0, lineHeight: 1.6 }}>
        Fields marked <span style={{ color: 'rgba(56,189,248,0.45)' }}>default</span> are pre-filled.
        Click to focus — all text is selected so you can type a replacement immediately.
      </p>
    </StepShell>
  )
}
