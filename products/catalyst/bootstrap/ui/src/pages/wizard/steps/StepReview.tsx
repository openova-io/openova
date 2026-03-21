import { useState } from 'react'
import { Zap } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_LABELS, PROVIDER_REGIONS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'
import { GROUPS } from './componentGroups'

/* ── Provider logos (mirrors StepProvider) ───────────────────────── */
const PROVIDER_LOGOS: Record<CloudProvider, React.ReactNode> = {
  hetzner: <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#D50C2D"/><path d="M5 6h5v12H5zM14 6h5v12h-5z" fill="#fff"/></svg>,
  huawei:  <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#CF0A2C"/><path d="M12 5L14 9.5L19 9.5L15 12.5L17 17L12 14L7 17L9 12.5L5 9.5L10 9.5Z" fill="#fff"/></svg>,
  oci:     <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#F80000"/><ellipse cx={12} cy={12} rx={7} ry={4.5} fill="none" stroke="#fff" strokeWidth={1.5}/></svg>,
  aws:     <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#232F3E"/><path d="M7 15c2.5 1.8 7.5 1.8 10 0" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round"/><path d="M12 8v5" stroke="#FF9900" strokeWidth={1.5} strokeLinecap="round"/><path d="M10 11l2-3 2 3" stroke="#FF9900" strokeWidth={1.2} fill="none" strokeLinecap="round"/></svg>,
  azure:   <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#0078D4"/><path d="M11 7L7 17h4l2-4 2 4h4L15 7z" fill="#fff" opacity={0.9}/></svg>,
}

const PROVIDER_NAMES: Record<CloudProvider, string> = {
  hetzner: 'Hetzner Cloud',
  huawei:  'Huawei Cloud',
  oci:     'Oracle Cloud (OCI)',
  aws:     'Amazon Web Services',
  azure:   'Microsoft Azure',
}

const TOPOLOGY_NAMES: Record<string, string> = {
  citadel: 'CITADEL — 4 regions, 6 clusters, 6 vClusters',
  dual:    'DUAL — 2 regions, 6 clusters, 6 vClusters',
  zoned:   'ZONED — 2 regions, 4 clusters, 4 vClusters',
  compact: 'COMPACT — 2 regions, 2 clusters, 6 vClusters',
  solo:    'SOLO — 1 region, 1 cluster, 3 vClusters',
}

/* ── M/R/O chip colours ──────────────────────────────────────────── */
const TIER_STYLE = {
  mandatory:   { label: 'M', color: '#4ADE80', bg: 'rgba(74,222,128,0.12)',  border: 'rgba(74,222,128,0.25)' },
  recommended: { label: 'R', color: '#38BDF8', bg: 'rgba(56,189,248,0.12)',  border: 'rgba(56,189,248,0.25)' },
  optional:    { label: 'O', color: '#A78BFA', bg: 'rgba(167,139,250,0.12)', border: 'rgba(167,139,250,0.25)' },
} as const

function TierChip({ tier, count }: { tier: 'mandatory' | 'recommended' | 'optional'; count: number }) {
  const s = TIER_STYLE[tier]
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 4,
      fontSize: 10, fontWeight: 700, padding: '2px 7px', borderRadius: 5,
      color: s.color, background: s.bg, border: `1px solid ${s.border}`,
    }}>
      {s.label}<span style={{ fontWeight: 400, opacity: 0.85 }}>{count}</span>
    </span>
  )
}

