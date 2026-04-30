/**
 * FlowPage — deployment-wide (or per-batch) flow canvas at
 * `/sovereign/provision/$deploymentId/flow?scope=...&view=...`.
 *
 * v4 redesign (issue #251 — supersedes the pill-card divergence shipped
 * in PR #245). Rendering layer rebuilt to MATCH the canonical mockup
 * `marketing/mockups/provision-mockup-v4.png`:
 *
 *   • Circular nodes with family-coloured rings + status arcs.
 *   • Multi-region grouping (top/bottom band per region).
 *   • Bezier curve edges; status-tinted arrow markers.
 *   • Persistent right-side log feed panel.
 *   • Static left-side deployment-progress tree (region → family →
 *     job, NO accordion per the operator's standing rule).
 *
 * The data adapter is preserved verbatim from PR #245+#249:
 *   • `useDeploymentEvents` — SSE replay of the deployment.
 *   • `useLiveJobsBackfill` — REST polling for the running jobs list.
 *   • `mergeJobs` — reconciles reducer + live sources (Agent E #249).
 *
 * Routing contract (unchanged):
 *   • `?scope=all`           → render every job in the deployment.
 *   • `?scope=batch:<id>`    → filter to a single batch.
 *   • `?view=jobs|batches`   → mode toggle (default = jobs).
 *
 * Mode contract (unchanged):
 *   • Jobs mode (default) — single-click opens the FloatingLogPane;
 *     double-click navigates to /jobs/$jobId.
 *   • Batches mode — single-click highlights; double-click drills into
 *     /flow?scope=batch:<id>.
 *
 * Embedded mode (`embedded` prop, used by JobDetail's Flow tab) drops
 * the PortalShell + StatusStrip chrome. `highlightJobId` pre-emphasises
 * the parent job on first paint.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape: circular nodes, multi-region,
 *      bezier, log feed, deployment tree all in this PR.
 *   #2 (no compromise) — rendering layer is the FlowCanvasV4 + log +
 *      tree triplet; no graph library, no canvas rendering.
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
  flowLayoutV4,
  DEFAULT_FAMILIES,
  FALLBACK_REGION_ID,
  type FlowFamily,
  type FlowRegion,
  type FlowNodeHints,
} from '@/lib/flowLayoutV4'
import type { Job, JobStatus } from '@/lib/jobs.types'
import { FloatingLogPane } from '@/components/FloatingLogPane'
import {
  StatusStrip,
  type ProvisioningStatus,
} from '@/components/StatusStrip'
import { FlowCanvasV4 } from './FlowCanvasV4'
import { FlowLogFeed, type FlowLogStreamLine } from './FlowLogFeed'
import {
  FlowDeploymentTree,
  type FlowGroupRow,
} from './FlowDeploymentTree'
import { buildFlowGroupRows } from './flowDeploymentTreeData'
import { PRODUCTS } from '@/pages/wizard/steps/componentGroups'

/* ──────────────────────────────────────────────────────────────────
 * Public types
 * ────────────────────────────────────────────────────────────────── */

export type FlowScope = { kind: 'all' } | { kind: 'batch'; batchId: string }
export type FlowMode = 'jobs' | 'batches'

/**
 * Resolve a free-form `?scope=...` query string into a typed scope.
 * Pure helper exported for unit-testing and for embedding callers
 * (JobDetail) that synthesise the scope from a parent batch id.
 */
export function resolveScope(raw: unknown): FlowScope {
  if (typeof raw !== 'string') return { kind: 'all' }
  if (raw === 'all') return { kind: 'all' }
  if (raw.startsWith('batch:')) {
    const batchId = raw.slice('batch:'.length)
    if (batchId.length > 0) return { kind: 'batch', batchId }
  }
  return { kind: 'all' }
}

export function resolveMode(raw: unknown): FlowMode {
  return raw === 'batches' ? 'batches' : 'jobs'
}

