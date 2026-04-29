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
 *                           catalog (PROVIDER_NODE_SIZES[provider]); CPX32
 *                           does not exist on Azure, m6i.xlarge does not
 *                           exist on Hetzner. The footer rolls each
 *                           region's (cp + worker*count) into the total.
 *   4. Credentials        — Hetzner project ID + masked token + SSH key
 *   5. Components         — product-family overview + per-component cards
 *   6. Domain             — pool subdomain + FQDN OR BYO + admin email
 *
 * ── Layout density (#review-density) ──────────────────────────────────
 * Sections lay their content out as `auto-fill / minmax(...)` CSS grids
 * so multiple small cards pack into the same row whenever the viewport
 * has room. The Components section is the canonical example: every
 * selected component renders as its own ComponentMiniCard rather than a
 * single per-family summary, so the operator can confirm exactly what
 * will be installed. The family overview chips remain above the
 * per-component grid for at-a-glance counts.
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
import { API_BASE, path } from '@/shared/config/urls'
import { StepShell, useStepNav } from './_shared'
import {
  GROUPS,
  findComponent,
  type ComponentEntry,
} from './componentGroups'
import { familyChipPalette } from '@/pages/marketplace/marketplaceCopy'

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
  bodyPadding = '8px 12px',
  testId,
  headerExtra,
}: {
  title: React.ReactNode
  children: React.ReactNode
  bodyPadding?: string | number
  testId?: string
  headerExtra?: React.ReactNode
}) {
  return (
    <div
      data-testid={testId}
      style={{
        borderRadius: 10,
        border: '1px solid var(--wiz-border-sub)',
        background: 'var(--wiz-bg-xs)',
        overflow: 'hidden',
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      <div style={{ padding: '5px 12px', borderBottom: '1px solid var(--wiz-border-sub)', flexShrink: 0, display: 'flex', alignItems: 'center', gap: 8 }}>
        <span
          style={{
            fontSize: 10,
            fontWeight: 700,
            letterSpacing: '0.12em',
            textTransform: 'uppercase',
            color: 'var(--wiz-text-sub)',
            display: 'inline-flex',
            alignItems: 'center',
            gap: 8,
          }}
        >
          {title}
        </span>
        {headerExtra && <div style={{ marginLeft: 'auto' }}>{headerExtra}</div>}
      </div>
      <div style={{ padding: bodyPadding, flex: 1 }}>{children}</div>
    </div>
  )
}

/* ── Field — compact label-on-top chip used inside multi-column grids ── */
function Field({
  label,
  value,
  fullWidth = false,
}: {
  label: string
  value: React.ReactNode
  fullWidth?: boolean
}) {
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 2,
        padding: '5px 8px',
        borderRadius: 6,
        background: 'rgba(255,255,255,0.02)',
        border: '1px solid rgba(255,255,255,0.04)',
        gridColumn: fullWidth ? '1 / -1' : undefined,
        minWidth: 0,
      }}
    >
      <span
        style={{
          fontSize: 9,
          fontWeight: 700,
          letterSpacing: '0.08em',
          textTransform: 'uppercase',
          color: 'var(--wiz-text-sub)',
          lineHeight: 1.3,
        }}
      >
        {label}
      </span>
      <span
        style={{
          fontSize: 11,
          color: 'var(--wiz-text-md)',
          lineHeight: 1.4,
          wordBreak: 'break-word',
          minWidth: 0,
        }}
      >
        {value}
      </span>
    </div>
  )
}

/* ── Multi-column field grid (auto-fill so columns collapse on narrow viewports) ── */
function FieldGrid({
  minColumnWidth = 180,
  gap = 6,
  children,
}: {
  minColumnWidth?: number
  gap?: number
  children: React.ReactNode
}) {
  return (
    <div
      style={{
        display: 'grid',
        gridTemplateColumns: `repeat(auto-fill, minmax(${minColumnWidth}px, 1fr))`,
        gap,
      }}
    >
      {children}
    </div>
  )
}

/* ── Component group mini-card (overview header) ─────────────────── */
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
        padding: '6px 8px',
        border: `1px solid ${hasAny ? 'var(--wiz-border-sub)' : 'rgba(255,255,255,0.04)'}`,
        background: hasAny ? 'var(--wiz-bg-xs)' : 'transparent',
        opacity: hasAny ? 1 : 0.38,
        display: 'flex',
        flexDirection: 'column',
        gap: 3,
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

