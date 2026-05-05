/**
 * FlowPage — recursive Job-tree canvas (issue #351).
 *
 * The page renders a full-bleed canvas of the deployment's Job tree.
 * The previous "jobs vs batches mode" toggle and `?scope=batch:<id>`
 * filter are gone; "batches" are now parent-group Jobs in the same
 * tree, surfaced via the {@link FoldControls} toolbar.
 *
 * Responsibilities:
 *   • Source the recursive Job list (reducer + live API merge).
 *   • Resolve fold state from URL (`?folded=id1,id2 + ?depth=1|2|3|all`)
 *     overlaid on the per-node manual fold set.
 *   • Hand a layout-ready bundle to {@link FlowCanvasOrganic}.
 *   • Surface single-click → set `openJobId` (the consumer wires that
 *     to the LogPane). Double-click on a leaf → navigate to its
 *     `/jobs/$jobId` home; double-click on a parent → toggle its
 *     fold state.
 *   • Pass `hostJobId` straight through so the canvas paints the
 *     teal host ring.
 *
 * Embedded mode (`embedded` prop, used by JobDetail) drops the
 * PortalShell + StatusStrip chrome — JobDetail owns those.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape: recursive tree, fold-aware
 *      layout, host vs selection rings, log pane open by default.
 *   #2 (no compromise) — d3-force layout layer is rebuilt as a single
 *      recursive engine; no parallel batch-vs-job code paths remain.
 *   #4 (never hardcode) — region descriptors come from the wizard
 *      store; family palette comes from componentGroups.PRODUCTS;
 *      every dimension is a geometry knob.
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications, type ApplicationDescriptor } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'
import {
  flowLayoutOrganic,
  defaultFoldedAtDepth,
  FALLBACK_REGION_ID,
  type OrganicFamily,
  type OrganicRegion,
  type OrganicNodeHints,
} from '@/lib/flowLayoutOrganic'
import { DEFAULT_FAMILIES } from '@/lib/flowFamilyPalette'
import type { Job } from '@/lib/jobs.types'
import {
  StatusStrip,
  type ProvisioningStatus,
} from '@/components/StatusStrip'
import { FlowCanvasOrganic } from './FlowCanvasOrganic'
import { FoldControls, hasGroupJobs, resolveDepth, type FoldDepth } from './FoldControls'
import { PRODUCTS } from '@/pages/wizard/steps/componentGroups'

/* ──────────────────────────────────────────────────────────────────
 * URL helpers
 * ────────────────────────────────────────────────────────────────── */