/* ── Row / Section helpers ───────────────────────────────────────── */
function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', padding: '8px 0', borderBottom: '1px solid var(--wiz-border-sub)' }}>
      <span style={{ width: 130, flexShrink: 0, fontSize: 11, fontWeight: 500, color: 'var(--wiz-text-sub)', lineHeight: 1.45 }}>{label}</span>
      <span style={{ fontSize: 12, color: 'var(--wiz-text-md)', lineHeight: 1.45, wordBreak: 'break-all' }}>{value}</span>
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ borderRadius: 10, border: '1px solid var(--wiz-border-sub)', background: 'var(--wiz-bg-xs)', overflow: 'hidden', marginBottom: 12 }}>
      <div style={{ padding: '7px 14px', borderBottom: '1px solid var(--wiz-border-sub)', background: 'var(--wiz-bg-xs)' }}>
        <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.12em', textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>{title}</span>
      </div>
      <div style={{ padding: '0 14px' }}>{children}</div>
    </div>
  )
}

/* ── Component group row with M/R/O breakdown ────────────────────── */
function ComponentGroupRow({ gid, selectedIds }: { gid: string; selectedIds: string[] }) {
  const group = GROUPS.find(g => g.id === gid)
  if (!group) return null

  const counts = { mandatory: 0, recommended: 0, optional: 0 }
  for (const id of selectedIds) {
    const comp = group.components.find(c => c.id === id)
    if (comp) counts[comp.tier]++
  }

  return (
    <div style={{ display: 'flex', alignItems: 'center', padding: '8px 0', borderBottom: '1px solid var(--wiz-border-sub)', gap: 8, flexWrap: 'wrap' }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--wiz-text-md)', letterSpacing: '0.02em' }}>{group.productName}</span>
        <span style={{ fontSize: 10, color: 'var(--wiz-text-hint)', marginLeft: 6 }}>{group.subtitle}</span>
      </div>
      <div style={{ display: 'flex', gap: 4, flexShrink: 0 }}>
        {counts.mandatory   > 0 && <TierChip tier="mandatory"   count={counts.mandatory}   />}
        {counts.recommended > 0 && <TierChip tier="recommended" count={counts.recommended} />}
        {counts.optional    > 0 && <TierChip tier="optional"    count={counts.optional}    />}
      </div>
    </div>
  )
}

