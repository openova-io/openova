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
 *   3. Provider           — the cloud, and per region: region, zone, control-
 *                           plane flavor, worker flavor+count and display
 *                           price, all read from the cloud profile
 *                           (GET /api/v1/providers/{provider}). The footer
 *                           sums the display prices when every flavor has one.
 *   4. Credentials        — the credential fields of the profile (secret
 *                           ones masked) + SSH key
 *   5. Components         — product-family overview + per-component cards
 *   6. Domain             — pool subdomain + FQDN OR BYO; Sovereign
 *                            owner row reads from session.email since
 *                            #762 dropped the wizard-captured admin
 *                            email (PR #759 enforces the binding
 *                            server-side).
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
 * Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) every secret
 * credential field and any registrar token are rendered as a fixed-length mask plus
 * the character count — never the plaintext. Same posture as the SSH
 * private key.
 */

import { useState } from 'react'
import { Zap } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  TOPOLOGY_REGION_LABELS,
  resolveSovereignDomain,
  SOVEREIGN_POOL_DOMAINS,
  browserTimeZone,
} from '@/entities/deployment/model'
import {
  useProfile, findFlavor, describeFlavor, totalPrice, credentialMap,
  type CloudProfile, type Flavor, type ProfileRegion,
} from '@/lib/providers.api'
import { API_BASE } from '@/shared/config/urls'
import { useRouter } from '@tanstack/react-router'
import { useSession } from '@/shared/lib/useSession'
import { PinSignInModal } from '@/widgets/auth/PinSignInModal'
import { StepShell, useStepNav } from './_shared'
import { RINGS } from './componentGroups.generated'
import {
  CONTROL_PLANE_OVERHEAD,
  recommendedWorkers,
  sumFootprints,
  workerCapacity,
  type ComponentFootprint,
} from './componentFootprints'

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

/* ── Per-region card (Provider section) ──────────────────────────── */
interface ReviewRegionRow {
  label: string
  cloudRegion: string
  zone: string
  /** The region of the profile with the code cloudRegion; undefined while the profile is not read. */
  region: ProfileRegion | undefined
  controlPlaneSize: string
  workerSize: string
  workerCount: number
  cp: Flavor | undefined
  wk: Flavor | undefined
  isAirgap: boolean
}

