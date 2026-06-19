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
/**
 * Lifecycle status of a Job. The first four are the one-shot axis
 * (pending → running → succeeded/failed); the last three are the HEALTH
 * axis (issue #3646 §4c) carried by recurring/reconciler kinds so
 * "running forever and that's correct" (`healthy`) is distinct from
 * "running forever and that's a hang" (`degraded`/`failing`).
 */
export type JobStatus =
  | 'pending'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'healthy'
  | 'degraded'
  | 'failing'

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
 * The typed activity discriminator (issue #3646 §4b) the backend stamps
 * on every Job — replaces the JobName-prefix string-matching the FE used
 * to do (`startsWith('install-')`, the `-step-` infix, the cutover
 * URL-strip). A consumer reads `job.kind` to know what a row IS:
 *
 *   install     — a HelmRelease install leaf.
 *   reconcile   — a Flux Kustomization reconcile leaf.
 *   step        — one step of a projected activity (cutover / DR switchover).
 *   mutation    — a Day-2 Crossplane XRC submission.
 *   cron        — a recurring CronJob (each spawned run is an Execution).
 *   task        — a standalone / owned batch Job.
 *   reconciler  — a long-running reconciler Deployment (HEALTH status).
 *   lifecycle   — a Phase-0 / lifecycle leaf.
 *   group       — a synthesised parent group.
 *
 * Optional on the wire (`omitempty`) — the backend always stamps it, but
 * a legacy row read before the field existed back-fills on read, so the
 * FE treats an absent kind as falling back to the type.
 */
export type JobKind =
  | 'install'
  | 'reconcile'
  | 'step'
  | 'mutation'
  | 'cron'
  | 'task'
  | 'reconciler'
  | 'lifecycle'
  | 'group'

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
  /**
   * The typed activity discriminator (issue #3646 §4b). Drives the Kind
   * column + the region/type derivation + the Retry affordance. Optional
   * because a legacy wire row may omit it; consumers fall back to deriving
   * from `type`/`jobName` via {@link jobKind}.
   */
  kind?: JobKind
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
   * Run-history depth (#3925) — the number of Executions (runs) behind
   * this leaf row. A recurring job that collapses to ONE identity-keyed
   * row (e.g. a trivy/syft scan) reports its full run count here ("600
   * runs"); a one-shot job reports 1. Backend-derived at read time
   * (Store.ListJobs / GetJob); omitted on the wire (and thus undefined
   * here) for a pending job with no run yet and for group rows. The Jobs
   * view renders this in the Runs column so the collapse is legible — one
   * row, N runs behind it.
   */
  runCount?: number
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

/**
 * jobKind — the canonical "read the kind, not the prefix" accessor
 * (issue #3646 §4b). Returns the backend-stamped `job.kind` when present;
 * otherwise derives it from `type`/`jobName` for a legacy row that
 * predates the field. This is the ONLY place the FE infers a kind — every
 * consumer (the Kind column, the region derivation, the Retry gate) calls
 * this instead of string-matching a JobName prefix.
 */
export function jobKind(job: Pick<Job, 'kind' | 'type' | 'jobName'>): JobKind {
  if (job.kind) return job.kind
  if (job.type === 'group') return 'group'
  const n = job.jobName
  if (n.includes('-step-')) return 'step'
  if (n.startsWith('mutation-')) return 'mutation'
  if (n.startsWith('install-')) return 'install'
  if (n.startsWith('reconcile-')) return 'reconcile'
  if (n.startsWith('cron-')) return 'cron'
  if (n.startsWith('task-')) return 'task'
  if (n.startsWith('reconciler-')) return 'reconciler'
  return 'lifecycle'
}

/**
 * isHealthAxisStatus — true when a status is on the HEALTH axis
 * (healthy/degraded/failing), as opposed to the one-shot lifecycle axis.
 * Recurring/reconciler kinds report this so the badge can render a health
 * tone distinct from `succeeded`.
 */
export function isHealthAxisStatus(s: JobStatus): boolean {
  return s === 'healthy' || s === 'degraded' || s === 'failing'
}

/**
 * isJobRetryable — a row offers the Retry affordance when it is a one-shot
 * `failed` or a HEALTH-axis `degraded`/`failing`. A `succeeded`/`running`/
 * `healthy` row has nothing to re-drive, so no control is shown (the
 * backend rejects a retry on those with 409 too).
 */
export function isJobRetryable(status: JobStatus): boolean {
  return status === 'failed' || status === 'degraded' || status === 'failing'
}
