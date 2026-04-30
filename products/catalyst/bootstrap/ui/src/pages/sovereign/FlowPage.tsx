/**
 * FlowPage — deployment-wide (or per-batch) flow canvas at
 * `/sovereign/provision/$deploymentId/flow?scope=...&view=...`.
 *
 * Replaces the previous JobsPage Tab pattern (PR #242). The founder
 * rejected the Tab-on-JobsPage approach; the canvas now lives at its
 * own URL so it's bookmarkable, sharable, and embeddable inside the
 * JobDetail Flow tab.
 *
 * Routing contract:
 *   • `?scope=all`           → render every job in the deployment
 *   • `?scope=batch:<id>`    → filter to a single batch
 *   • `?view=jobs|batches`   → mode toggle (default = jobs)
 *
 * Mode contract:
 *   • Jobs mode (default):
 *     - Every job rendered as a bubble; node border colour by status.
 *     - Single-click bubble  → opens the FloatingLogPane (right 25vw).
 *     - Double-click bubble  → navigates to /jobs/$jobId (JobDetail).
 *     - Click empty canvas   → closes any open FloatingLogPane.
 *   • Batches mode:
 *     - Each batch as a single supernode card.
 *     - Single-click batch   → highlights it (no log pane — batches
 *                              don't have execution logs).
 *     - Double-click batch   → switches to Jobs mode scoped to that
 *                              batch (URL becomes ?scope=batch:<id>).
 *
 * Embedded mode (`embedded` prop):
 *   • Reduces canvas height to ~50vh (so JobDetail's tab content
 *     panel doesn't stretch off screen).
 *   • Hides the canvas-level StatusStrip (JobDetail already shows
 *     a job-level breadcrumb + status badge).
 *   • Used by JobDetail's Flow tab. The `highlightJobId` prop
 *     pre-emphasises the parent job on first paint.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape ships in this PR: route,
 *      mode toggle, log pane, double-click drill, embedded variant.
 *   #2 (no compromise) — pure SVG + computed bezier; reuses the
 *      pipelineLayout.ts Sugiyama core (no graph library).
 *   #4 (never hardcode) — geometry, colours, mode keys all live in
 *      pipelineLayout / theme tokens / inline string unions.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'
import {
  pipelineLayout,
  edgeToPath,
  defaultCollapsedBatchIds,
  type FlowBatchLane,
  type FlowEdge,
  type FlowNode,
} from '@/lib/pipelineLayout'
import type { Job, JobStatus } from '@/lib/jobs.types'
import { FloatingLogPane } from '@/components/FloatingLogPane'
import {
  StatusStrip,
  type ProvisioningStatus,
} from '@/components/StatusStrip'

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
  /**
   * Embedded variant: render without the PortalShell + StatusStrip
   * chrome. Used by JobDetail's Flow tab.
   */
  embedded?: boolean
  /**
   * Override the URL-driven scope. When provided, the `?scope=`
   * search param is ignored. Used by embedded callers that already
   * have the parent batch id in hand.
   */
  scopeOverride?: FlowScope
  /**
   * Override the deploymentId param. Used by embedded callers
   * (JobDetail) that mount FlowPage from inside a different route
   * than `/flow` — TanStack Router's `useParams` cannot resolve the
   * canonical Flow route in that case.
   */
  deploymentIdOverride?: string
  /**
   * Job id to mark as highlighted (thicker border + glow). Used by
   * JobDetail's embedded Flow tab to draw the operator's eye to the
   * parent job on first paint.
   */
  highlightJobId?: string
  /**
   * Force the initial mode (overrides ?view= for unit tests).
   */
  initialMode?: FlowMode
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
  // When embedded inside JobDetail (or another non-/flow route),
  // useParams({from:'/flow'}) cannot resolve — fall back to a
  // strict:false read of the current route's params and pick the
  // deploymentId from there. The deploymentIdOverride prop short-
  // circuits both lookups for unit tests / explicit callers.
  const looseParams = useParams({ strict: false }) as {
    deploymentId?: string
  }
  const deploymentId =
    deploymentIdOverride ?? looseParams.deploymentId ?? ''
  const store = useWizardStore()

  // URL-driven search params (?scope, ?view). Read tolerantly via
  // strict:false — when the FlowPage is mounted as a child of an
  // embedded route (JobDetail Flow tab), the route-tree wiring is
  // different and strict-typed search would throw.
  const search = useSearch({ strict: false }) as {
    scope?: unknown
    view?: unknown
  }
  const navigate = useNavigate()

  const urlScope = useMemo<FlowScope>(
    () => resolveScope(search?.scope),
    [search?.scope],
  )
  const scope: FlowScope = scopeOverride ?? urlScope
  const mode: FlowMode = initialMode ?? resolveMode(search?.view)

  const setMode = useCallback(
    (next: FlowMode) => {
      if (scopeOverride) return // embedded variants don't drive the URL
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

  /* ── Data ────────────────────────────────────────────────────── */

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

  // Scope filter: 'all' → every job; 'batch:<id>' → matching batchId only.
  const scopedJobs = useMemo<Job[]>(() => {
    if (scope.kind === 'all') return [...allJobs]
    return allJobs.filter((j) => j.batchId === scope.batchId)
  }, [allJobs, scope])

  /* ── Layout ──────────────────────────────────────────────────── */

  // Jobs mode: every batch is rendered FLAT — we want a single
  // canvas with batch indication via node border colour, NOT
  // swimlane chrome. Implementation: collapse every batch in the
  // collapsedBatchIds set when mode === 'batches'; pass an empty
  // set when mode === 'jobs' so all jobs render as bubbles.
  //
  // For Jobs mode we keep the "default-collapse all-succeeded"
  // policy from JobsFlowView but only when there are multiple
  // batches in scope — single-batch scope always renders the inner
  // jobs (otherwise the operator clicked into a batch and would see
  // a useless supernode).
  const distinctBatchIds = useMemo(() => {
    const set = new Set<string>()
    for (const j of scopedJobs) set.add(j.batchId)
    return [...set]
  }, [scopedJobs])

  const collapsedBatchIds = useMemo<Set<string>>(() => {
    if (mode === 'batches') return new Set(distinctBatchIds)
    if (distinctBatchIds.length === 1) return new Set()
    return defaultCollapsedBatchIds(scopedJobs)
  }, [mode, scopedJobs, distinctBatchIds])

  const layout = useMemo(
    () => pipelineLayout(scopedJobs, { collapsedBatchIds, highlightJobId }),
    [scopedJobs, collapsedBatchIds, highlightJobId],
  )

  /* ── Floating Log Pane state ─────────────────────────────────── */

  const [openJobId, setOpenJobId] = useState<string | null>(null)
  const openJob = useMemo<Job | null>(() => {
    if (!openJobId) return null
    return scopedJobs.find((j) => j.id === openJobId) ?? null
  }, [openJobId, scopedJobs])

  // Single-click vs double-click — the SVG `onClick` fires on every
  // click in a double-click, so we debounce: schedule the single-click
  // action 220ms after the first click; if a second click arrives
  // before the timer fires, cancel the timer and fire the
  // double-click handler instead. 220ms matches the OS double-click
  // threshold in most browsers.
  const clickTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelPendingClick = () => {
    if (clickTimerRef.current) {
      clearTimeout(clickTimerRef.current)
      clickTimerRef.current = null
    }
  }
  useEffect(() => () => cancelPendingClick(), [])

  const onJobSingleClick = useCallback(
    (jobId: string) => {
      setOpenJobId(jobId)
    },
    [],
  )

  const onJobDoubleClick = useCallback(
    (jobId: string) => {
      navigate({
        to: '/provision/$deploymentId/jobs/$jobId' as never,
        params: { deploymentId, jobId } as never,
      })
    },
    [navigate, deploymentId],
  )

  const [highlightedBatchId, setHighlightedBatchId] = useState<string | null>(null)

  const onBatchSingleClick = useCallback((batchId: string) => {
    // Highlight only — no log pane on batch supernodes.
    setHighlightedBatchId(batchId)
  }, [])

  const onBatchDoubleClick = useCallback(
    (batchId: string) => {
      // Drill into the batch's Jobs view — close any open log pane,
      // clear highlights, push the new URL.
      cancelPendingClick()
      setHighlightedBatchId(null)
      setOpenJobId(null)
      if (scopeOverride) return
      navigate({
        to: '/provision/$deploymentId/flow' as never,
        params: { deploymentId } as never,
        search: { scope: `batch:${batchId}` } as never,
      })
    },
    [navigate, deploymentId, scopeOverride],
  )

  // Empty-canvas click → close any open pane / batch highlight.
  const onCanvasBackgroundClick = useCallback(() => {
    cancelPendingClick()
    setOpenJobId(null)
    setHighlightedBatchId(null)
  }, [])

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
    () =>
      scopedJobs.filter((j) => j.status === 'succeeded' || j.status === 'failed').length,
    [scopedJobs],
  )
  const totalCount = scopedJobs.length

  // Live ticking elapsed clock — recompute every second from the
  // earliest startedAt across all visible jobs (or the deployment
  // startedAt as a fallback).
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

  /* ── Render ──────────────────────────────────────────────────── */

  const canvas = (
    <FlowCanvas
      layout={layout}
      mode={mode}
      embedded={embedded}
      highlightJobId={highlightJobId ?? null}
      highlightedBatchId={highlightedBatchId}
      openJobId={openJobId}
      onJobSingleClick={onJobSingleClick}
      onJobDoubleClick={onJobDoubleClick}
      onBatchSingleClick={onBatchSingleClick}
      onBatchDoubleClick={onBatchDoubleClick}
      onCanvasBackgroundClick={onCanvasBackgroundClick}
      clickTimerRef={clickTimerRef}
      cancelPendingClick={cancelPendingClick}
    />
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
    // Embedded variant: no PortalShell, no StatusStrip — caller
    // (JobDetail) provides the chrome.
    return (
      <div className="flow-page-embedded" data-testid="flow-page-embedded">
        <style>{FLOW_PAGE_CSS}</style>
        {canvas}
      </div>
    )
  }

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{FLOW_PAGE_CSS}</style>
      {/* StatusStrip is rendered by PortalShell via PortalShellStatusStrip
          when route matches /flow* (see PortalShell.tsx). FlowPage owns
          ONLY the canvas + log pane below. */}
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

      <div className="mt-4">{canvas}</div>
      {logPane}
    </PortalShell>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Canvas — pure rendering primitive
 * ────────────────────────────────────────────────────────────────── */

interface FlowCanvasProps {
  layout: ReturnType<typeof pipelineLayout>
  mode: FlowMode
  embedded: boolean
  highlightJobId: string | null
  highlightedBatchId: string | null
  openJobId: string | null
  onJobSingleClick: (jobId: string) => void
  onJobDoubleClick: (jobId: string) => void
  onBatchSingleClick: (batchId: string) => void
  onBatchDoubleClick: (batchId: string) => void
  onCanvasBackgroundClick: () => void
  clickTimerRef: React.MutableRefObject<ReturnType<typeof setTimeout> | null>
  cancelPendingClick: () => void
}

function FlowCanvas({
  layout,
  mode,
  embedded,
  highlightJobId,
  highlightedBatchId,
  openJobId,
  onJobSingleClick,
  onJobDoubleClick,
  onBatchSingleClick,
  onBatchDoubleClick,
  onCanvasBackgroundClick,
  clickTimerRef,
  cancelPendingClick,
}: FlowCanvasProps) {
  if (layout.nodes.length === 0 && layout.batches.length === 0) {
    return (
      <div
        data-testid="flow-canvas-empty"
        className="rounded-xl border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-dim)]"
      >
        No jobs to render in the dependency graph.
      </div>
    )
  }

  return (
    <div
      className={`flow-canvas-wrap${embedded ? ' embedded' : ''}`}
      data-testid="flow-canvas-wrap"
    >
      <div className="flow-canvas-scroll">
        <svg
          width={layout.width}
          height={layout.height}
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          className="flow-canvas-svg"
          data-testid="flow-canvas-svg"
          role="img"
          aria-label="Job dependency flow"
          onClick={(e) => {
            // Delegated background click — only fire when the click
            // target is the SVG itself (not a child node/edge).
            if (e.target === e.currentTarget) onCanvasBackgroundClick()
          }}
        >
          <defs>
            <marker
              id="flow-arrow"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 Z" fill="var(--color-text-dim)" />
            </marker>
            <marker
              id="flow-arrow-meta"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 Z" fill="var(--color-accent)" />
            </marker>
            <marker
              id="flow-arrow-blocked"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 Z" fill="#F87171" />
            </marker>
          </defs>
          <style>{FLOW_CANVAS_INNER_CSS}</style>

          {/* 1. Batch swimlanes (drawn first so jobs sit on top) — only
                visible in jobs mode when there are multiple batches OR
                in batches mode (where the supernode IS the lane). */}
          {layout.batches.map((b) => (
            <BatchLane
              key={b.batchId}
              lane={b}
              mode={mode}
              highlighted={highlightedBatchId === b.batchId}
              onSingleClick={() => {
                cancelPendingClick()
                clickTimerRef.current = setTimeout(() => {
                  onBatchSingleClick(b.batchId)
                  clickTimerRef.current = null
                }, 220)
              }}
              onDoubleClick={() => {
                cancelPendingClick()
                onBatchDoubleClick(b.batchId)
              }}
            />
          ))}

          {/* 2. Edges */}
          {layout.edges.map((e, i) => (
            <FlowEdgePath key={`${e.fromId}-${e.toId}-${i}`} edge={e} />
          ))}

          {/* 3. Job bubbles */}
          {layout.nodes.map((n) =>
            n.kind === 'job' ? (
              <JobBubble
                key={n.id}
                node={n}
                isOpen={openJobId === n.id}
                isHighlighted={highlightJobId === n.id || n.highlighted === true}
                onSingleClick={() => {
                  cancelPendingClick()
                  clickTimerRef.current = setTimeout(() => {
                    onJobSingleClick(n.id)
                    clickTimerRef.current = null
                  }, 220)
                }}
                onDoubleClick={() => {
                  cancelPendingClick()
                  onJobDoubleClick(n.id)
                }}
              />
            ) : null,
          )}
        </svg>
      </div>
    </div>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Sub-components
 * ────────────────────────────────────────────────────────────────── */

interface BatchLaneProps {
  lane: FlowBatchLane
  mode: FlowMode
  highlighted: boolean
  onSingleClick: () => void
  onDoubleClick: () => void
}

const LANE_TONE: Record<
  FlowBatchLane['status'],
  { bg: string; border: string; headerBg: string; progress: string }
> = {
  succeeded: {
    bg: 'rgba(74,222,128,0.06)',
    border: 'rgba(74,222,128,0.45)',
    headerBg: 'rgba(74,222,128,0.16)',
    progress: '#4ADE80',
  },
  running: {
    bg: 'rgba(56,189,248,0.06)',
    border: 'rgba(56,189,248,0.45)',
    headerBg: 'rgba(56,189,248,0.18)',
    progress: '#38BDF8',
  },
  failed: {
    bg: 'rgba(248,113,113,0.06)',
    border: 'rgba(248,113,113,0.55)',
    headerBg: 'rgba(248,113,113,0.18)',
    progress: '#F87171',
  },
  pending: {
    bg: 'rgba(148,163,184,0.04)',
    border: 'rgba(148,163,184,0.35)',
    headerBg: 'rgba(148,163,184,0.14)',
    progress: '#94A3B8',
  },
  mixed: {
    bg: 'rgba(245,158,11,0.06)',
    border: 'rgba(245,158,11,0.45)',
    headerBg: 'rgba(245,158,11,0.16)',
    progress: '#F59E0B',
  },
}

function BatchLane({
  lane,
  mode,
  highlighted,
  onSingleClick,
  onDoubleClick,
}: BatchLaneProps) {
  const tone = LANE_TONE[lane.status]
  const HEADER_HEIGHT = 36
  const progressPct = lane.total === 0 ? 0 : Math.round((lane.finished / lane.total) * 100)
  const isInteractive = lane.collapsed && mode === 'batches'

  return (
    <g
      data-testid={`flow-batch-${lane.batchId}`}
      data-collapsed={lane.collapsed}
      data-highlighted={highlighted ? 'true' : 'false'}
      onClick={isInteractive ? onSingleClick : undefined}
      onDoubleClick={isInteractive ? onDoubleClick : undefined}
      style={isInteractive ? { cursor: 'pointer' } : undefined}
    >
      <rect
        x={lane.x}
        y={lane.y}
        width={lane.width}
        height={lane.height}
        rx={12}
        ry={12}
        fill={tone.bg}
        stroke={highlighted ? 'var(--color-accent)' : tone.border}
        strokeWidth={highlighted ? 2.5 : 1.5}
      />
      <rect
        x={lane.x}
        y={lane.y}
        width={lane.width}
        height={HEADER_HEIGHT}
        rx={12}
        ry={12}
        fill={tone.headerBg}
        stroke="none"
      />
      <foreignObject
        x={lane.x + 8}
        y={lane.y + 4}
        width={lane.width - 16}
        height={HEADER_HEIGHT - 8}
      >
        <div className="flow-batch-header">
          <span
            className="flow-batch-name"
            data-testid={`flow-batch-name-${lane.batchId}`}
            title={lane.batchId}
          >
            {lane.batchId}
          </span>
          <span className="flow-batch-count" data-testid={`flow-batch-count-${lane.batchId}`}>
            {lane.finished}/{lane.total}
          </span>
        </div>
      </foreignObject>

      {/* Mini progress bar */}
      <rect
        x={lane.x + 8}
        y={lane.y + HEADER_HEIGHT - 4}
        width={lane.width - 16}
        height={3}
        rx={1.5}
        ry={1.5}
        fill="rgba(148,163,184,0.20)"
      />
      <rect
        x={lane.x + 8}
        y={lane.y + HEADER_HEIGHT - 4}
        width={Math.max(0, ((lane.width - 16) * progressPct) / 100)}
        height={3}
        rx={1.5}
        ry={1.5}
        fill={tone.progress}
        data-testid={`flow-batch-progress-${lane.batchId}`}
      />

      {lane.collapsed ? (
        <foreignObject
          x={lane.x + 8}
          y={lane.y + HEADER_HEIGHT + 4}
          width={lane.width - 16}
          height={lane.height - HEADER_HEIGHT - 8}
        >
          <div
            className="flow-batch-collapsed"
            data-testid={`flow-batch-supernode-${lane.batchId}`}
          >
            <span className="flow-batch-collapsed-pct">{progressPct}%</span>
            <span className="flow-batch-collapsed-meta">
              {lane.total} {lane.total === 1 ? 'job' : 'jobs'}
            </span>
          </div>
        </foreignObject>
      ) : null}
    </g>
  )
}

interface JobBubbleProps {
  node: FlowNode
  isOpen: boolean
  isHighlighted: boolean
  onSingleClick: () => void
  onDoubleClick: () => void
}

const JOB_TONE: Record<JobStatus, { bg: string; border: string; label: string }> = {
  pending:   { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(148,163,184,0.30)', label: 'Pending'   },
  running:   { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(56,189,248,0.55)',  label: 'Running'   },
  succeeded: { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(74,222,128,0.55)',  label: 'Succeeded' },
  failed:    { bg: 'rgba(15,23,42,0.55)',     border: 'rgba(248,113,113,0.55)', label: 'Failed'    },
}

function JobBubble({
  node,
  isOpen,
  isHighlighted,
  onSingleClick,
  onDoubleClick,
}: JobBubbleProps) {
  if (!node.job) return null
  const j = node.job
  const tone = JOB_TONE[j.status]
  const tooltip = [
    j.jobName,
    j.appId,
    `Status: ${tone.label}`,
    j.batchId ? `Batch: ${j.batchId}` : '',
  ]
    .filter(Boolean)
    .join(' · ')
  return (
    <g
      data-testid={`flow-job-${j.id}`}
      data-status={j.status}
      data-highlighted={isHighlighted ? 'true' : 'false'}
      data-open={isOpen ? 'true' : 'false'}
      onClick={onSingleClick}
      onDoubleClick={onDoubleClick}
      style={{ cursor: 'pointer' }}
    >
      <title>{tooltip}</title>
      <rect
        x={node.x}
        y={node.y}
        width={node.width}
        height={node.height}
        rx={10}
        ry={10}
        fill={tone.bg}
        stroke={isHighlighted ? 'var(--color-accent)' : tone.border}
        strokeWidth={isHighlighted ? 2.4 : isOpen ? 2 : 1.25}
        className="flow-job-rect"
      />
      {isHighlighted ? (
        <rect
          x={node.x - 3}
          y={node.y - 3}
          width={node.width + 6}
          height={node.height + 6}
          rx={12}
          ry={12}
          fill="none"
          stroke="var(--color-accent)"
          strokeWidth={1}
          opacity={0.45}
        />
      ) : null}
      {/* Status indicator at left edge */}
      <rect
        x={node.x}
        y={node.y}
        width={4}
        height={node.height}
        rx={2}
        ry={2}
        fill={tone.border}
        opacity={j.status === 'pending' ? 0.5 : 1}
      />
      {j.status === 'running' ? (
        <circle
          cx={node.x + 12}
          cy={node.y + node.height / 2}
          r={3}
          fill="#38BDF8"
          className="flow-job-pulse-svg"
        />
      ) : null}
      <foreignObject x={node.x} y={node.y} width={node.width} height={node.height}>
        <div className="flow-job-card">
          <div className="flow-job-row">
            <span className="flow-job-name" title={j.jobName}>
              {j.jobName}
            </span>
            <span
              className={`flow-job-badge flow-job-badge-${j.status}`}
              data-testid={`flow-job-badge-${j.id}`}
            >
              {tone.label}
            </span>
          </div>
          <div className="flow-job-meta">
            <span className="flow-job-app" title={j.appId}>
              {j.appId}
            </span>
            <span className="flow-job-duration">{formatDuration(j.durationMs)}</span>
          </div>
        </div>
      </foreignObject>
    </g>
  )
}

interface FlowEdgePathProps {
  edge: FlowEdge
}

function FlowEdgePath({ edge }: FlowEdgePathProps) {
  const d = edgeToPath(edge.points)
  if (!d) return null
  let stroke = 'var(--color-text-dim)'
  let strokeWidth = 1.25
  let dash: string | undefined
  let marker = 'url(#flow-arrow)'
  let opacity = 0.6
  if (edge.kind === 'meta' || edge.kind === 'cross-batch-job') {
    stroke = 'var(--color-accent)'
    strokeWidth = 1.75
    marker = 'url(#flow-arrow-meta)'
    opacity = 0.85
  }
  if (edge.blocked) {
    stroke = '#F87171'
    dash = '4 4'
    marker = 'url(#flow-arrow-blocked)'
    opacity = 0.95
  }
  return (
    <path
      d={d}
      fill="none"
      stroke={stroke}
      strokeWidth={strokeWidth}
      strokeDasharray={dash}
      opacity={opacity}
      markerEnd={marker}
      data-testid={`flow-edge-${edge.fromId}-${edge.toId}`}
      data-kind={edge.kind}
      data-blocked={edge.blocked ? 'true' : 'false'}
    >
      {edge.tooltip ? <title>{edge.tooltip}</title> : null}
    </path>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Helpers
 * ────────────────────────────────────────────────────────────────── */

function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

/* ──────────────────────────────────────────────────────────────────
 * Styles — kept co-located so the canvas + log pane stay in lockstep.
 * ────────────────────────────────────────────────────────────────── */

const FLOW_PAGE_CSS = `
.flow-page-embedded { width: 100%; }
.flow-canvas-wrap   { width: 100%; }
.flow-canvas-wrap.embedded .flow-canvas-scroll { max-height: 50vh; }
.flow-canvas-scroll {
  width: 100%;
  overflow: auto;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
  max-height: 78vh;
}
.flow-canvas-svg {
  display: block;
  font-family: var(--font-sans, system-ui);
}
`

const FLOW_CANVAS_INNER_CSS = `
.flow-batch-header {
  display: flex;
  align-items: center;
  gap: 0.45rem;
  height: 100%;
  color: var(--color-text);
  font-size: 0.78rem;
  font-weight: 600;
}
.flow-batch-name {
  flex: 1 1 auto;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
}
.flow-batch-count {
  font-size: 0.7rem;
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.flow-batch-collapsed {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  gap: 0.15rem;
}
.flow-batch-collapsed-pct {
  font-size: 1.1rem;
  font-weight: 700;
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
}
.flow-batch-collapsed-meta {
  font-size: 0.7rem;
  color: var(--color-text-dim);
  font-variant-numeric: tabular-nums;
}

.flow-job-card {
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  height: 100%;
  width: 100%;
  padding: 0.45rem 0.55rem 0.45rem 0.85rem;
  box-sizing: border-box;
  pointer-events: none;
}
.flow-job-row {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  justify-content: space-between;
}
.flow-job-name {
  flex: 1 1 auto;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--color-text-strong);
}
.flow-job-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.4rem;
  font-size: 0.66rem;
  color: var(--color-text-dim);
  font-family: var(--font-mono, ui-monospace, monospace);
}
.flow-job-app {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--color-accent);
}
.flow-job-duration {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.flow-job-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  padding: 0.08rem 0.4rem;
  border-radius: 999px;
  font-size: 0.58rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  white-space: nowrap;
}
.flow-job-badge-pending   { background: rgba(148,163,184,0.10); color: var(--color-text-dim); }
.flow-job-badge-running   { background: rgba(56,189,248,0.10);  color: #38BDF8; }
.flow-job-badge-succeeded { background: rgba(74,222,128,0.10);  color: #4ADE80; }
.flow-job-badge-failed    { background: rgba(248,113,113,0.10); color: #F87171; }

.flow-job-pulse-svg {
  animation: flow-pulse 1.6s ease-in-out infinite;
}
@keyframes flow-pulse {
  0%,100% { opacity: 1; transform: scale(1); }
  50%     { opacity: 0.4; transform: scale(0.6); }
}

.flow-job-rect:hover { filter: brightness(1.08); }
`