interface FlowPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
  /** Embedded variant: render without the PortalShell + StatusStrip chrome. */
  embedded?: boolean
  /** Override the URL-driven scope. */
  scopeOverride?: FlowScope
  /** Override the deploymentId param. */
  deploymentIdOverride?: string
  /** Highlight a single job (thicker accent border + glow). */
  highlightJobId?: string
  /** Force the initial mode (overrides ?view= for unit tests). */
  initialMode?: FlowMode
}

/* ──────────────────────────────────────────────────────────────────
 * Helpers — family palette + per-job hints
 * ────────────────────────────────────────────────────────────────── */

/**
 * Build the family palette from the public PRODUCTS taxonomy in
 * componentGroups.ts (so the canvas + tree always match the wizard
 * StepComponents page colour-coding). Falls back to the default palette
 * for entries the catalog hasn't taught us yet (catalyst, platform).
 */
function useFamilyPalette(): FlowFamily[] {
  return useMemo(() => {
    const fromCatalog = PRODUCTS.map((p) => {
      const fallback = DEFAULT_FAMILIES.find((f) => f.id === p.id)
      return {
        id: p.id,
        label: p.name,
        color: fallback?.color ?? '#94A3B8',
      } satisfies FlowFamily
    })
    // Append entries that exist in DEFAULT_FAMILIES but not in PRODUCTS.
    const seen = new Set(fromCatalog.map((f) => f.id))
    for (const f of DEFAULT_FAMILIES) {
      if (!seen.has(f.id)) fromCatalog.push(f)
    }
    return fromCatalog
  }, [])
}

/** Family description lookup — feeds the deployment tree side panel. */
function useFamilyDescriptions(): Readonly<Record<string, string>> {
  return useMemo(() => {
    const out: Record<string, string> = {}
    for (const p of PRODUCTS) out[p.id] = p.subtitle ?? ''
    out.catalyst = 'Bootstrap & K8s'
    out.platform = 'Platform'
    return out
  }, [])
}

/**
 * Build the per-job hint map (regionId / familyId / label / stage /
 * extraDepIds) from the deployment store + ApplicationDescriptors.
 *
 * Stage hint derivation:
 *   • Phase 0 (`infrastructure:*`) → stage 1 (always first).
 *   • Cluster bootstrap (`cluster-bootstrap`) → stage 2.
 *   • Per-component jobs → stage = 3 + componentDepthInGraph.
 *     Where componentDepthInGraph is the longest-path depth of this
 *     component in the ApplicationDescriptor.dependencies graph.
 *
 * This produces the canonical 10-stage left→right install ordering the
 * v4 mockup specifies even when Job.dependsOn is empty (which is the
 * default for the test catalog and any deployment that hasn't started
 * shipping individual `dependsOn` arrays from catalyst-api yet).
 *
 * extraDepIds — surfaces the component-graph dependency edges the
 * layout would otherwise miss. Resolves bare component ids (e.g.
 * "cnpg") to their canonical Job ids (`bp-cnpg`). Phase 0 + bootstrap
 * jobs get an implicit edge from infrastructure → cluster-bootstrap →
 * every component-job, so the canvas always reads stage-1 → stage-2 →
 * components.
 */
