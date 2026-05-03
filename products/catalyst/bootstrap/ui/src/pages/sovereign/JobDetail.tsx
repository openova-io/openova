/**
 * JobDetail — per-Job home page (issue #351).
 *
 * Layout:
 *   ┌────────────────────────────────────────────────────────────────┐
 *   │ ← Back   <jobName>                              [STATUS CHIP]  │
 *   │ <jobId> · <appId> · <parentDisplayName> · last update HH:MM:SS │
 *   ├────────────────────────────────────────────────────────────────┤
 *   │                                                                 │
 *   │                full-bleed FlowPage canvas                       │
 *   │                                                                 │
 *   │                                                ┌──────────────┐ │
 *   │                                                │  LogPane     │ │
 *   │                                                │  (host job   │ │
 *   │                                                │   logs by    │ │
 *   │                                                │   default)   │ │
 *   │                                                └──────────────┘ │
 *   └────────────────────────────────────────────────────────────────┘
 *
 * Behavioural contract:
 *
 *   • The canvas opens with the page's host job auto-selected — its
 *     logs are visible in the LogPane on first paint.
 *   • Single-clicking another job swaps the LogPane's contents but
 *     the host's teal ring stays.
 *   • Double-clicking a leaf navigates to its own home; double-
 *     clicking a parent group toggles its fold state inline.
 *   • The LogPane is closeable; closing it restores the host as the
 *     selected job.
 *   • The canvas has no tabs and uses the entire viewport beneath the
 *     2-line header — replaces the previous Tabs.List / Tabs.Panel
 *     layout that wasted >80% of the canvas area.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape ships in this PR. The previous
 *      tab strip is gone, not feature-flagged.
 *   #2 (no compromise) — no tab/accordion fallback; full-bleed canvas
 *      + floating pane is the only layout.
 *   #4 (never hardcode) — every label / id / route key is derived.
 */

import { useCallback, useMemo, useState } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs, fmtTime, statusBadge } from './jobs'
import type { Job as DerivedJob, JobUiStatus, JobStep } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'
import { useJobDetail } from './useJobDetail'
import type { Job } from '@/lib/jobs.types'
import { LogPane } from '@/components/LogPane'
import type { LogLine, LogLevel } from '@/components/ExecutionLogs'
import { FlowPage } from './FlowPage'

interface JobDetailProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
}

