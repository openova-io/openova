/**
 * flowStreamToOrganic — bridge between the openova-flow SSE state and
 * the natural-view canvas (FlowCanvasOrganic).
 *
 * # Why this file exists
 *
 * FlowCanvasOrganic is the founder-tuned natural canvas — force-directed,
 * bounded, palette-locked (Bug #481 etc.). It consumes an
 * {@link OrganicLayoutResult} produced by {@link flowLayoutOrganic} from a
 * {@link Job} array.
 *
 * The post-2026-05-11 adapter-flux emits openova-flow `FlowNode` + FS
 * `Relationship` edges only (no synthetic phase / region parents). This
 * helper translates that wire shape into the Job array + hints the
 * organic layout expects, and bundles family/region descriptors derived
 * from the live stream.
 *
 *   • Each FlowNode becomes a leaf Job (`type: 'install'`) with its
 *     `dependsOn` populated from finish-to-start / start-to-start /
 *     finish-to-finish / start-to-finish / triggers relationships.
 *   • `contains` relationships (real chart-level parent-child, if any)
 *     become group parents: the parent is synthesized as a `type:
 *     'group'` Job that lists its children in `childIds` and the
 *     children's `parentId` points at it. Today the adapter does NOT
 *     emit any `contains` edges — this branch is dormant until a real
 *     chart-level grouping arrives.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 waterfall — single full target-state path: stream → Jobs →
 *     organic layout → canvas.
 *   #4 never hardcode — family / region come from the live stream,
 *     never from a baked list.
 */

import type {
  FlowNode,
  Relationship,
  RelationshipType,
} from '@openova/flow-core'
import type { Job, JobStatus } from './jobs.types'
import type {
  OrganicFamily,
  OrganicNodeHints,
  OrganicRegion,
} from './flowLayoutOrganic'

/* ────────────────────────────────────────────────────────────────────
 * Status mapping — adapter emits the canonical 4-bucket palette
 * already; we map any unexpected value to "pending" so the canvas
 * never sees an out-of-vocabulary status.
 * ──────────────────────────────────────────────────────────────────── */

const KNOWN_STATUSES: ReadonlyArray<JobStatus> = [
  'pending',
  'running',
  'succeeded',
  'failed',
]

function normaliseStatus(s: string): JobStatus {
  return (KNOWN_STATUSES as readonly string[]).includes(s)
    ? (s as JobStatus)
    : 'pending'
}

/* ────────────────────────────────────────────────────────────────────
 * Dependency / containment classification
 * ──────────────────────────────────────────────────────────────────── */

function isContainmentType(t: RelationshipType): boolean {
  return t === 'contains'
}

function isDependencyType(t: RelationshipType): boolean {
  return (
    t === 'finish-to-start' ||
    t === 'start-to-start' ||
    t === 'finish-to-finish' ||
    t === 'start-to-finish' ||
    t === 'triggers'
  )
}

/* ────────────────────────────────────────────────────────────────────
 * Build helpers
 * ──────────────────────────────────────────────────────────────────── */

export interface OrganicAdapterResult {
  jobs: Job[]
  hints: Map<string, OrganicNodeHints>
  regions: OrganicRegion[]
  families: OrganicFamily[]
  /**
   * Set of node ids that are the `toId` of at least one `contains`
   * relationship — i.e. real parent groups. Empty when the upstream
   * adapter (today: adapter-flux) emits no `contains` edges.
   */
  groupIds: Set<string>
}

export interface OrganicAdapterArgs {
  nodes: ReadonlyArray<FlowNode>
  relationships: ReadonlyArray<Relationship>
  /** Optional wizard-store region list — used to label region descriptors. */
  wizardRegions?: ReadonlyArray<{
    id: string
    code: string
    location: string
    name: string
  }>
  /** Per-bubble fold-disclosure: descendant counts for parents. */
}

/**
 * Translate the live SSE state into the inputs FlowCanvasOrganic expects.
 *
 * Group handling: a node X is a group when at least one Relationship
 * has `r.type === 'contains'` and `r.toId === X`. Children of X are the
 * `fromId`s of those relationships. The synthesized parent Job carries
 * status="pending" (the canvas's existing organic layout rolls status
 * up internally — we don't pre-rollup here).
 */
