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
  /** Issue #532 — global topological-sort rank. Lower depRank = earlier in
   *  the dependency order; higher depRank = deeper in the chain. The canvas
   *  uses this as the Y-coordinate target so the visual reading order
   *  (top → bottom) matches the dependency order.
   *
   *  Rank is dense (0..N-1) and stable across same-depth siblings via id-
   *  sort, so the layout is deterministic for tests and screenshots.
   *
   *  Optional for backwards compatibility with test fixtures that
   *  build OrganicNode literals directly — when undefined, FlowCanvasOrganic
   *  derives a rank from the layout.nodes order. Real flowLayoutOrganic()
   *  output always sets it. */
  depRank?: number
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

/** Bug #481 round 2 — defence-in-depth depth cap.
 *
 *  After parent-elision, the longest visible chain in OpenOva's real
 *  graph collapses from ~190 to ~5. This cap is the safety net: any
 *  node that lands at depth > MAX_VISIBLE_DEPTH after elision is
 *  clamped to MAX_VISIBLE_DEPTH so the natural bbox can never grow
 *  past MAX_VISIBLE_DEPTH * PER_DEPTH_X (≈ 8 * 160 = 1280px), keeping
 *  the canvas readable without relying on render-time compression.
 *
 *  Exported so the canvas + tests can read the same value. */
