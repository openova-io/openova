/**
 * dashboardJobsTreemap.ts — #4731 pure derivation for the Dashboard's
 * job-sourced treemap layers (`progress` / `kind`).
 *
 * A layer stack containing progress/kind builds its TreemapItem[] tree
 * client-side from the FULL platform inventory
 * (GET /api/v1/deployments/{id}/jobs?inventory=full — every HelmRelease
 * install, Flux Kustomization, CronJob, reconciler Deployment, cutover
 * step, lifecycle phase) instead of the pods/utilisation backend.
 *
 * ── #4731 amendment (founder escalation 2026-07-05) ─────────────────
 * The founder's four complaints on the CONVERGED-Sovereign treemap and
 * how this module answers each:
 *
 *   1. "where are all the other items" — the map showed ~14 finite rows
 *      because /jobs filters out the continuous install/reconcile/
 *      reconciler leaves. The Dashboard now reads ?inventory=full so
 *      EVERY component is a leaf (~90+ on an hw224-shaped env).
 *   2. "second layer is not kind by default" — Dashboard.tsx no longer
 *      flips the default stack away from [progress, kind] on ready.
 *   3. "cutover kinds still showing as pending" — the `progress` layer
 *      now buckets by PROGRESS STATE (done/running/pending/failed/
 *      dormant), and the dormant cutover steps collapse into ONE
 *      `cutover (dormant)` leaf coloured dormant-grey, never pending.
 *   4. relevance on BOTH moments — emergent: a converging env fills
 *      Running/Pending; a converged env is a big green Done block
 *      sub-divided by kind, with the parked cutover tether in Dormant.
 *
 * Layer semantics (orthogonal, exactly two default layers):
 *   • `progress` (Layer 1) — the leaf's progress STATE bucket
 *     (Running / Pending / Done / Degraded / Failed / Dormant).
 *   • `kind` (Layer 2) — the typed JobKind discriminator (#3646):
 *     install / reconcile / step / cron / task / mutation / reconciler /
 *     lifecycle.
 * Leaves are individual jobs carrying `jobId` (the JobDetail deep-link
 * discriminator), `statusKind` + `statusLabel` (colour + tooltip), and
 * uniform size (1 per job; the dormant cutover aggregate carries the
 * count of collapsed steps so the parked tether stays visible).
 *
 * NON-COMPONENT module by design: Dashboard.tsx (the page) may only
 * export components (react-refresh/only-export-components), so its
 * pure data logic lives here — the same co-location pattern as the
 * sibling `jobs.ts` / `applicationCatalog.ts` derivations. Everything
 * here is pure and unit-tested in Dashboard.progress-layers.test.tsx.
 */

import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { statusKindOf, type StatusKind } from '@/shared/lib/statusColors'
import { jobKind, JOB_ENGINE_LABELS, type Job, type JobKind } from '@/lib/jobs.types'
import {
  aggregateStatusKinds,
  colorFunctionFor,
  statusColor,
  type TreemapColorBy,
  type TreemapData,
  type TreemapDimension,
  type TreemapItem,
} from '@/lib/treemap.types'
import type { ApplicationDescriptor } from './applicationCatalog'

/** Default layer stack — [progress, kind] UNCONDITIONALLY (#4731 amend).
 *  Both the converging and the converged Sovereign default to this stack;
 *  the operator re-stacks (org/application/utilization stay one click away
 *  in the layer controller). */
export const PROVISIONING_DEFAULT_LAYERS: readonly TreemapDimension[] = [
  'progress',
  'kind',
]

/** Human label per JobKind for the `kind` dimension buckets — the REAL
 *  engine names, sourced from the canonical {@link JOB_ENGINE_LABELS} in
 *  jobs.types.ts (single source of truth, shared with the /jobs Kind
 *  filter; per INVIOLABLE-PRINCIPLES #4 no per-surface hardcoded map). */
export const JOB_KIND_LABELS: Record<JobKind, string> = JOB_ENGINE_LABELS

