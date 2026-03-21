import { useState } from 'react'
import { Zap } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_LABELS, PROVIDER_REGIONS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'
import { GROUPS } from './componentGroups'

/* ── Provider logos ──────────────────────────────────────────────── */
const PROVIDER_LOGOS: Record<CloudProvider, React.ReactNode> = {
  hetzner: <svg viewBox="0 0 24 24" width={14} height={14} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#D50C2D"/><path d="M5 6h5v12H5zM14 6h5v12h-5z" fill="#fff"/></svg>,
  huawei:  <svg viewBox="0 0 24 24" width={14} height={14} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#CF0A2C"/><path d="M12 5L14 9.5L19 9.5L15 12.5L17 17L12 14L7 17L9 12.5L5 9.5L10 9.5Z" fill="#fff"/></svg>,
  oci:     <svg viewBox="0 0 24 24" width={14} height={14} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#F80000"/><ellipse cx={12} cy={12} rx={7} ry={4.5} fill="none" stroke="#fff" strokeWidth={1.5}/></svg>,
  aws:     <svg viewBox="0 0 24 24" width={14} height={14} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#232F3E"/><path d="M7 15c2.5 1.8 7.5 1.8 10 0" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round"/><path d="M12 8v5" stroke="#FF9900" strokeWidth={1.5} strokeLinecap="round"/><path d="M10 11l2-3 2 3" stroke="#FF9900" strokeWidth={1.2} fill="none" strokeLinecap="round"/></svg>,
  azure:   <svg viewBox="0 0 24 24" width={14} height={14} style={{flexShrink:0}}><rect width={24} height={24} rx={3} fill="#0078D4"/><path d="M11 7L7 17h4l2-4 2 4h4L15 7z" fill="#fff" opacity={0.9}/></svg>,
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
  zoned:   'ZONED — 2 regions, 4 clusters, 6 vClusters',
  compact: 'COMPACT — 2 regions, 2 clusters, 6 vClusters',
  solo:    'SOLO — 1 region, 1 cluster, 3 vClusters',
}

/* ── Section shell ───────────────────────────────────────────────── */
function Section({ title, children, style }: { title: React.ReactNode; children: React.ReactNode; style?: React.CSSProperties }) {
  return (
    <div style={{ borderRadius: 10, border: '1px solid var(--wiz-border-sub)', background: 'var(--wiz-bg-xs)', overflow: 'hidden', display: 'flex', flexDirection: 'column', ...style }}>
      <div style={{ padding: '6px 14px', borderBottom: '1px solid var(--wiz-border-sub)', flexShrink: 0 }}>
        <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.12em', textTransform: 'uppercase', color: 'var(--wiz-text-sub)' }}>{title}</span>
      </div>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>{children}</div>
    </div>
  )
}

/* ── Compact label/value row ─────────────────────────────────────── */
function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', padding: '5px 14px', borderBottom: '1px solid var(--wiz-border-sub)' }}>
      <span style={{ width: 90, flexShrink: 0, fontSize: 10, fontWeight: 500, color: 'var(--wiz-text-sub)', lineHeight: 1.45 }}>{label}</span>
      <span style={{ fontSize: 11, color: 'var(--wiz-text-md)', lineHeight: 1.45, wordBreak: 'break-all' }}>{value}</span>
    </div>
  )
}

