/**
 * jobsAdapter.ts — bridge between the legacy reducer-derived Job model
 * (./jobs.ts — used by per-component status pills, deep-link banners,
 * and the canonical core/console event vocabulary) and the new flat
 * Job[] shape the JobsTable consumes (issue #204).
 *
 * Why a separate file?
 *   • ./jobs.ts owns RICH state (steps, app classification, noAppLink)
 *     that AdminPage / AppDetail still need for status pills and the
 *     dependencies viz.
 *   • lib/jobs.types.ts owns the FLAT row shape the founder asked for
 *     in the table (item #204): jobName, batchId, dependsOn, status,
 *     startedAt, finishedAt, durationMs.
 *   • Mixing the two would couple the table render to the reducer
 *     internals; instead, this adapter is the ONLY place where the
 *     mapping lives. When the catalyst-api jobs endpoint (#205) ships,
 *     the JobsPage swaps `adaptDerivedJobsToFlat()` for the API call
 *     and the JobsTable surface stays unchanged.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the batch
 * assignment is derived from the legacy job's `app` classification:
 *   • app === "infrastructure"        → batchId = "phase-0-infra"
 *   • app === "cluster-bootstrap"     → batchId = "cluster-bootstrap"
 *   • everything else (per-component) → batchId = "applications"
 *
 * Three batches reflect the three rollups the operator already sees on
 * the AdminPage banners; the table groups identically so there's only
 * one mental model.
 */

import type { Job as DerivedJob } from './jobs'
import type { Job, JobStatus } from '@/lib/jobs.types'

/** Map the legacy JobUiStatus vocabulary to JobStatus 1:1. */
function mapStatus(s: DerivedJob['status']): JobStatus {
  // Legacy and new vocabularies are aligned today; the indirection
  // exists so a future drift doesn't ripple to consumers.
  switch (s) {
    case 'running':   return 'running'
    case 'succeeded': return 'succeeded'
    case 'failed':    return 'failed'
    case 'pending':
    default:          return 'pending'
  }
}

/** Pick the batch label from the legacy `app` classification. */
function batchOf(app: DerivedJob['app']): string {
  if (app === 'infrastructure') return 'phase-0-infra'
  if (app === 'cluster-bootstrap') return 'cluster-bootstrap'
  return 'applications'
}

/**
 * Compute the wall-clock duration for a job from its step list.
 *
 *   • succeeded / failed: time between the first event and the last.
 *     If the reducer captured both, this is the source-of-truth value.
 *   • running: now - first event time.
 *   • pending: 0 (the table renders "—" for zero durations).
 *
 * Conservative on missing data — returns 0 when no usable timestamps
 * are present.
 */
function durationOf(job: DerivedJob): number {
  if (job.steps.length === 0) return 0
  const times: number[] = []
  for (const s of job.steps) {
    if (!s.startedAt) continue
    const t = new Date(s.startedAt).getTime()
    if (Number.isFinite(t) && t > 0) times.push(t)
  }
  if (times.length === 0) return 0
  const start = Math.min(...times)
  if (job.status === 'succeeded' || job.status === 'failed') {
    const end = Math.max(...times)
    return Math.max(0, end - start)
  }
  // running: elapsed-so-far. The table refreshes when the reducer
  // updates, so this is "good enough" without an interval timer.
  return Math.max(0, Date.now() - start)
}

/** First step's timestamp — used as the table's startedAt column. */
function startedAtOf(job: DerivedJob): string | null {
  for (const s of job.steps) {
    if (s.startedAt) return s.startedAt
  }
  return null
}

/** Last step's timestamp on terminal jobs — used as finishedAt. */
function finishedAtOf(job: DerivedJob): string | null {
  if (job.status !== 'succeeded' && job.status !== 'failed') return null
  let last: string | null = null
  for (const s of job.steps) {
    if (s.startedAt) last = s.startedAt
  }
  return last
}

/**
 * Adapt a legacy DerivedJob[] (from ./jobs.ts deriveJobs) into the new
 * flat Job[] the JobsTable consumes.
 *
 * Stable: re-running on the same input produces identical output. No
 * randomness, no cached state. The dependency graph is folded forward
 * by chaining each per-component job to the cluster-bootstrap job
 * (and that to the last tofu job) so the table's `deps` column is
 * non-empty for jobs the operator can act on.
 */
export function adaptDerivedJobsToFlat(derived: readonly DerivedJob[]): Job[] {
  // Pre-compute id chain so `dependsOn` in the flat shape mirrors the
  // implicit Phase 0 → cluster-bootstrap → application order. Without
  // this the deps column reads as a wall of em-dashes.
  const tofuOrder = derived.filter((j) => j.app === 'infrastructure').map((j) => j.id)
  const lastTofuId = tofuOrder.length > 0 ? tofuOrder[tofuOrder.length - 1] : null
  const bootstrap = derived.find((j) => j.app === 'cluster-bootstrap')

  return derived.map((j, idx) => {
    let dependsOn: string[] = []
    if (j.app === 'infrastructure') {
      // tofu-init has no deps; later phases depend on the previous tofu.
      const prev = tofuOrder.indexOf(j.id) - 1
      if (prev >= 0) dependsOn = [tofuOrder[prev]!]
    } else if (j.app === 'cluster-bootstrap') {
      if (lastTofuId) dependsOn = [lastTofuId]
    } else {
      // per-component → depends on cluster-bootstrap.
      if (bootstrap) dependsOn = [bootstrap.id]
    }
    void idx
    return {
      id: j.id,
      jobName: j.title,
      appId: j.app,
      batchId: batchOf(j.app),
      dependsOn,
      status: mapStatus(j.status),
      startedAt: startedAtOf(j),
      finishedAt: finishedAtOf(j),
      durationMs: durationOf(j),
    }
  })
}
