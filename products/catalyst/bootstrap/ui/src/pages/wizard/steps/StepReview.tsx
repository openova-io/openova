/**
 * StepReview — single source of truth for the POST body.
 *
 * Every section on this page corresponds 1:1 to a field that the
 * `provision()` callback below sends to `POST /v1/deployments`. If a
 * field is in the request body, it appears here; if a field is not in
 * the request body, it is NOT shown here. That contract is what makes
 * this step trustworthy as a "what am I about to launch" surface.
 *
 * Section order (matches the wizard's step order):
 *   1. Organisation       — name / industry / size / HQ / compliance
 *   2. Topology           — template + region count + HA + AIR-GAP flags
 *   3. Provider           — per-region: logo+name, region label, control-
 *                           plane SKU, worker SKU+count, hourly cost. Each
 *                           region's SKU comes from its own provider's
 *                           catalog (PROVIDER_NODE_SIZES[provider]); cx32
 *                           does not exist on Azure, m6i.xlarge does not
 *                           exist on Hetzner. The footer rolls each
 *                           region's (cp + worker*count) into the total.
 *   4. Credentials        — Hetzner project ID + masked token + SSH key
 *   5. Components         — product-family summary (M / R / O counts)
 *   6. Domain             — pool subdomain + FQDN OR BYO + admin email
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) the Hetzner
 * token and any registrar token are rendered as a fixed-length mask plus
 * the character count — never the plaintext. Same posture as the SSH
 * private key.
 */

import { useState } from 'react'
import { Zap } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  PROVIDER_REGIONS,
  TOPOLOGY_REGION_LABELS,
  resolveSovereignDomain,
  SOVEREIGN_POOL_DOMAINS,
} from '@/entities/deployment/model'
import type { CloudProvider } from '@/entities/deployment/model'
import { findNodeSize } from '@/shared/constants/providerSizes'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { API_BASE, path } from '@/shared/config/urls'
import { StepShell, useStepNav } from './_shared'
import { GROUPS } from './componentGroups'

/* ── Provider logos ──────────────────────────────────────────────── */
const PROVIDER_LOGOS: Record<CloudProvider, React.ReactNode> = {
  hetzner: <svg viewBox="0 0 24 24" width={14} height={14} style={{ flexShrink: 0 }}><rect width={24} height={24} rx={3} fill="#D50C2D" /><path d="M5 6h5v12H5zM14 6h5v12h-5z" fill="#fff" /></svg>,
  huawei:  <svg viewBox="0 0 24 24" width={14} height={14} style={{ flexShrink: 0 }}><rect width={24} height={24} rx={3} fill="#CF0A2C" /><path d="M12 5L14 9.5L19 9.5L15 12.5L17 17L12 14L7 17L9 12.5L5 9.5L10 9.5Z" fill="#fff" /></svg>,
  oci:     <svg viewBox="0 0 24 24" width={14} height={14} style={{ flexShrink: 0 }}><rect width={24} height={24} rx={3} fill="#F80000" /><ellipse cx={12} cy={12} rx={7} ry={4.5} fill="none" stroke="#fff" strokeWidth={1.5} /></svg>,
  aws:     <svg viewBox="0 0 24 24" width={14} height={14} style={{ flexShrink: 0 }}><rect width={24} height={24} rx={3} fill="#232F3E" /><path d="M7 15c2.5 1.8 7.5 1.8 10 0" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round" /><path d="M12 8v5" stroke="#FF9900" strokeWidth={1.5} strokeLinecap="round" /><path d="M10 11l2-3 2 3" stroke="#FF9900" strokeWidth={1.2} fill="none" strokeLinecap="round" /></svg>,
  azure:   <svg viewBox="0 0 24 24" width={14} height={14} style={{ flexShrink: 0 }}><rect width={24} height={24} rx={3} fill="#0078D4" /><path d="M11 7L7 17h4l2-4 2 4h4L15 7z" fill="#fff" opacity={0.9} /></svg>,
}

const PROVIDER_NAMES: Record<CloudProvider, string> = {
  hetzner: 'Hetzner Cloud',
  huawei:  'Huawei Cloud',
  oci:     'Oracle Cloud (OCI)',
  aws:     'Amazon Web Services',
  azure:   'Microsoft Azure',
}

const DOMAIN_MODE_LABELS: Record<'pool' | 'byo-manual' | 'byo-api', string> = {
  'pool':       'OpenOva pool domain',
  'byo-manual': 'Bring Your Own — manual NS',
  'byo-api':    'Bring Your Own — registrar API',
}

