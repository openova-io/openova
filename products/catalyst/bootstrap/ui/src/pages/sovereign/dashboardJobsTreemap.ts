/**
 * dashboardJobsTreemap.ts — #4731 pure derivation for the Dashboard's
 * job-sourced treemap layers (`progress` / `kind`).
 *
 * A layer stack containing progress/kind builds its TreemapItem[] tree
 * client-side from the recursive Job tree
 * (GET /api/v1/deployments/{id}/jobs) instead of the pods/utilisation
 * backend. Buckets get a rolled-up `statusKind`; leaves are individual
 * jobs carrying `jobId` (the JobDetail deep-link discriminator),
 * `statusKind` + `statusLabel` (colour + tooltip), and uniform size
 * (1 per job).
 *
 * NON-COMPONENT module by design: Dashboard.tsx (the page) may only
 * export components (react-refresh/only-export-components), so its
 * pure data logic lives here — the same co-location pattern as the
 * sibling `jobs.ts` / `applicationCatalog.ts` derivations. Everything
 * here is pure and unit-tested in Dashboard.progress-layers.test.tsx.
 */

import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { statusKindOf } from '@/shared/lib/statusColors'
import { jobKind, type Job, type JobKind } from '@/lib/jobs.types'
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

/** Default layer stack while a deployment is still converging. */
export const PROVISIONING_DEFAULT_LAYERS: readonly TreemapDimension[] = [
  'progress',
  'kind',
]

/** Human label per JobKind for the `kind` dimension buckets. */
export const JOB_KIND_LABELS: Record<JobKind, string> = {
  lifecycle: 'Lifecycle',
  install: 'Install',
  reconcile: 'Reconcile',
  mutation: 'Mutation',
  step: 'Step',
  task: 'Task',
  cron: 'Cron',
  reconciler: 'Reconciler',
  group: 'Group',
}

/**
 * Kind-derived lifecycle stages — the `progress` FALLBACK buckets used
 * when a deployment's group set is sparse (some stores only carry the
 * `provisioner` group, e.g. hw220's mothership store) or a leaf has no
 * group ancestor. Order = dependency order of the platform lifecycle:
 * Phase-0 infra → installs → reconciles → Day-2 → cutover/DR steps →
 * tasks → recurring → long-running reconcilers.
 */
export const KIND_STAGES: Record<
  JobKind,
  { id: string; label: string; order: number }
> = {
  lifecycle:  { id: 'stage-infrastructure', label: 'Infrastructure',  order: 0 },
  install:    { id: 'stage-installs',       label: 'Installs',        order: 1 },
  reconcile:  { id: 'stage-reconciles',     label: 'Reconciles',      order: 2 },
  mutation:   { id: 'stage-day-2',          label: 'Day-2 mutations', order: 3 },
  step:       { id: 'stage-steps',          label: 'Steps',           order: 4 },
  task:       { id: 'stage-tasks',          label: 'Tasks',           order: 5 },
  cron:       { id: 'stage-recurring',      label: 'Recurring',       order: 6 },
  reconciler: { id: 'stage-reconcilers',    label: 'Reconcilers',     order: 7 },
  // Never reached for leaves (groups are bucket sources, not leaves) —
  // present so the Record is total over JobKind.
  group:      { id: 'stage-other',          label: 'Other',           order: 8 },
}

/** A resolved grouping bucket for one fold dimension. */
interface JobBucketKey {
  id: string | null
  name: string
  /** Sort key within the level — dependency order for progress buckets,
   *  stage order for kinds; name-sorted when undefined. */
  order?: number
}

/**
 * Topological order of the Job tree's group rows (Kahn's algorithm over
 * the dependsOn edges BETWEEN groups, original array order as the
 * stable tiebreak). Returns group-id → dependency-order index.
 */
