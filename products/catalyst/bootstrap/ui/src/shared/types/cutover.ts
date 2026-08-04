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
 * Wire-level step identifier — matches the `stepName` data key + the
 * `bp.openova.io/cutover-order` label on each step ConfigMap installed
 * by the bp-self-sovereign-cutover chart, and the `name` field on the
 * per-step record the catalyst-api `/sovereign/cutover/status` endpoint
 * emits (`cutoverStepStatus.Name` in api/internal/handler/cutover.go).
 *
 * #3379 (hw159, 2026-06-18): the canonical chain GREW from 8 → 11. The
 * api handler header documents this drift verbatim ("the step count has
 * drifted (8→11) as gitea-token-mint / vcluster-registry-pivot /
 * crossplane-provider-pivot were appended"). The chart templates are the
 * ground truth — verified against
 * platform/self-sovereign-cutover/chart/templates/*.yaml:
 *   01 gitea-mirror · 02 harbor-projects · 03 harbor-prewarm ·
 *   04 registry-pivot · 05 flux-gitrepository-patch ·
 *   06 helmrepository-patches · 07 catalyst-api-env-patch ·
 *   08 egress-block-test · 09 gitea-token-mint ·
 *   10 vcluster-registry-pivot · 11 crossplane-provider-pivot
 * NOTE the slug is `helmrepository-patches` (NOT the prior `helmrepo-patches`)
 * — the old FE id never matched the chart, so that row could never light up.
 */
export type CutoverStepID =
  | 'gitea-mirror'
  | 'harbor-projects'
  | 'harbor-prewarm'
  | 'registry-pivot'
  | 'flux-gitrepository-patch'
  | 'helmrepository-patches'
  | 'catalyst-api-env-patch'
  | 'egress-block-test'
  | 'gitea-token-mint'
  | 'vcluster-registry-pivot'
  | 'crossplane-provider-pivot'

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
 * The canonical, ordered 11-step list. ANY code path that needs to
 * iterate the chain (UI rendering, reducer initialisation, e2e) MUST
 * read from this list — adding or removing a step ripples here once.
 *
 * Order + ids mirror the chart's `bp.openova.io/cutover-order` labels
 * (platform/self-sovereign-cutover/chart/templates/NN-*.yaml). The
 * catalyst-api engine discovers + executes them in this exact sequence.
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
      'to harbor.<sovereign-fqdn> via a DaemonSet sentinel; each node ' +
      'acks the v2 (local Harbor) rewrite.',
  },
  {
    id: 'flux-gitrepository-patch',
    label: 'Repoint Flux GitRepository',
    description:
      'Patch the flux-system/openova GitRepository.url from GitHub to ' +
      'the local Gitea HTTP service. Flux reconciles from local Git.',
  },
  {
    id: 'helmrepository-patches',
    label: 'Repoint HelmRepositories',
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
  {
    id: 'gitea-token-mint',
    label: 'Mint local Gitea token',
    description:
      'Mint a long-lived Gitea API token so the host GitOps loop + the ' +
      'mirror-resync CronJob authenticate against local Gitea after cutover.',
  },
  {
    id: 'vcluster-registry-pivot',
    label: 'Pivot vCluster registries',
    description:
      'Repoint every Organization vCluster’s containerd + pull secrets at the ' +
      'local Harbor so Organization workloads stop pulling from ghcr.io.',
  },
  {
    id: 'crossplane-provider-pivot',
    label: 'Pivot Crossplane providers',
    description:
      'Rewrite Crossplane provider + function package refs from upstream ' +
      'registries to the local Harbor proxy projects.',
  },
] as const

export const CUTOVER_STEP_COUNT = CUTOVER_STEPS.length

const CUTOVER_STEP_IDS: ReadonlySet<string> = new Set(
  CUTOVER_STEPS.map((s) => s.id),
)

export function isCutoverStepID(s: unknown): s is CutoverStepID {
  return typeof s === 'string' && CUTOVER_STEP_IDS.has(s)
}

