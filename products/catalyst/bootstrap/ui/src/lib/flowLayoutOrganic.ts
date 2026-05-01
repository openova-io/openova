/**
 * flowLayoutOrganic — fold-aware recursive Job-tree layout (issue #351).
 *
 * Returns ONLY topology — depth (longest path from a root), region,
 * family, status. The canvas's d3-force simulation does the actual
 * positioning.
 *
 * # Recursive Job model
 *
 * Every Job carries `parentId` and `childIds`. A "group" Job is a
 * synthesised parent whose children are leaf installs (or other
 * groups). The layout takes a `folded` set of Job ids: when a group
 * is folded, the layout emits ONE node for the group (with a
 * child-count badge) and substitutes group→group / group→leaf edges
 * for any cross-group dependencies that would otherwise hang at the
 * leaf level. When a group is unfolded, the layout emits nodes for
 * the group AND its children, with parent→child structural edges and
 * the children's ordinary dependsOn edges.
 *
 * # Design contract per operator (2026-04-30, refined #351)
 *
 *   • Bubbles spread organically across the FULL canvas width, x-axis
 *     determined by dependency depth (depth 0 → leftmost, deepest →
 *     rightmost). Group nodes sit at depth 0 (or one beyond their own
 *     parent's depth) so the recursion reads left → right.
 *   • Same-depth siblings scatter loosely vertically; they MUST NOT be
 *     vertically aligned in a strict column.
 *   • Edges are bezier curves between live positions with arrowheads
 *     and source-status colour.
 *   • NO "STAGE 1/2/..." labels. NO column dividers. The layout has
 *     no concept of "stage".
 *   • Folded parent-group bubbles render as a single node with a
 *     `childCount` badge; double-click "drills into" by emitting the
 *     parent's id from the canvas (the consumer changes `folded`).
 *
 * Pure: same input → same output. No DOM, no React, no side effects.
 */

import type { Job } from './jobs.types'

/** A blueprint family — used to colour-code bubbles. */
export interface OrganicFamily {
  id: string
  label: string
  color: string
}

/** A region descriptor — used to vertically group bubbles. */
export interface OrganicRegion {
  id: string
  label: string
  meta?: string
}

/** Per-job hint provided by the page — region + family + extra dep ids. */
export interface OrganicNodeHints {
  regionId: string
  familyId: string
  /** Optional jobIds to add as extra depsOn edges (component-graph deps). */
  extraDepIds?: string[]
}

/** A node in the organic layout output. */
export interface OrganicNode {
  /** Stable id — Job.id for both leaves and groups. */
  id: string
  /** 0-based longest-path depth from any root in the FOLD-RESPECTING view. */
  depth: number
  regionId: string
  familyId: string
  /** Display label — Job.displayName (groups) or jobName less leading "install-" (leaves). */
  label: string
  /** Sub-label — duration for leaves, "<n> jobs" for folded groups. */
  subLabel: string
  /** Status — drives ring colour. For groups this is the rolled-up status. */
  status: Job['status']
  /** True when the node represents a parent group. Drives shape (badge + child-count). */
  isGroup: boolean
  /** True when the node is a folded group (children hidden). False for leaves and unfolded groups. */
  isFolded: boolean
  /** Child count — used to render the "12 jobs inside" badge on folded groups. */
  childCount: number
  /** Underlying job — forwarded for tooltip / click handler. */
  job: Job
}

/** A directed edge between two nodes. */
export interface OrganicEdge {
  fromId: string
  toId: string
  fromStatus: Job['status']
  /** Cross-region edge — drawn with a different (warm) tone. */
  crossRegion: boolean
  /** Edge kind — drives stroke style (parent-child structural vs ordinary depends-on). */
  kind: 'depends-on' | 'parent-child'
}

/** The output. */
export interface OrganicLayoutResult {
  nodes: OrganicNode[]
  edges: OrganicEdge[]
  /** Max depth across all nodes — useful for x-axis scale. */
  maxDepth: number
  regions: OrganicRegion[]
  families: OrganicFamily[]
}

export const FALLBACK_REGION_ID = 'primary'

