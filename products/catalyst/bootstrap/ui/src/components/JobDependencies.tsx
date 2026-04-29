/**
 * JobDependencies — list view of jobs this job depends on.
 *
 * Founder requirement (epic #204 item 5): the Job Detail page tabs
 * are Execution Logs / Dependencies / Apps. Item 11 calls out a Gantt
 * or DAG visualisation for the dependency graph; that's owned by a
 * parallel agent. This component is the simple list-with-status surface
 * used until that agent's work lands — it does NOT attempt to render
 * the full DAG.
 *
 * Each dep row shows:
 *   • the upstream job's id + title
 *   • its current status (uses the same vocabulary as `JobUiStatus`)
 *   • a compact link to the upstream job's own detail page
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the dep list is
 * resolved from the Job's own `dependsOn` array against the supplied job
 * lookup — there's no inlined dependency map.
 */

import { Link } from '@tanstack/react-router'
import type { Job, JobUiStatus } from '@/pages/sovereign/jobs'

interface JobDependenciesProps {
  /** The job whose dependencies are being rendered. */
  job: { id: string; dependsOn: string[] }
  /** Lookup of all jobs in the deployment (for status + title). */
  jobsById: Record<string, Job>
  /** Stable deployment id — needed for the per-job detail link. */
  deploymentId: string
}

/** Label + colour mapping for a job UI status. Aligned with
 *  `statusBadge()` in jobs.ts so the visual vocabulary is consistent
 *  between the JobsPage list, the JobDetail header, and this dep list. */
const STATUS_PALETTE: Record<
  JobUiStatus,
  { label: string; bg: string; fg: string }
> = {
  succeeded: { label: 'Succeeded', bg: 'rgba(34, 197, 94, 0.15)',  fg: '#22c55e' },
  running:   { label: 'Running',   bg: 'rgba(59, 130, 246, 0.15)', fg: '#3b82f6' },
  failed:    { label: 'Failed',    bg: 'rgba(239, 68, 68, 0.15)',  fg: '#ef4444' },
  pending:   { label: 'Pending',   bg: 'rgba(245, 158, 11, 0.15)', fg: '#f59e0b' },
}

export function JobDependencies({
  job,
  jobsById,
  deploymentId,
}: JobDependenciesProps) {
  const deps = job.dependsOn ?? []

  if (deps.length === 0) {
    return (
      <div
        data-testid="job-deps-empty"
        className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4 text-sm text-[var(--color-text-dim)]"
      >
        This job has no upstream dependencies — it can start as soon as the
        deployment reaches its phase.
      </div>
    )
  }

  return (
    <div data-testid="job-deps-list" className="flex flex-col gap-2">
      <p className="text-xs text-[var(--color-text-dim)]">
        {deps.length} upstream {deps.length === 1 ? 'dependency' : 'dependencies'} —
        this job waits for all of them to finish before starting.
      </p>
      <ul className="flex flex-col gap-2">
        {deps.map((depId) => {
          const dep = jobsById[depId]
          const status: JobUiStatus = dep?.status ?? 'pending'
          const palette = STATUS_PALETTE[status]
          const title = dep?.title ?? depId
          return (
            <li
              key={depId}
              data-testid={`job-dep-${depId}`}
              data-status={status}
              className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3"
            >
              <div className="flex items-center gap-3">
                <Link
                  to="/provision/$deploymentId/jobs/$jobId"
                  params={{ deploymentId, jobId: depId }}
                  className="flex-1 truncate text-sm font-semibold text-[var(--color-text-strong)] hover:text-[var(--color-accent)] no-underline"
                >
                  {title}
                </Link>
                <span
                  className="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide"
                  style={{ background: palette.bg, color: palette.fg }}
                >
                  {palette.label}
                </span>
              </div>
              <p className="mt-1 truncate font-mono text-[11px] text-[var(--color-text-dim)]">
                {depId}
              </p>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