/**
 * Map the catalyst-api's native per-step `result` string onto the UI's
 * `CutoverStepStatus`. This is the seam that was MISSING — the live
 * `/sovereign/cutover/status` endpoint emits `steps[].result`
 * (`cutoverStepStatus.Result` in api/internal/handler/cutover.go), NOT
 * `steps[].status`, and its vocabulary is the ENGINE's, not the UI's:
 *
 *   • "success" → done      (the engine writes step.<n>.result=success)
 *   • "skipped" → done      (already-succeeded step on a resume)
 *   • "running" → running   (the projected-Job in-flight value)
 *   • "failed"  → failed
 *   • ""/absent → pending   (no per-step row written yet)
 *
 * Without this mapping every step record off the wire was dropped by the
 * `status !== 'done' …` filter, so the progress card rendered 8 (now 11)
 * permanently-pending rows even on a fully-completed cutover — the
 * GAP-missing-ui the hw159 walk recorded.
 *
 * Returns `null` for an unrecognised value so the caller can drop the
 * record (forward-compat with a future engine result string).
 */
export function cutoverResultToStatus(result: unknown): CutoverStepStatus | null {
  switch (result) {
    case 'success':
    case 'skipped':
      return 'done'
    case 'running':
      return 'running'
    case 'failed':
      return 'failed'
    case '':
    case undefined:
    case null:
      return 'pending'
    default:
      return null
  }
}

/**
 * Resolve a per-step status from a wire record that may carry EITHER the
 * UI vocabulary (`status: 'done'`) OR the engine vocabulary
 * (`result: 'success'`). The live catalyst-api uses `result`; the older
 * fixtures + the SSE-derived records use `status`. Prefer an explicit,
 * valid `status`; otherwise fall back to mapping `result`. Returns null
 * if neither yields a recognised status (record is then dropped).
 */
function resolveStepStatus(rec: Record<string, unknown>): CutoverStepStatus | null {
  const s = rec.status
  if (s === 'pending' || s === 'running' || s === 'done' || s === 'failed') {
    return s
  }
  // Only fall back to result-mapping when status is absent — an explicit
  // but unknown status string is a malformed record and must be dropped
  // (preserves the existing "drops malformed status values" contract).
  if (s === undefined || s === null) {
    return cutoverResultToStatus(rec.result)
  }
  return null
}

/**
 * Resolve the per-step wire id from a record that may carry EITHER `step`
 * (UI/SSE vocabulary) OR `name` (the live `/status` endpoint's
 * `cutoverStepStatus.Name`). Returns the branded id or null.
 */
function resolveStepID(rec: Record<string, unknown>): CutoverStepID | null {
  if (isCutoverStepID(rec.step)) return rec.step
  if (isCutoverStepID(rec.name)) return rec.name
  return null
}

/* ────────────────────────────────────────────────────────────────
 * 3. Wire shapes from /sovereign/cutover/{status,events}
 * ────────────────────────────────────────────────────────────── */

/**
 * Per-step record returned in `CutoverStatus.steps[]`. The catalyst-api
 * GET /status response carries each step as `{name, result, …}` (the
 * engine vocabulary) and the SSE stream signals transitions via the
 * `cutover-step-started/finished/failed` event NAMES; both are adapted
 * onto this normalized record. Timestamps are RFC 3339 UTC strings.
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
  /**
   * #5391 — the named settled-roll override audit record. Step-03 Phase A0
   * writes newline-joined `<ns>/<name>=<reason>` entries into the status
   * ConfigMap when the settled-roll gate passed WITH one or more validated
   * operator overrides (the catalyst.openova.io/cutover-settled-roll-override
   * annotation on a genuinely-stuck HelmRelease). Undefined/empty when no
   * override was used — the progress card MUST make a used override visible
   * so an acceptance walk can SEE the cutover ran with an exclusion.
   */
  settledRollOverrides?: string
  /** Error message at the first-failed step. Empty in the success path. */
  error?: string
}