function RegionCard({ row, index, cloud }: { row: ReviewRegionRow; index: number; cloud: string }) {
  const { cp, wk } = row
  const price = totalPrice([{ flavor: cp, count: 1 }, { flavor: wk, count: row.workerCount }])
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
        {price && (
          <span
            style={{
              marginLeft: 'auto',
              fontSize: 10,
              fontWeight: 700,
              color: 'var(--wiz-accent)',
            }}
          >
            {price.amount.toFixed(2)} {price.currency}/{price.per}
          </span>
        )}
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
        {cloud ? (
          <span style={{ fontSize: 11, color: 'var(--wiz-text-md)' }}>{cloud}</span>
        ) : (
          <span style={{ fontSize: 11, color: 'var(--wiz-text-hint)' }}>— provider not configured —</span>
        )}
      </div>
      <div style={{ fontSize: 11, color: 'var(--wiz-text-md)', minWidth: 0 }}>
        {row.cloudRegion ? (
          <span>
            <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 10 }}>{row.region?.label ?? row.cloudRegion}</code>
            <span style={{ color: 'var(--wiz-text-sub)' }}> · {row.zone}{row.region && ` · ${row.region.location}`}</span>
          </span>
        ) : (
          <span style={{ color: 'var(--wiz-text-hint)' }}>— region not selected —</span>
        )}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 1, fontSize: 10 }}>
        {row.controlPlaneSize ? (
          <span>
            CP: <strong style={{ color: 'var(--wiz-text-md)' }}>{row.controlPlaneSize}</strong>{cp && ` · ${describeFlavor(cp)}`}
          </span>
        ) : (
          <span style={{ color: 'var(--wiz-text-hint)' }}>— CP sizing —</span>
        )}
        {row.workerSize && row.workerCount > 0 ? (
          <span>
            W×{row.workerCount}: <strong style={{ color: 'var(--wiz-text-md)' }}>{row.workerSize}</strong>{wk && ` · ${describeFlavor(wk)}`}
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

/* ── Footprint formatters (issue #767) ───────────────────────────────
   The wizard's footprint estimate surfaces RAM in GB (with one decimal
   when the value is below 10 GB so a single-digit-MB shift on an
   optional component does not flicker the rollup) and CPU in whole
   vCPU. Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the
   conversion divisors live in componentFootprints.ts; the formatters
   here are display-only. */
function formatRam(ramMb: number): string {
  if (ramMb === 0) return '0 GB'
  const gb = ramMb / 1024
  if (gb < 10) return `${gb.toFixed(1)} GB`
  return `${Math.round(gb)} GB`
}
function formatCpu(cpuMilli: number): string {
  if (cpuMilli === 0) return '0 vCPU'
  const cpu = cpuMilli / 1000
  if (cpu < 10) return `${cpu.toFixed(1)} vCPU`
  return `${Math.round(cpu)} vCPU`
}

/* ── StepReview ──────────────────────────────────────────────────── */
export function StepReview() {
  const store = useWizardStore()
  const router = useRouter()
  const session = useSession()
  const { back } = useStepNav()
  const [loading, setLoading] = useState(false)
  // Issue #689 — anonymous-launch flow. When the operator clicks
  // [Launch OpenOva] without a session, we open the PIN modal first;
  // on successful verify, we re-trigger provision().
  const [pinModalOpen, setPinModalOpen] = useState(false)

  /* ── Derived values for display + POST body ──────────────────── */
  const sovereignFQDN = resolveSovereignDomain(store)

  // Pool domain row label (pool mode only): the wizard stores the pool
  // *id* ('omani-works'); the row needs the human-readable domain.
  const poolDomainLabel =
    SOVEREIGN_POOL_DOMAINS.find(p => p.id === store.sovereignPoolDomain)?.domain ?? store.sovereignPoolDomain

  // The profile of the chosen cloud: the labels, flavors and prices shown
  // below. The POST body is the store's and does not wait for it.
  const loaded = useProfile(store.provider, store.installation)
  const profile: CloudProfile | null = loaded?.state === 'ready' ? loaded.value : null
  const provider = store.provider ?? ''

  // Per-region payload — built once and used for both the on-screen
  // table and the POST body. Every region names the one provider of the
  // request; provisioner.Validate mirrors regions[0] into the legacy
  // singular fields, so the body does not carry them.
  const topologyRegionLabels = store.topology
    ? TOPOLOGY_REGION_LABELS[store.topology] ?? ['Region 1']
    : ['Region 1']
  const allRegionLabels = store.airgap
    ? [...topologyRegionLabels, 'AIR-GAP Region']
    : topologyRegionLabels
  const regionRows: ReviewRegionRow[] = allRegionLabels.map((label, i) => {
    const cloudRegion = store.regionCloudRegions[i] ?? ''
    const cpId = store.regionControlPlaneSizes[i] ?? ''
    const wkId = store.regionWorkerSizes[i] ?? ''
    return {
      label,
      cloudRegion,
      zone: store.regionZones[i] ?? '',
      region: profile?.regions.find(r => r.code === cloudRegion),
      controlPlaneSize: cpId,
      workerSize: wkId,
      workerCount: store.regionWorkerCounts[i] ?? 0,
      cp: profile ? findFlavor(profile, cpId) : undefined,
      wk: profile ? findFlavor(profile, wkId) : undefined,
      isAirgap: store.airgap && i === topologyRegionLabels.length,
    }
  })
  const regionsPayload = regionRows.map((r) => ({
    provider,
    cloudRegion:      r.cloudRegion,
    zone:             r.zone,
    controlPlaneSize: r.controlPlaneSize,
    workerSize:       r.workerSize,
    workerCount:      r.workerCount,
  }))
  const total = totalPrice(regionRows.flatMap(r => [{ flavor: r.cp, count: 1 }, { flavor: r.wk, count: r.workerCount }]))
  const credentials = credentialMap(store.credentials)
  // #4706 admission floor (#5661) — multi-region is the BCP default; a
  // single-region prov must DECLARE itself or CreateDeployment rejects the
  // body with HTTP 400 before Validate ever runs. The operator picking a
  // one-region topology card on StepTopology IS the deliberate, declared
  // act the floor demands — thread it to the wire. Multi-region topologies
  // leave the field unset (JSON.stringify drops undefined), preserving the
  // server's multi-region default semantics unchanged.
  const bcpTopology = regionsPayload.length < 2 ? 'single-region' : undefined

  // Every release of every ring is installed (the kit list generated by
  // tools/guards/cmd/kit-rings); the wizard selects nothing.
  const totalReleases = RINGS.reduce((n, ring) => n + ring.releases.length, 0)

  /* ── Footprint estimate (issue #767) ─────────────────────────────
     The kit footprint is the sum of the footprints of every kit release
     of every ring (COMPONENT_FOOTPRINTS is keyed by the Blueprint name
     without its bp- prefix). The TOTAL footprint = kit + control-plane
     overhead (k3s itself). We compute that against the OPERATOR'S worker
     SKU for Region 1 to derive a "minimum workers" recommendation that
     surfaces a WARN row when the operator picked fewer workers than
     the rollup needs. */
  const baselineFootprint: ComponentFootprint = sumFootprints(
    RINGS.flatMap(ring => ring.releases.map(r => r.blueprint.replace(/^bp-/, ''))),
  )
  const totalFootprint: ComponentFootprint = {
    ramMb: baselineFootprint.ramMb + CONTROL_PLANE_OVERHEAD.ramMb,
    cpuMilli: baselineFootprint.cpuMilli + CONTROL_PLANE_OVERHEAD.cpuMilli,
  }
  // Per-worker capacity for Region 1's chosen worker flavor. When the
  // operator hasn't picked a worker flavor yet (or the profile is not read
  // yet) we fall back to a sensible floor (4 vCPU / 8 GiB) so the footprint
  // section still surfaces a useful estimate before they touch the Provider step.
  const r0Worker = regionRows[0]?.wk
  const perWorkerCapacity: ComponentFootprint = r0Worker
    ? workerCapacity(r0Worker.vcpu, r0Worker.memoryGiB)
    : workerCapacity(4, 8)
  const recommendedWorkerCount = recommendedWorkers(perWorkerCapacity, {
    // The recommendation accounts for the work that lands on workers —
    // i.e. the kit. The CP overhead lives on the control-plane node,
    // not workers, so it's excluded from the worker-pool sizing math.
    ramMb: baselineFootprint.ramMb,
    cpuMilli: baselineFootprint.cpuMilli,
  })
  const operatorWorkerCount = regionRows[0]?.workerCount ?? 0
  const workerShortfall = operatorWorkerCount > 0 && operatorWorkerCount < recommendedWorkerCount
  const r0WorkerLabel = r0Worker?.name ?? '—'
  const r0WorkerVcpu = r0Worker?.vcpu ?? 0
  const r0WorkerRam = r0Worker?.memoryGiB ?? 0

  /* ── Submission ─────────────────────────────────────────────── */
  // Issue #689 — anonymous launch: if the user is not signed in, open
  // the PIN modal first; the modal's onAuthenticated callback re-runs
  // postDeployment(). Signed-in users skip the modal.
  async function onLaunch() {
    if (!session.signedIn) {
      setPinModalOpen(true)
      return
    }
    await postDeployment()
  }

  async function postDeployment() {
    setLoading(true)
    try {
      const res = await fetch(`${API_BASE}/v1/deployments`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        // Issue #689 — credentials: 'include' so the session cookie
        // (set either by the magic-link flow or — once #688 lands —
        // the PIN verify endpoint) reaches the catalyst-api. The
        // catalyst-api's auth.RequireSession middleware sets
        // X-User-Email from the cookie before CreateDeployment runs.
        credentials: 'include',
        body: JSON.stringify({
          // Identity
          orgName:  store.orgName,
          // Issue #762 — orgEmail is no longer captured by the wizard
          // (the "Admin contact email · locked to your sign-in" field
          // was redundant — PR #759 enforces `req.OrgEmail ==
          // session.email` on the catalyst-api). Stamp it here from
          // `session.email` so the wire payload matches what the
          // server expects. The PIN sign-in flow below mirrors the
          // verified email into store.orgEmail on its onAuthenticated
          // callback, so by the time we POST the session-bound email
          // is the canonical value; the store fallback handles the
          // brief moment between PIN-verify and the next session
          // refetch.
          orgEmail: session.email ?? store.orgEmail,
          // Sovereign domain — pool subdomain or BYO
          sovereignFQDN,
          sovereignDomainMode: store.sovereignDomainMode,
          sovereignPoolDomain: poolDomainLabel,
          sovereignSubdomain: store.sovereignSubdomain,
          // INF-054 — the Sovereign's own time zone: the operator's pick on
          // step 1, else the browser's. It becomes SOVEREIGN_TIME_ZONE.
          sovereignTimeZone: store.sovereignTimeZone || browserTimeZone(),
          // The cloud (the create body provisioner.Request reads): the provider pack,
          // the installation document laid over it (absent for the pack
          // alone) and the credential map keyed by the profile's credential
          // fields (absent when none was typed: a profile whose source is
          // operator or either then takes the operator's credentials).
          provider,
          installation: store.installation || undefined,
          credentials:  Object.keys(credentials).length > 0 ? credentials : undefined,
          haEnabled:        store.haEnabled,
          // Default storage class (issue #4057). Optional — an empty
          // string lets the catalyst-api Request struct fill the default
          // class of the cloud profile.
          // Captured on StepProvider as a sizing-adjacent cluster knob.
          // `local-path` is not a permitted value (durability guarantee).
          storageClass:     store.storageClass,
          // Canonical per-region payload: provider, cloud region, zone and
          // flavors of each region.
          regions: regionsPayload,
          // #4706/#5661 — deliberate single-region declaration; undefined
          // (and therefore absent from the wire) for multi-region.
          bcpTopology,
          // SSH key
          sshPublicKey: store.sshPublicKey,
          // Marketplace mode (issue #710 wave 3a) — captured by
          // StepMarketplace. The catalyst-api Request struct already
          // accepts `marketplaceEnabled` and threads it through to
          // OpenTofu via var.marketplace_enabled (PR #719); from there
          // cloud-init substitutes it into the bp-catalyst-platform
          // HelmRelease values (`ingress.marketplace.enabled`). The
          // brand fields are captured for forward-compat with the
          // post-launch settings page (companion PR) and ignored by
          // the chart for now.
          marketplaceEnabled: store.marketplaceEnabled,
          marketplaceBrand:   store.marketplaceBrand,
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
        to: '/provision/$deploymentId/dashboard',
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
      onNext={onLaunch}
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
            <Field label="BCP topology" value={bcpTopology ?? 'multi-region (default)'} />
          </FieldGrid>
        </Section>

        {/* ── 3. Provider — per-region cards (one card per region) ── */}
        <Section
          testId="review-section-provider"
          title="Cloud provider per region"
          headerExtra={total && (
            <span style={{ fontSize: 10, fontWeight: 700, color: 'var(--wiz-accent)' }}>
              {total.amount.toFixed(2)} {total.currency}/{total.per}
            </span>
          )}
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
              <RegionCard key={i} row={r} index={i} cloud={profile?.displayName ?? provider} />
            ))}
          </div>
          <div
            style={{
              padding: '4px 12px 8px',
              fontSize: 10,
              color: 'var(--wiz-text-sub)',
            }}
          >
            Regions, zones and flavors are those of the cloud profile{store.installation && ` (installation ${store.installation})`}.
          </div>
        </Section>

        {/* ── 4. Credentials ──────────────────────────────────── */}
        <Section title="Credentials" testId="review-section-credentials">
          <FieldGrid minColumnWidth={220}>
            {Object.keys(credentials).length === 0 && (
              <Field label="Source" value={profile?.credentials.source === 'user' ? dimIfMissing('') : "The operator's credentials"} />
            )}
            {/* A value is shown only for a field the profile marks not secret. */}
            {Object.entries(credentials).map(([key, value]) => {
              const field = profile?.credentials.fields.find(f => f.key === key)
              return (
                <Field
                  key={key}
                  label={field?.label ?? key}
                  value={
                    <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11, color: 'var(--wiz-text-md)' }}>
                      {field && !field.secret ? value : maskToken(value)}
                    </code>
                  }
                />
              )
            })}
            <Field
              label="Credentials validated"
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

        {/* ── 4b. Footprint estimate (issue #767) ─────────────────
            Pre-launch RAM / CPU floor across the bootstrap-kit + the
            operator's selected components, paired with a "minimum
            workers" recommendation against Region 1's chosen worker
            SKU. WARN row surfaces when the operator picked fewer
            workers than the rollup needs — live evidence (otech92):
            two cpx32 workers couldn't fit external-secrets-webhook
            because the bootstrap-kit ate the full 16 GB. Per
            componentFootprints.ts, the values are install-time
            REQUESTS (NOT limits, NOT actual usage), aggregated
            across every Pod each chart instantiates. */}
        <Section
          title="Footprint estimate"
          testId="review-section-footprint"
          headerExtra={
            <span
              data-testid="review-footprint-total"
              style={{ fontSize: 10, fontWeight: 700, color: 'var(--wiz-accent)' }}
            >
              {formatRam(totalFootprint.ramMb)} · {formatCpu(totalFootprint.cpuMilli)}
            </span>
          }
        >
          <FieldGrid minColumnWidth={210}>
            <Field
              label="Bootstrap-kit baseline"
              value={
                <span data-testid="review-footprint-baseline">
                  <strong style={{ color: 'var(--wiz-text-md)' }}>
                    {formatRam(baselineFootprint.ramMb)}
                  </strong>{' '}
                  RAM ·{' '}
                  <strong style={{ color: 'var(--wiz-text-md)' }}>
                    {formatCpu(baselineFootprint.cpuMilli)}
                  </strong>
                </span>
              }
            />
            <Field
              label="Control-plane overhead"
              value={
                <span data-testid="review-footprint-cp-overhead">
                  <strong style={{ color: 'var(--wiz-text-md)' }}>
                    {formatRam(CONTROL_PLANE_OVERHEAD.ramMb)}
                  </strong>{' '}
                  RAM ·{' '}
                  <strong style={{ color: 'var(--wiz-text-md)' }}>
                    {formatCpu(CONTROL_PLANE_OVERHEAD.cpuMilli)}
                  </strong>
                </span>
              }
            />
            <Field
              label="Recommended workers"
              fullWidth
              value={
                r0Worker ? (
                  <span data-testid="review-footprint-recommended">
                    <strong
                      style={{ color: workerShortfall ? '#F59E0B' : 'var(--wiz-text-md)' }}
                    >
                      {recommendedWorkerCount}× {r0WorkerLabel}
                    </strong>{' '}
                    <span style={{ color: 'var(--wiz-text-sub)' }}>
                      ({recommendedWorkerCount * r0WorkerRam} GB / {recommendedWorkerCount * r0WorkerVcpu} vCPU available)
                    </span>
                  </span>
                ) : (
                  <span style={{ color: 'var(--wiz-text-hint)' }}>
                    — pick a worker SKU on the Provider step to see a recommendation —
                  </span>
                )
              }
            />
          </FieldGrid>
          {workerShortfall && (
            <div
              data-testid="review-footprint-shortfall"
              style={{
                marginTop: 8,
                borderRadius: 6,
                padding: '6px 10px',
                background: 'rgba(245,158,11,0.08)',
                border: '1px solid rgba(245,158,11,0.25)',
                fontSize: 11,
                color: '#F59E0B',
                lineHeight: 1.5,
              }}
            >
              <strong>You picked {operatorWorkerCount} worker
              {operatorWorkerCount === 1 ? '' : 's'}</strong> ({operatorWorkerCount * r0WorkerRam} GB / {operatorWorkerCount * r0WorkerVcpu} vCPU) —
              the rollup needs at least {recommendedWorkerCount}. Bump the worker count on the Provider step
              or the cluster-autoscaler will need to scale up immediately after launch
              (live evidence: otech92 ran out of RAM on bootstrap and FailedScheduling-blocked external-secrets-webhook).
            </div>
          )}
          <p
            style={{
              margin: '6px 0 0',
              fontSize: 10,
              color: 'var(--wiz-text-sub)',
              lineHeight: 1.5,
            }}
          >
            Floor of <code>resources.requests</code> across every Pod each blueprint installs — not the limit,
            not steady-state usage. The runtime cluster-autoscaler scales workers up or down within the
            min/max bounds you set on the Provider step.
          </p>
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
          title={`Components · ${totalReleases} in ${RINGS.length} rings`}
          testId="review-section-components"
          bodyPadding="0.85rem 1rem"
        >
          <div
            data-testid="review-component-cards"
            className="review-stack-grid"
            style={{
              // Two columns; the single-column collapse is handled by the
              // matching <style> below so we do not need a JS media-query.
              display: 'grid',
              gridTemplateColumns: 'repeat(2, 1fr)',
              gap: '0.5rem',
            }}
          >
            {RINGS.map(ring => (
              <div
                key={ring.name}
                data-testid={`review-ring-${ring.name}`}
                style={{ fontSize: 11, color: 'var(--wiz-text-sub)' }}
              >
                <strong style={{ color: 'var(--wiz-text-hi)' }}>{ring.title}</strong>
                {' · '}
                {ring.releases.length}
                {ring.required ? ' · required' : ''}
              </div>
            ))}
          </div>
          <style>{`
            @media (max-width: 700px) {
              .review-stack-grid { grid-template-columns: 1fr !important; }
            }
          `}</style>
        </Section>

        {/* ── 6. Marketplace (issue #710 wave 3a) ──────────────── */}
        <Section
          title={
            <>
              <span>Marketplace</span>
              <span
                data-testid="review-marketplace-state"
                style={{
                  fontSize: 9,
                  fontWeight: 700,
                  letterSpacing: '0.06em',
                  color: store.marketplaceEnabled ? '#4ADE80' : 'var(--wiz-text-sub)',
                  background: store.marketplaceEnabled
                    ? 'rgba(74,222,128,0.12)'
                    : 'rgba(148,163,184,0.12)',
                  border: store.marketplaceEnabled
                    ? '1px solid rgba(74,222,128,0.25)'
                    : '1px solid var(--wiz-border-sub)',
                  borderRadius: 3,
                  padding: '1px 6px',
                  textTransform: 'uppercase',
                }}
              >
                {store.marketplaceEnabled ? 'Enabled' : 'Disabled'}
              </span>
            </>
          }
          testId="review-section-marketplace"
        >
          {store.marketplaceEnabled ? (
            <FieldGrid minColumnWidth={200}>
              <Field
                label="Storefront URL"
                fullWidth
                value={
                  sovereignFQDN ? (
                    <code
                      style={{
                        fontFamily: 'JetBrains Mono, monospace',
                        fontSize: 11,
                        color: '#38BDF8',
                      }}
                    >
                      marketplace.{sovereignFQDN}
                    </code>
                  ) : (
                    <span style={{ color: 'var(--wiz-text-hint)' }}>
                      — resolved from Domain step —
                    </span>
                  )
                }
              />
              <Field
                label="Brand name"
                value={dimIfMissing(store.marketplaceBrand.name, '— platform default —')}
              />
              <Field
                label="Tagline"
                value={dimIfMissing(store.marketplaceBrand.tagline, '— platform default —')}
              />
              <Field
                label="Primary colour"
                value={
                  store.marketplaceBrand.primaryColor ? (
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                      <span
                        aria-hidden
                        style={{
                          width: 12,
                          height: 12,
                          borderRadius: 3,
                          border: '1px solid var(--wiz-border-sub)',
                          background: store.marketplaceBrand.primaryColor.startsWith('#')
                            ? store.marketplaceBrand.primaryColor
                            : `#${store.marketplaceBrand.primaryColor}`,
                        }}
                      />
                      <code style={{ fontFamily: 'JetBrains Mono, monospace', fontSize: 11 }}>
                        {store.marketplaceBrand.primaryColor}
                      </code>
                    </span>
                  ) : (
                    <span style={{ color: 'var(--wiz-text-hint)' }}>— platform default —</span>
                  )
                }
              />
            </FieldGrid>
          ) : (
            <p
              style={{
                margin: 0,
                fontSize: 11,
                color: 'var(--wiz-text-sub)',
                lineHeight: 1.5,
              }}
            >
              Single-Organization private Sovereign. No storefront, no per-Organization subdomains. You can
              flip Marketplace mode on later from the post-launch Settings page without
              re-provisioning.
            </p>
          )}
        </Section>

        {/* ── 7. Domain ────────────────────────────────────────── */}
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
            {/* Issue #762 — Sovereign owner is stamped from the
                signed-in session rather than a wizard-captured field.
                Show the session email so the operator can see exactly
                which identity the Sovereign will be owned by; fall
                back to the store value (set on PIN-verify) for the
                brief window before the session refetch lands. */}
            <Field label="Sovereign owner" value={dimIfMissing(session.email ?? store.orgEmail)} />
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

      {/* Issue #689 — guest-mode launch.  Anonymous click on the
          [Launch OpenOva] button opens this modal; on successful PIN
          verify the session cookie is set and we immediately POST the
          deployment.  Pre-fill from the wizard's orgEmail field so the
          user doesn't have to re-type. */}
      <PinSignInModal
        open={pinModalOpen}
        onClose={() => setPinModalOpen(false)}
        onAuthenticated={(verifiedEmail) => {
          setPinModalOpen(false)
          // Mirror the verified email into the wizard's orgEmail so
          // every downstream display (Review, success, handover) carries
          // the actual signed-in identity rather than a stale typo.
          if (verifiedEmail && verifiedEmail !== store.orgEmail) {
            store.setOrgEmail(verifiedEmail)
          }
          // Refresh the session cache so the ProfileMenu re-paints,
          // then immediately POST the deployment.
          session.refetch()
          void postDeployment()
        }}
        initialEmail={store.orgEmail}
        title="Sign in to launch"
        subtitle="We'll email you a 6-digit PIN. Your wizard answers stay exactly as you left them."
      />
    </StepShell>
  )
}
