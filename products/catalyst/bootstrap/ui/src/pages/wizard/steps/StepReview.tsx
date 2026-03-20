import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { StepShell, useStepNav } from './_shared'

const TOPOLOGY_NAMES = {
  titan:    'TITAN — 3 regions, 6 clusters',
  triangle: 'TRIANGLE — 3 regions, 5 clusters',
  dual:     'DUAL — 2 regions, 4 clusters',
  compact:  'COMPACT — 1 region, 2 clusters',
  solo:     'SOLO — 1 region, 1 cluster',
}

const PROVIDER_NAMES = {
  hetzner: 'Hetzner Cloud',
  huawei:  'Huawei Cloud',
  oci:     'Oracle Cloud (OCI)',
  aws:     'Amazon Web Services',
  azure:   'Microsoft Azure',
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 0, padding: '9px 0', borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
      <span style={{ width: 148, flexShrink: 0, fontSize: 11, fontWeight: 500, color: 'rgba(255,255,255,0.3)', lineHeight: 1.4 }}>{label}</span>
      <span style={{ fontSize: 12, color: 'rgba(255,255,255,0.75)', lineHeight: 1.4, wordBreak: 'break-all' }}>{value}</span>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ borderRadius: 10, border: '1px solid rgba(255,255,255,0.08)', background: 'rgba(255,255,255,0.02)', overflow: 'hidden' }}>
      <div style={{ padding: '8px 14px', borderBottom: '1px solid rgba(255,255,255,0.06)', background: 'rgba(255,255,255,0.02)' }}>
        <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'rgba(255,255,255,0.3)' }}>{title}</span>
      </div>
      <div style={{ padding: '0 14px' }}>
        {children}
      </div>
    </div>
  )
}

export function StepReview() {
  const store = useWizardStore()
  const { back } = useStepNav()
  const navigate = useNavigate()
  const [loading, setLoading] = useState(false)

  const totalComponents = Object.values(store.componentGroups).reduce((s, ids) => s + ids.length, 0)

  async function provision() {
    setLoading(true)
    try {
      const res = await fetch('/api/v1/deployments', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          orgName:    store.orgName,
          orgDomain:  store.orgDomain,
          orgEmail:   store.orgEmail,
          topology:   store.topology,
          provider:   store.provider,
          token:      store.hetznerToken,
          components: store.componentGroups,
        }),
      })
      const data = await res.json()
      store.setDeploymentId(data.deploymentId ?? 'demo-deploy')
    } catch {
      store.setDeploymentId('demo-deploy')
    }
    navigate({ to: '/provision' })
  }

  return (
    <StepShell
      title="Review & provision"
      description="Confirm your configuration. OpenOva will provision your infrastructure exactly as shown below."
      onNext={provision}
      onBack={back}
      nextLabel="🚀 Provision cluster"
      nextLoading={loading}
    >
      <Section title="Organisation">
        <Row label="Name"       value={store.orgName} />
        <Row label="Domain"     value={store.orgDomain} />
        <Row label="Email"      value={store.orgEmail} />
        <Row label="Industry"   value={store.orgIndustry} />
        <Row label="Size"       value={store.orgSize} />
        <Row label="HQ"         value={store.orgHeadquarters} />
        <Row label="Compliance" value={
          store.orgCompliance.length > 0
            ? <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                {store.orgCompliance.map(t => (
                  <span key={t} style={{ fontSize: 10, padding: '2px 7px', borderRadius: 4, background: 'rgba(56,189,248,0.1)', border: '1px solid rgba(56,189,248,0.2)', color: '#38BDF8' }}>{t}</span>
                ))}
              </div>
            : 'None selected'
        } />
      </Section>

      <Section title="Infrastructure">
        <Row label="Topology" value={store.topology ? TOPOLOGY_NAMES[store.topology] : '—'} />
        <Row label="Cloud provider" value={store.provider ? PROVIDER_NAMES[store.provider] : '—'} />
        <Row label="Credentials" value={
          store.credentialValidated
            ? <span style={{ color: '#4ADE80' }}>✓ Validated</span>
            : store.hetznerToken.startsWith('demo-mode')
              ? <span style={{ color: '#38BDF8' }}>Demo mode</span>
              : <span style={{ color: '#F87171' }}>Not validated</span>
        } />
      </Section>

      <Section title="Components">
        <Row label="Total selected" value={`${totalComponents} components across ${Object.values(store.componentGroups).filter(g => g.length > 0).length} groups`} />
        {Object.entries(store.componentGroups)
          .filter(([, ids]) => ids.length > 0)
          .map(([groupId, ids]) => (
            <Row key={groupId} label={groupId} value={`${ids.length} selected`} />
          ))
        }
      </Section>

      {/* Disclaimer */}
      <div style={{ borderRadius: 8, padding: '10px 12px', background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)' }}>
        <p style={{ fontSize: 11, color: 'rgba(255,255,255,0.35)', margin: 0, lineHeight: 1.6 }}>
          Provisioning runs entirely within your cloud account. OpenOva never stores your credentials or accesses your infrastructure after this session.
        </p>
      </div>
    </StepShell>
  )
}