export function JobDetail({
  disableStream = false,
  disableJobsBackfill = false,
}: JobDetailProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/jobs/$jobId' as never,
  }) as { deploymentId: string; jobId: string }
  const { deploymentId, jobId } = params
  const store = useWizardStore()

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot, streamStatus } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })

  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const derivedJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const derivedJobsById = useMemo<Record<string, DerivedJob>>(() => {
    const out: Record<string, DerivedJob> = {}
    for (const j of derivedJobs) out[j.id] = j
    return out
  }, [derivedJobs])
  const reducerJobs = useMemo(() => adaptDerivedJobsToFlat(derivedJobs), [derivedJobs])
  const inFlight = streamStatus !== 'completed' && streamStatus !== 'failed'
  const { liveJobs } = useLiveJobsBackfill({
    deploymentId,
    enabled: !disableJobsBackfill,
    disablePolling: disableJobsBackfill || !inFlight,
  })
  const jobs = useMemo(
    () => mergeJobs(reducerJobs, liveJobs),
    [reducerJobs, liveJobs],
  )
  const jobsById = useMemo<Record<string, Job>>(() => {
    const out: Record<string, Job> = {}
    for (const j of jobs) out[j.id] = j
    return out
  }, [jobs])
  const job = jobsById[jobId]

  // Host job = the page owner. The canvas paints a persistent teal
  // ring on this id and the LogPane defaults to the host's logs.
  // The selected-for-logs id can change as the operator clicks
  // around; the host stays put.
  const [selectedJobId, setSelectedJobId] = useState<string>(jobId)
  const selectedJob: Job | null = jobsById[selectedJobId] ?? job ?? null
  // Pane visibility — open by default. The X / Esc dismisses it;
  // any explicit canvas-bubble click reopens it.
  const [paneOpen, setPaneOpen] = useState<boolean>(true)

  // Resolve the REAL execution id by polling the per-job detail
  // endpoint. The selected job (NOT the host) drives this so the
  // pane updates when the operator clicks around.
  const detail = useJobDetail({
    deploymentId,
    jobId: selectedJob?.id ?? jobId,
    disablePolling: disableJobsBackfill || !inFlight,
    enabled: !disableJobsBackfill,
  })
  const executionId = detail.latestExecutionId
  const detailJobStatus = detail.job?.status

  // Bug #481 — derived-job fallback log lines. When `useJobDetail` has no
  // Bridge-allocated execution (Phase-0 tofu jobs, cluster-bootstrap, or
  // any per-bp-* job whose Helm watch has not yet emitted an execution
  // row), the LogPane would render the "No execution recorded yet"
  // empty state — even though we already have the SSE event log buffered
  // in derivedJobs[selectedJobId].steps. Map those steps to LogLines so
  // the operator sees the captured events.
  const fallbackLines = useMemo<LogLine[]>(() => {
    if (executionId) return [] // Real execution wins — don't double-render.
    const id = selectedJob?.id ?? jobId
    const dj = derivedJobsById[id]
    if (!dj) return []
    return stepsToLogLines(dj.steps)
  }, [executionId, selectedJob, jobId, derivedJobsById])

  // Issue #669 — provision-wide global log stream. Merge the SSE-event
  // step log of every derived job (Phase 0, cluster-bootstrap, every
  // bp-*) into a single timestamp-sorted list. Each line's message is
  // prefixed with the source job's display name so the column reads as
  // a unified provision tail without losing the per-component context.
  // Re-runs on every reducer state change (every SSE event) so the
  // global stream is live, no polling needed.
  const globalLines = useMemo<LogLine[]>(() => {
    const merged: Array<{ ts: number; line: LogLine }> = []
    let counter = 1
    for (const dj of derivedJobs) {
      // DerivedJob (./jobs.ts) uses `title` for the human-readable
      // label — not `displayName`/`jobName` which belong to the flat
      // Job in @/lib/jobs.types. Fall back to the id when title is
      // unset (it shouldn't be, but defensive).
      const label = dj.title || dj.id
      for (const ll of stepsToLogLines(dj.steps)) {
        const ts = ll.timestamp ? Date.parse(ll.timestamp) : NaN
        merged.push({
          ts: Number.isFinite(ts) ? ts : 0,
          line: {
            lineNumber: counter++,
            timestamp: ll.timestamp,
            level: ll.level,
            message: `[${label}] ${ll.message}`,
          },
        })
      }
    }
    merged.sort((a, b) => a.ts - b.ts)
    // Renumber after sort so lineNumber reads top-down in chronological
    // order rather than the per-job order produced above.
    return merged.map((m, i) => ({ ...m.line, lineNumber: i + 1 }))
  }, [derivedJobs])

  const onCanvasJobSelect = useCallback(
    (id: string | null) => {
      // Empty / null — operator clicked the canvas background; restore
      // the host as the selected job so the LogPane never goes
      // contextless. (Background click does NOT dismiss the pane —
      // only the explicit X button or Esc does that.)
      setSelectedJobId(id ?? jobId)
      // Any explicit selection re-opens the pane if the operator had
      // dismissed it earlier.
      if (id) setPaneOpen(true)
    },
    [jobId],
  )

  if (!job) {
    return (
      <PortalShell
        deploymentId={deploymentId}
        sovereignFQDN={sovereignFQDN}
        pageTitle="Job not found"
      >
        <div className="mx-auto max-w-3xl py-8" data-testid="job-detail-not-found">
          <Link
            to="/provision/$deploymentId/jobs"
            params={{ deploymentId }}
            className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
            data-testid="job-detail-back"
          >
            ← Back to jobs
          </Link>
          <div className="mt-6 rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-8 text-center">
            <h1 className="text-lg font-semibold text-[var(--color-text-strong)]">Job not found</h1>
            <p className="mt-2 text-sm text-[var(--color-text-dim)]">
              <code className="font-mono">{jobId}</code> is not part of this deployment.
            </p>
          </div>
        </div>
      </PortalShell>
    )
  }

  const badge = statusBadge(job.status as JobUiStatus)
  const lastUpdate = job.finishedAt ?? job.startedAt
  const titleLabel = job.displayName ?? job.jobName
  const lastUpdateLabel = fmtTime(lastUpdate)

  // LogPane title + status — driven by the SELECTED job (operator
  // clicked something), defaulting to the host job.
  const logPaneJob = selectedJob ?? job
  const logPaneStatus = (detailJobStatus as JobUiStatus | undefined) ?? logPaneJob.status

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle={titleLabel}
    >
      <div
        className={`job-detail-page${paneOpen ? '' : ' is-pane-closed'}`}
        data-testid={`job-detail-${jobId}`}
      >
        <style>{JOB_DETAIL_CSS}</style>

        {/* Issue #669 — collapsed header.
         *
         * The page title (titleLabel) is already rendered by PortalShell
         * via `pageTitle={titleLabel}` above; we no longer duplicate it
         * here. The previous subtitle row (`<jobId> · <appId> ·
         * <parentDisplayName> · last update`) is gone — jobId / appId /
         * parent are derivable from the title and the canvas selection,
         * and the only piece operators routinely scan is the
         * last-update timestamp, which now lives inline next to the
         * back link. Title + status chip remain available; meta clutter
         * does not. */}
        <header className="job-detail-header" data-testid="job-detail-header">
          <Link
            to="/provision/$deploymentId/jobs"
            params={{ deploymentId }}
            className="job-detail-back"
            data-testid="job-detail-back"
          >
            ← Back to jobs
          </Link>
          {lastUpdateLabel ? (
            <span className="job-detail-last-update" data-testid="job-detail-last-update">
              last update {lastUpdateLabel}
            </span>
          ) : null}
          <span
            className={`job-detail-status-chip ${badge.classes}`}
            data-testid="job-detail-status"
          >
            {badge.text}
          </span>
        </header>

        {/* Full-bleed canvas. The embedded FlowPage owns its own
            chrome-less surface; it streams hostJobId so the canvas
            paints the teal home ring. The `paneOpen` flag widens the
            canvas back out to the full viewport when the operator
            dismisses the LogPane. */}
        <div
          className={`job-detail-canvas${paneOpen ? '' : ' is-pane-closed'}`}
          data-testid="job-detail-canvas"
          data-pane-open={paneOpen ? 'true' : 'false'}
        >
          <FlowPage
            disableStream={disableStream}
            disableJobsBackfill={disableJobsBackfill || disableStream}
            embedded
            deploymentIdOverride={deploymentId}
            hostJobId={job.id}
            onOpenJobChange={onCanvasJobSelect}
          />
        </div>

        {/* Issue #669 — the floating "Logs" reopen chip overlapped the
         * status chip in the top-right corner. Removed: operators
         * reopen the pane by clicking any bubble in the canvas (still
         * wired in `onCanvasJobSelect`), or by toggling fullscreen
         * with the existing keyboard shortcut. */}
      </div>

      {/* Floating log pane — open by default on the host's logs.
          The X button + Esc dismiss it; the canvas widens to fill
          the full viewport. Single-click on any bubble re-opens it. */}
      {paneOpen ? (
        <CanvasLogBridge
          executionId={executionId}
          fallbackLines={fallbackLines}
          globalLines={globalLines}
          jobTitle={logPaneJob.displayName ?? logPaneJob.jobName}
          jobStatus={logPaneStatus}
          onClose={() => {
            setPaneOpen(false)
            setSelectedJobId(jobId)
          }}
        />
      ) : null}
    </PortalShell>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * CanvasLogBridge — bridges the floating LogPane into JobDetail.
 *
 * The pane is its own positioned element, mounted at the page level
 * so it overlays the canvas without changing the canvas layout. Kept
 * as a tiny presentational shim so the JobDetail body stays readable.
 * ────────────────────────────────────────────────────────────────── */