export function flowStreamToOrganic(
  args: OrganicAdapterArgs,
): OrganicAdapterResult {
  const { nodes, relationships, wizardRegions } = args

  // Index relationships.
  const childrenOf = new Map<string, string[]>()
  const parentOf = new Map<string, string>()
  const depsOf = new Map<string, string[]>()
  for (const r of relationships) {
    if (isContainmentType(r.type)) {
      const arr = childrenOf.get(r.toId) ?? []
      arr.push(r.fromId)
      childrenOf.set(r.toId, arr)
      parentOf.set(r.fromId, r.toId)
    } else if (isDependencyType(r.type)) {
      const arr = depsOf.get(r.toId) ?? []
      arr.push(r.fromId)
      depsOf.set(r.toId, arr)
    }
  }

  const groupIds = new Set<string>(childrenOf.keys())

  // Build Jobs.
  const jobs: Job[] = []
  const hints = new Map<string, OrganicNodeHints>()
  const familyIds = new Set<string>()
  const regionIds = new Set<string>()

  for (const n of nodes) {
    const isGroup = groupIds.has(n.id)
    const status = normaliseStatus(n.status)
    const family = (n.family ?? 'platform').trim() || 'platform'
    const regionId = (n.region ?? '').trim()
    familyIds.add(family)
    if (regionId.length > 0) regionIds.add(regionId)
    const job: Job = {
      id: n.id,
      jobName: n.label || n.id,
      displayName: n.label,
      type: isGroup ? 'group' : 'install',
      appId: '',
      parentId: parentOf.get(n.id) ?? '',
      dependsOn: depsOf.get(n.id) ?? [],
      childIds: childrenOf.get(n.id) ?? [],
      status,
      startedAt:
        typeof n.startedAt === 'number' && Number.isFinite(n.startedAt)
          ? new Date(n.startedAt).toISOString()
          : null,
      finishedAt:
        typeof n.endedAt === 'number' && Number.isFinite(n.endedAt)
          ? new Date(n.endedAt).toISOString()
          : null,
      durationMs:
        typeof n.startedAt === 'number' &&
        typeof n.endedAt === 'number' &&
        Number.isFinite(n.startedAt) &&
        Number.isFinite(n.endedAt) &&
        n.endedAt > n.startedAt
          ? n.endedAt - n.startedAt
          : 0,
    }
    jobs.push(job)
    hints.set(n.id, {
      regionId: regionId.length > 0 ? regionId : 'primary',
      familyId: family,
    })
  }

  // Families — one descriptor per seen family id. Colour is left to the
  // existing DEFAULT_FAMILIES palette resolution in FlowPage; here we
  // just declare the ids exist.
  const families: OrganicFamily[] = [...familyIds].sort().map((id) => ({
    id,
    label: id,
    color: '#94A3B8',
  }))

  // Regions — derived from the live stream; fall back to the wizard
  // store labels when present, otherwise use the bare location code.
  let regions: OrganicRegion[]
  if (regionIds.size > 0) {
    regions = [...regionIds].sort().map((id) => {
      const fromWizard = wizardRegions?.find(
        (r) => r.id === id || r.code === id,
      )
      if (fromWizard) {
        return {
          id,
          label: `${fromWizard.code.toUpperCase()} · ${fromWizard.location}`,
          meta: fromWizard.name,
        }
      }
      return { id, label: id.toUpperCase() }
    })
  } else if (wizardRegions && wizardRegions.length > 0) {
    regions = wizardRegions.map((r) => ({
      id: r.id,
      label: `${r.code.toUpperCase()} · ${r.location}`,
      meta: r.name,
    }))
  } else {
    regions = [{ id: 'primary', label: 'Primary Region' }]
  }

  return { jobs, hints, regions, families, groupIds }
}

/* ────────────────────────────────────────────────────────────────────
 * Descendant counts — per-bubble fold-disclosure badge support
 * ──────────────────────────────────────────────────────────────────── */

/**
 * For each group id, count its visible descendants currently hidden
 * under the `folded` set. Walks the contains tree.
 */
export function descendantCountByGroup(
  jobs: ReadonlyArray<Job>,
  folded: ReadonlySet<string>,
): Map<string, number> {
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)
  const out = new Map<string, number>()
  for (const g of jobs) {
    if (g.type !== 'group') continue
    if (!folded.has(g.id)) {
      out.set(g.id, 0)
      continue
    }
    let count = 0
    const stack = [...(g.childIds ?? [])]
    const seen = new Set<string>()
    while (stack.length > 0) {
      const id = stack.pop()!
      if (seen.has(id)) continue
      seen.add(id)
      const child = byId.get(id)
      if (!child) continue
      count++
      for (const c of child.childIds ?? []) stack.push(c)
    }
    out.set(g.id, count)
  }
  return out
}

/**
 * Compute the maximum nesting depth across the contains tree — used by
 * the depth chip's L<n>/<max> readout. depth 1 = top-level groups,
 * depth 2 = their children, etc.
 */
export function maxContainmentDepth(jobs: ReadonlyArray<Job>): number {
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)
  // Memoised DFS depth.
  const memo = new Map<string, number>()
  function depthAt(id: string, seen = new Set<string>()): number {
    if (seen.has(id)) return 0
    if (memo.has(id)) return memo.get(id)!
    seen.add(id)
    const j = byId.get(id)
    if (!j) return 0
    const kids = j.childIds ?? []
    if (kids.length === 0) {
      memo.set(id, 0)
      return 0
    }
    let m = 0
    for (const c of kids) {
      const d = depthAt(c, seen)
      if (d + 1 > m) m = d + 1
    }
    memo.set(id, m)
    return m
  }
  // Roots: groups with no parent.
  let best = 0
  for (const j of jobs) {
    if (j.type !== 'group') continue
    if (j.parentId && byId.has(j.parentId)) continue
    const d = depthAt(j.id) + 1 // include the root itself
    if (d > best) best = d
  }
  return best
}

/**
 * Defaults the fold set so every group whose containment depth is ≥
 * `depth` is folded.
 */
export function defaultFoldedAtContainmentDepth(
  jobs: ReadonlyArray<Job>,
  depth: number | 'all',
): Set<string> {
  if (depth === 'all') return new Set()
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)
  const groupAtDepth = (id: string): number => {
    let d = 1
    let pid = byId.get(id)?.parentId
    const seen = new Set<string>([id])
    while (pid) {
      if (seen.has(pid)) break
      seen.add(pid)
      d++
      const parent = byId.get(pid)
      if (!parent) break
      pid = parent.parentId
    }
    return d
  }
  const out = new Set<string>()
  for (const j of jobs) {
    if (j.type !== 'group') continue
    if (groupAtDepth(j.id) >= depth) out.add(j.id)
  }
  return out
}
