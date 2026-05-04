/**
 * cutover.ts — branded `CutoverState` type + canonical 8-step list +
 * runtime parsers for the responses the catalyst-api `/sovereign/cutover`
 * endpoints emit (see openova-io/openova#792).
 *
 * Why branded types
 * ─────────────────
 * The cutover surface in the admin console — "Achieve True Sovereignty"
 * button + 8-step progress card — switches HARD on `cutoverComplete:
 * true | false`. A free-form `string` for the overall state lets a future
 * feature (or a renamed wire field) silently flip the UI to the wrong
 * branch with no compile-time error. Mirrors the `DeploymentID` brand in
 * `shared/types/deployment.ts` — runtime parser is the only way to
 * materialise a `CutoverState`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (never compromise): the parser rejects unknown values; an
 *      out-of-band string from a hostile or buggy backend cannot trick
 *      the UI into rendering "sovereign" when reality says "tethered".
 *   #4 (never hardcode): the canonical step list lives ONCE here. The
 *      progress card iterates it; the SSE reducer keys off it; the
 *      Playwright E2E reads it. One file = one truth.
 */

/* ────────────────────────────────────────────────────────────────
 * 1. Cutover overall state — branded enum-like string
 * ────────────────────────────────────────────────────────────── */

/**
 * The overall sovereignty state of a Sovereign cluster.
 *
 *   • `tethered`  — soft-tethered to mothership Harbor + GitHub for
 *                   container pulls + GitOps source. The default state
 *                   for a freshly-handed-over Sovereign.
 *   • `sovereign` — hard-independent: gitea-mirror complete, Harbor
 *                   proxy projects warmed, registries.yaml + Flux
 *                   GitRepository + 38 HelmRepositories all repointed
 *                   to local. Egress-block self-test passed.
 *
 * No third value is legal on the wire. The catalyst-api emits exactly
 * one of these two; the UI MUST refuse to render any other value rather
 * than silently treat unknown as `tethered`.
 */
export type CutoverState = ('tethered' | 'sovereign') & {
  readonly __brand: 'CutoverState'
}

const CUTOVER_STATES: ReadonlySet<string> = new Set(['tethered', 'sovereign'])

/**
 * Parse + validate a value as a `CutoverState`. Throws on any input
 * that isn't exactly `tethered` or `sovereign`.
 *
 *   • `cutoverComplete: false` on the wire → server emits `tethered`
 *   • `cutoverComplete: true`  on the wire → server emits `sovereign`
 *   • anything else (including `null`, the empty string, `'pending'`,
 *     'in-progress', or a typo) → throws.
 *
 * Mirrors `parseDeploymentID` shape: the error message truncates the
 * preview so a runaway buffer doesn't leak into a logged exception.
 */
export function parseCutoverState(s: unknown): CutoverState {
  if (typeof s !== 'string' || !CUTOVER_STATES.has(s)) {
    const preview =
      typeof s === 'string' ? `"${s.slice(0, 32)}"` : `<${typeof s}>`
    throw new Error(`invalid CutoverState: ${preview}`)
  }
  return s as CutoverState
}

/**
 * Type-guard variant — true iff the value is a valid `CutoverState`.
 * Use at boundaries where you want to BRANCH on whether the input is
 * a recognised state, not throw.
 */
export function isCutoverState(s: unknown): s is CutoverState {
  return typeof s === 'string' && CUTOVER_STATES.has(s)
}

/* ────────────────────────────────────────────────────────────────
 * 2. The canonical 8-step list
 * ────────────────────────────────────────────────────────────── */

/**
 * One cutover step. The `id` is the wire identifier the catalyst-api
 * emits in `CutoverStatus.steps[]`; the `label` and `description` are
 * the human strings the progress card renders.
 *
 * Step order matters — both for display AND for the catalyst-api Job
 * sequencing (gitea-mirror must finish before harbor-projects, etc).
 * The list at `CUTOVER_STEPS` below is the single source of truth that
 * the api handler, the chart Jobs, and this UI all agree on.
 */
export interface CutoverStepDef {
  readonly id: CutoverStepID
  readonly label: string
  readonly description: string
}

/**
 * Wire-level step identifier — matches the JobTemplate ConfigMap key
 * names installed by the bp-self-sovereign-cutover chart and the
 * `step` field on the `CutoverStepStatus` payload from
 * GET /sovereign/cutover/status.
 */
export type CutoverStepID =
  | 'gitea-mirror'
  | 'harbor-projects'
  | 'harbor-prewarm'
  | 'registry-pivot'
  | 'flux-gitrepository-patch'
  | 'helmrepo-patches'
  | 'catalyst-api-env-patch'
  | 'egress-block-test'

