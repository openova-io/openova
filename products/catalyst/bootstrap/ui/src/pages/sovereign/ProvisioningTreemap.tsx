/**
 * ProvisioningTreemap — the provisioning half of the Dashboard's single
 * pane of glass (#4704).
 *
 * While a Sovereign is converging, the Dashboard's Progress view renders
 * THIS pane instead of the old 5-phase ConvergenceWizard: a treemap
 * SKELETON showing the SAME tile set the live resource treemap will show
 * — every bootstrap component as a tile, greyed at first, each filling
 * to its semantic status colour as its events arrive on the SSE stream:
 *
 *   grey   → pending / not started        (--color-text-dim)
 *   blue   → installing / reconciling     (--color-accent, pulsing)
 *   green  → installed / succeeded        (--color-success)
 *   amber  → degraded / warning           (--color-warn)
 *   red    → failed                       (--color-danger)
 *
 * Header strip: "Provisioning N%" + per-phase chips (Phase 0 · Cloud
 * infrastructure, Phase 1 · Bootstrap kit n/m, Applications n/m), each
 * coloured by its aggregate state. The phase model comes from
 * shared/constants/bootstrap-phases.ts; colours come from the ONE
 * semantic mapping in shared/lib/statusColors.ts. No new backend — the
 * data source is the SAME useDeploymentEvents reducer state + snapshot
 * (SSE GET /api/v1/deployments/{id}/logs) the Jobs surface consumes.
 *
 * Tile CLICK drills down to that component's log-bearing JobDetail
 * surface — the SAME link convention JobsTable's useJobLinkBuilder uses
 * (`/provision/$deploymentId/jobs/$jobId` on the mothership,
 * `/jobs/$jobId` on the chroot Sovereign Console), so a tile click and
 * the corresponding Jobs-table row click land on the same page.
 *
 * The layout is the SAME squarified algorithm (lib/treemap-squarified)
 * the converged FleetTreemap / resource treemap render with, so when the
 * Dashboard auto-flips this pane to the treemap on `status == ready`
 * the transition reads as an in-place morph (same pane position, same
 * tile language, no route change) — not a page swap.
 *
 * The pane also renders sensibly for an already-READY deployment (an
 * operator clicking the Progress tab on a converged Sovereign sees the
 * final all-green tile grid seeded from the durable componentStates map,
 * never an empty state).
 *
 * Per docs/PRINCIPLES.md #4 (never hardcode): the tile set derives from
 * the application catalog + bootstrap-phases at runtime; nothing here
 * maintains a component list by hand.
 */

import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useRouter } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import {
  STATUS_KIND_BADGE_CLASSES,
  statusKindOf,
  type StatusKind,
} from '@/shared/lib/statusColors'
import { OPENTOFU_PHASES, findPhase } from '@/shared/constants/bootstrap-phases'
import type { DeploymentSnapshot } from './useDeploymentEvents'
import type { ApplicationStatus, ReducerState } from './eventReducer'
import type { ApplicationDescriptor } from './applicationCatalog'
import { deriveJobs, type JobUiStatus } from './jobs'
import {
  computeSquarifiedLayout,
  NESTED_HEADER_HEIGHT_PX,
  type SquarifiedRect,
} from '@/lib/treemap-squarified'
import type { TreemapItem } from '@/lib/treemap.types'

/* ── Constants ───────────────────────────────────────────────────── */

/** Pixel height of the tile surface. Matches the order of magnitude of
 *  the converged treemap surface (600px) so the ready-morph keeps the
 *  pane's footprint stable. Exported for tests. */
export const PROVISIONING_SURFACE_HEIGHT_PX = 560

/** Minimum cell size for rendering the tile label / glyph. */
const TILE_LABEL_MIN_WIDTH_PX = 46
const TILE_LABEL_MIN_HEIGHT_PX = 22

/** Synthetic group id for the Phase-0 cloud-infrastructure tiles. */
export const PHASE0_GROUP_ID = 'phase-0-infra'
export const PHASE0_GROUP_LABEL = 'Cloud infrastructure'

/* ── Tile model (pure derivation — exported for tests) ──────────── */