interface CanvasLogBridgeProps {
  executionId: string | null | undefined
  /** Bug #481 — derived-job fallback lines (Phase-0, cluster-bootstrap). */
  fallbackLines: readonly LogLine[]
  /** Issue #669 — provision-wide merged log stream. */
  globalLines: readonly LogLine[]
  jobTitle: string
  jobStatus: string
  onClose: () => void
}

function CanvasLogBridge({
  executionId,
  fallbackLines,
  globalLines,
  jobTitle,
  jobStatus,
  onClose,
}: CanvasLogBridgeProps) {
  const tone: 'pending' | 'running' | 'succeeded' | 'failed' =
    jobStatus === 'running' || jobStatus === 'succeeded' || jobStatus === 'failed'
      ? jobStatus
      : 'pending'
  return (
    <LogPane
      executionId={executionId ?? null}
      fallbackLines={fallbackLines}
      globalLines={globalLines}
      jobTitle={jobTitle}
      statusLabel={jobStatus}
      statusTone={tone}
      onClose={onClose}
    />
  )
}

/* ──────────────────────────────────────────────────────────────────
 * stepsToLogLines — DerivedJob.step[] → LogLine[] adapter (Bug #481)
 *
 * The reducer state owns the live SSE event log for derived jobs
 * (Phase-0 tofu, cluster-bootstrap, per-bp-* installs whose Helm
 * watcher hasn't allocated an Execution row yet). Each step maps 1:1
 * to a LogLine — we just need to pick a sensible LogLevel from the
 * step's status + name and number the lines from 1.
 * ────────────────────────────────────────────────────────────────── */