/**
 * Per-kind stage order — used ONLY to sort the `kind` buckets in a stable
 * platform-lifecycle order (Infrastructure → installs → reconciles →
 * Day-2 → steps → tasks → recurring → reconcilers). It is NOT a bucketing
 * dimension: the `progress` layer buckets by state (below), never by kind.
 */
export const KIND_STAGES: Record<JobKind, { order: number }> = {
  lifecycle: { order: 0 },
  install: { order: 1 },
  reconcile: { order: 2 },
  mutation: { order: 3 },
  step: { order: 4 },
  task: { order: 5 },
  cron: { order: 6 },
  reconciler: { order: 7 },
  group: { order: 8 },
}

/**
 * Progress state — the Layer-1 `progress` bucket a leaf lands in. These
 * are the founder's five (+degraded) progress states, orthogonal to kind:
 * a converged env is mostly Done, the tethered cutover is Dormant, and any
 * in-flight/queued/failed work surfaces in its own bucket.
 */
export type ProgressState =
  | 'running'
  | 'pending'
  | 'done'
  | 'degraded'
  | 'failed'
  | 'dormant'

/** Bucket chrome per progress state. `statusKind` drives the bucket colour
 *  (the same channel the leaves colour by), `order` fixes the left→right
 *  reading order (active → queued → done → problems → parked). */
export const PROGRESS_STATES: Record<
  ProgressState,
  { id: string; label: string; statusKind: StatusKind; order: number }
> = {
  running:  { id: 'progress-running',  label: 'Running',  statusKind: 'in-progress', order: 0 },
  pending:  { id: 'progress-pending',  label: 'Pending',  statusKind: 'pending',     order: 1 },
  done:     { id: 'progress-done',     label: 'Done',     statusKind: 'success',     order: 2 },
  degraded: { id: 'progress-degraded', label: 'Degraded', statusKind: 'warning',     order: 3 },
  failed:   { id: 'progress-failed',   label: 'Failed',   statusKind: 'failed',      order: 4 },
  dormant:  { id: 'progress-dormant',  label: 'Dormant',  statusKind: 'dormant',     order: 5 },
}

/** Map a leaf's semantic status kind onto its progress-state bucket. */
export function progressStateForKind(kind: StatusKind): ProgressState {
  switch (kind) {
    case 'in-progress':
      return 'running'
    case 'success':
      return 'done'
    case 'warning':
      return 'degraded'
    case 'failed':
      return 'failed'
    case 'dormant':
      return 'dormant'
    case 'pending':
    default:
      return 'pending'
  }
}

/** Display name for the collapsed dormant-cutover aggregate leaf. */
export const CUTOVER_DORMANT_LEAF_NAME = 'cutover (dormant)'

/** A resolved grouping bucket for one fold dimension. */
interface JobBucketKey {
  id: string | null
  name: string
  /** Sort key within the level — state order for progress, stage order for
   *  kinds; name-sorted when undefined. */
  order?: number
}

/**
 * Per-leaf intermediate the fold operates on. Decouples the derivation
 * from raw `Job.status` so the synthetic dormant-cutover aggregate can
 * carry a `dormant` statusKind without inventing a fake JobStatus.
 */
interface LeafInfo {
  /** JobDetail deep-link target — a real leaf's id, or the cutover GROUP
   *  id for the dormant aggregate (so a click opens the per-step Cutover
   *  detail; GetJob is unfiltered). */
  jobId: string
  name: string
  kind: JobKind
  statusKind: StatusKind
  /** Raw status word for the tooltip / cell sub-label. */
  statusLabel: string
  progress: ProgressState
  appId: string
  region?: string
  /** 1 for a real leaf; the collapsed step count for the dormant aggregate
   *  (keeps the parked tether proportionally visible under uniform size). */
  count: number
}

/** Walk parentId links to a leaf's TOPMOST group ancestor (null when
 *  the leaf hangs off the root with no group above it). */
