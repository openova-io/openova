/**
 * jobs.types.ts — shared TypeScript contract for the Jobs surface.
 *
 * Single shape — `Job`. Batches were collapsed into a recursive Job
 * model in issue #351: a group is just a Job whose `type === 'group'`
 * and whose `childIds` enumerates its descendants. The previous
 * `Batch` interface and `/jobs/batches` endpoint are gone — the
 * canvas, the per-job detail page, and the table all read the single
 * recursive tree from
 *
 *   GET /api/v1/deployments/{depId}/jobs   → { jobs: Job[] }
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), this module
 * exports types only — there is no inlined job id, group slug, or
 * status literal anywhere in this file or its consumers.
 */

/**
 * Lifecycle status of a single Job. Aligned 1:1 with the canonical
 * helmwatch state vocabulary in core/console (`pending` → `running` →
 * `succeeded`/`failed`); the canvas + table use the same four buckets
 * so the operator never sees a status they have to translate from a
 * different surface.
 */
export type JobStatus = 'pending' | 'running' | 'succeeded' | 'failed'

/**
 * `'install'` — a leaf Job: one HelmRelease watch attempt, one Day-2
 * mutation, one Phase-0 step. Has Executions and at most one
 * `latestExecutionId` to deep-link to.
 *
 * `'group'` — a synthesised parent: status, startedAt/finishedAt and
 * durationMs are derived from descendants by the backend at read
 * time. Has no Executions of its own. Children are listed in
 * `childIds` and link back via `parentId`.
 */
export type JobType = 'install' | 'group'

/**
 * One node in the recursive Job tree. The catalyst-api emits exactly
 * this shape on
 *
 *   GET /api/v1/deployments/{depId}/jobs       → { jobs: Job[] }
 *   GET /api/v1/deployments/{depId}/jobs/{id}  → { job: Job, executions: [...] }
 *
 * Fields:
 *   • id          — stable, opaque job id ("<deploymentId>:<jobName>"
 *                   for backend rows; reducer-derived rows use the
 *                   legacy id pattern).
 *   • jobName     — slug used in URLs and as the persistence key.
 *                   Leaf installs are "install-<chart>"; groups use
 *                   their slug ("bootstrap-kit", "day-2-mutations").
 *   • displayName — optional user-visible label. Groups carry
 *                   "Bootstrap"/"Day-2 Mutations"; leaves leave this
 *                   empty and the UI falls back to jobName.
 *   • type        — see {@link JobType}.
 *   • appId       — the bp-* Application this job is attributed to.
 *                   Empty for groups and Day-2 mutations. On a
 *                   multi-region Sovereign a secondary region's appId
 *                   carries a "<region>:<chart>" prefix.
 *   • region      — the cloud region the job's HelmRelease was observed
 *                   in ("me-east-215-b-1"). Empty for primary-region
 *                   rows and groups; the backend stamps it from the
 *                   "<region>:" appId prefix. First-class source of
 *                   truth for the Region column/filter — preferred over
 *                   parsing the appId prefix. omitempty on the wire, so
 *                   optional here.
 *   • parentId    — full id of the parent Job, or "" for top-level.
 *                   Replaces the old `batchId` denormalisation.
 *   • dependsOn   — ids of upstream jobs in the same DAG. Surfaced
 *                   as edges on the canvas. Empty array when none.
 *   • childIds    — ids of jobs whose `parentId === this.id`. Always
 *                   populated by the backend; empty for leaf jobs.
 *   • status      — see {@link JobStatus}. For groups: rolled up.
 *   • startedAt   — ISO timestamp of first non-pending transition;
 *                   null while pending. For groups: earliest across
 *                   descendants.
 *   • finishedAt  — ISO timestamp of terminal transition; null while
 *                   running. For groups: latest across descendants
 *                   only when every descendant is terminal.
 *   • durationMs  — total wall-clock duration in ms; 0 while pending.
 */
export interface Job {
  id: string
  jobName: string
  displayName?: string
  type: JobType
  appId: string
  region?: string
  parentId: string
  dependsOn: string[]
  childIds: string[]
  status: JobStatus
  startedAt: string | null
  finishedAt: string | null
  durationMs: number
  /**
   * #3656 (founder #6) — FE-only provisional flag. The Jobs canvas merges
   * reducer-derived rows (folded from the SSE replay buffer) with the live
   * /jobs query, and the live source wins on conflict. Before the live
   * fetch lands, a reducer-derived row's non-terminal status (pending /
   * running) is UNCONFIRMED — a job that actually completed long ago can
   * still read "Pending" until the live source corrects it, then flip. To
   * stop that assert-then-retract the merge marks such a row `provisional`
   * and the StatusBadge renders a distinct "Confirming…" state instead of a
   * definitive Pending/Running. NEVER set on the wire — the catalyst-api
   * Job shape has no such field; it is stamped purely in the FE merge and
   * cleared the moment the live source confirms the row. Optional + absent
   * by default so every backend-sourced row is non-provisional.
   */
  provisional?: boolean
}