function stepsToLogLines(steps: readonly JobStep[]): LogLine[] {
  return steps.map((s, i): LogLine => {
    let level: LogLevel = 'INFO'
    if (s.status === 'failed') level = 'ERROR'
    // Heuristic: name strings starting with a `-` / `+` / `Initializing`
    // are tofu CLI noise — surface them as DEBUG so a level-filter on
    // INFO-only doesn't drown the operator. The reducer doesn't carry
    // a level field for derived steps, so this is the cleanest place
    // to derive one.
    else if (
      s.name.startsWith('+ ') ||
      s.name.startsWith('- ') ||
      s.name.startsWith('# ')
    ) {
      level = 'DEBUG'
    }
    return {
      lineNumber: i + 1,
      timestamp: s.startedAt ?? '',
      level,
      message: s.name,
    }
  })
}

const JOB_DETAIL_CSS = `
.job-detail-page {
  position: relative;
  display: flex;
  flex-direction: column;
  height: calc(100vh - 56px);
  width: 100%;
  padding: 0.75rem 1rem 0;
  box-sizing: border-box;
  /* Reserve space for the floating LogPane on the right so the canvas
     centroid lands inside the visible area instead of being hidden
     behind the pane. The LogPane width is also bound to this var via
     --log-pane-width, set on the root below. When the operator
     dismisses the pane (paneOpen=false → .is-pane-closed) the canvas
     reclaims the reserved space. */
  padding-right: calc(var(--log-pane-width, 30vw) + 1rem);
  min-width: 0;
  transition: padding-right 220ms cubic-bezier(0.4, 0, 0.2, 1);
}
.job-detail-page.is-pane-closed {
  padding-right: 1rem;
}

/* Issue #669 round 2 — sticky header. The Back link, last-update
 * timestamp and status chip stay frozen at the top of the canvas
 * scroll area while the flow-canvas / LogPane content scrolls.
 * position:sticky + top:0 pin against the .job-detail-page
 * container; a solid surface background prevents the header text
 * from blending into the canvas backdrop while content slides
 * underneath. */
.job-detail-header {
  position: sticky;
  top: 0;
  z-index: 5;
  background: var(--color-bg);
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 0.45rem 0;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  min-width: 0;
}
.job-detail-back {
  font-size: 0.78rem;
  color: var(--color-text-dim);
  text-decoration: none;
  white-space: nowrap;
  flex-shrink: 0;
  transition: color 0.12s ease;
}
.job-detail-back:hover { color: var(--color-text-strong); }
.job-detail-last-update {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.72rem;
  color: var(--color-text-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1 1 auto;
  min-width: 0;
}
.job-detail-status-chip {
  flex-shrink: 0;
  border-radius: 999px;
  padding: 0.18rem 0.7rem;
  font-size: 0.7rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}
.job-detail-canvas {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  margin-top: 0.6rem;
}
.job-detail-canvas > * {
  flex: 1 1 auto;
  min-height: 0;
  height: 100%;
}
@media (max-width: 1100px) {
  .job-detail-page {
    padding-right: 1rem;
  }
}
`
