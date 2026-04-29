/**
 * JobDependencies — Dependencies tab for the Job Detail page.
 *
 * Founder requirement (epic #204):
 *   • Item 5 — JobDetail tabs are Execution Logs / Dependencies / Apps.
 *   • Item 11 — "Job dependencies are very important [...] we may
 *     consider having gantt or like view, you may suggest". Sub-ticket
 *     #206 owns the recommendation + implementation; the proposal at
 *     `docs/proposals/jobs-dependencies-viz.md` selects an SVG DAG
 *     primary surface (this tab) + a fullscreen Gantt timeline at
 *     `/sovereign/provision/$id/jobs/timeline` for retrospective.
 *
 * Surface contract:
 *   1. SVG DAG (`<JobDependenciesGraph />`) showing this job + its
 *      upstream chain, color-coded by status, click-to-navigate.
 *   2. Below the graph, a compact list of immediate upstream deps with
 *      status + per-job link — preserved for keyboard accessibility +
 *      screen readers (the SVG nodes are also focusable, but the list
 *      stays the canonical "jump to dep" affordance).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every node /
 * edge / colour comes from the supplied jobs lookup or the depsLayout
 * function — no inlined dependency maps.
 */

import { Link, useNavigate } from '@tanstack/react-router'
import type { Job, JobUiStatus } from '@/pages/sovereign/jobs'
import {
  JobDependenciesGraph,
  type JobNode,
} from '@/widgets/job-deps-graph/JobDependenciesGraph'

interface JobDependenciesProps {
  /** The job whose dependencies are being rendered. */
  job: { id: string; title?: string; status?: JobUiStatus; dependsOn: string[] }
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
  const navigate = useNavigate()

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

  // Build the graph slice: this job + every transitively-upstream job
  // we can resolve from `jobsById`. Each node carries its own status
  // and dependsOn so the layout can render multi-level chains, not
  // just the one-hop immediate-deps view.
  const graphJobs = buildGraphSlice(job, jobsById)

  return (
    <div data-testid="job-deps-section" className="flex flex-col gap-4">
      <JobDependenciesGraph
        jobs={graphJobs}
        height={380}
        onNodeClick={(jobId) => {
          if (jobId === job.id) return // Already on this job's page.
          navigate({
            to: '/provision/$deploymentId/jobs/$jobId',
            params: { deploymentId, jobId },
          })
        }}
      />
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
    </div>
  )
}

/**
 * Walk upstream from `job` collecting every reachable upstream job and
 * the job itself. Stops at jobs not present in `jobsById` (the dep
 * graph is intentionally narrow until the backend lands).
 */
function buildGraphSlice(
  job: { id: string; title?: string; status?: JobUiStatus; dependsOn: string[] },
  jobsById: Record<string, Job>,
): JobNode[] {
  const out: JobNode[] = []
  const seen = new Set<string>()

  // Self.
  out.push({
    id: job.id,
    title: job.title ?? jobsById[job.id]?.title ?? job.id,
    status: job.status ?? jobsById[job.id]?.status ?? 'pending',
    dependsOn: job.dependsOn,
  })
  seen.add(job.id)

  // BFS upstream.
  const queue = [...job.dependsOn]
  while (queue.length > 0) {
    const id = queue.shift()!
    if (seen.has(id)) continue
    seen.add(id)
    const upstream = jobsById[id]
    if (!upstream) {
      // Render as an empty pending node so the edge still draws.
      out.push({ id, title: id, status: 'pending', dependsOn: [] })
      continue
    }
    out.push({
      id: upstream.id,
      title: upstream.title,
      status: upstream.status,
      // The current Job model in jobs.ts doesn't carry `dependsOn` yet
      // (sibling backend ticket #205). Render upstream nodes with an
      // empty dependsOn until that lands; the chain still draws because
      // the *current* job's dependsOn is the only edge source we need.
      dependsOn: [],
    })
  }

  return out
}