function useJobHints(args: {
  jobs: readonly Job[]
  applications: readonly ApplicationDescriptor[]
  regions: readonly FlowRegion[]
}): Map<string, FlowNodeHints> {
  const { jobs, applications, regions } = args
  return useMemo(() => {
    const out = new Map<string, FlowNodeHints>()
    const appById = new Map<string, ApplicationDescriptor>()
    const appByBareId = new Map<string, ApplicationDescriptor>()
    for (const a of applications) {
      appById.set(a.id, a)
      appByBareId.set(a.bareId, a)
    }
    const fallbackRegion = regions[0]?.id ?? FALLBACK_REGION_ID

    // Compute per-app component-graph depth (longest path in
    // dependencies). Independent components → depth 0; depends on
    // depth-0 → depth 1; etc.
    const depthCache = new Map<string, number>()
    function depth(bareId: string, seen: Set<string> = new Set()): number {
      if (depthCache.has(bareId)) return depthCache.get(bareId)!
      if (seen.has(bareId)) return 0 // cycle defence
      seen.add(bareId)
      const app = appByBareId.get(bareId)
      const deps = app?.dependencies ?? []
      if (deps.length === 0) {
        depthCache.set(bareId, 0)
        return 0
      }
      const d = 1 + Math.max(0, ...deps.map((d) => depth(d, seen)))
      depthCache.set(bareId, d)
      return d
    }

    // Resolve a Phase 0 / bootstrap-aware Job id from a bareId.
    function jobIdForBare(bareId: string): string | null {
      const app = appByBareId.get(bareId)
      return app?.id ?? null
    }

    // Find the cluster-bootstrap job id (it's emitted with id
    // 'cluster-bootstrap' by deriveJobs).
    const bootstrapJobId = jobs.find((j) => j.appId === 'cluster-bootstrap')?.id ?? null
    // Phase 0 final stage — the tofu-output job kicks off everything
    // downstream.
    const phase0FinalJobId =
      jobs.find((j) => j.id === 'infrastructure:tofu-output')?.id ?? null

    for (const j of jobs) {
      // Region hint: respect a "::<regionId>" suffix on the job id.
      let regionId = fallbackRegion
      const sep = j.id.indexOf('::')
      if (sep > 0) {
        const candidate = j.id.slice(sep + 2)
        if (regions.some((r) => r.id === candidate)) regionId = candidate
      }

      let familyId: string
      let stage: number
      const extraDepIds: string[] = []

      if (j.appId === 'infrastructure') {
        familyId = 'catalyst'
        // Stage by tofu phase order — init/plan/apply/output → 1/1/1/1
        // (single-column anchor in the mockup's stage 1).
        stage = 1
      } else if (j.appId === 'cluster-bootstrap') {
        familyId = 'catalyst'
        stage = 2
        if (phase0FinalJobId) extraDepIds.push(phase0FinalJobId)
      } else {
        const app = appById.get(j.appId)
        familyId = app?.familyId ?? 'platform'
        const d = app?.bareId ? depth(app.bareId) : 0
        stage = 3 + d
        // Inject component-level deps as extra layout edges.
        if (app) {
          for (const dep of app.dependencies ?? []) {
            const depJobId = jobIdForBare(dep)
            if (depJobId) extraDepIds.push(depJobId)
          }
        }
        // Every leaf component-job depends on cluster-bootstrap so the
        // canvas reads "infra → bootstrap → components" left to right.
        if (bootstrapJobId) extraDepIds.push(bootstrapJobId)
      }

      const label = j.jobName.replace(/^install-/, '')
      out.set(j.id, { regionId, familyId, label, stage, extraDepIds })
    }
    return out
  }, [jobs, applications, regions])
}

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