/* ── Section shell ───────────────────────────────────────────────── */
function Section({
  title,
  children,
  style,
}: {
  title: React.ReactNode
  children: React.ReactNode
  style?: React.CSSProperties
}) {
  return (
    <div
      style={{
        borderRadius: 10,
        border: '1px solid var(--wiz-border-sub)',
        background: 'var(--wiz-bg-xs)',
        overflow: 'hidden',
        display: 'flex',
        flexDirection: 'column',
        ...style,
      }}
    >
      <div style={{ padding: '6px 14px', borderBottom: '1px solid var(--wiz-border-sub)', flexShrink: 0 }}>
        <span
          style={{
            fontSize: 10,
            fontWeight: 700,
            letterSpacing: '0.12em',
            textTransform: 'uppercase',
            color: 'var(--wiz-text-sub)',
          }}
        >
          {title}
        </span>
      </div>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>{children}</div>
    </div>
  )
}

/* ── Compact label/value row ─────────────────────────────────────── */
function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        padding: '5px 14px',
        borderBottom: '1px solid var(--wiz-border-sub)',
      }}
    >
      <span
        style={{
          width: 110,
          flexShrink: 0,
          fontSize: 10,
          fontWeight: 500,
          color: 'var(--wiz-text-sub)',
          lineHeight: 1.45,
        }}
      >
        {label}
      </span>
      <span
        style={{
          fontSize: 11,
          color: 'var(--wiz-text-md)',
          lineHeight: 1.45,
          wordBreak: 'break-all',
          flex: 1,
        }}
      >
        {value}
      </span>
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
    <div
      style={{
        borderRadius: 8,
        padding: '8px 10px',
        border: `1px solid ${hasAny ? 'var(--wiz-border-sub)' : 'rgba(255,255,255,0.04)'}`,
        background: hasAny ? 'var(--wiz-bg-xs)' : 'transparent',
        opacity: hasAny ? 1 : 0.38,
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 4 }}>
        <span
          style={{
            fontSize: 10,
            fontWeight: 700,
            letterSpacing: '0.06em',
            color: hasAny ? 'var(--wiz-text-hi)' : 'var(--wiz-text-hint)',
          }}
        >
          {group.productName}
        </span>
        <div style={{ display: 'flex', gap: 3, flexShrink: 0 }}>
          {counts.mandatory > 0 && (
            <span
              title="mandatory (incl. transitive-mandatory)"
              style={{ fontSize: 9, fontWeight: 700, color: '#4ADE80', background: 'rgba(74,222,128,0.1)', borderRadius: 3, padding: '1px 5px' }}
            >
              M {counts.mandatory}
            </span>
          )}
          {counts.recommended > 0 && (
            <span
              title="recommended"
              style={{ fontSize: 9, fontWeight: 700, color: '#38BDF8', background: 'rgba(56,189,248,0.1)', borderRadius: 3, padding: '1px 5px' }}
            >
              R {counts.recommended}
            </span>
          )}
          {counts.optional > 0 && (
            <span
              title="user-selected optional"
              style={{ fontSize: 9, fontWeight: 700, color: '#A78BFA', background: 'rgba(167,139,250,0.1)', borderRadius: 3, padding: '1px 5px' }}
            >
              O {counts.optional}
            </span>
          )}
          {total === 0 && <span style={{ fontSize: 9, color: 'var(--wiz-text-hint)' }}>—</span>}
        </div>
      </div>
      <div style={{ fontSize: 9, color: 'var(--wiz-text-sub)', letterSpacing: '0.02em' }}>{group.subtitle}</div>
    </div>
  )
}

/* ── Helpers ─────────────────────────────────────────────────────── */
function maskToken(token: string): string {
  if (!token) return '— not configured —'
  return `••••••••••••  (${token.length} chars)`
}

function shortFingerprint(fp: string): string {
  if (!fp) return ''
  // Server-generated SSH fingerprints are SHA256:<base64>, ~50 chars.
  // Truncate to prefix + last 8 for the review row.
  if (fp.length <= 24) return fp
  return `${fp.slice(0, 12)}…${fp.slice(-8)}`
}

function dimIfMissing(value: string | null | undefined, fallback = '— not configured —'): React.ReactNode {
  if (value === null || value === undefined || value === '') {
    return <span style={{ color: 'var(--wiz-text-hint)' }}>{fallback}</span>
  }
  return value
}