/* ── Component group mini-card ───────────────────────────────────── */
function GroupMiniCard({ gid }: { gid: string }) {
  const store = useWizardStore()
  const group = GROUPS.find(g => g.id === gid)
  if (!group) return null

  const selectedIds = store.componentGroups[gid] ?? []
  const counts = { mandatory: 0, recommended: 0, optional: 0 }
  for (const id of selectedIds) {
    const comp = group.components.find(c => c.id === id)
    if (comp) counts[comp.tier]++
  }
  const total = selectedIds.length
  const hasAny = total > 0

  return (
    <div style={{
      borderRadius: 8, padding: '8px 10px',
      border: `1px solid ${hasAny ? 'var(--wiz-border-sub)' : 'rgba(255,255,255,0.04)'}`,
      background: hasAny ? 'var(--wiz-bg-xs)' : 'transparent',
      opacity: hasAny ? 1 : 0.38,
      display: 'flex', flexDirection: 'column', gap: 4,
    }}>
      {/* Line 1: product name (left) + color-coded counts (right) */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 4 }}>
        <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.06em', color: hasAny ? 'var(--wiz-text-hi)' : 'var(--wiz-text-hint)' }}>{group.productName}</span>
        <div style={{ display: 'flex', gap: 3, flexShrink: 0 }}>
          {counts.mandatory > 0 && (
            <span style={{ fontSize: 9, fontWeight: 700, color: '#4ADE80', background: 'rgba(74,222,128,0.1)', borderRadius: 3, padding: '1px 5px' }}>M {counts.mandatory}</span>
          )}
          {counts.recommended > 0 && (
            <span style={{ fontSize: 9, fontWeight: 700, color: '#38BDF8', background: 'rgba(56,189,248,0.1)', borderRadius: 3, padding: '1px 5px' }}>R {counts.recommended}</span>
          )}
          {counts.optional > 0 && (
            <span style={{ fontSize: 9, fontWeight: 700, color: '#A78BFA', background: 'rgba(167,139,250,0.1)', borderRadius: 3, padding: '1px 5px' }}>O {counts.optional}</span>
          )}
          {total === 0 && (
            <span style={{ fontSize: 9, color: 'var(--wiz-text-hint)' }}>—</span>
          )}
        </div>
      </div>
      {/* Line 2: conceptual product family name */}
      <div style={{ fontSize: 9, color: 'var(--wiz-text-sub)', letterSpacing: '0.02em' }}>{group.subtitle}</div>
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

  const topology = store.topology
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? []) : []
  const regionProviders = store.regionProviders
  const totalComponents = Object.values(store.componentGroups).reduce((s, ids) => s + ids.length, 0)

  /* Include air-gap region if enabled */
  const allRegionLabels = store.airgap
    ? [...regionLabels, 'AIR-GAP Region']
    : regionLabels

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

  const isMobile = bp === 'mobile'

  return (
    <StepShell
      title="Ready to launch"
      description="Your full OpenOva ecosystem — infrastructure, platform stack, security, and observability — will be provisioned exactly as configured below."
      onNext={provision}
      onBack={back}
      nextLabel={<><Zap size={13} style={{ marginRight: 5 }} />Launch OpenOva</>}
      nextLoading={loading}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>

        {/* ── Row 1: Organisation (1fr) + Components (2fr) ── */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: isMobile ? '1fr' : '1fr 2fr',
          gap: 14,
          alignItems: 'stretch',
        }}>

          {/* Organisation */}
          <Section title="Organisation">
            <Row label="Name"       value={store.orgName} />
            <Row label="Domain"     value={store.orgDomain} />
            <Row label="Email"      value={store.orgEmail} />
            <Row label="Industry"   value={store.orgIndustry} />
            <Row label="Size"       value={store.orgSize} />
            <Row label="HQ"         value={store.orgHeadquarters} />
            <Row label="Compliance" value={
              store.orgCompliance.length > 0
                ? <div style={{ display: 'flex', flexWrap: 'wrap', gap: 3 }}>
                    {store.orgCompliance.map(t => (
                      <span key={t} style={{ fontSize: 9, padding: '1px 6px', borderRadius: 3, background: 'rgba(56,189,248,0.1)', border: '1px solid rgba(56,189,248,0.2)', color: '#38BDF8' }}>{t}</span>
                    ))}
                  </div>
                : <span style={{ color: 'var(--wiz-text-hint)' }}>None</span>
            } />
          </Section>

          {/* Components — 3×3 grid of group mini-cards */}
          <Section title={`Components · ${totalComponents} selected`}>
            <div style={{ padding: '10px 14px', display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 8 }}>
              {GROUPS.map(g => <GroupMiniCard key={g.id} gid={g.id} />)}
            </div>
          </Section>
        </div>

        {/* ── Row 2: Infrastructure — full width ── */}
        <Section title={
          <span style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span>Infrastructure</span>
            {topology && (
              <span style={{ fontSize: 10, fontWeight: 500, color: 'var(--wiz-text-md)', letterSpacing: 0 }}>
                {TOPOLOGY_NAMES[topology]}
              </span>
            )}
            {store.airgap && (
              <span style={{ fontSize: 9, fontWeight: 700, color: '#F59E0B', background: 'rgba(245,158,11,0.12)', border: '1px solid rgba(245,158,11,0.25)', borderRadius: 3, padding: '1px 6px', letterSpacing: '0.04em' }}>AIR-GAP</span>
            )}
          </span>
        }>
          {/* Region cards — flex row, all equal height, 1–5 cards */}
          <div style={{ padding: '10px 14px', display: 'flex', gap: 8, alignItems: 'stretch', flexWrap: 'wrap' }}>
            {allRegionLabels.map((rl, i) => {
              const isAirgap = store.airgap && i === regionLabels.length
              const p = regionProviders[i] as CloudProvider | undefined
              const cloudRegionId = store.regionCloudRegions[i]
              const cloudRegionDef = p && cloudRegionId ? PROVIDER_REGIONS[p].find(r => r.id === cloudRegionId) : undefined
              return (
                <div key={i} style={{
                  flex: '1 1 0',
                  minWidth: isMobile ? '100%' : 0,
                  borderRadius: 8, padding: '8px 10px',
                  border: `1px solid ${isAirgap ? 'rgba(245,158,11,0.3)' : 'var(--wiz-border-sub)'}`,
                  background: isAirgap ? 'rgba(245,158,11,0.03)' : 'var(--wiz-bg-xs)',
                  display: 'flex', flexDirection: 'column', gap: 4,
                }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                    <span style={{ fontSize: 9, fontWeight: 700, color: isAirgap ? '#F59E0B' : 'var(--wiz-accent)', width: 12, flexShrink: 0 }}>{i + 1}</span>
                    {isAirgap && <span style={{ fontSize: 8, fontWeight: 700, color: '#F59E0B', background: 'rgba(245,158,11,0.12)', borderRadius: 3, padding: '0 4px' }}>AIR-GAP</span>}
                  </div>
                  <div style={{ fontSize: 10, color: 'var(--wiz-text-lo)', lineHeight: 1.3 }}>{rl}</div>
                  {p && (
                    <div style={{ marginTop: 4, display: 'flex', flexDirection: 'column', gap: 3 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                        {PROVIDER_LOGOS[p]}
                        <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)' }}>{PROVIDER_NAMES[p]}</span>
                      </div>
                      {cloudRegionDef && (
                        <div style={{ fontSize: 9, color: 'var(--wiz-text-hint)' }}>{cloudRegionDef.label} — {cloudRegionDef.location}</div>
                      )}
                    </div>
                  )}
                  {!p && <div style={{ fontSize: 10, color: 'var(--wiz-text-hint)', marginTop: 4 }}>Not configured</div>}
                </div>
              )
            })}
          </div>
        </Section>

        {/* Privacy note */}
        <div style={{ borderRadius: 8, padding: '9px 12px', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.1)' }}>
          <p style={{ fontSize: 11, color: 'var(--wiz-text-sub)', margin: 0, lineHeight: 1.6 }}>
            Provisioning runs entirely within your cloud account. OpenOva never stores your credentials or accesses your infrastructure after this session.
          </p>
        </div>
      </div>
    </StepShell>
  )
}
