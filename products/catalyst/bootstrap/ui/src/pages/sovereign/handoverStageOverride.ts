/**
 * handoverStageOverride — derive synthetic Apps / Handover / Cutover
 * stage status from the deployment record's handover state.
 *
 * Why this exists:
 *   The catalyst-api openova-flow snapshot (flow_snapshot_local.go,
 *   line 595+) ALWAYS emits the three lifecycle phase groups (cutover,
 *   handover, apps) at depth=1 so the canvas surfaces the full
 *   five-phase lifecycle even when those phases have no leaf jobs yet.
 *   The same emitter also writes per-region sub-groups for every
 *   phase ("<dep>:<region>:apps" etc). When those groups have NO
 *   descendants — the common case on a freshly-converged Sovereign
 *   that has no operator-installed apps yet, and where Sovereign-wide
 *   handover has fired but no per-region handover job rows exist
 *   because handover is a once-per-Sovereign event — the API emits
 *   them with Status="pending" (line 582). The bottom-up rollup at
 *   line 745-761 then leaves them as "pending" because they have no
 *   children to derive from.
 *
 *   Result on the JobsPage: 8 phantom "Pending" rows (Apps, Handover,
 *   plus Apps (<region>) + Handover (<region>) per region) that
 *   contradict the deployment record's `status=ready` +
 *   `handoverFiredAt` truth and break DoD gates D6 + D7.
 *
 *   This module bridges the gap at the UI layer (the catalyst-api
 *   contract is stable; we cannot modify the snapshot emitter without
 *   coordinating with every other flow-snapshot consumer). When the
 *   deployment record carries proof the lifecycle has reached the
 *   handover phase (status="ready" OR handoverFiredAt non-null), we
 *   override these synthetic stages' status from "pending" to
 *   "succeeded" — they have no work to do, vacuously done.
 *
 * Scope of the override (DO NOT widen without re-thinking):
 *   1. Only stages whose id matches `<dep>:apps`, `<dep>:handover`,
 *      `<dep>:cutover`, or the per-region variants
 *      `<dep>:<region>:apps`/`/handover`/`/cutover`. Other stages
 *      (bootstrap-kit, provisioner) carry their own real status from
 *      backend job rows and must NEVER be touched.
 *   2. Only when the deployment record proves handover has fired. A
 *      genuinely-pending Apps stage on a still-installing Sovereign
 *      MUST remain pending.
 *   3. Only override `pending` (and `running`). NEVER overwrite a
 *      terminal `succeeded` / `failed` value — that would mask real
 *      backend signal.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall): no MVP "show as Done badge" — actually mutate the
 *     status field so every downstream consumer (sort, filter,
 *     status-pill, DoD validator) sees the correct truth.
 *   #2 (no compromise): we don't fabricate timestamps. The synthetic
 *     stages keep their startedAt/finishedAt as the backend emitted
 *     them (null for empty groups).
 *   #4 (never hardcode): every slug comes from `LIFECYCLE_STAGE_SLUGS`,
 *     mirroring `products/catalyst/bootstrap/api/internal/jobs/types.go`
 *     `GroupApps`/`GroupHandover`/`GroupCutover`.
 */

import type { Job, JobStatus } from '@/lib/jobs.types'

/**
 * Canonical lifecycle phase slugs eligible for the handover-fired
 * override. These mirror the Go constants in
 * `products/catalyst/bootstrap/api/internal/jobs/types.go`:
 *
 *   GroupApps     = "apps"
 *   GroupHandover = "handover"
 *   GroupCutover  = "cutover"
 *
 * Bootstrap-kit + provisioner are deliberately NOT included — those
 * stages carry real backend status from leaf job rollups and must not
 * be coerced from the deployment record.
 */
export const LIFECYCLE_STAGE_SLUGS = ['apps', 'handover', 'cutover'] as const

export type LifecycleStageSlug = (typeof LIFECYCLE_STAGE_SLUGS)[number]

/**
 * Minimal subset of `DeploymentSnapshot` this module reads. Keeps the
 * coupling between JobsPage + handover state explicit.
 */