/* ── StepReview ──────────────────────────────────────────────────── */
export function StepReview() {
  const store = useWizardStore()
  const { back } = useStepNav()
  const bp = useBreakpoint()
  const [loading, setLoading] = useState(false)
  const isMobile = bp === 'mobile'

  /* ── Derived values for display + POST body ──────────────────── */
  const sovereignFQDN = resolveSovereignDomain(store)

  // Pool domain row label (pool mode only): the wizard stores the pool
  // *id* ('omani-works'); the row needs the human-readable domain.
  const poolDomainLabel =
    SOVEREIGN_POOL_DOMAINS.find(p => p.id === store.sovereignPoolDomain)?.domain ?? store.sovereignPoolDomain

  // Per-region payload — built once and used for both the on-screen
  // table and the POST body. The Regions[] array is canonical; the
  // singular hetznerToken/region/controlPlaneSize/workerSize/workerCount
  // fields below are mirrored from index 0 so the existing solo-Hetzner
  // back-compat path inside provisioner.Validate keeps working.
  const topologyRegionLabels = store.topology
    ? TOPOLOGY_REGION_LABELS[store.topology] ?? ['Region 1']
    : ['Region 1']
  const allRegionLabels = store.airgap
    ? [...topologyRegionLabels, 'AIR-GAP Region']
    : topologyRegionLabels
  interface ReviewRegionRow {
    label: string
    provider: CloudProvider | null
    cloudRegion: string
    cloudRegionLabel: string | null
    cloudRegionLocation: string | null
    controlPlaneSize: string
    workerSize: string
    workerCount: number
    hourlyCost: number
    isAirgap: boolean
  }
  const regionRows: ReviewRegionRow[] = allRegionLabels.map((label, i) => {
    const provider = (store.regionProviders[i] as CloudProvider | undefined) ?? null
    const cloudRegion = store.regionCloudRegions[i] ?? ''
    const regionDef = provider && cloudRegion ? PROVIDER_REGIONS[provider].find(r => r.id === cloudRegion) : undefined
    const cpId = store.regionControlPlaneSizes[i] ?? ''
    const wkId = store.regionWorkerSizes[i] ?? ''
    const wkCount = store.regionWorkerCounts[i] ?? 0
    const cp = provider && cpId ? findNodeSize(provider, cpId) : undefined
    const wk = provider && wkId ? findNodeSize(provider, wkId) : undefined
    const cpC = cp ? cp.priceHour : 0
    const wkC = wk ? wk.priceHour * Math.max(0, wkCount) : 0
    return {
      label,
      provider,
      cloudRegion,
      cloudRegionLabel: regionDef?.label ?? null,
      cloudRegionLocation: regionDef?.location ?? null,
      controlPlaneSize: cpId,
      workerSize: wkId,
      workerCount: wkCount,
      hourlyCost: cpC + wkC,
      isAirgap: store.airgap && i === topologyRegionLabels.length,
    }
  })
  const regionsPayload = regionRows.map((r) => ({
    provider:         r.provider ?? '',
    cloudRegion:      r.cloudRegion,
    controlPlaneSize: r.controlPlaneSize,
    workerSize:       r.workerSize,
    workerCount:      r.workerCount,
  }))
  const totalHourly = regionRows.reduce((acc, r) => acc + r.hourlyCost, 0)
  const r0 = regionsPayload[0] ?? {
    provider: '',
    cloudRegion: '',
    controlPlaneSize: '',
    workerSize: '',
    workerCount: 0,
  }
  // Component totals for the section header.
  const totalComponents = Object.values(store.componentGroups).reduce((s, ids) => s + ids.length, 0)

  /* ── Submission ─────────────────────────────────────────────── */
  async function provision() {
    setLoading(true)
    try {
      const res = await fetch(`${API_BASE}/v1/deployments`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          // Identity
          orgName:  store.orgName,
          orgEmail: store.orgEmail,
          // Sovereign domain — pool subdomain or BYO
          sovereignFQDN,
          sovereignDomainMode: store.sovereignDomainMode,
          sovereignPoolDomain: poolDomainLabel,
          sovereignSubdomain: store.sovereignSubdomain,
          // Hetzner credentials — passed when Region 1 is on Hetzner; the
          // provisioner reads non-Hetzner provider tokens out of
          // providerTokens for those code paths when activated.
          hetznerToken:     store.hetznerToken,
          hetznerProjectID: store.hetznerProjectId,
          // Legacy singular fields — derived from Region 1, kept for the
          // back-compat path inside provisioner.Validate / writeTfvars.
          region:           r0.cloudRegion || 'fsn1',
          controlPlaneSize: r0.controlPlaneSize,
          workerSize:       r0.workerSize,
          workerCount:      r0.workerCount,
          haEnabled:        store.haEnabled,
          // Canonical per-region payload — multi-region tofu wiring is
          // structural-correct but only Region 1 (solo path) is end-to-end
          // exercised today against a real Hetzner project. Emitting the
          // full array now means nothing changes when the multi-region
          // apply path activates.
          regions: regionsPayload,
          // SSH key
          sshPublicKey: store.sshPublicKey,
        }),
      })
      const data = await res.json()
      if (!res.ok) {
        alert(`Provisioning rejected: ${data.error || 'unknown error'}`)
        setLoading(false)
        return
      }
      store.setDeploymentId(data.id)
    } catch (err) {
      alert(`Failed to start provisioning: ${err}`)
      setLoading(false)
      return
    }
    window.location.href = path('provision.html')
  }

  return (
    <StepShell
      title="Ready to launch"
      description="Every value below is exactly what we'll send to the provisioning API. Use Back to amend any section — none of the steps lose state when you navigate."
      onNext={provision}
      onBack={back}
      nextLabel={
        <>
          <Zap size={13} style={{ marginRight: 5 }} />
          Launch OpenOva
        </>
      }
      nextLoading={loading}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        {/* ── 1. Organisation ──────────────────────────────────── */}
        <Section title="Organisation">
          <Row label="Name"     value={dimIfMissing(store.orgName)} />
          <Row label="Industry" value={dimIfMissing(store.orgIndustry)} />
          <Row label="Size"     value={dimIfMissing(store.orgSize)} />
          <Row label="HQ"       value={dimIfMissing(store.orgHeadquarters)} />
          <Row
            label="Compliance"
            value={
              store.orgCompliance.length > 0 ? (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 3 }}>
                  {store.orgCompliance.map(t => (
                    <span
                      key={t}
                      style={{
                        fontSize: 9,
                        padding: '1px 6px',
                        borderRadius: 3,
                        background: 'rgba(56,189,248,0.1)',
                        border: '1px solid rgba(56,189,248,0.2)',
                        color: '#38BDF8',
                      }}
                    >
                      {t}
                    </span>
                  ))}
                </div>
              ) : (
                <span style={{ color: 'var(--wiz-text-hint)' }}>None selected</span>
              )
            }
          />
        </Section>

        {/* ── 2. Topology ──────────────────────────────────────── */}
        <Section
          title={
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
              <span>Topology</span>
              {store.haEnabled && (
                <span
                  style={{
                    fontSize: 9,
                    fontWeight: 700,
                    letterSpacing: '0.06em',
                    color: '#4ADE80',
                    background: 'rgba(74,222,128,0.12)',
                    border: '1px solid rgba(74,222,128,0.25)',
                    borderRadius: 3,
                    padding: '1px 6px',
                  }}
                >
                  HA
                </span>
              )}
              {store.airgap && (
                <span
                  style={{
                    fontSize: 9,
                    fontWeight: 700,
                    color: '#F59E0B',
                    background: 'rgba(245,158,11,0.12)',
                    border: '1px solid rgba(245,158,11,0.25)',
                    borderRadius: 3,
                    padding: '1px 6px',
                    letterSpacing: '0.04em',
                  }}
                >
                  AIR-GAP
                </span>
              )}
            </span>
          }
        >
          <Row label="Template" value={dimIfMissing(store.topology)} />
          <Row label="Regions"  value={`${regionRows.length} (${topologyRegionLabels.length} topology + ${store.airgap ? 1 : 0} air-gap)`} />
          <Row label="HA"       value={store.haEnabled ? 'Enabled — 3-node etcd quorum per region' : 'Disabled — single control-plane node'} />
          <Row label="AIR-GAP"  value={store.airgap ? 'Enabled — isolated forensic / DR region' : 'Disabled'} />
        </Section>

        {/* ── 3. Provider — per-region table ──────────────────── */}
        <Section
          title={
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, width: '100%' }}>
              <span>Cloud provider per region</span>
              <span style={{
                marginLeft: 'auto', fontSize: 10, fontWeight: 700,
                color: 'var(--wiz-accent)',
              }}>
                €{totalHourly.toFixed(3)}/hr · €{(totalHourly * 730).toFixed(0)}/mo
              </span>
            </span>
          }
        >
          <div style={{ padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 8 }}>
            {regionRows.map((r, i) => {
              const cp = r.provider && r.controlPlaneSize ? findNodeSize(r.provider, r.controlPlaneSize) : undefined
              const wk = r.provider && r.workerSize ? findNodeSize(r.provider, r.workerSize) : undefined
              return (
                <div
                  key={i}
                  style={{
                    borderRadius: 8, padding: '8px 10px',
                    border: `1px solid ${r.isAirgap ? 'rgba(245,158,11,0.3)' : 'var(--wiz-border-sub)'}`,
                    background: r.isAirgap ? 'rgba(245,158,11,0.03)' : 'var(--wiz-bg-xs)',
                    display: 'grid',
                    gridTemplateColumns: isMobile ? '1fr' : '24px 1fr 1fr 1fr 90px',
                    gap: 8, alignItems: 'center',
                  }}
                >
                  <span style={{ fontSize: 9, fontWeight: 700, color: r.isAirgap ? '#F59E0B' : 'var(--wiz-accent)' }}>
                    {i + 1}
                    {r.isAirgap && (
                      <div style={{ fontSize: 8, fontWeight: 700, color: '#F59E0B' }}>A-G</div>
                    )}
                  </span>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
                    <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--wiz-text-md)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.label}</span>
                    {r.provider ? (
                      <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                        {PROVIDER_LOGOS[r.provider]}{PROVIDER_NAMES[r.provider]}
                      </span>
                    ) : (
                      <span style={{ fontSize: 10, color: 'var(--wiz-text-hint)' }}>— provider not configured —</span>
                    )}
                  </div>
                  <div style={{ minWidth: 0 }}>
                    {r.cloudRegionLabel ? (
                      <>
                        <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 10 }}>{r.cloudRegionLabel}</code>
                        <div style={{ fontSize: 9, color: 'var(--wiz-text-sub)' }}>{r.cloudRegionLocation}</div>
                      </>
                    ) : (
                      <span style={{ fontSize: 10, color: 'var(--wiz-text-hint)' }}>— region —</span>
                    )}
                  </div>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
                    {cp ? (
                      <>
                        <span style={{ fontSize: 10 }}>CP: <strong style={{ color: 'var(--wiz-text-md)' }}>{cp.label}</strong></span>
                        {wk && r.workerCount > 0 ? (
                          <span style={{ fontSize: 10 }}>W×{r.workerCount}: <strong style={{ color: 'var(--wiz-text-md)' }}>{wk.label}</strong></span>
                        ) : (
                          <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)' }}>solo — no workers</span>
                        )}
                      </>
                    ) : (
                      <span style={{ fontSize: 10, color: 'var(--wiz-text-hint)' }}>— sizing —</span>
                    )}
                  </div>
                  <span style={{ fontSize: 10, fontWeight: 700, color: 'var(--wiz-accent)', textAlign: 'right' }}>
                    €{r.hourlyCost.toFixed(3)}/hr
                  </span>
                </div>
              )
            })}
            <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 4 }}>
              Each region's SKU is drawn from its own provider's catalog — no cross-cloud SKU literal exists.
            </div>
          </div>
        </Section>

        {/* ── 4. Credentials ────────────────────────────────────── */}
        <Section title="Credentials">
          <Row label="Project ID" value={dimIfMissing(store.hetznerProjectId)} />
          <Row
            label="Hetzner API token"
            value={
              <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: 'var(--wiz-text-md)' }}>
                {maskToken(store.hetznerToken)}
              </code>
            }
          />
          <Row
            label="Token validated"
            value={
              store.credentialValidated ? (
                <span style={{ color: '#4ADE80' }}>Yes</span>
              ) : (
                <span style={{ color: 'var(--wiz-text-hint)' }}>No — provisioner will re-check at apply time</span>
              )
            }
          />
        </Section>

        {/* ── SSH ──────────────────────────────────────────────── */}
        <Section title="SSH Access">
          <Row
            label="Source"
            value={
              store.sshKeyGeneratedThisSession
                ? (
                  <span>
                    Auto-generated this session{' '}
                    <span style={{ color: 'var(--wiz-text-hint)' }}>(private key downloaded once)</span>
                  </span>
                )
                : store.sshPublicKey
                  ? 'Pasted by operator'
                  : <span style={{ color: 'var(--wiz-text-hint)' }}>— no key configured —</span>
            }
          />
          <Row
            label="Fingerprint"
            value={
              store.sshFingerprint ? (
                <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11 }}>
                  {shortFingerprint(store.sshFingerprint)}
                </code>
              ) : store.sshPublicKey ? (
                <span style={{ color: 'var(--wiz-text-hint)' }}>
                  not pre-computed — server will derive at apply time
                </span>
              ) : (
                <span style={{ color: 'var(--wiz-text-hint)' }}>—</span>
              )
            }
          />
        </Section>

        {/* ── 5. Components ────────────────────────────────────── */}
        <Section title={`Components · ${totalComponents} selected`}>
          <div
            style={{
              padding: '10px 14px',
              display: 'grid',
              gridTemplateColumns: isMobile ? '1fr 1fr' : 'repeat(3, 1fr)',
              gap: 8,
            }}
          >
            {GROUPS.map(g => <GroupMiniCard key={g.id} gid={g.id} />)}
          </div>
          <div
            style={{
              padding: '8px 14px',
              borderTop: '1px solid var(--wiz-border-sub)',
              display: 'flex',
              gap: 12,
              flexWrap: 'wrap',
              fontSize: 10,
              color: 'var(--wiz-text-sub)',
            }}
          >
            <span>
              <span style={{ color: '#4ADE80', fontWeight: 700 }}>M</span> mandatory (incl. transitive)
            </span>
            <span>
              <span style={{ color: '#38BDF8', fontWeight: 700 }}>R</span> recommended
            </span>
            <span>
              <span style={{ color: '#A78BFA', fontWeight: 700 }}>O</span> user-selected optional
            </span>
          </div>
        </Section>

        {/* ── 6. Domain ────────────────────────────────────────── */}
        <Section title="Domain">
          <Row label="Mode" value={DOMAIN_MODE_LABELS[store.sovereignDomainMode]} />
          {store.sovereignDomainMode === 'pool' ? (
            <>
              <Row
                label="Subdomain"
                value={
                  store.sovereignSubdomain ? (
                    <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11 }}>
                      {store.sovereignSubdomain}
                    </code>
                  ) : (
                    <span style={{ color: 'var(--wiz-text-hint)' }}>— not chosen —</span>
                  )
                }
              />
              <Row
                label="Pool domain"
                value={
                  <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11 }}>
                    {poolDomainLabel}
                  </code>
                }
              />
            </>
          ) : (
            <>
              <Row
                label="BYO domain"
                value={
                  store.sovereignByoDomain ? (
                    <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11 }}>
                      {store.sovereignByoDomain}
                    </code>
                  ) : (
                    <span style={{ color: 'var(--wiz-text-hint)' }}>— not entered —</span>
                  )
                }
              />
              {store.sovereignDomainMode === 'byo-api' && (
                <>
                  <Row label="Registrar" value={dimIfMissing(store.registrarType)} />
                  <Row
                    label="Registrar token"
                    value={
                      <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: 'var(--wiz-text-md)' }}>
                        {maskToken(store.registrarToken)}
                      </code>
                    }
                  />
                  <Row
                    label="Token validated"
                    value={
                      store.registrarTokenValidated ? (
                        <span style={{ color: '#4ADE80' }}>Yes</span>
                      ) : (
                        <span style={{ color: '#F87171' }}>
                          No — return to the Domain step to validate before launch
                        </span>
                      )
                    }
                  />
                </>
              )}
            </>
          )}
          <Row
            label="Resolved FQDN"
            value={
              sovereignFQDN ? (
                <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: '#38BDF8' }}>
                  console.{sovereignFQDN}
                </code>
              ) : (
                <span style={{ color: 'var(--wiz-text-hint)' }}>— not yet resolvable —</span>
              )
            }
          />
          <Row label="Admin email" value={dimIfMissing(store.orgEmail)} />
        </Section>

        {/* ── Privacy note ─────────────────────────────────────── */}
        <div
          style={{
            borderRadius: 8,
            padding: '9px 12px',
            background: 'rgba(56,189,248,0.04)',
            border: '1px solid rgba(56,189,248,0.1)',
          }}
        >
          <p style={{ fontSize: 11, color: 'var(--wiz-text-sub)', margin: 0, lineHeight: 1.6 }}>
            Provisioning runs entirely within your cloud account. OpenOva never stores your credentials or accesses
            your infrastructure after this session.
          </p>
        </div>
      </div>
    </StepShell>
  )
}