function topGroupOf(job: Job, byId: ReadonlyMap<string, Job>): Job | null {
  let top: Job | null = null
  let cur: Job | undefined = job
  const seen = new Set<string>([job.id])
  while (cur && cur.parentId) {
    const parent = byId.get(cur.parentId)
    if (!parent || seen.has(parent.id)) break
    seen.add(parent.id)
    if (parent.type === 'group') top = parent
    cur = parent
  }
  return top
}

/** Strip the multi-region "<region>:" prefix off an appId so catalog
 *  lookups match the canonical bp-* id. */
function bareAppId(appId: string): string {
  return appId.includes(':') ? appId.slice(appId.lastIndexOf(':') + 1) : appId
}

/** Canonical coverage key for an appId — region prefix stripped, bp-
 *  prefix stripped — so a live install job (appId "cilium" / "bp-cilium" /
 *  "<region>:cilium") matches its catalog Application (bareId "cilium"). */
function appCoverageKey(appId: string): string {
  const bare = bareAppId(appId)
  return bare.startsWith('bp-') ? bare.slice(3) : bare
}

/**
 * isCutoverStep — a leaf that is one step of the bp-self-sovereign-cutover
 * activity (kind=step under the Cutover group). Matches the group ancestry
 * OR the canonical "cutover-step-<slug>" jobName so a sparse store (steps
 * without a materialised group row) still classifies correctly.
 */
function isCutoverStep(leaf: Job, byId: ReadonlyMap<string, Job>): boolean {
  if (jobKind(leaf) !== 'step') return false
  if (leaf.jobName.startsWith('cutover-step-')) return true
  const top = topGroupOf(leaf, byId)
  return !!top && (top.jobName === 'cutover' || top.id.endsWith(':cutover'))
}

/**
 * buildJobsTreemapData — fold the flat Job list into the TreemapItem tree
 * for the given layer stack.
 *
 * Steps:
 *   1. Take every non-group leaf (the full inventory when the caller read
 *      ?inventory=full).
 *   2. Dormant-cutover collapse: while the cutover tether has never fired
 *      (every cutover step leaf is pending), replace the N step leaves with
 *      ONE `cutover (dormant)` aggregate coloured dormant-grey — so the
 *      parked tether never pollutes the pending bucket. Once cutover fires
 *      (any step running/succeeded/failed) the steps expand as live leaves.
 *   3. Fold the resulting leaves by the layer stack; empty buckets are
 *      never emitted; buckets sort by state/stage order then name.
 *
 * `applications` feeds the `family` dimension (appId → catalog family).
 */