export interface HandoverGate {
  /** Deployment status — "ready" means Phase-1 reached OutcomeReady. */
  status?: string
  /** Top-level RFC 3339 timestamp; non-null after fireHandover persists. */
  handoverFiredAt?: string
  /** Top-level handover JWT URL; non-empty after fireHandover persists. */
  handoverURL?: string
  /** Same fields nested on `result` per the wire shape. */
  result?: {
    handoverFiredAt?: string
    handoverURL?: string
  }
}

/**
 * True when the deployment record proves the lifecycle has reached
 * handover. Reads BOTH top-level AND `result.*` so a reply that only
 * populates one shape (the Go server emits both in parallel) still
 * trips the gate.
 *
 * status="ready" alone is sufficient — Phase-1 OutcomeReady is the
 * only path to that status, and OutcomeReady triggers fireHandover
 * synchronously (see phase1_watch.markPhase1Done). The
 * handoverFiredAt check is the belt-and-braces fallback for replays
 * where the record was loaded mid-fire.
 */
export function isHandoverFired(snapshot: HandoverGate | null | undefined): boolean {
  if (!snapshot) return false
  if (snapshot.status === 'ready') return true
  if (snapshot.handoverFiredAt && snapshot.handoverFiredAt !== '') return true
  if (snapshot.result?.handoverFiredAt && snapshot.result.handoverFiredAt !== '') return true
  return false
}

/**
 * Recognise a synthetic lifecycle-stage job id. Returns the matched
 * slug ("apps"/"handover"/"cutover") or null. Matches:
 *
 *   <deploymentId>:apps
 *   <deploymentId>:handover
 *   <deploymentId>:cutover
 *   <deploymentId>:<region>:apps
 *   <deploymentId>:<region>:handover
 *   <deploymentId>:<region>:cutover
 *
 * The deploymentId is opaque (16 hex chars in practice; not validated
 * here — the suffix match is sufficient because no real install-* job
 * ID can end in a bare lifecycle slug).
 *
 * Type-guarding only — does not allocate.
 */
export function matchLifecycleStageSlug(jobId: string): LifecycleStageSlug | null {
  for (const slug of LIFECYCLE_STAGE_SLUGS) {
    // Both top-level (":apps") and per-region (":<region>:apps") end
    // with the same `:apps` suffix; one suffix check covers both.
    if (jobId.endsWith(':' + slug)) {
      return slug
    }
  }
  return null
}

/**
 * Apply the handover-fired override across a job list. Pure function
 * — no mutation of the input array or its elements.
 *
 *   • Identifies lifecycle-stage jobs via `matchLifecycleStageSlug`.
 *   • For each match, if the snapshot proves handover fired AND the
 *     current status is non-terminal (`pending` or `running`), emits a
 *     new Job object with `status: 'succeeded'`.
 *   • Terminal statuses (`succeeded` / `failed`) are passed through
 *     untouched — backend signal wins over UI inference.
 *   • Non-matching jobs are passed through by identity (reference
 *     stability for memoised consumers).
 *
 * Returns the same array reference when nothing was overridden, so
 * useMemo callers see a stable result on no-op invocations.
 */
export function applyHandoverStageOverride(
  jobs: readonly Job[],
  snapshot: HandoverGate | null | undefined,
): Job[] {
  if (!isHandoverFired(snapshot)) {
    return jobs as Job[]
  }
  let mutated = false
  const out: Job[] = new Array(jobs.length)
  for (let i = 0; i < jobs.length; i++) {
    const j = jobs[i]!
    const slug = matchLifecycleStageSlug(j.id)
    if (slug !== null && isOverridableStatus(j.status)) {
      // #3656 — coercing to a terminal status SETTLES the row: clear any
      // provisional flag so the badge shows the confirmed "Succeeded",
      // never a terminal status still wearing the "Confirming…" label.
      out[i] = { ...j, status: 'succeeded', provisional: false }
      mutated = true
    } else {
      out[i] = j
    }
  }
  return mutated ? out : (jobs as Job[])
}

/** Only override non-terminal status values. */
function isOverridableStatus(s: JobStatus): boolean {
  return s === 'pending' || s === 'running'
}
