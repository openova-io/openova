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
import type { JobUiStatus } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'
import { useJobDetail } from './useJobDetail'
import type { Job } from '@/lib/jobs.types'
import { LogPane } from '@/components/LogPane'
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

  const onCanvasJobSelect = useCallback(
    (id: string | null) => {
      // Empty / null — operator clicked the canvas background; restore
      // the host as the selected job so the LogPane never goes
      // contextless.
      setSelectedJobId(id ?? jobId)
    },
    [jobId],
  )

  if (!job) {
    return (
      <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
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
  const parent = job.parentId ? jobsById[job.parentId] ?? null : null
  const parentLabel = parent ? parent.displayName ?? parent.jobName : null
  const titleLabel = job.displayName ?? job.jobName

  // Subtitle pieces — composed into a single line so the chrome stays
  // at 2 lines (header title + meta). Empty pieces drop out.
  const subtitlePieces: string[] = [job.id]
  if (job.appId && job.appId !== 'infrastructure' && job.appId !== 'cluster-bootstrap' && job.type !== 'group') {
    subtitlePieces.push(job.appId)
  }
  if (parentLabel) subtitlePieces.push(parentLabel)
  const lastUpdateLabel = fmtTime(lastUpdate)
  if (lastUpdateLabel) subtitlePieces.push(`last update ${lastUpdateLabel}`)

  // LogPane title + status — driven by the SELECTED job (operator
  // clicked something), defaulting to the host job.
  const logPaneJob = selectedJob ?? job
  const logPaneStatus = (detailJobStatus as JobUiStatus | undefined) ?? logPaneJob.status

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <div className="job-detail-page" data-testid={`job-detail-${jobId}`}>
        <style>{JOB_DETAIL_CSS}</style>

        {/* Two-line compact header with status chip top-right. */}
        <header className="job-detail-header" data-testid="job-detail-header">
          <div className="job-detail-header-titles">
            <div className="job-detail-header-line1">
              <Link
                to="/provision/$deploymentId/jobs"
                params={{ deploymentId }}
                className="job-detail-back"
                data-testid="job-detail-back"
              >
                ← Back to jobs
              </Link>
              <h1 className="job-detail-title" data-testid="job-detail-title" title={titleLabel}>
                {titleLabel}
              </h1>
            </div>
            <div className="job-detail-header-line2" data-testid="job-detail-meta">
              {subtitlePieces.map((p, idx) => (
                <span key={idx} className="job-detail-meta-piece">
                  {idx > 0 ? <span className="job-detail-meta-sep" aria-hidden> · </span> : null}
                  {p}
                </span>
              ))}
            </div>
          </div>
          <span
            className={`job-detail-status-chip ${badge.classes}`}
            data-testid="job-detail-status"
          >
            {badge.text}
          </span>
        </header>

        {/* Full-bleed canvas. The embedded FlowPage owns its own
            chrome-less surface; it streams hostJobId so the canvas
            paints the teal home ring. */}
        <div className="job-detail-canvas" data-testid="job-detail-canvas">
          <FlowPage
            disableStream={disableStream}
            disableJobsBackfill={disableJobsBackfill || disableStream}
            embedded
            deploymentIdOverride={deploymentId}
            hostJobId={job.id}
            onOpenJobChange={onCanvasJobSelect}
          />
        </div>
      </div>

      {/* Floating log pane — open by default on the host's logs.
          Hidden when the operator dismisses it; clicking another
          canvas job re-arms via onJobSelect inside FlowPage. */}
      <CanvasLogBridge
        executionId={executionId}
        jobTitle={logPaneJob.displayName ?? logPaneJob.jobName}
        jobStatus={logPaneStatus}
        onClose={() => setSelectedJobId(jobId)}
      />
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
  jobTitle: string
  jobStatus: string
  onClose: () => void
}

function CanvasLogBridge({ executionId, jobTitle, jobStatus, onClose }: CanvasLogBridgeProps) {
  const tone: 'pending' | 'running' | 'succeeded' | 'failed' =
    jobStatus === 'running' || jobStatus === 'succeeded' || jobStatus === 'failed'
      ? jobStatus
      : 'pending'
  return (
    <LogPane
      executionId={executionId ?? null}
      jobTitle={jobTitle}
      statusLabel={jobStatus}
      statusTone={tone}
      onClose={onClose}
    />
  )
}

const JOB_DETAIL_CSS = `
.job-detail-page {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 56px);
  width: 100%;
  padding: 0.75rem 1rem 0;
  box-sizing: border-box;
  /* Reserve space for the floating LogPane on the right so the canvas
     centroid lands inside the visible area instead of being hidden
     behind the pane. The LogPane width is also bound to this var via
     --log-pane-width, set on the root below. */
  padding-right: calc(var(--log-pane-width, 30vw) + 1rem);
  min-width: 0;
}

.job-detail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  padding-bottom: 0.45rem;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.job-detail-header-titles {
  display: flex;
  flex-direction: column;
  min-width: 0;
  flex: 1 1 auto;
  gap: 0.05rem;
}
.job-detail-header-line1 {
  display: flex;
  align-items: baseline;
  gap: 0.7rem;
  min-width: 0;
}
.job-detail-back {
  font-size: 0.72rem;
  color: var(--color-text-dim);
  text-decoration: none;
  white-space: nowrap;
  flex-shrink: 0;
  transition: color 0.12s ease;
}
.job-detail-back:hover { color: var(--color-text-strong); }
.job-detail-title {
  margin: 0;
  font-size: 1.4rem;
  font-weight: 700;
  color: var(--color-text-strong);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1 1 auto;
  min-width: 0;
}
.job-detail-header-line2 {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.72rem;
  color: var(--color-text-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.job-detail-meta-sep {
  margin: 0 0.4rem;
  opacity: 0.6;
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