export interface ProvisioningTile {
  /** Job id — the SAME id the JobsTable row for this work item carries,
   *  so tile click and job-row click resolve to the same JobDetail. */
  jobId: string
  /** Short display label. */
  label: string
  /** Semantic status kind — drives the fill colour. */
  kind: StatusKind
}

export interface ProvisioningTileGroup {
  id: string
  label: string
  /** Aggregate kind across the group's tiles. */
  kind: StatusKind
  tiles: ProvisioningTile[]
}

/** JobUiStatus → semantic StatusKind (statusColors vocabulary). */
function jobStatusKind(s: JobUiStatus): StatusKind {
  return statusKindOf(s)
}

/** ApplicationStatus → semantic StatusKind. Kept separate from
 *  jobStatusKind because the reducer's per-app vocabulary retains
 *  `degraded`, which must render AMBER (warning), not red. */
export function appStatusKind(s: ApplicationStatus | undefined): StatusKind {
  switch (s) {
    case 'installed':
      return 'success'
    case 'installing':
      return 'in-progress'
    case 'degraded':
      return 'warning'
    case 'failed':
      return 'failed'
    default:
      return 'pending'
  }
}

/** Aggregate a set of tile kinds into one chip/group kind.
 *  failed > warning > in-progress (incl. partially-done) > success > pending. */
export function aggregateKind(kinds: readonly StatusKind[]): StatusKind {
  if (kinds.length === 0) return 'pending'
  if (kinds.some((k) => k === 'failed')) return 'failed'
  if (kinds.some((k) => k === 'warning')) return 'warning'
  if (kinds.some((k) => k === 'in-progress')) return 'in-progress'
  if (kinds.every((k) => k === 'success')) return 'success'
  // Mixed success + pending — the group is mid-flight even if no tile
  // is individually "installing" right now.
  if (kinds.some((k) => k === 'success')) return 'in-progress'
  return 'pending'
}

/**
 * Derive the full tile-group list from the reducer state + the resolved
 * application catalog.
 *
 *   • Group 1 — Phase-0 cloud infrastructure: the 5 OpenTofu phases from
 *     bootstrap-phases.ts (tofu-init … flux-bootstrap). Their states come
 *     from the SAME deriveJobs derivation the Jobs table uses; their tile
 *     jobIds are the Jobs-table row ids (`infrastructure:<phase>` /
 *     `cluster-bootstrap`).
 *   • Then one group per application family (bootstrap-kit families
 *     first, catalog order preserved), one tile per bp-* component, its
 *     state from the reducer's per-app map (retaining degraded=amber).
 */
export function deriveProvisioningTiles(
  state: ReducerState,
  applications: readonly ApplicationDescriptor[],
): ProvisioningTileGroup[] {
  const groups: ProvisioningTileGroup[] = []

  // Phase-0 tiles — reuse the Jobs derivation with an EMPTY application
  // list so only the 4 tofu jobs + the cluster-bootstrap job come back.
  const infraJobs = deriveJobs(state, [])
  const infraTiles: ProvisioningTile[] = []
  for (const phase of OPENTOFU_PHASES) {
    if (phase.id === 'flux-bootstrap') {
      const j = infraJobs.find((x) => x.id === 'cluster-bootstrap')
      infraTiles.push({
        jobId: 'cluster-bootstrap',
        label: phase.label,
        kind: j ? jobStatusKind(j.status) : 'pending',
      })
      continue
    }
    const jobId = `infrastructure:${phase.id}`
    const j = infraJobs.find((x) => x.id === jobId)
    infraTiles.push({
      jobId,
      label: findPhase(phase.id)?.label ?? phase.id,
      kind: j ? jobStatusKind(j.status) : 'pending',
    })
  }
  groups.push({
    id: PHASE0_GROUP_ID,
    label: PHASE0_GROUP_LABEL,
    kind: aggregateKind(infraTiles.map((t) => t.kind)),
    tiles: infraTiles,
  })

  // Component tiles — grouped by family, catalog order preserved.
  const familyOrder: string[] = []
  const byFamily = new Map<string, { label: string; tiles: ProvisioningTile[] }>()
  for (const app of applications) {
    const key = app.familyId || 'platform'
    if (!byFamily.has(key)) {
      byFamily.set(key, { label: app.familyName || 'Platform', tiles: [] })
      familyOrder.push(key)
    }
    byFamily.get(key)!.tiles.push({
      jobId: app.id,
      label: app.title,
      kind: appStatusKind(state.apps[app.id]?.status),
    })
  }
  for (const key of familyOrder) {
    const fam = byFamily.get(key)!
    groups.push({
      id: key,
      label: fam.label,
      kind: aggregateKind(fam.tiles.map((t) => t.kind)),
      tiles: fam.tiles,
    })
  }

  return groups
}