/* ── Per-component mini card (one card per selected component) ──── */
const TIER_BADGE: Record<'mandatory' | 'recommended' | 'optional', { letter: string; bg: string; fg: string; label: string }> = {
  mandatory:   { letter: 'M', bg: 'rgba(74,222,128,0.14)', fg: '#4ADE80', label: 'mandatory (incl. transitive)' },
  recommended: { letter: 'R', bg: 'rgba(56,189,248,0.14)', fg: '#38BDF8', label: 'recommended' },
  optional:    { letter: 'O', bg: 'rgba(167,139,250,0.14)', fg: '#A78BFA', label: 'user-selected optional' },
}

function LetterFallback({ name }: { name: string }) {
  const letter = (name[0] ?? '?').toUpperCase()
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = (hash * 31 + name.charCodeAt(i)) | 0
  const hue = Math.abs(hash) % 360
  return (
    <span
      aria-hidden
      style={{
        width: 22,
        height: 22,
        borderRadius: 6,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        color: '#fff',
        fontSize: 11,
        fontWeight: 700,
        background: `oklch(58% 0.12 ${hue})`,
      }}
    >
      {letter}
    </span>
  )
}

function ComponentMiniCard({ entry }: { entry: ComponentEntry }) {
  const palette = familyChipPalette(entry.product)
  const tier = TIER_BADGE[entry.tier]
  return (
    <div
      data-testid={`review-component-${entry.id}`}
      data-component-id={entry.id}
      data-tier={entry.tier}
      data-product={entry.product}
      style={{
        borderRadius: 7,
        padding: '6px 8px',
        border: '1px solid var(--wiz-border-sub)',
        background: 'var(--wiz-bg-xs)',
        display: 'flex',
        flexDirection: 'column',
        gap: 4,
        minWidth: 0,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
        {entry.logoUrl ? (
          <span
            aria-hidden
            style={{
              width: 22,
              height: 22,
              borderRadius: 6,
              display: 'inline-flex',
              alignItems: 'center',
              justifyContent: 'center',
              flexShrink: 0,
              background: 'rgba(255,255,255,0.04)',
              overflow: 'hidden',
            }}
          >
            <img
              src={entry.logoUrl}
              alt=""
              loading="lazy"
              data-testid={`review-component-logo-${entry.id}`}
              style={{ width: '100%', height: '100%', objectFit: 'contain', display: 'block' }}
            />
          </span>
        ) : (
          <LetterFallback name={entry.name} />
        )}
        <span
          style={{
            fontSize: 11,
            fontWeight: 600,
            color: 'var(--wiz-text-hi)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            flex: 1,
            minWidth: 0,
          }}
          title={entry.name}
        >
          {entry.name}
        </span>
        <span
          title={tier.label}
          style={{
            fontSize: 9,
            fontWeight: 700,
            color: tier.fg,
            background: tier.bg,
            borderRadius: 3,
            padding: '1px 5px',
            flexShrink: 0,
          }}
        >
          {tier.letter}
        </span>
      </div>
      <span
        style={{
          fontSize: 9,
          fontWeight: 700,
          letterSpacing: '0.08em',
          color: palette.fg,
          background: palette.bg,
          border: `1px solid ${palette.border}`,
          borderRadius: 999,
          padding: '1px 6px',
          alignSelf: 'flex-start',
        }}
      >
        {entry.groupName}
      </span>
    </div>
  )
}

/* ── Per-region card (Provider section) ──────────────────────────── */
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

function RegionCard({ row, index }: { row: ReviewRegionRow; index: number }) {
  const cp = row.provider && row.controlPlaneSize ? findNodeSize(row.provider, row.controlPlaneSize) : undefined
  const wk = row.provider && row.workerSize ? findNodeSize(row.provider, row.workerSize) : undefined
  return (
    <div
      data-testid={`review-region-${index}`}
      style={{
        borderRadius: 8,
        padding: '8px 10px',
        border: `1px solid ${row.isAirgap ? 'rgba(245,158,11,0.3)' : 'var(--wiz-border-sub)'}`,
        background: row.isAirgap ? 'rgba(245,158,11,0.03)' : 'var(--wiz-bg-xs)',
        display: 'flex',
        flexDirection: 'column',
        gap: 5,
        minWidth: 0,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <span
          style={{
            fontSize: 9,
            fontWeight: 700,
            letterSpacing: '0.1em',
            textTransform: 'uppercase',
            color: row.isAirgap ? '#F59E0B' : 'var(--wiz-text-sub)',
          }}
        >
          {row.isAirgap ? `Region ${index + 1} · AIR-GAP` : `Region ${index + 1}`}
        </span>
        <span
          style={{
            marginLeft: 'auto',
            fontSize: 10,
            fontWeight: 700,
            color: 'var(--wiz-accent)',
          }}
        >
          €{row.hourlyCost.toFixed(3)}/hr
        </span>
      </div>
      <span
        style={{
          fontSize: 11,
          fontWeight: 600,
          color: 'var(--wiz-text-md)',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}
        title={row.label}
      >
        {row.label}
      </span>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        {row.provider ? (
          <>
            {PROVIDER_LOGOS[row.provider]}
            <span style={{ fontSize: 11, color: 'var(--wiz-text-md)' }}>{PROVIDER_NAMES[row.provider]}</span>
          </>
        ) : (
          <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>— provider not configured —</span>
        )}
      </div>
      <div style={{ fontSize: 11, color: 'var(--wiz-text-md)', minWidth: 0 }}>
        {row.cloudRegionLabel ? (
          <span>
            <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 10 }}>{row.cloudRegionLabel}</code>
            <span style={{ color: 'var(--wiz-text-sub)' }}> · {row.cloudRegionLocation}</span>
          </span>
        ) : (
          <span style={{ color: 'var(--wiz-text-hint)' }}>— region not selected —</span>
        )}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 1, fontSize: 10 }}>
        {cp ? (
          <span>
            CP: <strong style={{ color: 'var(--wiz-text-md)' }}>{cp.label}</strong>
          </span>
        ) : (
          <span style={{ color: 'var(--wiz-text-hint)' }}>— CP sizing —</span>
        )}
        {wk && row.workerCount > 0 ? (
          <span>
            W×{row.workerCount}: <strong style={{ color: 'var(--wiz-text-md)' }}>{wk.label}</strong>
          </span>
        ) : (
          <span style={{ color: 'var(--wiz-text-sub)' }}>solo — no workers</span>
        )}
      </div>
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
  const [loading, setLoading] = useState(false)

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

  // Per-component card data — every selected component id, materialised
  // through the catalog so we can render logo + family chip + tier badge.
  // GROUPS-ordered so visually the cards group by family without us
  // having to manually section them.
  const selectedComponentEntries: ComponentEntry[] = (() => {
    const out: ComponentEntry[] = []
    const seen = new Set<string>()
    for (const g of GROUPS) {
      const ids = store.componentGroups[g.id] ?? []
      for (const id of ids) {
        if (seen.has(id)) continue
        seen.add(id)
        const entry = findComponent(id)
        if (entry) out.push(entry)
      }
    }
    return out
  })()

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
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {/* ── 1. Organisation ──────────────────────────────────── */}
        <Section title="Organisation" testId="review-section-organisation">
          <FieldGrid minColumnWidth={170}>
            <Field label="Name"     value={dimIfMissing(store.orgName)} />
            <Field label="Industry" value={dimIfMissing(store.orgIndustry)} />
            <Field label="Size"     value={dimIfMissing(store.orgSize)} />
            <Field label="HQ"       value={dimIfMissing(store.orgHeadquarters)} />
            <Field
              label="Compliance"
              fullWidth
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
          </FieldGrid>
        </Section>

        {/* ── 2. Topology ──────────────────────────────────────── */}
        <Section
          testId="review-section-topology"
          title={
            <>
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
            </>
          }
        >
          <FieldGrid minColumnWidth={170}>
            <Field label="Template" value={dimIfMissing(store.topology)} />
            <Field
              label="Regions"
              value={`${regionRows.length} (${topologyRegionLabels.length} topology + ${store.airgap ? 1 : 0} air-gap)`}
            />
            <Field label="HA"      value={store.haEnabled ? 'Enabled — 3-node etcd quorum per region' : 'Disabled — single CP node'} />
            <Field label="AIR-GAP" value={store.airgap ? 'Enabled — isolated forensic / DR region' : 'Disabled'} />
          </FieldGrid>
        </Section>

        {/* ── 3. Provider — per-region cards (one card per region) ── */}
        <Section
          testId="review-section-provider"
          title="Cloud provider per region"
          headerExtra={
            <span style={{ fontSize: 10, fontWeight: 700, color: 'var(--wiz-accent)' }}>
              €{totalHourly.toFixed(3)}/hr · €{(totalHourly * 730).toFixed(0)}/mo
            </span>
          }
          bodyPadding={0}
        >
          <div
            style={{
              padding: '8px 12px',
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
              gap: 8,
            }}
          >
            {regionRows.map((r, i) => (
              <RegionCard key={i} row={r} index={i} />
            ))}
          </div>
          <div
            style={{
              padding: '4px 12px 8px',
              fontSize: 10,
              color: 'var(--wiz-text-sub)',
            }}
          >
            Each region's SKU is drawn from its own provider's catalog — no cross-cloud SKU literal exists.
          </div>
        </Section>

        {/* ── 4. Credentials ──────────────────────────────────── */}
        <Section title="Credentials" testId="review-section-credentials">
          <FieldGrid minColumnWidth={220}>
            <Field label="Project ID" value={dimIfMissing(store.hetznerProjectId)} />
            <Field
              label="Hetzner API token"
              value={
                <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: 'var(--wiz-text-md)' }}>
                  {maskToken(store.hetznerToken)}
                </code>
              }
            />
            <Field
              label="Token validated"
              value={
                store.credentialValidated ? (
                  <span style={{ color: '#4ADE80' }}>Yes</span>
                ) : (
                  <span style={{ color: 'var(--wiz-text-hint)' }}>No — provisioner re-checks at apply time</span>
                )
              }
            />
          </FieldGrid>
        </Section>

        {/* ── SSH ──────────────────────────────────────────────── */}
        <Section title="SSH Access" testId="review-section-ssh">
          <FieldGrid minColumnWidth={220}>
            <Field
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
            <Field
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
          </FieldGrid>
        </Section>

        {/* ── 5. Components ────────────────────────────────────── */}
        <Section
          title={`Components · ${totalComponents} selected`}
          testId="review-section-components"
          bodyPadding={0}
        >
          {/* Family overview — at-a-glance counts per product family.
              Kept above the per-component grid so the operator can scan
              "every family I expected" without counting cards. */}
          <div
            data-testid="review-component-families"
            style={{
              padding: '8px 12px',
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))',
              gap: 6,
              borderBottom: '1px solid var(--wiz-border-sub)',
            }}
          >
            {GROUPS.map(g => <GroupMiniCard key={g.id} gid={g.id} />)}
          </div>

          {/* Per-component grid — one card per selected component (incl.
              mandatory). Operator sees EVERYTHING that will be installed.
              auto-fill / minmax(180px) so 4-6 cards fit per row at 1440px. */}
          <div
            data-testid="review-component-cards"
            style={{
              padding: '8px 12px',
              display: 'grid',
              gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))',
              gap: 6,
            }}
          >
            {selectedComponentEntries.length > 0 ? (
              selectedComponentEntries.map(entry => (
                <ComponentMiniCard key={entry.id} entry={entry} />
              ))
            ) : (
              <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>
                No components selected — return to the Components step.
              </span>
            )}
          </div>

          {/* Tier legend */}
          <div
            style={{
              padding: '6px 12px',
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
        <Section title="Domain" testId="review-section-domain">
          <FieldGrid minColumnWidth={200}>
            <Field label="Mode" value={DOMAIN_MODE_LABELS[store.sovereignDomainMode]} />
            {store.sovereignDomainMode === 'pool' ? (
              <>
                <Field
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
                <Field
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
                <Field
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
                    <Field label="Registrar" value={dimIfMissing(store.registrarType)} />
                    <Field
                      label="Registrar token"
                      value={
                        <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: 'var(--wiz-text-md)' }}>
                          {maskToken(store.registrarToken)}
                        </code>
                      }
                    />
                    <Field
                      label="Token validated"
                      value={
                        store.registrarTokenValidated ? (
                          <span style={{ color: '#4ADE80' }}>Yes</span>
                        ) : (
                          <span style={{ color: '#F87171' }}>
                            No — return to Domain step to validate
                          </span>
                        )
                      }
                    />
                  </>
                )}
              </>
            )}
            <Field label="Admin email" value={dimIfMissing(store.orgEmail)} />
            <Field
              label="Resolved FQDN"
              fullWidth
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
          </FieldGrid>
        </Section>

        {/* ── Privacy note ─────────────────────────────────────── */}
        <div
          style={{
            borderRadius: 8,
            padding: '7px 12px',
            background: 'rgba(56,189,248,0.04)',
            border: '1px solid rgba(56,189,248,0.1)',
          }}
        >
          <p style={{ fontSize: 11, color: 'var(--wiz-text-sub)', margin: 0, lineHeight: 1.5 }}>
            Provisioning runs entirely within your cloud account. OpenOva never stores your credentials or accesses
            your infrastructure after this session.
          </p>
        </div>
      </div>
    </StepShell>
  )
}