/* ── StepReview ──────────────────────────────────────────────────── */
export function StepReview() {
  const store = useWizardStore()
  const { back } = useStepNav()
  const navigate = useNavigate()
  const bp = useBreakpoint()
  const [loading, setLoading] = useState(false)

  const totalComponents = Object.values(store.componentGroups).reduce((s, ids) => s + ids.length, 0)
  const topology = store.topology
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? []) : []
  const regionProviders = store.regionProviders
  const selectedGroups = Object.entries(store.componentGroups).filter(([, ids]) => ids.length > 0)

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
      title="Ready to launch"
      description="Your full OpenOva ecosystem — infrastructure, platform stack, security, and observability — will be provisioned exactly as configured below."
      onNext={provision}
      onBack={back}
      nextLabel={<><Zap size={13} style={{ marginRight: 5 }} />Launch OpenOva</>}
      nextLoading={loading}
    >
      {/* 2-column review layout — stacks on mobile */}
      <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? '1fr' : '1fr 1fr', gap: 16, alignItems: 'start' }}>

        {/* Left: Organisation + Infrastructure */}
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
                : <span style={{ color: 'var(--wiz-text-hint)' }}>None selected</span>
            } />
          </Section>

          <Section title="Infrastructure">
            <Row label="Topology" value={topology ? TOPOLOGY_NAMES[topology] : '—'} />
            <Row label="AIR-GAP" value={
              store.airgap
                ? <span style={{ color: '#F59E0B', fontWeight: 500 }}>Enabled — +1 isolated region</span>
                : <span style={{ color: 'var(--wiz-text-hint)' }}>Not enabled</span>
            } />
            {regionLabels.length > 0 && (
              <Row label="Regions" value={
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {regionLabels.map((rl, i) => {
                    const p = regionProviders[i] as CloudProvider | undefined
                    const cloudRegionId = store.regionCloudRegions[i]
                    const cloudRegionDef = p && cloudRegionId ? PROVIDER_REGIONS[p].find(r => r.id === cloudRegionId) : undefined
                    return (
                      <div key={i} style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
                        <span style={{ fontSize: 10, color: 'var(--wiz-accent)', fontWeight: 700, width: 14, marginTop: 2, flexShrink: 0 }}>{i + 1}</span>
                        <div style={{ flex: 1, minWidth: 0 }}>
                          <div style={{ fontSize: 11, color: 'var(--wiz-text-lo)' }}>{rl}</div>
                          {p && (
                            <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginTop: 3 }}>
                              {PROVIDER_LOGOS[p]}
                              <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)' }}>
                                {PROVIDER_NAMES[p]}{cloudRegionDef ? ` · ${cloudRegionDef.label} — ${cloudRegionDef.location}` : ''}
                              </span>
                            </div>
                          )}
                        </div>
                      </div>
                    )
                  })}
                </div>
              } />
            )}
          </Section>
        </div>

        {/* Right: Credentials + Components */}
        <div>
          <Section title="Credentials">
            <div style={{ padding: '4px 0' }}>
              {[...new Set(Object.values(regionProviders))]
                .filter(Boolean)
                .map((p) => {
                  const validated = store.providerValidated[p as CloudProvider]
                  const isDemo = (store.providerTokens[p as CloudProvider] ?? '').startsWith('demo-mode')
                  return (
                    <div key={p} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 0', borderBottom: '1px solid var(--wiz-border-sub)', fontSize: 11 }}>
                      {PROVIDER_LOGOS[p as CloudProvider]}
                      <span style={{ flex: 1, color: 'var(--wiz-text-lo)' }}>{PROVIDER_NAMES[p as CloudProvider]}</span>
                      {isDemo
                        ? <span style={{ color: '#38BDF8', fontWeight: 500 }}>Demo mode</span>
                        : validated
                          ? <span style={{ color: '#4ADE80', fontWeight: 500 }}>✓ Validated</span>
                          : <span style={{ color: '#F87171', fontWeight: 500 }}>Not validated</span>
                      }
                    </div>
                  )
                })}
              {Object.values(regionProviders).length === 0 && store.credentialValidated && (
                <div style={{ padding: '7px 0', fontSize: 11 }}>
                  <span style={{ color: '#4ADE80', fontWeight: 500 }}>✓ Validated</span>
                </div>
              )}
            </div>
          </Section>

          <Section title={`Components · ${totalComponents} across ${selectedGroups.length} groups`}>
            {/* Legend */}
            <div style={{ display: 'flex', gap: 10, padding: '8px 0 4px', borderBottom: '1px solid var(--wiz-border-sub)', marginBottom: 2 }}>
              {(['mandatory', 'recommended', 'optional'] as const).map(tier => (
                <div key={tier} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                  <span style={{ width: 8, height: 8, borderRadius: 2, background: TIER_STYLE[tier].color, flexShrink: 0 }} />
                  <span style={{ fontSize: 9, color: 'var(--wiz-text-hint)', textTransform: 'capitalize' }}>{tier}</span>
                </div>
              ))}
            </div>
            {selectedGroups.map(([gid, ids]) => (
              <ComponentGroupRow key={gid} gid={gid} selectedIds={ids} />
            ))}
            {selectedGroups.length === 0 && (
              <div style={{ padding: '12px 0', fontSize: 11, color: 'var(--wiz-text-hint)', textAlign: 'center' }}>No components selected</div>
            )}
          </Section>
        </div>
      </div>

      {/* Privacy note */}
      <div style={{ borderRadius: 8, padding: '10px 12px', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.1)' }}>
        <p style={{ fontSize: 11, color: 'var(--wiz-text-sub)', margin: 0, lineHeight: 1.6 }}>
          Provisioning runs entirely within your cloud account. OpenOva never stores your credentials or accesses your infrastructure after this session.
        </p>
      </div>
    </StepShell>
  )
}