/**
 * Validate + brand a `CutoverStatus` payload off the wire. Defensive
 * because the SSE channel is a hostile boundary; a malformed frame
 * MUST NOT crash the page or render an undefined badge.
 *
 * Strategy:
 *   • `state` MUST resolve to a valid `CutoverState`. The wire SHOULD
 *     emit `tethered` or `sovereign`; if the field is missing/undefined
 *     (older catalyst-api Pod, sparse status ConfigMap mid-rollout)
 *     we DERIVE it from `cutoverComplete` rather than throwing.
 *     Throwing was the otech113 regression — `parseCutoverState`
 *     surfaced `invalid CutoverState: <undefined>` as user-visible
 *     text on the dashboard. Default = `tethered` (safe / accurate
 *     for newly-handed-over Sovereigns where no cutover has run yet).
 *   • A wire frame with an EXPLICITLY-WRONG state value (a typo, a
 *     hostile string) still rejects via `parseCutoverState` — we
 *     only soften the missing-field case.
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
  // Resolve state and cutoverComplete jointly. Either field can be the
  // primary source; if neither is present we default to `tethered`
  // (a freshly-handed-over Sovereign that hasn't run cutover yet).
  // The API contract guarantees an explicit state field on every
  // post-#933 catalyst-api build, but a stale Pod or partial-rollout
  // wire snapshot must not crash the UI.
  let state: CutoverState
  if (raw.state === undefined || raw.state === null) {
    // Wire field missing — derive from cutoverComplete. Default tethered.
    const fromComplete =
      typeof raw.cutoverComplete === 'boolean' && raw.cutoverComplete
    state = (fromComplete ? 'sovereign' : 'tethered') as CutoverState
  } else {
    // Wire field present — branded parse (rejects typos / hostile input).
    state = parseCutoverState(raw.state)
  }
  // cutoverComplete prefers the explicit boolean wire value when present,
  // otherwise mirrors the resolved state.
  const cutoverComplete =
    typeof raw.cutoverComplete === 'boolean'
      ? raw.cutoverComplete
      : state === 'sovereign'
  // The top-level failed-step + error live on the status ConfigMap as
  // `failedStep` / `lastError` (NOT on the per-step record) — the engine
  // writes them in patchCutoverStatus when a step fails. Fold lastError
  // onto the matching step's `message` so the progress card's red error
  // row lights up off the live wire shape (it reads rec.message).
  const failedStep =
    typeof raw.failedStep === 'string' && raw.failedStep !== ''
      ? raw.failedStep
      : undefined
  const lastError =
    typeof raw.lastError === 'string' && raw.lastError !== ''
      ? raw.lastError
      : undefined

  const stepsRaw = Array.isArray(raw.steps) ? raw.steps : []
  const steps: CutoverStepStatusRecord[] = []
  for (const r of stepsRaw) {
    if (r === null || typeof r !== 'object') continue
    const rec = r as Record<string, unknown>
    const id = resolveStepID(rec)
    if (id === null) continue
    const status = resolveStepStatus(rec)
    if (status === null) continue
    // Per-step message wins; otherwise inherit the top-level lastError
    // when THIS is the failed step the engine flagged.
    let message =
      typeof rec.message === 'string' && rec.message !== ''
        ? rec.message
        : undefined
    if (message === undefined && status === 'failed' && id === failedStep) {
      message = lastError
    }
    steps.push({
      step: id,
      status,
      startedAt: typeof rec.startedAt === 'string' ? rec.startedAt : undefined,
      finishedAt:
        typeof rec.finishedAt === 'string' ? rec.finishedAt : undefined,
      message,
    })
  }
  // Timestamps: prefer the legacy/typed `startedAt`/`finishedAt`, fall
  // back to the live catalyst-api field names (`cutoverStartedAt` /
  // `cutoverFinishedAt` — see cutoverStatusResponse in cutover.go).
  const startedAt = firstString(raw.startedAt, raw.cutoverStartedAt)
  const finishedAt = firstString(raw.finishedAt, raw.cutoverFinishedAt)

  // egressTestPassed: the live backend does NOT emit a typed boolean; the
  // proof is the `egress-block-test` step reaching result=success. Prefer
  // an explicit boolean (forward-compat) then derive from that step.
  let egressTestPassed: boolean | undefined
  if (typeof raw.egressTestPassed === 'boolean') {
    egressTestPassed = raw.egressTestPassed
  } else {
    const egressStep = steps.find((s) => s.step === 'egress-block-test')
    if (egressStep) {
      if (egressStep.status === 'done') egressTestPassed = true
      else if (egressStep.status === 'failed') egressTestPassed = false
    }
  }

  return {
    state,
    cutoverComplete,
    startedAt,
    finishedAt,
    steps,
    // Summary fields are optional on the live wire (they live under the
    // status ConfigMap's free-form keys, surfaced via `raw`). Read the
    // typed top-level value first, then fall back to the raw key.
    mirroredCommitSHA: firstString(raw.mirroredCommitSHA, rawKey(raw, 'mirroredCommitSHA')),
    harborProjectCount: firstNumber(raw.harborProjectCount, rawKey(raw, 'harborProjectCount')),
    egressTestPassed,
    settledRollOverrides: firstString(
      raw.settledRollOverrides,
      rawKey(raw, 'settledRollOverrides'),
    ),
    error: firstString(raw.error, raw.lastError),
  }
}

/** First argument that is a non-empty string, else undefined. */
function firstString(...vals: unknown[]): string | undefined {
  for (const v of vals) {
    if (typeof v === 'string' && v !== '') return v
  }
  return undefined
}

