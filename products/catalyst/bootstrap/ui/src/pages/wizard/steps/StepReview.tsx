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
 * selected component renders as its own ComponentMiniCard, pixel-mirrored
 * from the marketplace `.stack-card` on
 * https://marketplace.openova.io/review/, so the operator can confirm
 * exactly what will be installed in the same visual rhythm they see
 * across every other Catalyst surface.
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
import { API_BASE } from '@/shared/config/urls'
import { useRouter } from '@tanstack/react-router'
import { StepShell, useStepNav } from './_shared'
import {
  GROUPS,
  findComponent,
  type ComponentEntry,
} from './componentGroups'
import { getLogoToneStyle } from './logoTone'

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
        /* Section sits one elevation BELOW the cards inside it so the
           card surfaces (--wiz-bg-input / --wiz-bg-card) lift visibly off
           the section in both wizard themes. The previous --wiz-bg-xs
           was the same near-white as the cards in light mode → cards
           visually melted into the section ("white-on-white"). */
        background: 'var(--wiz-bg-sub)',
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
        /* Field chip — sits one elevation ABOVE the Section surface so
           every chip lifts off the parent section in both wizard themes.
           The previous hardcoded rgba(255,255,255,0.02) was invisible in
           light mode (white over white). --wiz-bg-input flips to #f8fafc
           in light (clearly above --wiz-bg-sub #f4f6f8 section) and to
           rgba(255,255,255,0.05) in dark (clearly above --wiz-bg-sub
           rgba(255,255,255,0.025) section). */
        background: 'var(--wiz-bg-input)',
        border: '1px solid var(--wiz-border-sub)',
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

/* ── Per-component mini card (one card per selected component) ────
   Pixel-mirrors the canonical `.stack-card` on
   https://marketplace.openova.io/review/ — the SME marketplace's review
   surface. Same horizontal flex layout, same 40×40 logo tile, same
   semibold name + low-key category pill + single-line description. The
   review is the launch-confirmation surface and inherits the marketplace
   "compact card grid" review aesthetic verbatim. Wizard tokens map to
   the marketplace tokens 1:1 (light theme):

     marketplace `--color-bg`           → wizard `--wiz-bg-input`
     marketplace `--color-border`       → wizard `--wiz-border`
     marketplace `--color-text-strong`  → wizard `--wiz-text-hi`
     marketplace `--color-text-dim`     → wizard `--wiz-text-md` (desc),
                                                  `--wiz-text-sub` (cat)

   Tier (M/R/O) is intentionally NOT shown — the canonical stack-card has
   no tier indicator; the Components step prior to Review already enforces
   tier semantics, and the review's job is to mirror the marketplace card
   shape exactly. The category pill renders `entry.groupName` (PILOT,
   SPINE, …) which is the wizard equivalent of `app.category`. */

function LetterFallback({ entry }: { entry: ComponentEntry }) {
  const letter = (entry.name[0] ?? '?').toUpperCase()
  const tone = getLogoToneStyle(entry.id)
  return (
    <span
      aria-hidden
      style={{
        // .stack-icon equivalent — same 40×40, 10px-radius pill so the
        // logo column geometry is identical whether or not the component
        // has a vendored SVG. Per-brand surface (see logoTone.ts) so the
        // letter-mark fallback inherits the same tile colour the
        // `<img>` tiles use elsewhere on the wizard and marketplace.
        width: 40,
        height: 40,
        borderRadius: 10,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        color: tone.text,
        fontSize: 14,
        fontWeight: 700,
        background: tone.background,
        border: `1px solid ${tone.border}`,
      }}
    >
      {letter}
    </span>
  )
}