/**
 * Compute the layout-ready data from a recursive Job tree + fold
 * state. The contract:
 *
 *   • A folded group emits ONE node (no children, no inner edges).
 *   • An unfolded group emits a node for itself AND each child whose
 *     own ancestors are all unfolded. Parent-child structural edges
 *     connect each visible parent to its visible direct children.
 *   • Each visible leaf's `dependsOn` becomes a `depends-on` edge IFF
 *     the dependency is itself visible. Otherwise the edge is
 *     elevated to the dependency's nearest visible ancestor — so a
 *     leaf depending on something inside a folded group sees an edge
 *     to the folded group node (which is what the operator expects).
 *
 * Depth = longest-path from any root in this filtered/elevated graph.
 */
export function flowLayoutOrganic(
  jobs: readonly Job[],
  opts: {
    hints: ReadonlyMap<string, OrganicNodeHints>
    regions: readonly OrganicRegion[]
    families: readonly OrganicFamily[]
    /** Set of Job ids that are folded (group → single node, children hidden). */
    folded?: ReadonlySet<string>
  },
): OrganicLayoutResult {
  const { hints, regions, families } = opts
  const folded = opts.folded ?? new Set<string>()

  const fallbackRegion: OrganicRegion =
    regions.find((r) => r.id === FALLBACK_REGION_ID) ?? regions[0] ?? {
      id: FALLBACK_REGION_ID,
      label: 'Primary Region',
    }

  // Index every job by id; build parent / child adjacency.
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)

  // Determine which jobs are visible: a job is visible when none of
  // its ancestors are folded. Walk parentId chain; bail to false on
  // any folded ancestor.
  function isVisible(j: Job): boolean {
    let pid = j.parentId
    while (pid) {
      if (folded.has(pid)) return false
      const parent = byId.get(pid)
      if (!parent) break
      pid = parent.parentId
    }
    return true
  }

  // Resolve a referenced id (a `dependsOn` target) to its nearest
  // visible ancestor — the visible representative the canvas should
  // draw the edge to.
  function visibleRepresentative(id: string): string | null {
    let node: Job | undefined = byId.get(id)
    while (node) {
      if (isVisible(node) && (node.type !== 'group' || !folded.has(node.id))) {
        // For a group the folded check above is redundant with
        // isVisible (a folded group is itself visible — it's the
        // representative), but keep it explicit so the intent reads.
        return node.id
      }
      // Stop walking up at the first ancestor that IS visible — the
      // folded group itself is visible (just collapsed). isVisible
      // returns true for the folded group when its own ancestors are
      // unfolded.
      if (isVisible(node)) return node.id
      if (!node.parentId) break
      node = byId.get(node.parentId)
    }
    return null
  }

  // Build the visible job list.
  const visibleJobs: Job[] = []
  for (const j of jobs) {
    if (isVisible(j)) visibleJobs.push(j)
  }
  const visibleIdSet = new Set(visibleJobs.map((j) => j.id))

  // Build edge set.
  const inEdges = new Map<string, Set<string>>()
  const outEdges = new Map<string, Set<string>>()
  const edgeKind = new Map<string, OrganicEdge['kind']>()
  for (const j of visibleJobs) {
    inEdges.set(j.id, new Set())
    outEdges.set(j.id, new Set())
  }
  function addEdge(from: string, to: string, kind: OrganicEdge['kind']) {
    if (!visibleIdSet.has(from) || !visibleIdSet.has(to)) return
    if (from === to) return
    inEdges.get(to)!.add(from)
    outEdges.get(from)!.add(to)
    const key = `${from}→${to}`
    // depends-on dominates parent-child if both exist, since the user
    // is more interested in execution order than structural nesting.
    if (kind === 'depends-on' || !edgeKind.has(key)) {
      edgeKind.set(key, kind)
    }
  }
  // Parent-child structural edges (one per visible parent → visible
  // direct child). Folded groups have no visible children so this
  // skips them naturally.
  for (const j of visibleJobs) {
    if (j.type !== 'group') continue
    if (folded.has(j.id)) continue
    for (const childId of j.childIds ?? []) {
      const childRep = visibleRepresentative(childId)
      if (childRep) addEdge(j.id, childRep, 'parent-child')
    }
  }
  // depends-on edges: each visible job's dependsOn lifted to the
  // nearest visible ancestor when the target itself is hidden.
  for (const j of visibleJobs) {
    const fromRep = visibleRepresentative(j.id)
    if (!fromRep) continue
    const deps = new Set<string>(j.dependsOn ?? [])
    const h = hints.get(j.id)
    for (const d of h?.extraDepIds ?? []) deps.add(d)
    for (const dep of deps) {
      const depRep = visibleRepresentative(dep)
      if (depRep && depRep !== fromRep) addEdge(depRep, fromRep, 'depends-on')
    }
  }

  // Compute depth = longest-path from any root (any node with no
  // in-edges). Iterative relaxation: depth[v] = max(depth[u] + 1) for
  // u in inEdges[v].
  const depth = new Map<string, number>()
  for (const j of visibleJobs) depth.set(j.id, 0)
  let changed = true
  let iterations = 0
  const cap = visibleJobs.length + 2
  while (changed && iterations < cap) {
    changed = false
    iterations++
    for (const j of visibleJobs) {
      const ins = inEdges.get(j.id)!
      let bestParent = -1
      for (const p of ins) bestParent = Math.max(bestParent, depth.get(p) ?? 0)
      const want = bestParent + 1
      if (want > 0 && want > (depth.get(j.id) ?? 0)) {
        depth.set(j.id, want)
        changed = true
      }
    }
  }

  // Emit nodes.
  const nodes: OrganicNode[] = visibleJobs.map((j) => {
    const h = hints.get(j.id)
    const regionId = h?.regionId && regions.some((r) => r.id === h.regionId)
      ? h.regionId
      : fallbackRegion.id
    const familyId = h?.familyId ?? 'platform'
    const isGroup = j.type === 'group'
    const isFolded = isGroup && folded.has(j.id)
    const childCount = (j.childIds ?? []).length
    const label = labelFor(j)
    const subLabel = isFolded
      ? `${childCount} ${childCount === 1 ? 'job' : 'jobs'}`
      : j.durationMs > 0
        ? formatDurationShort(j.durationMs)
        : ''
    return {
      id: j.id,
      depth: depth.get(j.id) ?? 0,
      regionId,
      familyId,
      label,
      status: j.status,
      subLabel,
      isGroup,
      isFolded,
      childCount,
      job: j,
    }
  })

  // Emit edges.
  const nodeById = new Map(nodes.map((n) => [n.id, n]))
  const edges: OrganicEdge[] = []
  for (const from of visibleJobs) {
    const outs = outEdges.get(from.id) ?? new Set<string>()
    for (const toId of outs) {
      const fromNode = nodeById.get(from.id)
      const toNode = nodeById.get(toId)
      if (!fromNode || !toNode) continue
      const kind = edgeKind.get(`${from.id}→${toId}`) ?? 'depends-on'
      edges.push({
        fromId: from.id,
        toId,
        fromStatus: fromNode.status,
        crossRegion: fromNode.regionId !== toNode.regionId,
        kind,
      })
    }
  }

  const maxDepth = nodes.reduce((m, n) => Math.max(m, n.depth), 0)

  return {
    nodes,
    edges,
    maxDepth,
    regions: [...regions],
    families: [...families],
  }
}

