import { useState } from 'react'
import { Zap } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_LABELS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

const TOPOLOGY_NAMES = {
  delta:    'DELTA — 3 regions, 6 clusters',
  triangle: 'TRIANGLE — 3 regions, 5 clusters',
  dual:     'DUAL — 2 regions, 4 clusters',
  compact:  'COMPACT — 1 region, 2 clusters',
  solo:     'SOLO — 1 region, 1 cluster',
}

const PROVIDER_NAMES: Record<CloudProvider, string> = {
  hetzner: 'Hetzner Cloud',
  huawei:  'Huawei Cloud',
  oci:     'Oracle Cloud (OCI)',
  aws:     'Amazon Web Services',
  azure:   'Microsoft Azure',
}

const GROUP_NAMES: Record<string, string> = {
  security:     'Security & Compliance',
  identity:     'Identity & Secrets',
  networking:   'Networking & Ingress',
  gitops:       'GitOps & Platform Ops',
  observability:'Observability',
  data:         'Data & Storage',
  resilience:   'Resilience & Scaling',
  ai:           'AI & Machine Learning',
  events:       'Event & Integration',
  comms:        'Communication',
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', padding: '8px 0', borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
      <span style={{ width: 130, flexShrink: 0, fontSize: 11, fontWeight: 500, color: 'rgba(255,255,255,0.28)', lineHeight: 1.45 }}>{label}</span>
      <span style={{ fontSize: 12, color: 'rgba(255,255,255,0.72)', lineHeight: 1.45, wordBreak: 'break-all' }}>{value}</span>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ borderRadius: 10, border: '1px solid rgba(255,255,255,0.07)', background: 'rgba(255,255,255,0.02)', overflow: 'hidden', marginBottom: 12 }}>
      <div style={{ padding: '7px 14px', borderBottom: '1px solid rgba(255,255,255,0.06)', background: 'rgba(255,255,255,0.02)' }}>
        <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.12em', textTransform: 'uppercase', color: 'rgba(255,255,255,0.28)' }}>{title}</span>
      </div>
      <div style={{ padding: '0 14px' }}>{children}</div>
    </div>
  )
}

export function StepReview() {
  const store = useWizardStore()
  const { back } = useStepNav()
  const navigate = useNavigate()
  const bp = useBreakpoint()
  const [loading, setLoading] = useState(false)

  const totalComponents = Object.values(store.componentGroups).reduce((s, ids) => s + ids.length, 0)
  const topology = store.topology
  const regionLabels = topology ? TOPOLOGY_REGION_LABELS[topology] : []
  const regionProviders = store.regionProviders

  async function provision() {
    setLoading(true)
    try {
      const res = await fetch('/api/v1/deployments', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          orgName:         store.orgName,
          orgDomain:       store.orgDomain,
          orgEmail:        store.orgEmail,
          orgIndustry:     store.orgIndustry,
          orgCompliance:   store.orgCompliance,
          topology:        store.topology,
          regionProviders: store.regionProviders,
          components:      store.componentGroups,
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
      description="Confirm your configuration below. OpenOva will provision exactly what you see here."
      onNext={provision}
      onBack={back}
      nextLabel={<><Zap size={13} style={{ marginRight: 4 }} />Provision cluster</>}
      nextLoading={loading}
    >
      {/* 2-column review layout — stacks on mobile */}
      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? '1fr' : '1fr 1fr', gap: 16, alignItems: 'start' }}>

        {/* Left column: Organisation + Infrastructure */}
        <div>
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
                : <span style={{ color: 'rgba(255,255,255,0.2)' }}>None selected</span>
            } />
          </Section>

          <Section title="Infrastructure">
            <Row label="Topology" value={topology ? TOPOLOGY_NAMES[topology] : '—'} />
            {regionLabels.length > 0 && (
              <Row label="Regions" value={
                <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                  {regionLabels.map((rl, i) => {
                    const p = regionProviders[i] as CloudProvider | undefined
                    return (
                      <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <span style={{ fontSize: 10, color: 'rgba(56,189,248,0.6)', fontWeight: 700, width: 14 }}>{i + 1}</span>
                        <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.5)' }}>{rl}</span>
                        {p && <span style={{ fontSize: 10, color: 'rgba(255,255,255,0.3)' }}>· {PROVIDER_NAMES[p]}</span>}
                      </div>
                    )
                  })}
                </div>
              } />
            )}
          </Section>
        </div>

        {/* Right column: Credentials + Components */}
        <div>
          <Section title="Credentials">
            <div style={{ padding: '4px 0' }}>
              {[...new Set(Object.values(regionProviders))]
                .filter(Boolean)
                .map((p) => {
                  const validated = store.providerValidated[p as CloudProvider]
                  const isDemo = (store.providerTokens[p as CloudProvider] ?? '').startsWith('demo-mode')
                  return (
                    <div key={p} style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '7px 0', borderBottom: '1px solid rgba(255,255,255,0.06)', fontSize: 11 }}>
                      <span style={{ flex: 1, color: 'rgba(255,255,255,0.5)' }}>{PROVIDER_NAMES[p as CloudProvider]}</span>
                      {isDemo
                        ? <span style={{ color: '#38BDF8' }}>Demo mode</span>
                        : validated
                          ? <span style={{ color: '#4ADE80' }}>✓ Validated</span>
                          : <span style={{ color: '#F87171' }}>Not validated</span>
                      }
                    </div>
                  )
                })}
              {Object.values(regionProviders).length === 0 && store.credentialValidated && (
                <div style={{ padding: '7px 0', fontSize: 11 }}>
                  <span style={{ color: '#4ADE80' }}>✓ Validated</span>
                </div>
              )}
            </div>
          </Section>

          <Section title="Components">
            <Row label="Total" value={`${totalComponents} components across ${Object.values(store.componentGroups).filter(g => g.length > 0).length} groups`} />
            {Object.entries(store.componentGroups)
              .filter(([, ids]) => ids.length > 0)
              .map(([gid, ids]) => (
                <Row key={gid} label={GROUP_NAMES[gid] ?? gid} value={`${ids.length} selected`} />
              ))
            }
          </Section>
        </div>
      </div>

      {/* Privacy note */}
      <div style={{ borderRadius: 8, padding: '10px 12px', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.1)' }}>
        <p style={{ fontSize: 11, color: 'rgba(255,255,255,0.3)', margin: 0, lineHeight: 1.6 }}>
          Provisioning runs entirely within your cloud account. OpenOva never stores your credentials or accesses your infrastructure after this session.
        </p>
      </div>
    </StepShell>
  )
}