/** Parse `?folded=id1,id2,…` into a Set. */
export function resolveFolded(raw: unknown): Set<string> {
  if (typeof raw !== 'string' || raw.length === 0) return new Set()
  return new Set(
    raw
      .split(',')
      .map((s) => s.trim())
      .filter((s) => s.length > 0),
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Family palette + per-job hints
 * ────────────────────────────────────────────────────────────────── */

function useFamilyPalette(): OrganicFamily[] {
  return useMemo(() => {
    const fromCatalog = PRODUCTS.map((p) => {
      const fallback = DEFAULT_FAMILIES.find((f) => f.id === p.id)
      return {
        id: p.id,
        label: p.name,
        color: fallback?.color ?? '#94A3B8',
      } satisfies OrganicFamily
    })
    const seen = new Set(fromCatalog.map((f) => f.id))
    for (const f of DEFAULT_FAMILIES) {
      if (!seen.has(f.id)) fromCatalog.push(f)
    }
    return fromCatalog
  }, [])
}

// FLUX-CANONICAL DEPENDENCIES — single source of truth. The local
// hardcoded BOOTSTRAP_KIT_DEPS map carried hand-maintained edges that
// drifted from clusters/_template/bootstrap-kit/*.yaml HelmRelease
// `dependsOn` (e.g. keycloak→openbao was rendered here while the
// real Flux install graph is keycloak→[cert-manager, gateway-api]).
// blueprint-deps.generated.json is built from the YAMLs by
// scripts/generate-blueprint-deps.sh; the wizard catalog reads from
// the same file. No FlowPage-private dependency table.
import { BLUEPRINT_DEPS } from '../../data/blueprintDeps'
const BOOTSTRAP_KIT_DEPS: Record<string, string[]> = BLUEPRINT_DEPS

function useJobHints(args: {
  jobs: readonly Job[]
  applications: readonly ApplicationDescriptor[]
  regions: readonly OrganicRegion[]
}): Map<string, OrganicNodeHints> {
  const { jobs, applications, regions } = args
  return useMemo(() => {
    const out = new Map<string, OrganicNodeHints>()
    const appById = new Map<string, ApplicationDescriptor>()
    const appByBareId = new Map<string, ApplicationDescriptor>()
    for (const a of applications) {
      appById.set(a.id, a)
      appByBareId.set(a.bareId, a)
    }
    const fallbackRegion = regions[0]?.id ?? FALLBACK_REGION_ID

    const liveIdByBare = new Map<string, string>()
    for (const j of jobs) {
      if (j.type === 'group') continue
      const bare = j.appId.startsWith('bp-') ? j.appId.slice(3) : j.appId
      if (!liveIdByBare.has(bare)) liveIdByBare.set(bare, j.id)
      const m = j.jobName.match(/^install-(.+)$/)
      if (m) {
        const fromName = m[1]
        if (!liveIdByBare.has(fromName)) liveIdByBare.set(fromName, j.id)
      }
      const idMatch = j.id.match(/install-([a-z0-9-]+)/)
      if (idMatch) {
        const fromId = idMatch[1]
        if (!liveIdByBare.has(fromId)) liveIdByBare.set(fromId, j.id)
      }
    }

    function bareIdOf(j: Job): string {
      const m = j.jobName.match(/^install-(.+)$/)
      if (m) return m[1]
      const m2 = j.id.match(/install-([a-z0-9-]+)/)
      if (m2) return m2[1]
      const m3 = j.appId.startsWith('bp-') ? j.appId.slice(3) : j.appId
      return m3
    }

    function bootstrapDepsFor(bare: string): string[] {
      const canon = BOOTSTRAP_KIT_DEPS[bare] ?? []
      const ids: string[] = []
      for (const d of canon) {
        const liveId = liveIdByBare.get(d)
        if (liveId) ids.push(liveId)
      }
      return ids
    }

    const bootstrapJobId = jobs.find((j) => j.appId === 'cluster-bootstrap')?.id ?? null
    const phase0FinalJobId = jobs.find((j) => j.id === 'infrastructure:tofu-output')?.id ?? null

    for (const j of jobs) {
      let regionId = fallbackRegion
      const sep = j.id.indexOf('::')
      if (sep > 0) {
        const candidate = j.id.slice(sep + 2)
        if (regions.some((r) => r.id === candidate)) regionId = candidate
      }

      let familyId: string
      const extraDepIds: string[] = []

      if (j.type === 'group') {
        familyId = 'catalyst'
      } else if (j.appId === 'infrastructure') {
        familyId = 'catalyst'
      } else if (j.appId === 'cluster-bootstrap') {
        familyId = 'catalyst'
        if (phase0FinalJobId) extraDepIds.push(phase0FinalJobId)
      } else {
        const app = appById.get(j.appId)
        familyId = app?.familyId ?? 'platform'
        if (app) {
          for (const dep of app.dependencies ?? []) {
            const depApp = appByBareId.get(dep)
            if (depApp) extraDepIds.push(depApp.id)
            const fromBare = liveIdByBare.get(dep)
            if (fromBare) extraDepIds.push(fromBare)
          }
        }
        const bare = bareIdOf(j)
        for (const d of bootstrapDepsFor(bare)) extraDepIds.push(d)
        if (extraDepIds.length === 0 && bootstrapJobId) {
          extraDepIds.push(bootstrapJobId)
        }
      }

      out.set(j.id, { regionId, familyId, extraDepIds })
    }
    return out
  }, [jobs, applications, regions])
}

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

interface FlowPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
  /** Embedded variant: render without the PortalShell + StatusStrip chrome. */
  embedded?: boolean
  /** Override the deploymentId param. */
  deploymentIdOverride?: string
  /**
   * Job id that "owns" this page — typically the JobDetail route's
   * `$jobId`. The canvas paints a persistent teal ring on this node
   * regardless of which job is currently single-clicked. The default
   * `openJobId` is set to this id on first paint so the LogPane
   * shows the host's logs immediately.
   */
  hostJobId?: string | null
  /**
   * Notifies the parent (JobDetail) every time the canvas's selected
   * job changes. The host stays put across single-click selections;
   * the parent uses this hook to keep its LogPane in sync with the
   * currently-clicked node.
   */
  onOpenJobChange?: (jobId: string | null) => void
  /** Override the global default fold depth (test seam). */
  initialDepth?: FoldDepth
}

export function FlowPage({
  disableStream = false,
  disableJobsBackfill = false,
  embedded = false,
  deploymentIdOverride,
  hostJobId = null,
  onOpenJobChange,
  initialDepth,
}: FlowPageProps = {}) {
  const looseParams = useParams({ strict: false }) as { deploymentId?: string }
  const deploymentId = deploymentIdOverride ?? looseParams.deploymentId ?? ''
  const store = useWizardStore()

  const search = useSearch({ strict: false }) as {
    folded?: unknown
    depth?: unknown
  }
  const navigate = useNavigate()

  /* ── Data adapter (preserved verbatim from PR #249) ──────────── */

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot, streamStatus, startedAt } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const derivedJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const reducerJobs = useMemo(() => adaptDerivedJobsToFlat(derivedJobs), [derivedJobs])
  const inFlight = streamStatus !== 'completed' && streamStatus !== 'failed'
  const { liveJobs } = useLiveJobsBackfill({
    deploymentId,
    enabled: !disableJobsBackfill,
    disablePolling: disableJobsBackfill || !inFlight,
  })
  const allJobs = useMemo(
    () => mergeJobs(reducerJobs, liveJobs),
    [reducerJobs, liveJobs],
  )

  /* ── Region descriptors (multi-region support) ───────────────── */

  const regions = useMemo<OrganicRegion[]>(() => {
    if (store.regions && store.regions.length > 0) {
      return store.regions.map((r) => ({
        id: r.id,
        label: `${r.code.toUpperCase()} · ${r.location}`,
        meta: r.name,
      }))
    }
    return [
      {
        id: FALLBACK_REGION_ID,
        label: sovereignFQDN ? `${sovereignFQDN}` : 'Primary Region',
        meta: 'Single-region cluster',
      },
    ]
  }, [store.regions, sovereignFQDN])

  /* ── Family palette + descriptions + per-job hints ──────────── */

  const families = useFamilyPalette()
  const hints = useJobHints({ jobs: allJobs, applications, regions })

  /* ── Fold state — URL (depth + folded) + per-node manual ─────── */

  const urlDepth = resolveDepth(search?.depth)
  const depth: FoldDepth = initialDepth ?? urlDepth
  const urlFoldedSet = useMemo(() => resolveFolded(search?.folded), [search?.folded])

  const foldedSet = useMemo(() => {
    const baseline = depth === 'all' ? new Set<string>() : defaultFoldedAtDepth(allJobs, depth)
    // Manual per-node overrides: an id present in `?folded=` forces folded
    // (additive) — the UI's Expand-all sets `?folded=` to empty, so this
    // composition reads cleanly.
    for (const id of urlFoldedSet) baseline.add(id)
    return baseline
  }, [allJobs, depth, urlFoldedSet])

  const setSearchPatch = useCallback(
    (patch: { folded?: string | undefined; depth?: string | undefined }) => {
      navigate({
        to: '.',
        search: (prev) => {
          const next: Record<string, unknown> = { ...(prev ?? {}) }
          if ('folded' in patch) {
            if (patch.folded && patch.folded.length > 0) next.folded = patch.folded
            else delete next.folded
          }
          if ('depth' in patch) {
            if (patch.depth) next.depth = patch.depth
            else delete next.depth
          }
          return next
        },
      })
    },
    [navigate],
  )

  const onDepthChange = useCallback(
    (next: FoldDepth) => {
      setSearchPatch({
        depth: next === 2 ? undefined : String(next),
        // Clear manual overrides — depth is the new global truth.
        folded: undefined,
      })
    },
    [setSearchPatch],
  )

  const onCollapseAll = useCallback(() => {
    const allGroups = allJobs.filter((j) => j.type === 'group').map((j) => j.id)
    setSearchPatch({ depth: '1', folded: allGroups.join(',') })
  }, [allJobs, setSearchPatch])

  const onExpandAll = useCallback(() => {
    setSearchPatch({ depth: 'all', folded: undefined })
  }, [setSearchPatch])

  const toggleFold = useCallback(
    (jobId: string) => {
      const job = allJobs.find((j) => j.id === jobId)
      if (!job || job.type !== 'group') return
      const next = new Set(urlFoldedSet)
      const isFolded = foldedSet.has(jobId)
      if (isFolded) next.delete(jobId)
      else next.add(jobId)
      const arr = [...next].filter(Boolean)
      setSearchPatch({ folded: arr.length > 0 ? arr.join(',') : undefined })
    },
    [allJobs, foldedSet, urlFoldedSet, setSearchPatch],
  )

  /* ── Layout ───────────────────────────────────────────────────── */

  const layout = useMemo(
    () => flowLayoutOrganic(allJobs, { hints, regions, families, folded: foldedSet }),
    [allJobs, hints, regions, families, foldedSet],
  )

  /* ── Click semantics (single vs double, debounced 220ms) ────── */

  const [openJobId, setOpenJobIdState] = useState<string | null>(hostJobId)
  // Keep `openJobId` synced with `hostJobId` when the host changes
  // (e.g. operator navigated to a different /jobs/{id}). Only on
  // entry — once the operator clicks another job, we don't snap them
  // back.
  const prevHostRef = useRef<string | null>(null)
  useEffect(() => {
    if (hostJobId !== prevHostRef.current) {
      prevHostRef.current = hostJobId
      setOpenJobIdState(hostJobId)
      onOpenJobChange?.(hostJobId)
    }
  }, [hostJobId, onOpenJobChange])
  const setOpenJobId = useCallback(
    (next: string | null) => {
      setOpenJobIdState(next)
      onOpenJobChange?.(next)
    },
    [onOpenJobChange],
  )

  const clickTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelPendingClick = useCallback(() => {
    if (clickTimerRef.current) {
      clearTimeout(clickTimerRef.current)
      clickTimerRef.current = null
    }
  }, [])
  useEffect(() => () => cancelPendingClick(), [cancelPendingClick])

  const handleJobClick = useCallback(
    (jobId: string, _event: ReactMouseEvent<SVGGElement>) => {
      cancelPendingClick()
      clickTimerRef.current = setTimeout(() => {
        setOpenJobId(jobId)
        clickTimerRef.current = null
      }, 220)
    },
    [cancelPendingClick],
  )

  const handleJobDoubleClick = useCallback(
    (jobId: string) => {
      cancelPendingClick()
      const job = allJobs.find((j) => j.id === jobId)
      // Group: toggle fold in place (rest of the canvas keeps its
      // current fold state — operator spec).
      if (job?.type === 'group') {
        toggleFold(jobId)
        return
      }
      // Leaf: navigate to its own home.
      navigate({
        to: '/jobs/$jobId' as never,
        params: { deploymentId, jobId } as never,
      })
    },
    [navigate, deploymentId, cancelPendingClick, allJobs, toggleFold],
  )

  const handleCanvasBackgroundClick = useCallback(() => {
    cancelPendingClick()
    setOpenJobId(hostJobId)
  }, [cancelPendingClick, hostJobId])

  /* ── StatusStrip rollup ──────────────────────────────────────── */

  const leafJobs = useMemo(() => allJobs.filter((j) => j.type !== 'group'), [allJobs])
  const provisioningStatus: ProvisioningStatus = useMemo(() => {
    if (leafJobs.length === 0) return 'pending'
    const buckets = new Set(leafJobs.map((j) => j.status))
    if (buckets.has('failed')) {
      const allTerminal = leafJobs.every((j) => j.status === 'succeeded' || j.status === 'failed')
      return allTerminal ? 'failed' : 'running'
    }
    if (buckets.has('running') || buckets.has('pending')) return 'running'
    return 'succeeded'
  }, [leafJobs])

  const finishedCount = useMemo(
    () => leafJobs.filter((j) => j.status === 'succeeded' || j.status === 'failed').length,
    [leafJobs],
  )
  const totalCount = leafJobs.length

  const earliestStarted = useMemo<number | null>(() => {
    let earliest: number | null = null
    for (const j of leafJobs) {
      if (!j.startedAt) continue
      const t = Date.parse(j.startedAt)
      if (!Number.isFinite(t)) continue
      if (earliest === null || t < earliest) earliest = t
    }
    if (earliest !== null) return earliest
    return startedAt ?? null
  }, [leafJobs, startedAt])

  const [now, setNow] = useState<number>(() => Date.now())
  useEffect(() => {
    if (provisioningStatus !== 'running') return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [provisioningStatus])
  const elapsedMs = earliestStarted === null ? 0 : Math.max(0, now - earliestStarted)

  /* ── Canvas full-screen ──────────────────────────────────────── */

  const [canvasFullScreen, setCanvasFullScreen] = useState<boolean>(false)
  const toggleCanvasFullScreen = useCallback(() => {
    setCanvasFullScreen((v) => !v)
  }, [])
  // Esc exits canvas full-screen — only when no LogPane is mounted
  // ahead of us in the keydown listener stack. (LogPane attaches its
  // own Esc listener at document level; React handles dispatch in
  // mount order, so the LogPane's first-Esc takes priority. When the
  // pane is dismissed, the canvas's listener takes over.)
  useEffect(() => {
    if (!canvasFullScreen) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.stopPropagation()
        setCanvasFullScreen(false)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [canvasFullScreen])

  /* ── Render ──────────────────────────────────────────────────── */

  const canvas = (
    <FlowCanvasOrganic
      layout={layout}
      embedded={embedded}
      hostJobId={hostJobId}
      openJobId={openJobId}
      onJobClick={handleJobClick}
      onJobDoubleClick={handleJobDoubleClick}
      onCanvasBackgroundClick={handleCanvasBackgroundClick}
    />
  )

  const fullScreenButton = (
    <button
      type="button"
      className="flow-fullscreen-btn"
      data-testid="flow-fullscreen-btn"
      aria-label={canvasFullScreen ? 'Exit canvas full-screen' : 'Canvas full-screen'}
      aria-pressed={canvasFullScreen}
      title={canvasFullScreen ? 'Exit full-screen (Esc)' : 'Full-screen canvas'}
      onClick={toggleCanvasFullScreen}
    >
      {canvasFullScreen ? (
        <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
          <path d="M5 1 L5 5 L1 5 M9 1 L9 5 L13 5 M5 13 L5 9 L1 9 M9 13 L9 9 L13 9"
                stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      ) : (
        <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden>
          <path d="M1 5 L1 1 L5 1 M9 1 L13 1 L13 5 M13 9 L13 13 L9 13 M5 13 L1 13 L1 9"
                stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )}
    </button>
  )

  const flowSurface = (
    <div
      className={`flow-surface${canvasFullScreen ? ' is-fullscreen' : ''}`}
      data-testid="flow-surface"
      data-canvas-fullscreen={canvasFullScreen ? 'true' : 'false'}
    >
      <div className="flow-canvas-host" data-testid="flow-canvas-host">
        {canvas}
        {fullScreenButton}
      </div>
    </div>
  )

  if (embedded) {
    return (
      <div className="flow-page-embedded" data-testid="flow-page-embedded">
        <style>{FLOW_PAGE_CSS}</style>
        {flowSurface}
      </div>
    )
  }

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Flow"
      headerSlotLeft={
        <Link
          to={`/jobs` as never}
          params={{ deploymentId }}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="flow-page-back-to-table"
        >
          ← Back to table
        </Link>
      }
    >
      <style>{FLOW_PAGE_CSS}</style>

      <div className="mt-2">
        <StatusStrip
          deploymentId={deploymentId}
          sovereignFQDN={sovereignFQDN}
          status={provisioningStatus}
          finished={finishedCount}
          total={totalCount}
          elapsedMs={elapsedMs}
          trailing={
            <FoldControls
              depth={depth}
              onDepthChange={onDepthChange}
              onCollapseAll={onCollapseAll}
              onExpandAll={onExpandAll}
              hasGroups={hasGroupJobs(allJobs)}
            />
          }
        />
      </div>

      <div className="mt-4">{flowSurface}</div>
    </PortalShell>
  )
}

const FLOW_PAGE_CSS = `
.flow-page-embedded { width: 100%; height: 100%; }

.flow-surface {
  display: block;
  width: 100%;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface, rgba(7,10,18,0.55));
  padding: 12px;
  min-height: 540px;
  height: calc(100vh - 220px);
  max-height: 820px;
}

.flow-page-embedded .flow-surface {
  min-height: 0;
  padding: 0;
  border: 0;
  background: transparent;
  height: 100%;
  max-height: none;
}

/* Canvas full-screen mode — overlays the entire viewport above
   everything except the LogPane (which has z-index 60 by default
   and 80 in its own full-screen mode). The canvas occupies 100vw /
   100vh; the LogPane stays docked on top of it so the operator can
   still tail logs in the wider canvas. The Esc key exits — the
   FlowPage's keydown listener handles that. */
.flow-surface.is-fullscreen {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  width: 100vw;
  height: 100vh;
  max-height: none;
  /* Sits ABOVE the LogPane (z=60 docked / z=80 own full-screen) so
     the operator gets a true full-viewport canvas without the pane
     covering 30% of the right edge. */
  z-index: 90;
  border-radius: 0;
  border: 0;
  padding: 0;
  background: rgba(2, 6, 15, 0.98);
}

.flow-canvas-host {
  position: relative;
  min-width: 0;
  height: 100%;
  /* Issue #669 — theme-aware backdrop. Dark mode keeps the navy
   * radial gradient; light mode flips to a soft slate gradient so
   * canvas chrome doesn't fight the green succeeded bubbles or the
   * blue running ring. */
  background: var(--flow-canvas-bg, radial-gradient(ellipse at 20% 0%, rgba(11,28,58,0.85) 0%, rgba(7,10,18,0.85) 75%));
  border-radius: 12px;
  border: 1px solid var(--flow-canvas-border, rgba(255,255,255,0.04));
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
}
[data-theme="light"] .flow-canvas-host {
  --flow-canvas-bg: radial-gradient(ellipse at 20% 0%, #f1f5f9 0%, #e2e8f0 75%);
  --flow-canvas-border: #cbd5e1;
}

.flow-surface.is-fullscreen .flow-canvas-host {
  border-radius: 0;
  border: 0;
}

.flow-page-embedded .flow-canvas-host {
  min-height: 0;
  height: 100%;
  max-height: none;
}

.flow-canvas-svg-organic {
  display: block;
  width: 100%;
  max-width: 100%;
  height: 100%;
  max-height: 100%;
}

/* Full-screen toggle button — sits in the top-right of the canvas
   host, mirroring the LogPane's full-screen toggle so the two
   surfaces feel symmetric. */
.flow-fullscreen-btn {
  position: absolute;
  top: 12px;
  right: 12px;
  width: 32px;
  height: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  appearance: none;
  border: 1px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text-dim);
  border-radius: 6px;
  cursor: pointer;
  z-index: 50;
  transition: color 0.12s ease, background-color 0.12s ease, border-color 0.12s ease;
}
.flow-fullscreen-btn:hover {
  color: var(--color-text-strong);
  border-color: var(--color-text-dim);
  background: rgba(148, 163, 184, 0.10);
}
.flow-fullscreen-btn[aria-pressed='true'] {
  color: var(--color-accent, #38BDF8);
  border-color: var(--color-accent, #38BDF8);
  background: rgba(56, 189, 248, 0.12);
}
`