export function buildJobsTreemapData(
  jobs: readonly Job[],
  layers: readonly TreemapDimension[],
  applications: readonly ApplicationDescriptor[] = [],
): TreemapData {
  const byId = new Map(jobs.map((j) => [j.id, j]))
  const leaves = jobs.filter((j) => j.type !== 'group')
  const appById = new Map(applications.map((a) => [a.id, a]))

  // ── Dormant-cutover collapse (#4731 complaint #3) ────────────────────
  const cutoverSteps = leaves.filter((l) => isCutoverStep(l, byId))
  const cutoverDormant =
    cutoverSteps.length > 0 &&
    cutoverSteps.every((s) => statusKindOf(s.status) === 'pending')

  const renderLeaves = cutoverDormant
    ? leaves.filter((l) => !isCutoverStep(l, byId))
    : leaves

  function toLeafInfo(leaf: Job): LeafInfo {
    const sk = statusKindOf(leaf.status)
    return {
      jobId: leaf.id,
      name: leaf.displayName || leaf.jobName,
      kind: jobKind(leaf),
      statusKind: sk,
      statusLabel: leaf.status,
      progress: progressStateForKind(sk),
      appId: leaf.appId,
      region: leaf.region,
      count: 1,
    }
  }

  const leafInfos: LeafInfo[] = renderLeaves.map(toLeafInfo)
  if (cutoverDormant) {
    const cutoverGroup = topGroupOf(cutoverSteps[0]!, byId)
    const cutoverGroupId =
      cutoverGroup?.id || cutoverSteps[0]!.parentId || cutoverSteps[0]!.id
    leafInfos.push({
      jobId: cutoverGroupId,
      name: CUTOVER_DORMANT_LEAF_NAME,
      kind: 'step',
      statusKind: 'dormant',
      statusLabel: 'dormant',
      progress: 'dormant',
      appId: '',
      count: cutoverSteps.length,
    })
  }

  // ── Upfront full expected inventory (founder: "see all of them at the
  // same time from the beginning") ────────────────────────────────────
  // Seed EVERY expected Application (bootstrap-kit + the operator's
  // selection, resolved by resolveApplications) that has no job leaf yet as
  // a PENDING install leaf. This makes the FULL planned inventory visible
  // from t=0 — the map is not a progressively-growing set that only shows a
  // component once its job starts; every planned HR is a Pending leaf up
  // front and flips to running/done/failed as its job arrives (the live job
  // WINS wherever it exists, so a covered app is never duplicated).
  const covered = new Set<string>()
  for (const li of leafInfos) {
    if (li.appId) covered.add(appCoverageKey(li.appId))
  }
  for (const app of applications) {
    if (covered.has(app.bareId)) continue
    covered.add(app.bareId)
    leafInfos.push({
      // No job yet — id-less, so the planned leaf is not clickable until
      // its real install job lands and takes over the cell.
      jobId: '',
      name: `install-${app.bareId}`,
      kind: 'install',
      statusKind: 'pending',
      statusLabel: 'pending',
      progress: 'pending',
      appId: app.id,
      count: 1,
    })
  }

  function bucketFor(dimension: TreemapDimension, leaf: LeafInfo): JobBucketKey {
    switch (dimension) {
      case 'progress': {
        const st = PROGRESS_STATES[leaf.progress]
        return { id: st.id, name: st.label, order: st.order }
      }
      case 'kind': {
        const k = leaf.kind
        return { id: `kind-${k}`, name: JOB_KIND_LABELS[k], order: KIND_STAGES[k].order }
      }
      case 'application':
        return leaf.appId
          ? { id: leaf.appId, name: leaf.appId }
          : { id: 'no-application', name: 'No application' }
      case 'family': {
        const app = leaf.appId ? appById.get(bareAppId(leaf.appId)) : undefined
        return app
          ? { id: app.familyId, name: app.familyName }
          : { id: 'platform', name: 'Platform' }
      }
      case 'region': {
        const region = leaf.region || 'primary'
        return { id: region, name: region }
      }
      default:
        // Jobs carry no namespace/cluster/org/… axis — one rollup bucket.
        return { id: `all-${dimension}`, name: '—' }
    }
  }

  function leafItem(leaf: LeafInfo): TreemapItem {
    return {
      id: leaf.jobId,
      name: leaf.name,
      count: leaf.count,
      percentage: null,
      size_value: leaf.count,
      statusKind: leaf.statusKind,
      statusLabel: leaf.statusLabel,
      jobId: leaf.jobId,
    }
  }

  function fold(subset: readonly LeafInfo[], depth: number): TreemapItem[] {
    if (depth >= layers.length) {
      return subset.map(leafItem)
    }
    const dimension = layers[depth]!
    const buckets = new Map<string, { key: JobBucketKey; leaves: LeafInfo[] }>()
    for (const leaf of subset) {
      const key = bucketFor(dimension, leaf)
      const mapKey = key.id ?? key.name
      const entry = buckets.get(mapKey)
      if (entry) entry.leaves.push(leaf)
      else buckets.set(mapKey, { key, leaves: [leaf] })
    }
    const sorted = [...buckets.values()].sort((a, b) => {
      const ao = a.key.order
      const bo = b.key.order
      if (ao !== undefined && bo !== undefined && ao !== bo) return ao - bo
      if (ao !== undefined && bo === undefined) return -1
      if (ao === undefined && bo !== undefined) return 1
      return a.key.name.localeCompare(b.key.name)
    })
    return sorted.map(({ key, leaves: bucketLeaves }) => {
      const children = fold(bucketLeaves, depth + 1)
      const totalCount = bucketLeaves.reduce((n, l) => n + l.count, 0)
      return {
        id: key.id,
        name: key.name,
        count: totalCount,
        percentage: null,
        size_value: totalCount,
        statusKind: aggregateStatusKinds(bucketLeaves.map((l) => l.statusKind)),
        children,
      }
    })
  }

  // total_count reflects the FULL rendered inventory size: every real leaf
  // (with the collapsed dormant cutover steps counted individually) PLUS the
  // upfront-seeded expected-but-not-yet-started components, so the page
  // header is honest about how many components the operator is watching —
  // from t=0, not once each job starts.
  const totalCount = leafInfos.reduce((n, l) => n + l.count, 0)
  return { items: fold(leafInfos, 0), total_count: totalCount }
}

