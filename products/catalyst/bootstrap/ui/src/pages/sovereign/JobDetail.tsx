/**
 * JobDetail — per-Job detail surface served at
 * `/sovereign/provision/$deploymentId/jobs/$jobId`.
 *
 * Founder requirements (epic #204):
 *   • Item 2: jobs are granular; clicking a row opens this detail page.
 *   • Item 3: Execution log viewer styled like GitLab CI runner — the
 *     `<ExecutionLogs />` component owns that surface.
 *   • Item 5: tabs are Execution Logs / Dependencies / Apps. NOT
 *     accordions, NOT a flat scroll. (Item 1 explicitly forbids
 *     accordions everywhere in the wizard.)
 *
 * Tab parity with the AppDetail surface is intentional: the visual
 * vocabulary (header chip, status pill, tablist) mirrors the rest of
 * the Sovereign-provision portal so an operator sees the same chrome
 * regardless of whether they're inspecting a job or an application.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label,
 * dep id, and app id comes from the derived job set + application
 * catalog. The mock-fallback path (used while the backend lands) reads
 * from `deriveJobs()` so a JobDetail URL never 404s when the catalyst-
 * api hasn't surfaced its own /jobs endpoint yet.
 */

import { useMemo, useState } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs, fmtTime, statusBadge } from './jobs'
import type { Job } from './jobs'
import { ExecutionLogs } from '@/components/ExecutionLogs'
import { JobDependencies } from '@/components/JobDependencies'
import { JobApps } from '@/components/JobApps'

type TabKey = 'logs' | 'dependencies' | 'apps'

const TABS: { key: TabKey; label: string }[] = [
  { key: 'logs',         label: 'Execution Logs' },
  { key: 'dependencies', label: 'Dependencies' },
  { key: 'apps',         label: 'Apps' },
]

interface JobDetailProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — initial tab override. */
  initialTab?: TabKey
}

export function JobDetail({ disableStream = false, initialTab = 'logs' }: JobDetailProps = {}) {
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

  const { state, snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })

  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // Derive the full job set + index by id. The current data model attaches
  // exactly one app to each Job; the JobApps component accepts an array so
  // the surface is forward-compatible.
  const jobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const jobsById = useMemo<Record<string, Job>>(() => {
    const out: Record<string, Job> = {}
    for (const j of jobs) out[j.id] = j
    return out
  }, [jobs])
  const job = jobsById[jobId]

  const appsById = useMemo(() => {
    const out: Record<string, typeof applications[number]> = {}
    for (const a of applications) out[a.id] = a
    return out
  }, [applications])

  // Mock dependency lookup — until the backend lands, infer dependencies
  // from job ordering: tofu phases depend on the previous tofu phase,
  // cluster-bootstrap depends on the last tofu phase, per-component jobs
  // depend on cluster-bootstrap. The list is stable so the UI is
  // deterministic.
  const dependsOn = useMemo<string[]>(() => {
    if (!job) return []
    const i = jobs.findIndex((j) => j.id === jobId)
    if (i <= 0) return []
    return [jobs[i - 1]!.id]
  }, [job, jobId, jobs])

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

  const badge = statusBadge(job.status)
  const completedN = job.steps.filter((s) => s.status === 'succeeded').length
  const total = job.steps.length

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
        <header className="mt-4 flex items-start justify-between gap-4 border-b border-[var(--color-border)] pb-4" data-testid="job-detail-header">
          <div className="min-w-0 flex-1">
            <h1
              className="truncate text-2xl font-bold text-[var(--color-text-strong)]"
              data-testid="job-detail-title"
            >
              {job.title}
            </h1>
            <p className="mt-1 truncate font-mono text-xs text-[var(--color-text-dim)]">
              {job.id}
              {job.app && job.app !== 'infrastructure' && job.app !== 'cluster-bootstrap'
                ? ` · ${job.app}`
                : ''}
            </p>
            <p className="mt-2 text-xs text-[var(--color-text-dim)]">
              {completedN}/{total} steps
              {fmtTime(job.updatedAt) ? ` · last update ${fmtTime(job.updatedAt)}` : ''}
            </p>
          </div>
          <span
            className={`shrink-0 rounded-full px-3 py-1 text-xs font-semibold uppercase tracking-wide ${badge.classes}`}
            data-testid="job-detail-status"
          >
            {badge.text}
          </span>
        </header>

        {/* Tablist — proper Tabs, NOT accordions (item 1 forbids accordions). */}
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
              id={`job-detail-tab-${t.key}`}
              onClick={() => setTab(t.key)}
              data-testid={`job-detail-tab-${t.key}`}
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
          {tab === 'dependencies' && (
            <div
              role="tabpanel"
              id="job-detail-panel-dependencies"
              aria-labelledby="job-detail-tab-dependencies"
              data-testid="job-detail-deps-panel"
            >
              <JobDependencies
                job={{ id: job.id, dependsOn }}
                jobsById={jobsById}
                deploymentId={deploymentId}
              />
            </div>
          )}
          {tab === 'apps' && (
            <div
              role="tabpanel"
              id="job-detail-panel-apps"
              aria-labelledby="job-detail-tab-apps"
              data-testid="job-detail-apps-panel"
            >
              <JobApps
                appIds={[job.app]}
                appsById={appsById}
                deploymentId={deploymentId}
              />
            </div>
          )}
        </div>
      </div>
    </PortalShell>
  )
}
