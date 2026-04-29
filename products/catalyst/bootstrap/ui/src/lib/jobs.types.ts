/**
 * jobs.types.ts — shared TypeScript contract for the Jobs table view
 * surface (issue #204).
 *
 * Two distinct shapes for two surfaces:
 *
 *   • {@link Job} — one row of the JobsTable. Captures the founder
 *     verbatim spec for issue #204 (item 6/7): jobName, appId,
 *     batchId, dependsOn, status, startedAt, finishedAt, durationMs.
 *
 *   • {@link Batch} — one progress row above the JobsTable. Captures
 *     the rollup the BatchProgress component renders for item #4
 *     (group jobs by batchId → batch with progress bar).
 *
 * The HTTP shape these align to lives in
 *   GET /api/v1/deployments/{depId}/jobs           → { jobs: Job[] }
 *   GET /api/v1/deployments/{depId}/jobs/batches   → { batches: Batch[] }
 *
 * The sibling backend agent on issue #205 owns the catalyst-api handler
 * for these endpoints. Until that lands, the UI populates from a fixture
 * (src/test/fixtures/jobs.fixture.ts) so the table view can ship and be
 * pixel-validated independently of backend availability.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), this module
 * exports types only — there is NO inlined job id, batch id, or status
 * literal anywhere in this file or its consumers.
 */

/**
 * Lifecycle status of a single Job. Aligned 1:1 with the canonical
 * helmwatch state vocabulary in core/console (`pending` → `running` →
 * `succeeded`/`failed`); the JobsTable uses the same four buckets so
 * the operator never sees a status they have to translate from a
 * different surface.
 */
export type JobStatus = 'pending' | 'running' | 'succeeded' | 'failed'

/**
 * One row of the JobsTable. Matches the contract in the issue #204
 * scope brief verbatim — no extra fields, no missing fields. Backend
 * (#205) emits exactly this shape on
 *   GET /api/v1/deployments/{depId}/jobs
 *
 * Fields:
 *   • id          — stable, opaque job id (used as React key + URL param).
 *   • jobName     — display label rendered in the "name" column.
 *   • appId       — the bp-* Application this job is attributed to. Used
 *                   by AppDetail's Jobs tab to filter the table.
 *   • batchId     — the batch this job belongs to. Used by BatchProgress
 *                   to group jobs into rollup rows.
 *   • dependsOn   — ids of upstream jobs in the same DAG. Surfaced as a
 *                   chip list in the "deps" column. Empty array when the
 *                   job has no upstream.
 *   • status      — see {@link JobStatus}.
 *   • startedAt   — ISO timestamp the job transitioned from `pending` to
 *                   `running`. Null while pending. Drives the "started"
 *                   column + the default sort comparator.
 *   • finishedAt  — ISO timestamp the job reached `succeeded`/`failed`.
 *                   Null while pending or running.
 *   • durationMs  — total wall-clock duration in ms. Computed by backend
 *                   so the UI never has to derive it from start/finish
 *                   (avoids clock-skew between UI tab and pod). For
 *                   running jobs this is the elapsed-so-far value.
 */
export interface Job {
  id: string
  jobName: string
  appId: string
  batchId: string
  dependsOn: string[]
  status: JobStatus
  startedAt: string | null
  finishedAt: string | null
  durationMs: number
}

/**
 * One row of the BatchProgress strip above the JobsTable. The five
 * counters are mutually exclusive bucket counts that sum to `total`
 * (a job lives in exactly one bucket at a time).
 *
 *   • finished = succeeded + failed (the pre-computed rollup the
 *     progress bar renders against `total`)
 *   • succeeded / failed / running / pending — the four discrete
 *     bucket counts the operator sees on the chip-row beside the bar.
 */
export interface Batch {
  batchId: string
  total: number
  finished: number
  failed: number
  running: number
  pending: number
}