/**
 * Job leaf → JobDetail href. Mirrors JobsTable's useJobLinkBuilder
 * exactly: strip the FIRST "<prefix>:" from the full job id (the
 * mothership emits "<deploymentId>:install-X" / multi-region
 * "<deploymentId>:<region>:install-X" while jobs.Store keys by the bare
 * name), URL-encode, and build the mode-aware path — `/jobs/$jobId` on
 * the chroot Sovereign Console, `/provision/$deploymentId/jobs/$jobId`
 * on the mothership. An id-less mothership link falls back to
 * /deployments (#4704 Task B — never a literal in the $deploymentId
 * slot).
 */
export function jobTileHref(jobId: string, deploymentId: string): string {
  const bare = jobId.includes(':') ? jobId.slice(jobId.indexOf(':') + 1) : jobId
  const encoded = encodeURIComponent(bare)
  if (DETECTED_MODE.mode === 'sovereign') return `/jobs/${encoded}`
  if (!deploymentId) return '/deployments'
  return `/provision/${deploymentId}/jobs/${encoded}`
}

/** Neutral fill used when a cell's percentage is null (e.g. utilization
 *  requested but metrics-server is not installed on the Sovereign).
 *  Desaturated grey, visibly different from any point on the
 *  utilization/health/age gradients. */
export const NULL_PERCENTAGE_FILL = 'rgba(125, 125, 125, 0.45)'

/**
 * cellFillFor — resolve a treemap cell's fill for the active colour
 * mode. `status` is CATEGORICAL: the fill comes from the cell's typed
 * `statusKind` channel (statusColor), never from the 0..100 percentage,
 * and an absent statusKind renders as pending grey. Gradient modes keep
 * the exact pre-#4731 behaviour (percentage → gradient, null → neutral
 * grey).
 */
export function cellFillFor(colorBy: TreemapColorBy, item: TreemapItem): string {
  if (colorBy === 'status') return statusColor(item.statusKind)
  return item.percentage === null
    ? NULL_PERCENTAGE_FILL
    : colorFunctionFor(colorBy)(item.percentage)
}

/** cellPulseFor — running cells pulse in status colour mode so an
 *  in-flight job is unmistakably distinct from pending and success. */
export function cellPulseFor(colorBy: TreemapColorBy, item: TreemapItem): boolean {
  return colorBy === 'status' && item.statusKind === 'in-progress'
}

/** cellDashedFor — a dormant cell (the parked cutover tether) renders with
 *  a dashed outline so it is unmistakably distinct from a solid pending
 *  cell — dormant ≠ pending, the confusion the founder flagged (#4731). */
export function cellDashedFor(colorBy: TreemapColorBy, item: TreemapItem): boolean {
  return colorBy === 'status' && item.statusKind === 'dormant'
}