/**
 * Per-step status the SSE stream + `/status` endpoint emit.
 *
 *   • `pending`  — not started
 *   • `running`  — Job created, watching for completion
 *   • `done`     — Job succeeded
 *   • `failed`   — Job terminated with non-zero exit; cutover halts
 *
 * Mirrors the `PhaseStatus` shape used by the bootstrap progress widget
 * for visual consistency, minus the `skipped` value (cutover steps are
 * never skipped — they're a hard chain).
 */
export type CutoverStepStatus = 'pending' | 'running' | 'done' | 'failed'

/**
 * The canonical, ordered 8-step list. ANY code path that needs to
 * iterate the chain (UI rendering, reducer initialisation, e2e) MUST
 * read from this list — adding or removing a step ripples here once.
 */
export const CUTOVER_STEPS: readonly CutoverStepDef[] = [
  {
    id: 'gitea-mirror',
    label: 'Mirror GitOps repository',
    description:
      'Clone github.com/openova-io/openova into the local Gitea so Flux ' +
      'can read manifests without leaving the cluster.',
  },
  {
    id: 'harbor-projects',
    label: 'Create Harbor proxy projects',
    description:
      'Provision proxy-ghcr / proxy-docker / proxy-k8s / proxy-gcr / ' +
      'proxy-quay / proxy-xpkg / proxy-ecr in the local Harbor.',
  },
  {
    id: 'harbor-prewarm',
    label: 'Pre-warm critical images',
    description:
      'Pull every image referenced by an installed HelmRelease through ' +
      'Harbor so the cluster never depends on the mothership again.',
  },
  {
    id: 'registry-pivot',
    label: 'Pivot containerd registries',
    description:
      'Hot-reload registries.yaml on every node from harbor.openova.io ' +
      'to harbor.<sovereign-fqdn> via a DaemonSet sentinel.',
  },
  {
    id: 'flux-gitrepository-patch',
    label: 'Repoint Flux GitRepository',
    description:
      'Patch the flux-system/openova GitRepository.url from GitHub to ' +
      'the local Gitea HTTP service. Flux reconciles from local Git.',
  },
  {
    id: 'helmrepo-patches',
    label: 'Repoint 38 HelmRepositories',
    description:
      'Patch every oci://ghcr.io/openova-io HelmRepository to ' +
      'oci://harbor.<sovereign-fqdn>/openova-io.',
  },
  {
    id: 'catalyst-api-env-patch',
    label: 'Update catalyst-api environment',
    description:
      'Set CATALYST_GITOPS_REPO_URL to the local Gitea URL and remove ' +
      'the upstream-fallback default.',
  },
  {
    id: 'egress-block-test',
    label: 'Egress-block self-test',
    description:
      'Apply a NetworkPolicy denying egress to github.com + ghcr.io + ' +
      'harbor.openova.io for 10 minutes; assert all reconciles stay green.',
  },
] as const

export const CUTOVER_STEP_COUNT = CUTOVER_STEPS.length

const CUTOVER_STEP_IDS: ReadonlySet<string> = new Set(
  CUTOVER_STEPS.map((s) => s.id),
)

export function isCutoverStepID(s: unknown): s is CutoverStepID {
  return typeof s === 'string' && CUTOVER_STEP_IDS.has(s)
}

/* ────────────────────────────────────────────────────────────────
 * 3. Wire shapes from /sovereign/cutover/{status,events}
 * ────────────────────────────────────────────────────────────── */

/**
 * Per-step record returned in `CutoverStatus.steps[]` and emitted on
 * the SSE stream as `event: cutover-step` (or, for the terminal frame,
 * folded into the `cutover-status` payload). Timestamps are RFC 3339
 * UTC strings.
 */
export interface CutoverStepStatusRecord {
  /** The wire step id; the parser drops records with unknown ids. */
  step: CutoverStepID
  status: CutoverStepStatus
  /** Set when the catalyst-api enqueues the underlying Job. */
  startedAt?: string
  /** Set when the underlying Job reaches a terminal state. */
  finishedAt?: string
  /** Populated only when `status === 'failed'`; surfaces in the row. */
  message?: string
}

/**
 * The full cutover status the GET endpoint and the terminal SSE frame
 * carry. Field names mirror the catalyst-api struct so the UI does not
 * need a translation layer.
 */
export interface CutoverStatus {
  /** The branded overall state — drives the badge + button visibility. */
  state: CutoverState
  /** Convenience boolean — `true` iff `state === 'sovereign'`. */
  cutoverComplete: boolean
  /** When the cutover Jobs started (RFC 3339 UTC). Empty until first POST /start. */
  startedAt?: string
  /** When the egress-block test finished green (RFC 3339 UTC). */
  finishedAt?: string
  /** Per-step records — sparse: only steps we have a record for appear. */
  steps: readonly CutoverStepStatusRecord[]
  /** SHA mirrored into local Gitea — populated after gitea-mirror done. */
  mirroredCommitSHA?: string
  /** Number of Harbor proxy projects created — ≥ 7 at completion. */
  harborProjectCount?: number
  /** True iff the egress-block test ran for 10 min with all reconciles green. */
  egressTestPassed?: boolean
  /** Error message at the first-failed step. Empty in the success path. */
  error?: string
}