function ComponentMiniCard({ entry }: { entry: ComponentEntry }) {
  const tone = getLogoToneStyle(entry.id)
  return (
    <div
      data-testid={`review-component-${entry.id}`}
      data-component-id={entry.id}
      data-tier={entry.tier}
      data-product={entry.product}
      style={{
        // .stack-card — display:flex; align-items:flex-start; gap:0.65rem;
        // padding:0.65rem; background:var(--color-bg); border-radius:8px;
        // border:1px solid var(--color-border); transition:border-color 0.15s.
        display: 'flex',
        alignItems: 'flex-start',
        gap: '0.65rem',
        padding: '0.65rem',
        background: 'var(--wiz-bg-input)',
        borderRadius: 8,
        border: '1px solid var(--wiz-border)',
        color: 'inherit',
        textDecoration: 'none',
        transition: 'border-color 0.15s',
        minWidth: 0,
      }}
    >
      {entry.logoUrl ? (
        <span
          aria-hidden
          style={{
            // .stack-logo — 40×40, 10px radius, flex-shrink:0. Per-brand
            // surface (see logoTone.ts) — each tile uses the brand's own
            // canonical surface colour (Alloy on Grafana orange, FerretDB
            // on its navy, Temporal on its blue, …). Mirrors how each
            // project displays its mark on its own homepage.
            width: 40,
            height: 40,
            borderRadius: 10,
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            flexShrink: 0,
            background: tone.background,
            border: `1px solid ${tone.border}`,
            overflow: 'hidden',
            padding: 4,
            boxSizing: 'border-box',
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
        <LetterFallback entry={entry} />
      )}
      {/* .stack-body — flex:1; min-width:0. */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <span
          style={{
            // .stack-name — 0.82rem / 600 / line-height:1.2 / 0.4rem
            // right margin to give the cat pill breathing room.
            color: 'var(--wiz-text-hi)',
            fontSize: '0.82rem',
            fontWeight: 600,
            lineHeight: 1.2,
            marginRight: '0.4rem',
          }}
          title={entry.name}
        >
          {entry.name}
        </span>
        <span
          style={{
            // .stack-cat — 0.62rem / capitalize / 0.08rem 0.35rem padding
            // / 3px radius / color-mix(border 50%, transparent) bg.
            color: 'var(--wiz-text-sub)',
            fontSize: '0.62rem',
            textTransform: 'capitalize',
            background: 'color-mix(in srgb, var(--wiz-border) 50%, transparent)',
            padding: '0.08rem 0.35rem',
            borderRadius: 3,
          }}
        >
          {entry.groupName}
        </span>
        <p
          style={{
            // .stack-desc — 0.72rem / line-height:1.4 / margin-top:0.2rem
            // / single-line clamp via -webkit-box (matches the canonical
            // -webkit-line-clamp: 1 rule on the marketplace card).
            margin: '0.2rem 0 0',
            color: 'var(--wiz-text-md)',
            fontSize: '0.72rem',
            lineHeight: 1.4,
            display: '-webkit-box',
            WebkitLineClamp: 1,
            WebkitBoxOrient: 'vertical',
            overflow: 'hidden',
          }}
        >
          {entry.desc}
        </p>
      </div>
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
        /* RegionCard sits on the Section surface (--wiz-bg-sub) so it
           uses --wiz-bg-input one elevation above for visible card lift
           in both themes. Air-gap row keeps its amber tint. */
        background: row.isAirgap ? 'rgba(245,158,11,0.03)' : 'var(--wiz-bg-input)',
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
  const router = useRouter()
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
      router.navigate({
        to: '/provision/$deploymentId',
        params: { deploymentId: data.id },
      })
    } catch (err) {
      alert(`Failed to start provisioning: ${err}`)
      setLoading(false)
      return
    }
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

        {/* ── 5. Components ──────────────────────────────────────
            Pixel-mirrors the canonical `.stack-grid` /  `.stack-card`
            layout from https://marketplace.openova.io/review/. The
            family-summary mini-card overview that previously lived above
            the per-component grid was removed: every selected component
            already renders as its own card with the family chip baked in
            (`entry.groupName`), so a separate family-count strip was a
            duplicate read of the same data. The tier legend below the
            grid was likewise dropped — the marketplace stack-card has no
            tier indicator, and the component step before review already
            enforces tier semantics. The grid now uses the same `repeat(2,
            1fr)` columns the marketplace ships, collapsing to a single
            column under 700px. */}
        <Section
          title={`Components · ${totalComponents} selected`}
          testId="review-section-components"
          bodyPadding="0.85rem 1rem"
        >
          <div
            data-testid="review-component-cards"
            className="review-stack-grid"
            style={{
              // .stack-grid — repeat(2, 1fr); gap: 0.5rem. The
              // single-column collapse is handled by the matching
              // <style> below so we do not need a JS media-query.
              display: 'grid',
              gridTemplateColumns: 'repeat(2, 1fr)',
              gap: '0.5rem',
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
          <style>{`
            @media (max-width: 700px) {
              .review-stack-grid { grid-template-columns: 1fr !important; }
            }
          `}</style>
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
