/**
 * JobDetail — per-Job detail surface served at
 * `/sovereign/provision/$deploymentId/jobs/$jobId`.
 *
 * v3 (this PR) — the founder consolidated the tab set:
 *   • Tab 1 (default): "Flow" — embedded FlowPage canvas scoped to the
 *     parent batch with this job pre-highlighted (thicker border + glow).
 *   • Tab 2: "Exec Log" — the existing GitLab-CI-runner-style log
 *     viewer (epic #204 item 3).
 *
 * Dropped from v2 (PR #208 + #242 era):
 *   • Dependencies tab — replaced by the Flow tab (the canvas IS the
 *     dependency view, scoped + highlighted).
 *   • Apps tab — collapsed into the header chip + the Flow canvas's
 *     per-bubble appId display.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — full target shape ships in this PR. The previous
 *      3-tab shape is gone, not feature-flagged.
 *   #2 (no compromise) — Mantine-style tablist (proper Tabs.List /
 *      Tabs.Tab / Tabs.Panel pattern), NOT accordions (founder spec).
 *   #4 (never hardcode) — every label / id / route key is derived.
 */

import { useMemo, useState } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs, fmtTime, statusBadge } from './jobs'
import type { JobUiStatus } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'
import type { Job } from '@/lib/jobs.types'
import { ExecutionLogs } from '@/components/ExecutionLogs'
import { FlowPage } from './FlowPage'

type TabKey = 'flow' | 'logs'

const TABS: { key: TabKey; label: string; testid: string }[] = [
  { key: 'flow', label: 'Flow',     testid: 'job-detail-tab-flow' },
  { key: 'logs', label: 'Exec Log', testid: 'job-detail-tab-logs' },
]

interface JobDetailProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
  /** Test seam — initial tab override. */
  initialTab?: TabKey
}

export function JobDetail({
  disableStream = false,
  disableJobsBackfill = false,
  initialTab = 'flow',
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

  // Mirror JobsPage / FlowPage data-source:
  //   deriveJobs (reducer)  →  adaptDerivedJobsToFlat  →  reducerJobs
  //   useLiveJobsBackfill   →  liveJobs (backend Jobs API)
  //   mergeJobs(reducer, live) — backend wins as soon as it returns ≥1 row.
  //
  // Why this matters: FlowPage navigates with the BACKEND-format id
  // (`<deploymentId>:install-cilium`) the moment the backend has data.
  // The reducer-only deriveJobs() output uses catalog ids
  // (`bp-cilium`, `cluster-bootstrap`, `infrastructure:tofu-init`) that
  // never match — so without the merge, every double-click on a Flow
  // bubble lands on the not-found state. See useLiveJobsBackfill.ts:142
  // for the divergence comment.
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

  const [tab, setTab] = useState<TabKey>(initialTab)

  // Derive a synthetic execution id for the log viewer. Until the
  // backend surfaces `executions[]` on the Job model, the most-recent
  // execution is identified as `<jobId>:latest` so the URL the viewer
  // hits is stable and the backend can route by job id when it lands.
  const executionId = `${jobId}:latest`

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

  // statusBadge accepts the legacy JobUiStatus union; JobStatus shares
  // the same four string literals so the cast is a no-op at runtime.
  const badge = statusBadge(job.status as JobUiStatus)
  const lastUpdate = job.finishedAt ?? job.startedAt

  // Resolve the parent batch id for the embedded FlowPage. The flat Job
  // shape always carries `batchId` (assigned by the backend Jobs API or
  // by adaptDerivedJobsToFlat for reducer-only fallback rows).
  const batchId = job.batchId

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <div className="mx-auto max-w-5xl" data-testid={`job-detail-${jobId}`}>
        <Link
          to="/provision/$deploymentId/jobs"
          params={{ deploymentId }}
          className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="job-detail-back"
        >
          ← Back to jobs
        </Link>

        {/* Header */}
        <header
          className="mt-4 flex items-start justify-between gap-4 border-b border-[var(--color-border)] pb-4"
          data-testid="job-detail-header"
        >
          <div className="min-w-0 flex-1">
            <h1
              className="truncate text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="job-detail-title"
            >
              {job.jobName}
            </h1>
            <p className="mt-1 truncate font-mono text-xs text-[var(--color-text-dim)]">
              {job.id}
              {job.appId && job.appId !== 'infrastructure' && job.appId !== 'cluster-bootstrap'
                ? ` · ${job.appId}`
                : ''}
            </p>
            <p className="mt-2 text-xs text-[var(--color-text-dim)]">
              <span data-testid="job-detail-batch">{job.batchId}</span>
              {fmtTime(lastUpdate) ? ` · last update ${fmtTime(lastUpdate)}` : ''}
            </p>
          </div>
          <span
            className={`shrink-0 rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-wide ${badge.classes}`}
            data-testid="job-detail-status"
          >
            {badge.text}
          </span>
        </header>

        {/* Tablist — proper Tabs (Mantine-style), NOT accordions. The
            tab strip exposes EXACTLY two tabs (Flow + Exec Log) per
            the v3 founder spec; Dependencies and Apps were retired. */}
        <div
          role="tablist"
          aria-label="Job detail sections"
          className="mt-6 flex gap-1 border-b border-[var(--color-border)]"
          data-testid="job-detail-tablist"
        >
          {TABS.map((t) => (
            <button
              key={t.key}
              type="button"
              role="tab"
              aria-selected={tab === t.key}
              aria-controls={`job-detail-panel-${t.key}`}
              id={t.testid}
              onClick={() => setTab(t.key)}
              data-testid={t.testid}
              className={`relative -mb-px px-4 py-2 text-sm font-medium transition-colors ${
                tab === t.key
                  ? 'border-b-2 border-[var(--color-accent)] text-[var(--color-text-strong)]'
                  : 'border-b-2 border-transparent text-[var(--color-text-dim)] hover:text-[var(--color-text)]'
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>

        {/* Panels */}
        <div className="py-6" data-testid={`job-detail-panel-${tab}`}>
          {tab === 'flow' && (
            <div
              role="tabpanel"
              id="job-detail-panel-flow"
              aria-labelledby="job-detail-tab-flow"
              data-testid="job-detail-flow-panel"
            >
              <FlowPage
                disableStream={disableStream}
                disableJobsBackfill={disableJobsBackfill || disableStream}
                embedded
                deploymentIdOverride={deploymentId}
                scopeOverride={{ kind: 'batch', batchId }}
                highlightJobId={job.id}
              />
            </div>
          )}
          {tab === 'logs' && (
            <div
              role="tabpanel"
              id="job-detail-panel-logs"
              aria-labelledby="job-detail-tab-logs"
              data-testid="job-detail-logs-panel"
            >
              <ExecutionLogs executionId={executionId} />
            </div>
          )}
        </div>
      </div>
    </PortalShell>
  )
}