export function FlowPage({
  disableStream = false,
  disableJobsBackfill = false,
  embedded = false,
  scopeOverride,
  deploymentIdOverride,
  highlightJobId,
  initialMode,
}: FlowPageProps = {}) {
  const looseParams = useParams({ strict: false }) as { deploymentId?: string }
  const deploymentId = deploymentIdOverride ?? looseParams.deploymentId ?? ''
  const store = useWizardStore()

  const search = useSearch({ strict: false }) as { scope?: unknown; view?: unknown }
  const navigate = useNavigate()

  const urlScope = useMemo<FlowScope>(() => resolveScope(search?.scope), [search?.scope])
  const scope: FlowScope = scopeOverride ?? urlScope
  const mode: FlowMode = initialMode ?? resolveMode(search?.view)

  const setMode = useCallback(
    (next: FlowMode) => {
      if (scopeOverride) return
      const nextSearch: Record<string, string> = {}
      if (scope.kind === 'batch') nextSearch.scope = `batch:${scope.batchId}`
      else nextSearch.scope = 'all'
      if (next === 'batches') nextSearch.view = 'batches'
      navigate({
        to: '/provision/$deploymentId/flow' as never,
        params: { deploymentId } as never,
        search: nextSearch as never,
      })
    },
    [navigate, deploymentId, scope, scopeOverride],
  )

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

  const scopedJobs = useMemo<Job[]>(() => {
    if (scope.kind === 'all') return [...allJobs]
    return allJobs.filter((j) => j.batchId === scope.batchId)
  }, [allJobs, scope])

  /* ── Region descriptors (multi-region support) ───────────────── */

  const regions = useMemo<FlowRegion[]>(() => {
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
  const familyDescriptions = useFamilyDescriptions()
  const hints = useJobHints({ jobs: scopedJobs, applications, regions })

  /* ── Layout ───────────────────────────────────────────────────── */

  const layout = useMemo(
    () => flowLayoutV4(scopedJobs, { hints, regions, families, highlightJobId }),
    [scopedJobs, hints, regions, families, highlightJobId],
  )

  /* ── Click semantics (single vs double, debounced 220ms) ────── */

  const [openJobId, setOpenJobId] = useState<string | null>(null)
  const openJob = useMemo<Job | null>(() => {
    if (!openJobId) return null
    return scopedJobs.find((j) => j.id === openJobId) ?? null
  }, [openJobId, scopedJobs])

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
      navigate({
        to: '/provision/$deploymentId/jobs/$jobId' as never,
        params: { deploymentId, jobId } as never,
      })
    },
    [navigate, deploymentId, cancelPendingClick],
  )

  const handleCanvasBackgroundClick = useCallback(() => {
    cancelPendingClick()
    setOpenJobId(null)
  }, [cancelPendingClick])

  /* ── StatusStrip rollup ──────────────────────────────────────── */

  const provisioningStatus: ProvisioningStatus = useMemo(() => {
    if (scopedJobs.length === 0) return 'pending'
    const buckets = new Set(scopedJobs.map((j) => j.status))
    if (buckets.has('failed')) {
      const allTerminal = scopedJobs.every((j) => j.status === 'succeeded' || j.status === 'failed')
      return allTerminal ? 'failed' : 'running'
    }
    if (buckets.has('running') || buckets.has('pending')) return 'running'
    return 'succeeded'
  }, [scopedJobs])

  const finishedCount = useMemo(
    () => scopedJobs.filter((j) => j.status === 'succeeded' || j.status === 'failed').length,
    [scopedJobs],
  )
  const totalCount = scopedJobs.length

  const earliestStarted = useMemo<number | null>(() => {
    let earliest: number | null = null
    for (const j of scopedJobs) {
      if (!j.startedAt) continue
      const t = Date.parse(j.startedAt)
      if (!Number.isFinite(t)) continue
      if (earliest === null || t < earliest) earliest = t
    }
    if (earliest !== null) return earliest
    return startedAt ?? null
  }, [scopedJobs, startedAt])

  const [now, setNow] = useState<number>(() => Date.now())
  useEffect(() => {
    if (provisioningStatus !== 'running') return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [provisioningStatus])
  const elapsedMs = earliestStarted === null ? 0 : Math.max(0, now - earliestStarted)

  /* ── Deployment-tree rows ────────────────────────────────────── */

  const deploymentTreeGroups: FlowGroupRow[] = useMemo(
    () =>
      buildFlowGroupRows({
        jobs: scopedJobs,
        hintByJob: hints,
        regions,
        families,
        familyDescriptions,
      }),
    [scopedJobs, hints, regions, families, familyDescriptions],
  )

  /* ── Log-feed lines: derive from the active job's events ─────── */

  // Pick a focused job for the right-side log feed when nothing has been
  // explicitly opened: prefer the first running job, then the first
  // pending, else the first job overall. This matches the mockup's
  // "always show something" behaviour.
  const focusedJob = useMemo<Job | null>(() => {
    if (openJob) return openJob
    if (highlightJobId) {
      const h = scopedJobs.find((j) => j.id === highlightJobId)
      if (h) return h
    }
    const running = scopedJobs.find((j) => j.status === 'running')
    if (running) return running
    const failed = scopedJobs.find((j) => j.status === 'failed')
    if (failed) return failed
    return scopedJobs[0] ?? null
  }, [openJob, highlightJobId, scopedJobs])

  const logLines = useMemo<FlowLogStreamLine[]>(() => {
    if (!focusedJob) return []
    // Replay the focused job's recent events from the reducer state. We
    // mirror what JobDetail's Exec Log surfaces but condensed to a few
    // lines for the always-on stream.
    const eventsFor = focusedJob.appId
    const buckets = state.eventsByTarget ?? {}
    const stream = buckets[eventsFor] ?? buckets[focusedJob.id] ?? []
    const recent = stream.slice(-12)
    return recent.map((ev, i) => {
      const status: JobStatus =
        ev.level === 'error'
          ? 'failed'
          : ev.state === 'installed'
            ? 'succeeded'
            : ev.state === 'failed'
              ? 'failed'
              : ev.state === 'pending'
                ? 'pending'
                : 'running'
      const message = ev.message?.trim() || `${ev.phase}${ev.component ? ` · ${ev.component}` : ''}`
      return {
        id: `${ev.phase}-${ev.component ?? '_'}-${i}`,
        timestamp: ev.time ?? null,
        status,
        message,
      }
    })
  }, [focusedJob, state])

  const logFeedLive =
    !!focusedJob && (focusedJob.status === 'running' || focusedJob.status === 'pending')

  /* ── Render ──────────────────────────────────────────────────── */

  const canvas = (
    <FlowCanvasV4
      layout={layout}
      families={families}
      embedded={embedded}
      highlightJobId={highlightJobId ?? null}
      openJobId={openJobId}
      onJobClick={handleJobClick}
      onJobDoubleClick={handleJobDoubleClick}
      onCanvasBackgroundClick={handleCanvasBackgroundClick}
    />
  )

  // The canvas + log feed + tree always render together — even during
  // the (intentionally rare) "no jobs" empty state — so the operator
  // always sees the same chrome and the live log panel persists.
  const flowSurface = (
    <div className="flow-surface" data-testid="flow-surface">
      {!embedded ? (
        <FlowDeploymentTree
          groups={deploymentTreeGroups}
          selectedJobId={openJobId}
          onSelectJob={(id) => setOpenJobId(id)}
          totals={{ finished: finishedCount, total: totalCount }}
        />
      ) : null}
      <div className="flow-canvas-host" data-testid="flow-canvas-host">
        {canvas}
      </div>
      {!embedded ? (
        <FlowLogFeed
          job={focusedJob}
          lines={logLines}
          live={logFeedLive}
        />
      ) : null}
    </div>
  )

  const logPane =
    !embedded && openJob ? (
      <FloatingLogPane
        executionId={openJob.startedAt ? `${openJob.id}:latest` : null}
        jobTitle={openJob.jobName}
        statusLabel={openJob.status}
        statusTone={openJob.status}
        onClose={() => setOpenJobId(null)}
      />
    ) : null

  if (embedded) {
    return (
      <div className="flow-page-embedded" data-testid="flow-page-embedded">
        <style>{FLOW_PAGE_CSS_V4}</style>
        {flowSurface}
      </div>
    )
  }

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{FLOW_PAGE_CSS_V4}</style>
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-[var(--color-text-strong)]">
            {scope.kind === 'all' ? 'Flow' : `Flow · ${scope.batchId}`}
          </h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            {scope.kind === 'all'
              ? 'Deployment-wide dependency graph for '
              : 'Batch-scoped dependency graph for '}
            <span className="font-mono">
              {sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`}
            </span>
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Link
            to="/provision/$deploymentId/jobs"
            params={{ deploymentId }}
            className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
            data-testid="flow-page-back-to-table"
          >
            ← Back to table
          </Link>
        </div>
      </div>

      <div className="mt-4">
        <StatusStrip
          deploymentId={deploymentId}
          sovereignFQDN={sovereignFQDN}
          status={provisioningStatus}
          finished={finishedCount}
          total={totalCount}
          elapsedMs={elapsedMs}
          modeToggle={{ mode, onChange: setMode }}
        />
      </div>

      <div className="mt-4">{flowSurface}</div>
      {logPane}
    </PortalShell>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Inline CSS — kept co-located so canvas + log + tree stay in lockstep
 * ────────────────────────────────────────────────────────────────── */

const FLOW_PAGE_CSS_V4 = `
.flow-page-embedded { width: 100%; }

.flow-surface {
  display: grid;
  grid-template-columns: 232px minmax(0, 1fr) 280px;
  gap: 12px;
  width: 100%;
  align-items: stretch;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface, rgba(7,10,18,0.55));
  padding: 12px;
  min-height: 540px;
  height: calc(100vh - 220px);
  max-height: 820px;
}

.flow-page-embedded .flow-surface {
  grid-template-columns: 1fr;
  min-height: 0;
  padding: 0;
  border: 0;
  background: transparent;
}

.flow-canvas-host {
  position: relative;
  min-width: 0;
  height: 100%;
  background: radial-gradient(ellipse at 20% 0%, rgba(11,28,58,0.85) 0%, rgba(7,10,18,0.85) 75%);
  border-radius: 12px;
  border: 1px solid rgba(255,255,255,0.04);
  overflow: auto;
  display: flex;
  align-items: center;
  justify-content: center;
}

.flow-page-embedded .flow-canvas-host {
  min-height: 0;
  max-height: 50vh;
  height: 50vh;
}

.flow-canvas-svg-v4 {
  display: block;
  width: 100%;
  max-width: 100%;
  height: 100%;
  max-height: 100%;
}

/* ── Deployment tree (left) ─────────────────────────────────────── */

.flow-deployment-tree {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-height: 0;
  height: 100%;
  border-radius: 10px;
  border: 1px solid rgba(255,255,255,0.05);
  background: rgba(255,255,255,0.015);
  padding: 10px 0 12px;
  overflow: hidden;
}
.flow-tree-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px 6px;
  border-bottom: 1px solid rgba(255,255,255,0.05);
}
.flow-tree-header-label {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.10em;
  text-transform: uppercase;
  color: rgba(255,255,255,0.40);
}
.flow-tree-header-count {
  font-size: 10px;
  font-weight: 700;
  color: var(--color-accent, #38BDF8);
  font-variant-numeric: tabular-nums;
}
.flow-tree-progress-bar {
  height: 2px;
  background: rgba(255,255,255,0.06);
  margin: 0 12px 4px;
  border-radius: 1px;
  overflow: hidden;
}
.flow-tree-progress-fill {
  height: 100%;
  background: linear-gradient(90deg, #38BDF8, #818CF8);
  transition: width 0.4s ease;
}
.flow-tree-body {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 4px 0;
  scrollbar-width: thin;
}
.flow-tree-empty {
  padding: 16px 12px;
  font-size: 11px;
  color: rgba(255,255,255,0.30);
  text-align: center;
}
.flow-tree-group { margin-bottom: 4px; }
.flow-tree-region-header {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 8px 12px 4px;
  border-top: 1px solid rgba(255,255,255,0.04);
}
.flow-tree-region-header:first-child { border-top: 0; }
.flow-tree-region-dot {
  width: 7px; height: 7px; border-radius: 50%;
  background: var(--color-accent, #38BDF8);
  box-shadow: 0 0 6px rgba(56,189,248,0.55);
}
.flow-tree-region-meta {
  display: flex; flex-direction: column;
  flex: 1; min-width: 0;
}
.flow-tree-region-name {
  font-size: 11px;
  font-weight: 700;
  color: rgba(255,255,255,0.9);
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.04em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.flow-tree-region-sub {
  font-size: 9px;
  color: rgba(255,255,255,0.30);
  margin-top: 1px;
}
.flow-tree-family-row {
  display: flex; align-items: center; gap: 6px;
  padding: 4px 12px 4px 18px;
}
.flow-tree-family-dot {
  width: 6px; height: 6px; border-radius: 50%;
  flex-shrink: 0;
}
.flow-tree-family-name {
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.04em;
  min-width: 56px;
}
.flow-tree-family-desc {
  flex: 1; min-width: 0;
  font-size: 9px;
  color: rgba(255,255,255,0.32);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.flow-tree-family-count {
  font-size: 9px;
  color: rgba(255,255,255,0.40);
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
.flow-tree-job-row {
  display: flex; align-items: center; gap: 6px;
  width: 100%;
  padding: 3px 12px 3px 30px;
  background: transparent;
  border: 0;
  cursor: pointer;
  text-align: left;
  transition: background-color 0.15s;
}
.flow-tree-job-row:hover { background: rgba(255,255,255,0.025); }
.flow-tree-job-row.is-selected { background: rgba(56,189,248,0.10); }
.flow-tree-job-dot {
  width: 5px; height: 5px; border-radius: 50%;
  flex-shrink: 0;
}
.flow-tree-job-name {
  flex: 1; min-width: 0;
  font-size: 10px;
  color: rgba(255,255,255,0.65);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.flow-tree-job-row.is-selected .flow-tree-job-name { color: rgba(255,255,255,0.95); }
.flow-tree-job-duration {
  font-size: 9px;
  color: rgba(255,255,255,0.30);
  font-variant-numeric: tabular-nums;
}

/* ── Log feed (right) ───────────────────────────────────────────── */

.flow-log-feed {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  border-radius: 10px;
  border: 1px solid rgba(255,255,255,0.05);
  background: rgba(2,6,15,0.75);
  overflow: hidden;
}
.flow-log-feed-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 11px;
  border-bottom: 1px solid rgba(255,255,255,0.05);
  flex-shrink: 0;
}
.flow-log-feed-label {
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.10em;
  text-transform: uppercase;
  color: rgba(255,255,255,0.32);
}
.flow-log-feed-chip {
  font-size: 9px;
  font-weight: 600;
  padding: 2px 7px;
  border-radius: 4px;
  color: var(--color-accent, #38BDF8);
  background: rgba(56,189,248,0.08);
  border: 1px solid rgba(56,189,248,0.20);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 110px;
}
.flow-log-feed-status {
  margin-left: auto;
  font-size: 9px;
  color: rgba(255,255,255,0.32);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.flow-log-feed-stream {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 8px 11px;
  font-family: 'JetBrains Mono', ui-monospace, monospace;
  font-size: 10px;
  line-height: 1.85;
  scrollbar-width: thin;
}
.flow-log-feed-empty {
  padding: 16px 0;
  font-size: 10px;
  color: rgba(255,255,255,0.32);
  text-align: center;
  font-family: 'Inter', system-ui, sans-serif;
}
.flow-log-feed-line {
  display: flex;
  gap: 8px;
}
.flow-log-feed-ts {
  color: rgba(255,255,255,0.28);
  font-size: 9px;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}
.flow-log-feed-msg {
  flex: 1;
  word-break: break-word;
}
.flow-log-feed-cursor {
  display: inline-block;
  width: 5px;
  height: 10px;
  background: var(--color-accent, #38BDF8);
  margin-left: 3px;
  vertical-align: text-bottom;
  animation: flow-log-blink 1.1s step-end infinite;
}
@keyframes flow-log-blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0; }
}

/* ── Responsive — drop side panels on narrower viewports ────────── */
@media (max-width: 1280px) {
  .flow-surface { grid-template-columns: 200px minmax(0, 1fr) 240px; }
}
@media (max-width: 1080px) {
  .flow-surface { grid-template-columns: minmax(0, 1fr) 240px; }
  .flow-deployment-tree { display: none; }
}
@media (max-width: 840px) {
  .flow-surface { grid-template-columns: minmax(0, 1fr); }
  .flow-log-feed { display: none; }
}
`