export const MAX_VISIBLE_DEPTH = 8

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
  //
  // Cycle-safe: if the input has duplicate ids — e.g. a leaf whose id
  // collides with a synthesised group's slug, putting `parentId === id`
  // after byId.set() last-wins — we must never hang in this loop. Any
  // node we revisit during the walk is treated as a terminator, so the
  // layout degrades gracefully (the offending node renders as a root)
  // rather than freezing the browser. Bug #476.
  function isVisible(j: Job): boolean {
    let pid = j.parentId
    const seen = new Set<string>([j.id])
    while (pid) {
      if (seen.has(pid)) return true
      seen.add(pid)
      if (folded.has(pid)) return false
      const parent = byId.get(pid)
      if (!parent) break
      pid = parent.parentId
    }
    return true
  }

  // Resolve a referenced id (a `dependsOn` target) to its nearest
  // visible-AND-not-elided ancestor — the visible representative the
  // canvas should draw the edge to.
  //
  // Same cycle protection as isVisible(): track visited ids so a self-
  // referential parent chain returns the first visible node rather
  // than spinning forever. Bug #476.
  //
  // Bug #481 round 2 — `elided` (initialised below as `elidedIds`) is
  // a closure-captured Set that becomes non-empty AFTER this function
  // is defined. visibleRepresentative is only CALLED from edge-build
  // code that runs after elidedIds is populated, so the closure read
  // sees the final set. Both folded and elided groups are skipped
  // here; elided groups are not visible representatives — their
  // children are.
  function visibleRepresentative(id: string): string | null {
    let node: Job | undefined = byId.get(id)
    const seen = new Set<string>()
    while (node) {
      if (seen.has(node.id)) return node.id
      seen.add(node.id)
      const nodeVisible = isVisible(node)
      const nodeIsElided = elidedIds.has(node.id)
      if (nodeVisible && !nodeIsElided && (node.type !== 'group' || !folded.has(node.id))) {
        return node.id
      }
      // Stop walking up at the first ancestor that IS visible AND not
      // elided — folded groups still qualify (they're visible, just
      // collapsed); elided groups do NOT (their children are the
      // visible representatives).
      if (nodeVisible && !nodeIsElided) return node.id
      if (!node.parentId) break
      node = byId.get(node.parentId)
    }
    return null
  }

  // Build the visible job list.
  const allVisibleJobs: Job[] = []
  for (const j of jobs) {
    if (isVisible(j)) allVisibleJobs.push(j)
  }

  // Bug #481 (round 2 — founder directive 2026-05-02): parent-elision.
  //
  // When a group is unfolded AND its children are visible, the group
  // node becomes redundant clutter. The previous behaviour rendered
  // BOTH the parent ("Applications") and all 50+ children as bubbles,
  // plus structural parent→child edges. With long sibling chains and
  // the natural-bbox depth math this drove the longest-path depth from
  // ~5 to ~190 (each generation adds the parent at one depth and its
  // children at depth+1; nested unfolded groups stack), which the
  // viewBox compression then squashed into a single vertical column.
  //
  // Founder's exact directive (2026-05-02):
  //   "if there is parent-child relation between tasks and when the
  //    child is expanded disappear the parent process from the canvas
  //    since all the children are visible, but it would require
  //    rewiring of the children to other jobs and parent calling their
  //    parents"
  //
  // Translation: an unfolded group with at least one visible child is
  // elided from the rendered set. Its inbound deps are rewired to its
  // children (so a "Foundation → Applications" edge becomes
  // "Foundation → bp-cilium, bp-coredns, …"). Its outbound deps are
  // lifted from its children (so any "Applications → Sentinel"
  // dependency becomes "<some-child> → Sentinel"). The parent-child
  // structural edges that previously connected (group → child) are
  // dropped — the children are visible on their own merits.
  //
  // Defence-in-depth (founder's #4): a max-depth cap kicks in if any
  // node still ends up at depth > MAX_VISIBLE_DEPTH after elision —
  // those nodes get their depth clamped so the layout's natural width
  // can never blow past MAX_VISIBLE_DEPTH * PER_DEPTH_X.
  const elidedIds = new Set<string>()
  for (const j of allVisibleJobs) {
    if (j.type !== 'group') continue
    if (folded.has(j.id)) continue
    // Bug #476 cycle-safety: under id-collision (two jobs with the same
    // id, the last write wins in byId), only elide when this Job is the
    // canonical entry for its id. The shadow copy is ignored — eliding
    // by id would otherwise drop *both* entries from visibleJobs.
    if (byId.get(j.id) !== j) continue
    // At least one direct child must itself be visible (children whose
    // own ancestors are all unfolded — the recursion handles nested
    // cases). If a group has zero children, or all children are hidden
    // by some quirk, keep the group node so the operator still sees
    // *something* for that scope.
    //
    // Self-reference / cycle guard: skip childIds that resolve back to
    // this group's own id (the #476 shape where a leaf collides with
    // the group's slug and points its parentId at the group). Counting
    // such an "edge" would erroneously elide a group whose only
    // visible "child" is itself.
    const childIds = j.childIds ?? []
    let visibleChildCount = 0
    for (const childId of childIds) {
      if (childId === j.id) continue
      const child = byId.get(childId)
      if (!child) continue
      if (child === j) continue
      if (isVisible(child)) visibleChildCount++
    }
    if (visibleChildCount > 0) elidedIds.add(j.id)
  }

  // The visible set rendered as bubbles excludes elided groups. Filter
  // by reference + id together: if id-collision put a non-group entry
  // alongside an elided group's id, keep the non-group entry — only
  // the actual group reference is dropped.
  const visibleJobs: Job[] = allVisibleJobs.filter((j) => {
    if (!elidedIds.has(j.id)) return true
    // The elided entry is the one byId returns; any other reference
    // with the same id is a non-group shadow (#476 collision shape) —
    // keep it so the layout still emits at least one node.
    return byId.get(j.id) !== j
  })
  const visibleIdSet = new Set(visibleJobs.map((j) => j.id))

  // Resolve a chain of elided groups → the first non-elided ancestor's
  // visible children. Used when an inbound edge targets a group whose
  // children are themselves groups (some of which are also elided).
  // Returns the set of leaf-most visible representatives the inbound
  // edge should fan out to.
  function fanOutVisibleChildren(jobId: string): string[] {
    const job = byId.get(jobId)
    if (!job) return []
    if (!elidedIds.has(jobId)) return [jobId]
    const out: string[] = []
    const seen = new Set<string>([jobId])
    const stack: string[] = [...(job.childIds ?? [])]
    while (stack.length > 0) {
      const cid = stack.pop()!
      if (seen.has(cid)) continue
      seen.add(cid)
      const c = byId.get(cid)
      if (!c) continue
      if (!isVisible(c)) continue
      if (elidedIds.has(cid)) {
        for (const gc of c.childIds ?? []) stack.push(gc)
      } else {
        out.push(cid)
      }
    }
    return out
  }

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
  // Parent-child structural edges. Bug #481 round 2: elided groups do
  // not render bubbles, so they emit no parent→child structural edges.
  // Visible (non-elided) groups — folded groups, or groups that have
  // zero visible children — still emit one parent→child edge per
  // visible direct child.
  for (const j of visibleJobs) {
    if (j.type !== 'group') continue
    if (folded.has(j.id)) continue
    for (const childId of j.childIds ?? []) {
      const childRep = visibleRepresentative(childId)
      if (childRep) addEdge(j.id, childRep, 'parent-child')
    }
  }
  // depends-on edges. Bug #481 round 2 rewire rules:
  //
  //   • If the dep TARGET is an elided group, the edge fans out to
  //     every visible (non-elided) child of that group — "Foundation →
  //     Applications" becomes "Foundation → bp-cilium, …, bp-coredns".
  //   • If the SOURCE itself is an elided group's child… wait, the
  //     source is always a visible (non-elided) job — we iterate
  //     visibleJobs. The case the founder called out — "parent calling
  //     their parents" — applies when an elided group's `dependsOn`
  //     pointed at another job: those deps must be honoured by EACH of
  //     that group's visible children, so the children can take over
  //     the parent's outbound edges. We handle that below by also
  //     iterating elided groups' deps and fanning them out as edges
  //     from each visible child of the elided group.
  for (const j of visibleJobs) {
    const fromRep = visibleRepresentative(j.id)
    if (!fromRep) continue
    const deps = new Set<string>(j.dependsOn ?? [])
    const h = hints.get(j.id)
    for (const d of h?.extraDepIds ?? []) deps.add(d)
    for (const dep of deps) {
      // Resolve the dep to one or more rendered representatives. For a
      // non-elided dep this is the single visibleRepresentative result.
      // For an elided group dep, we fan out to its visible children.
      const depJob = byId.get(dep)
      if (depJob && elidedIds.has(depJob.id)) {
        for (const fanned of fanOutVisibleChildren(depJob.id)) {
          if (fanned !== fromRep) addEdge(fanned, fromRep, 'depends-on')
        }
        continue
      }
      const depRep = visibleRepresentative(dep)
      if (!depRep) continue
      if (depRep !== fromRep) addEdge(depRep, fromRep, 'depends-on')
    }
  }
  // Bug #481 round 2: lift elided groups' OUTBOUND deps onto each of
  // their visible children — "parent calling their parents". If the
  // unfolded "Applications" group itself depended on "bp-foundation",
  // every visible child of Applications now depends on bp-foundation
  // (or whichever visible representative bp-foundation resolves to).
  // The hints (extraDepIds) are forwarded the same way.
  for (const elidedId of elidedIds) {
    const elided = byId.get(elidedId)
    if (!elided) continue
    const deps = new Set<string>(elided.dependsOn ?? [])
    const h = hints.get(elidedId)
    for (const d of h?.extraDepIds ?? []) deps.add(d)
    if (deps.size === 0) continue
    const visibleChildrenOfElided = fanOutVisibleChildren(elidedId)
    for (const childId of visibleChildrenOfElided) {
      for (const dep of deps) {
        const depJob = byId.get(dep)
        if (depJob && elidedIds.has(depJob.id)) {
          for (const fanned of fanOutVisibleChildren(depJob.id)) {
            if (fanned !== childId) addEdge(fanned, childId, 'depends-on')
          }
          continue
        }
        const depRep = visibleRepresentative(dep)
        if (!depRep) continue
        if (depRep !== childId) addEdge(depRep, childId, 'depends-on')
      }
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

  // Bug #481 round 2 — defence-in-depth max-depth cap (founder
  // directive #4: "ALSO add a max-depth cap as defence-in-depth"). Even
  // after parent-elision, a malformed graph could still produce a
  // longest-path > MAX_VISIBLE_DEPTH. Clamp every node's depth to
  // MAX_VISIBLE_DEPTH so the natural-bbox horizontal span stays under
  // MAX_VISIBLE_DEPTH * PER_DEPTH_X, ensuring the FlowCanvas viewBox
  // never has to compress depth pathologically. This is structural
  // protection against a future regression in upstream graph shape.
  for (const [id, d] of depth) {
    if (d > MAX_VISIBLE_DEPTH) depth.set(id, MAX_VISIBLE_DEPTH)
  }

  // Issue #532 — global topological-sort rank for Y-axis positioning.
  //
  // Founder requirement (verbatim 2026-05-02):
  //   "following the dependency order in the y axis they must
  //    homogenously spread"
  //
  // The flow canvas wants Y to read as dependency order (top → bottom).
  // We assign each visible node a dense rank in [0, N-1] using a
  // stable Kahn-style topological sort with deterministic tie-breaking:
  //
  //   • Primary key: longest-path depth (already computed above).
  //   • Secondary key: stable visibleJobs index — preserves the original
  //     job-feed order for siblings at the same depth and ensures the
  //     layout is reproducible across reloads / tests.
  //
  // The canvas reads `depRank` as the Y target. Combined with the
  // homogeneous-spread pre-pass it places node[i] at
  //   y = (depRank[i] / (N - 1)) * usableHeight
  // so all visible nodes are evenly distributed top-to-bottom and the
  // dependency order is the visual reading order.
  const indexInVisible = new Map<string, number>()
  visibleJobs.forEach((j, i) => indexInVisible.set(j.id, i))
  const sortedByDep = visibleJobs
    .slice()
    .sort((a, b) => {
      const da = depth.get(a.id) ?? 0
      const db = depth.get(b.id) ?? 0
      if (da !== db) return da - db
      return (indexInVisible.get(a.id) ?? 0) - (indexInVisible.get(b.id) ?? 0)
    })
  const depRank = new Map<string, number>()
  sortedByDep.forEach((j, i) => depRank.set(j.id, i))

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
      depRank: depRank.get(j.id) ?? 0,
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
 *  AND their leaves; depth=1 keeps the top-level groups folded.
 *
 *  Cycle-safe: walks parentId until either the chain ends or a visited
 *  id is revisited. Bug #476 — defends against malformed inputs where
 *  a leaf id collides with a group slug, putting `parentId === id`. */
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
    const seen = new Set<string>([j.id])
    while (pid) {
      if (seen.has(pid)) break
      seen.add(pid)
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