/**
 * Validate + brand a `CutoverStatus` payload off the wire. Defensive
 * because the SSE channel is a hostile boundary; a malformed frame
 * MUST NOT crash the page or render an undefined badge.
 *
 * Strategy:
 *   • `state` MUST round-trip through `parseCutoverState`. A wire frame
 *     with an unknown overall state is rejected outright.
 *   • Step records are filtered: any record whose `step` id is unknown
 *     is dropped (forward-compat — a future api version that adds a
 *     9th step won't break a stale UI).
 *   • Optional fields default to `undefined` rather than fabricated
 *     values; the renderer is responsible for picking copy when a
 *     field is missing.
 */
export function parseCutoverStatus(input: unknown): CutoverStatus {
  if (input === null || typeof input !== 'object') {
    throw new Error('invalid CutoverStatus: not an object')
  }
  const raw = input as Record<string, unknown>
  const state = parseCutoverState(raw.state)
  const cutoverComplete =
    typeof raw.cutoverComplete === 'boolean'
      ? raw.cutoverComplete
      : state === 'sovereign'
  const stepsRaw = Array.isArray(raw.steps) ? raw.steps : []
  const steps: CutoverStepStatusRecord[] = []
  for (const r of stepsRaw) {
    if (r === null || typeof r !== 'object') continue
    const rec = r as Record<string, unknown>
    if (!isCutoverStepID(rec.step)) continue
    const status = rec.status
    if (
      status !== 'pending' &&
      status !== 'running' &&
      status !== 'done' &&
      status !== 'failed'
    )
      continue
    steps.push({
      step: rec.step,
      status,
      startedAt: typeof rec.startedAt === 'string' ? rec.startedAt : undefined,
      finishedAt:
        typeof rec.finishedAt === 'string' ? rec.finishedAt : undefined,
      message: typeof rec.message === 'string' ? rec.message : undefined,
    })
  }
  return {
    state,
    cutoverComplete,
    startedAt: typeof raw.startedAt === 'string' ? raw.startedAt : undefined,
    finishedAt: typeof raw.finishedAt === 'string' ? raw.finishedAt : undefined,
    steps,
    mirroredCommitSHA:
      typeof raw.mirroredCommitSHA === 'string'
        ? raw.mirroredCommitSHA
        : undefined,
    harborProjectCount:
      typeof raw.harborProjectCount === 'number'
        ? raw.harborProjectCount
        : undefined,
    egressTestPassed:
      typeof raw.egressTestPassed === 'boolean'
        ? raw.egressTestPassed
        : undefined,
    error: typeof raw.error === 'string' ? raw.error : undefined,
  }
}

/**
 * Build a fresh "tethered" status object the UI can seed itself with
 * before the first `/status` GET resolves. Every step starts as
 * `pending` so the progress card has 8 placeholder rows on first
 * paint instead of an empty list.
 */
export function buildInitialCutoverStatus(): CutoverStatus {
  return {
    state: 'tethered' as CutoverState,
    cutoverComplete: false,
    steps: CUTOVER_STEPS.map<CutoverStepStatusRecord>((s) => ({
      step: s.id,
      status: 'pending',
    })),
  }
}

/**
 * Merge a per-step update onto an existing status snapshot. Used by
 * the SSE reducer when it receives `event: cutover-step` frames.
 *
 * Idempotent: an update for a step that already has the same status +
 * timestamps returns the input unchanged so React's state-equality
 * check skips the re-render.
 *
 * Append-on-miss: a wire snapshot from POST /start may carry only the
 * currently-running step (e.g. just `gitea-mirror`); subsequent SSE
 * `cutover-step` frames for steps that aren't present yet must be
 * APPENDED, not silently dropped. Otherwise the progress card never
 * reflects steps 2..8 because the reducer's map() never sees them.
 */
export function applyCutoverStepUpdate(
  prev: CutoverStatus,
  update: CutoverStepStatusRecord,
): CutoverStatus {
  if (!isCutoverStepID(update.step)) return prev
  let changed = false
  let matched = false
  const nextSteps = prev.steps.map((s) => {
    if (s.step !== update.step) return s
    matched = true
    if (
      s.status === update.status &&
      s.startedAt === update.startedAt &&
      s.finishedAt === update.finishedAt &&
      s.message === update.message
    ) {
      return s
    }
    changed = true
    return { ...s, ...update }
  })
  if (!matched) {
    return { ...prev, steps: [...prev.steps, update] }
  }
  if (!changed) return prev
  return { ...prev, steps: nextSteps }
}