/**
 * Tile → JobDetail href. Mirrors JobsTable's useJobLinkBuilder exactly:
 * strip any "<prefix>:" from the job id, URL-encode the bare name, and
 * build the mode-aware path — `/jobs/$jobId` on the chroot Sovereign
 * Console, `/provision/$deploymentId/jobs/$jobId` on the mothership.
 * An id-less mothership link falls back to /deployments (#4704 Task B
 * rule — never let a literal land in the $deploymentId slot).
 */
export function provisioningTileHref(jobId: string, deploymentId: string): string {
  const bare = jobId.includes(':') ? jobId.slice(jobId.indexOf(':') + 1) : jobId
  const encoded = encodeURIComponent(bare)
  if (DETECTED_MODE.mode === 'sovereign') return `/jobs/${encoded}`
  if (!deploymentId) return '/deployments'
  return `/provision/${deploymentId}/jobs/${encoded}`
}

/* ── Component ───────────────────────────────────────────────────── */

export interface ProvisioningTreemapProps {
  snapshot: DeploymentSnapshot | null
  state: ReducerState
  applications: readonly ApplicationDescriptor[]
  deploymentId: string
  /** Test seam — pins the measured surface width (jsdom has no layout). */
  fixedWidth?: number
}

export function ProvisioningTreemap({
  snapshot,
  state,
  applications,
  deploymentId,
  fixedWidth,
}: ProvisioningTreemapProps) {
  const router = useRouter()

  const groups = useMemo(
    () => deriveProvisioningTiles(state, applications),
    [state, applications],
  )

  const allTiles = useMemo(() => groups.flatMap((g) => g.tiles), [groups])
  const doneCount = allTiles.filter((t) => t.kind === 'success').length
  const percent =
    allTiles.length > 0 ? Math.round((doneCount / allTiles.length) * 100) : 0

  const overallKind = statusKindOf(snapshot?.status)
  const isReady = (snapshot?.status ?? '').trim().toLowerCase() === 'ready'

  // Phase chips — phase-level rollups (distinct from the family-level
  // tile grouping): Phase 0 = the infra group; Phase 1 = the
  // bootstrap-kit component tiles; Applications = everything else.
  const infraGroup = groups.find((g) => g.id === PHASE0_GROUP_ID)
  const kitIds = useMemo(
    () => new Set(applications.filter((a) => a.bootstrapKit).map((a) => a.id)),
    [applications],
  )
  const componentTiles = useMemo(
    () => groups.filter((g) => g.id !== PHASE0_GROUP_ID).flatMap((g) => g.tiles),
    [groups],
  )
  const kitTiles = componentTiles.filter((t) => kitIds.has(t.jobId))
  const extraTiles = componentTiles.filter((t) => !kitIds.has(t.jobId))

  const headline = isReady
    ? 'Ready'
    : overallKind === 'failed'
      ? `Provisioning failed · ${percent}%`
      : overallKind === 'warning'
        ? `Provisioning degraded · ${percent}%`
        : `Provisioning ${percent}%`

  function openTile(tile: ProvisioningTile) {
    const href = provisioningTileHref(tile.jobId, deploymentId)
    router.navigate({ to: href as never })
  }

  return (
    <div
      data-testid="provisioning-treemap"
      data-status={snapshot?.status ?? ''}
      data-ready={isReady ? 'true' : 'false'}
      className="prov-pane rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
    >
      <style>{PROV_CSS}</style>

      {/* Header strip — overall percentage + per-phase chips. */}
      <div
        data-testid="provisioning-treemap-header"
        className="mb-3 flex flex-wrap items-center gap-2"
      >
        <h2
          data-testid="provisioning-progress-label"
          className={`mr-2 rounded-md px-2 py-1 text-sm font-semibold ${STATUS_KIND_BADGE_CLASSES[isReady ? 'success' : overallKind]}`}
        >
          {isReady ? '✓ ' : ''}
          {headline}
        </h2>
        {infraGroup ? (
          <PhaseChip
            testId="prov-chip-phase0"
            kind={infraGroup.kind}
            label="Phase 0 · Cloud infrastructure"
            done={infraGroup.tiles.filter((t) => t.kind === 'success').length}
            total={infraGroup.tiles.length}
          />
        ) : null}
        {kitTiles.length > 0 ? (
          <PhaseChip
            testId="prov-chip-bootstrap-kit"
            kind={aggregateKind(kitTiles.map((t) => t.kind))}
            label="Phase 1 · Bootstrap kit"
            done={kitTiles.filter((t) => t.kind === 'success').length}
            total={kitTiles.length}
          />
        ) : null}
        {extraTiles.length > 0 ? (
          <PhaseChip
            testId="prov-chip-applications"
            kind={aggregateKind(extraTiles.map((t) => t.kind))}
            label="Applications"
            done={extraTiles.filter((t) => t.kind === 'success').length}
            total={extraTiles.length}
          />
        ) : null}
      </div>

      {/* The treemap-skeleton tile surface. */}
      <ProvisioningSurface
        groups={groups}
        onTileClick={openTile}
        fixedWidth={fixedWidth}
      />

      {/* Footer hint — what this pane is and what it becomes. */}
      <p
        data-testid="provisioning-treemap-hint"
        className="mt-3 text-[11px] text-[var(--color-text-dim)]"
      >
        {isReady ? (
          <>
            Converged — these are the final install states. The Dashboard
            switches this pane to the live resource treemap (same tiles,
            sized by real usage) automatically.
          </>
        ) : (
          <>
            ⓘ These are the SAME tiles the live resource treemap will show —
            each fills with colour as its component reports in, and this pane
            becomes the live treemap the moment every component reports
            Ready. Click a tile to open its job logs.
          </>
        )}
        {state.phase1WatchSkipped ? (
          <span data-testid="provisioning-treemap-watch-skipped">
            {' '}
            Per-component install monitoring is unavailable for this
            deployment — component tiles reflect the last known state.
          </span>
        ) : null}
      </p>
    </div>
  )
}