/** Default fold state — groups deeper than `depth` are folded. The
 *  recursive tree has at most one level of grouping today (groups own
 *  leaves, no nested groups), so depth=2 unfolds the top-level groups
 *  AND their leaves; depth=1 keeps the top-level groups folded. */
export function defaultFoldedAtDepth(jobs: readonly Job[], depth: number): Set<string> {
  if (depth <= 0) return new Set(jobs.filter((j) => j.type === 'group').map((j) => j.id))
  // Walk parent chain to derive depth-from-root. depth 1 = root.
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)
  const out = new Set<string>()
  for (const j of jobs) {
    if (j.type !== 'group') continue
    let d = 1
    let pid = j.parentId
    while (pid) {
      d++
      const parent = byId.get(pid)
      if (!parent) break
      pid = parent.parentId
    }
    if (d >= depth) out.add(j.id)
  }
  return out
}

/** Format ms → "1m 23s" / "12s" / "152ms". */
function formatDurationShort(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  if (m < 60) return rs ? `${m}m ${rs}s` : `${m}m`
  const h = Math.floor(m / 60)
  const rm = m % 60
  return rm ? `${h}h ${rm}m` : `${h}h`
}

function labelFor(j: Job): string {
  if (j.displayName && j.displayName.length > 0) return j.displayName
  return j.jobName.replace(/^install-/, '')
}