function groupDependencyOrder(groups: readonly Job[]): Map<string, number> {
  const ids = new Set(groups.map((g) => g.id))
  const indegree = new Map<string, number>()
  const dependents = new Map<string, string[]>()
  for (const g of groups) {
    indegree.set(g.id, (g.dependsOn ?? []).filter((d) => ids.has(d)).length)
    for (const d of g.dependsOn ?? []) {
      if (!ids.has(d)) continue
      dependents.set(d, [...(dependents.get(d) ?? []), g.id])
    }
  }
  const order = new Map<string, number>()
  // Stable Kahn: always take the lowest-array-index ready node next.
  const remaining = [...groups]
  while (remaining.length > 0) {
    const readyIdx = remaining.findIndex((g) => (indegree.get(g.id) ?? 0) === 0)
    // Cycle guard — fall back to array order for whatever is left.
    const takeIdx = readyIdx === -1 ? 0 : readyIdx
    const [g] = remaining.splice(takeIdx, 1)
    order.set(g!.id, order.size)
    for (const dep of dependents.get(g!.id) ?? []) {
      indegree.set(dep, (indegree.get(dep) ?? 1) - 1)
    }
  }
  return order
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

/**
 * buildJobsTreemapData — fold the flat Job list into the TreemapItem
 * tree for the given layer stack. Leaves = individual (non-group) jobs;
 * every stack level groups them by that dimension's key. Buckets are
 * emitted in dependency/stage order (then name order); empty buckets
 * are never emitted. `applications` feeds the `family` dimension
 * (appId → catalog family) — every other non-job dimension collapses
 * to a single "—" bucket because jobs carry no such axis.
 */
export function buildJobsTreemapData(
  jobs: readonly Job[],
  layers: readonly TreemapDimension[],
  applications: readonly ApplicationDescriptor[] = [],
): TreemapData {
  const byId = new Map(jobs.map((j) => [j.id, j]))
  const leaves = jobs.filter((j) => j.type !== 'group')

  // Progress buckets: bind to the REAL group rows unless the group set
  // is sparse (< 2 distinct top groups across all leaves), in which
  // case every leaf falls back to its kind-derived stage.
  const topGroupByLeaf = new Map<string, Job | null>(
    leaves.map((l) => [l.id, topGroupOf(l, byId)]),
  )
  const usedGroupIds = new Set(
    [...topGroupByLeaf.values()].filter((g): g is Job => g !== null).map((g) => g.id),
  )
  const sparseGroups = usedGroupIds.size < 2
  const groupOrder = groupDependencyOrder(
    jobs.filter((j) => j.type === 'group' && usedGroupIds.has(j.id)),
  )

  const appById = new Map(applications.map((a) => [a.id, a]))

  function bucketFor(dimension: TreemapDimension, leaf: Job): JobBucketKey {
    switch (dimension) {
      case 'progress': {
        const group = sparseGroups ? null : (topGroupByLeaf.get(leaf.id) ?? null)
        if (group) {
          return {
            id: group.id,
            name: group.displayName || group.jobName,
            order: groupOrder.get(group.id),
          }
        }
        const stage = KIND_STAGES[jobKind(leaf)]
        // Kind-derived fallback stages sort AFTER any real groups.
        return { id: stage.id, name: stage.label, order: groupOrder.size + stage.order }
      }
      case 'kind': {
        const k = jobKind(leaf)
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

  function jobLeafItem(leaf: Job): TreemapItem {
    return {
      id: leaf.id,
      name: leaf.displayName || leaf.jobName,
      count: 1,
      percentage: null,
      size_value: 1,
      statusKind: statusKindOf(leaf.status),
      statusLabel: leaf.status,
      jobId: leaf.id,
    }
  }

  function fold(subset: readonly Job[], depth: number): TreemapItem[] {
    if (depth >= layers.length) {
      return subset.map(jobLeafItem)
    }
    const dimension = layers[depth]!
    const buckets = new Map<string, { key: JobBucketKey; jobs: Job[] }>()
    for (const leaf of subset) {
      const key = bucketFor(dimension, leaf)
      const mapKey = key.id ?? key.name
      const entry = buckets.get(mapKey)
      if (entry) entry.jobs.push(leaf)
      else buckets.set(mapKey, { key, jobs: [leaf] })
    }
    const sorted = [...buckets.values()].sort((a, b) => {
      const ao = a.key.order
      const bo = b.key.order
      if (ao !== undefined && bo !== undefined && ao !== bo) return ao - bo
      if (ao !== undefined && bo === undefined) return -1
      if (ao === undefined && bo !== undefined) return 1
      return a.key.name.localeCompare(b.key.name)
    })
    return sorted.map(({ key, jobs: bucketJobs }) => {
      const children = fold(bucketJobs, depth + 1)
      return {
        id: key.id,
        name: key.name,
        count: bucketJobs.length,
        percentage: null,
        size_value: bucketJobs.length,
        statusKind: aggregateStatusKinds(
          bucketJobs.map((j) => statusKindOf(j.status)),
        ),
        children,
      }
    })
  }

  return { items: fold(leaves, 0), total_count: leaves.length }
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