/**
 * First argument coercible to a number, else undefined. Accepts a real
 * number or a numeric string (the status ConfigMap stores everything as
 * strings, so `raw.harborProjectCount` arrives as e.g. "7").
 */
function firstNumber(...vals: unknown[]): number | undefined {
  for (const v of vals) {
    if (typeof v === 'number' && Number.isFinite(v)) return v
    if (typeof v === 'string' && v.trim() !== '') {
      const n = Number(v)
      if (Number.isFinite(n)) return n
    }
  }
  return undefined
}

/** Read a key off the optional `raw` string-map the backend nests under `raw`. */
function rawKey(obj: Record<string, unknown>, key: string): unknown {
  const r = obj.raw
  if (r !== null && typeof r === 'object') {
    return (r as Record<string, unknown>)[key]
  }
  return undefined
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
 * the SSE reducer when it receives `cutover-step-started/finished/failed`
 * frames.
 *
 * Idempotent: an update for a step that already has the same status +
 * timestamps returns the input unchanged so React's state-equality
 * check skips the re-render.
 *
 * Append-on-miss: a wire snapshot from POST /start may carry only the
 * currently-running step (e.g. just `gitea-mirror`); subsequent SSE
 * step frames for steps that aren't present yet must be APPENDED, not
 * silently dropped. Otherwise the progress card never reflects later
 * steps because the reducer's map() never sees them.
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

/**
 * Parse the FLAT status-ConfigMap map (`Record<string, string>`) that the
 * catalyst-api SSE `cutover-status` snapshot frame carries.
 *
 * The SSE channel's terminal/replay snapshot is NOT a `CutoverStatus`
 * object — `HandleCutoverEvents` JSON-encodes the raw status ConfigMap
 * (`readCutoverStatus` → `map[string]string`) into the event `message`.
 * That map has NO `steps` array; the per-step rows are flat keys:
 *   step.<stepName>.result      "success" | "failed" | "running" | ""
 *   step.<stepName>.startedAt    RFC3339
 *   step.<stepName>.finishedAt   RFC3339
 *   step.<stepName>.jobName
 * plus top-level cutoverComplete / failedStep / lastError / etc.
 *
 * We reconstruct a `steps` array (exactly as the backend's
 * buildCutoverStatusResponseFromMap does for the GET /status response) and
 * delegate to `parseCutoverStatus`, so the SSE snapshot and the GET
 * snapshot converge on one validated shape.
 *
 * Returns null if `input` is not a flat string-map (caller then ignores
 * the frame rather than crashing the stream).
 */
export function parseCutoverStatusConfigMap(input: unknown): CutoverStatus | null {
  if (input === null || typeof input !== 'object' || Array.isArray(input)) {
    return null
  }
  const m = input as Record<string, unknown>
  // Collect step names from the `step.<name>.<field>` keys.
  const stepNames = new Set<string>()
  for (const k of Object.keys(m)) {
    if (!k.startsWith('step.')) continue
    const rest = k.slice('step.'.length)
    const dot = rest.lastIndexOf('.')
    if (dot <= 0) continue
    stepNames.add(rest.slice(0, dot))
  }
  const steps = [...stepNames].map((name) => ({
    name,
    result: m[`step.${name}.result`],
    startedAt: m[`step.${name}.startedAt`],
    finishedAt: m[`step.${name}.finishedAt`],
    jobName: m[`step.${name}.jobName`],
  }))
  // Hand the reconstructed object to the single validated parser. The
  // flat map's `state` key may be absent — parseCutoverStatus derives it
  // from cutoverComplete, which IS present on the status ConfigMap.
  return parseCutoverStatus({
    ...m,
    cutoverComplete: m.cutoverComplete === 'true' || m.cutoverComplete === true,
    steps,
  })
}