/* ── Phase chip ──────────────────────────────────────────────────── */

function PhaseChip({
  testId,
  kind,
  label,
  done,
  total,
}: {
  testId: string
  kind: StatusKind
  label: string
  done: number
  total: number
}) {
  const suffix = done >= total && total > 0 ? '✓' : `${done}/${total}`
  return (
    <span
      data-testid={testId}
      data-kind={kind}
      className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ${STATUS_KIND_BADGE_CLASSES[kind]}`}
    >
      <span
        aria-hidden
        className={`prov-chip-dot prov-kind-${kind}`}
        data-testid={`${testId}-dot`}
      />
      {label}
      <span className="font-mono">{suffix}</span>
    </span>
  )
}

/* ── Tile surface (squarified SVG) ───────────────────────────────── */

interface ProvisioningSurfaceProps {
  groups: readonly ProvisioningTileGroup[]
  onTileClick: (tile: ProvisioningTile) => void
  fixedWidth?: number
}

function ProvisioningSurface({ groups, onTileClick, fixedWidth }: ProvisioningSurfaceProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [measured, setMeasured] = useState<number>(0)
  const width = fixedWidth ?? measured

  useLayoutEffect(() => {
    if (fixedWidth !== undefined) return
    const el = containerRef.current
    if (!el) return
    function measure() {
      if (!el) return
      setMeasured(el.clientWidth)
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [fixedWidth])

  // Tile lookup by jobId — the squarified layout only carries the
  // TreemapItem back-pointer; the status kind lives on our tile model.
  const tileById = useMemo(() => {
    const m = new Map<string, ProvisioningTile>()
    for (const g of groups) for (const t of g.tiles) m.set(t.jobId, t)
    return m
  }, [groups])

  // Uniform tile sizes (size_value = 1) — the SKELETON foreshadows the
  // converged treemap's geometry; real sizing (by CPU/mem) arrives when
  // FleetTreemap takes the pane over on ready.
  const items = useMemo<TreemapItem[]>(
    () =>
      groups
        .filter((g) => g.tiles.length > 0)
        .map((g) => ({
          id: g.id,
          name: g.label,
          count: g.tiles.length,
          percentage: null,
          size_value: g.tiles.length,
          children: g.tiles.map((t) => ({
            id: t.jobId,
            name: t.label,
            count: 1,
            percentage: null,
            size_value: 1,
          })),
        })),
    [groups],
  )

  const rects = useMemo<SquarifiedRect[]>(() => {
    if (width <= 0) return []
    return computeSquarifiedLayout(items, width, PROVISIONING_SURFACE_HEIGHT_PX)
  }, [items, width])

  return (
    <div
      ref={containerRef}
      data-testid="provisioning-treemap-surface"
      style={{ width: '100%', height: PROVISIONING_SURFACE_HEIGHT_PX }}
    >
      {width > 0 && (
        <svg
          width={width}
          height={PROVISIONING_SURFACE_HEIGHT_PX}
          role="img"
          aria-label="Provisioning progress treemap"
          style={{ display: 'block' }}
        >
          {rects.map((r, i) =>
            r.isParent ? (
              <ProvGroupCell key={`g-${r.item.id}-${i}`} rect={r} />
            ) : (
              <ProvTileCell
                key={`t-${r.item.id}-${i}`}
                rect={r}
                tile={r.item.id ? tileById.get(r.item.id) : undefined}
                onClick={onTileClick}
              />
            ),
          )}
        </svg>
      )}
    </div>
  )
}

/** Parent (family/phase) cell — frame + header strip, neutral colours
 *  so the status tiles carry all the semantic signal. */
function ProvGroupCell({ rect }: { rect: SquarifiedRect }) {
  const { x0, y0, x1, y1, item } = rect
  const w = x1 - x0
  const h = y1 - y0
  if (w <= 0 || h <= 0) return null
  return (
    <g data-testid={`prov-group-${item.id}`}>
      <rect
        x={x0}
        y={y0}
        width={w}
        height={h}
        style={{
          fill: 'transparent',
          stroke: 'var(--color-border)',
          strokeWidth: 1,
        }}
      />
      <rect
        x={x0}
        y={y0}
        width={w}
        height={NESTED_HEADER_HEIGHT_PX}
        style={{
          fill: 'color-mix(in srgb, var(--color-border) 45%, transparent)',
          stroke: 'var(--color-border)',
          strokeWidth: 1,
        }}
      />
      {w >= TILE_LABEL_MIN_WIDTH_PX && (
        <text
          x={x0 + 8}
          y={y0 + 16}
          fontSize={11}
          fontWeight={600}
          style={{ fill: 'var(--color-text-dim)', pointerEvents: 'none' }}
        >
          {truncateLabel(item.name, w)}
        </text>
      )}
    </g>
  )
}

/** Status glyph per kind — reinforces the colour signal for colour-blind
 *  operators (same glyph vocabulary as the old wizard dots). */
const KIND_GLYPH: Record<StatusKind, string> = {
  success: '✓',
  'in-progress': '⟳',
  warning: '!',
  failed: '✕',
  pending: '○',
}

function ProvTileCell({
  rect,
  tile,
  onClick,
}: {
  rect: SquarifiedRect
  tile: ProvisioningTile | undefined
  onClick: (tile: ProvisioningTile) => void
}) {
  const { x0, y0, x1, y1, item } = rect
  const w = x1 - x0
  const h = y1 - y0
  if (w <= 0 || h <= 0) return null
  const kind: StatusKind = tile?.kind ?? 'pending'
  const showLabel = w >= TILE_LABEL_MIN_WIDTH_PX && h >= TILE_LABEL_MIN_HEIGHT_PX

  return (
    <g
      data-testid={`prov-tile-${item.id ?? item.name}`}
      data-kind={kind}
      role="button"
      aria-label={`${item.name} — ${kind}. Open job logs.`}
      tabIndex={0}
      className={`prov-tile prov-kind-${kind}`}
      style={{ cursor: 'pointer' }}
      onClick={() => tile && onClick(tile)}
      onKeyDown={(e) => {
        if ((e.key === 'Enter' || e.key === ' ') && tile) onClick(tile)
      }}
    >
      {/* Native SVG tooltip — small cells suppress their text label
          (same threshold behaviour as the converged treemap), so the
          hover title is the identification fallback for tiny tiles. */}
      <title>{`${item.name} — ${kind}`}</title>
      <rect
        className="prov-tile-rect"
        x={x0 + 1}
        y={y0 + 1}
        width={Math.max(0, w - 2)}
        height={Math.max(0, h - 2)}
        rx={2}
      />
      {showLabel && (
        <>
          <text
            x={x0 + 8}
            y={y0 + 16}
            fontSize={11}
            fontWeight={600}
            style={{ fill: 'var(--color-text-strong)', pointerEvents: 'none' }}
          >
            {truncateLabel(item.name, w)}
          </text>
          {h >= 34 && (
            <text
              x={x0 + 8}
              y={y0 + 30}
              fontSize={10}
              style={{ fill: 'var(--color-text-dim)', pointerEvents: 'none' }}
            >
              {KIND_GLYPH[kind]} {kind}
            </text>
          )}
        </>
      )}
    </g>
  )
}

/** Truncate a label to the cell width — rough 6.5px/char @ 11px font
 *  (same heuristic as the converged treemap's cell renderer). */
function truncateLabel(name: string, width: number): string {
  const maxChars = Math.max(3, Math.floor((width - 12) / 6.5))
  if (name.length <= maxChars) return name
  return name.slice(0, Math.max(1, maxChars - 1)) + '…'
}

/* ── CSS — semantic fills via the statusColors theme tokens ─────────
 *
 * Fill colours reference the SAME CSS custom properties as
 * shared/lib/statusColors.ts (both themes define them); tints via
 * color-mix so light + dark themes stay correct with no hex forks.
 * In-progress pulses so a converging tile is unmistakably distinct
 * from both pending (grey) and success (green). The fill transition
 * makes each tile visibly "colour in" as its events arrive — the
 * skeleton→treemap morph the #4704 wireframe specifies. */
const PROV_CSS = `
.prov-pane { animation: prov-fade-in 0.25s ease; }
.prov-tile-rect { stroke: var(--color-border); stroke-width: 1; transition: fill 0.4s ease; }
.prov-kind-pending .prov-tile-rect { fill: color-mix(in srgb, var(--color-text-dim) 16%, transparent); }
.prov-kind-in-progress .prov-tile-rect { fill: color-mix(in srgb, var(--color-accent) 60%, transparent); animation: prov-pulse 2s ease-in-out infinite; }
.prov-kind-success .prov-tile-rect { fill: color-mix(in srgb, var(--color-success) 65%, transparent); }
.prov-kind-warning .prov-tile-rect { fill: color-mix(in srgb, var(--color-warn) 65%, transparent); }
.prov-kind-failed .prov-tile-rect { fill: color-mix(in srgb, var(--color-danger) 70%, transparent); }
.prov-tile:focus-visible .prov-tile-rect { stroke: var(--color-accent); stroke-width: 2; }
.prov-chip-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; }
.prov-chip-dot.prov-kind-pending { background: var(--color-text-dim); }
.prov-chip-dot.prov-kind-in-progress { background: var(--color-accent); animation: prov-pulse 2s ease-in-out infinite; }
.prov-chip-dot.prov-kind-success { background: var(--color-success); }
.prov-chip-dot.prov-kind-warning { background: var(--color-warn); }
.prov-chip-dot.prov-kind-failed { background: var(--color-danger); }
@keyframes prov-pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.45; } }
@keyframes prov-fade-in { from { opacity: 0; } to { opacity: 1; } }
`
