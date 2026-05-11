/**
 * @openova/flow-core — layout module.
 *
 * Pure (no DOM, no React, no side effects) topology computation:
 *
 *   Input  — { flow, nodes, relationships, folded, hints? }
 *   Output — positioned nodes (depth + depRank + region + family),
 *            tagged edges (per-relationship-type + crossRegion flag),
 *            and connected-component metadata for gutters.
 *
 * # Migration from the legacy `flowLayoutOrganic`
 *
 * The legacy layout took a `readonly Job[]` (with `parentId` +
 * `dependsOn` on each node) and emitted `kind: 'depends-on' |
 * 'parent-child'`. This version takes `FlowNode[] + Relationship[]`
 * and emits `relType: RelationshipType` per edge. Internally:
 *
 *   • Hierarchy graph = `relationships.filter(r => r.type === 'contains')`.
 *     `r.toId` is the parent (container); `r.fromId` is the child.
 *   • Blocking-DAG graph = `relationships.filter(r => isBlocking(r.type))`.
 *     All FS/SS/FF/SF/triggers edges are counted for depth.
 *   • `condition === 'on-failure'` edges are emitted as overlay
 *     edges but NOT counted for depth (they only fire on a failed
 *     predecessor; they should not push successors deeper into the
 *     canvas).
 *
 * # Founder-locked invariants preserved verbatim from the legacy:
 *
 *   • Cycle-safety in the parent-chain walks (bug #476) — every
 *     traversal tracks visited ids and exits gracefully on cycles.
 *   • Fold-aware rewiring: a folded `contains`-parent emits ONE node
 *     and its dep edges fan out to the parent.
 *   • Parent-elision (bug #481): when a `contains`-parent is unfolded
 *     AND has at least one visible child, the parent is elided. Its
 *     inbound deps are rewired to its visible children; its outbound
 *     deps are lifted onto each child ("parent calling their parents").
 *   • `MAX_VISIBLE_DEPTH = 8` defence-in-depth depth cap.
 *   • Global topological-sort rank for Y-axis positioning (issue
 *     #532) — dense, stable across same-depth siblings.
 *   • Legacy `extraDepIds` hint stays supported via the new `hints`
 *     map; adapters use it to inject component-graph deps that aren't
 *     present in the per-node Relationship list.
 */

import type {
  FlowInstance,
  FlowNode,
  Relationship,
  RelationshipType,
  FamilyDescriptor,
  RegionDescriptor,
} from './types'
import { isBlockingRelationship } from './types'

/* ────────────────────────────────────────────────────────────────────
 * Hint shape (region / family / extra deps, per-node)
 * ──────────────────────────────────────────────────────────────────── */

/** A per-node hint — used when the host wants to inject region/family
 *  overrides or `extraDepIds` (blocking deps not present in the
 *  relationship feed). Mirrors the legacy `OrganicNodeHints`. */
export interface FlowLayoutHint {
  region?: string
  family?: string
  /** Extra blocking-dep node ids — treated as `finish-to-start` /
   *  `on-success` edges synthesised onto this node's inbound side. */
  extraDepIds?: string[]
}

export interface LayoutHints {
  perNode?: ReadonlyMap<string, FlowLayoutHint>
  /** Adapter's family descriptor list — used for family-id validation
   *  and to forward as the layout output's family palette. */
  families?: readonly FamilyDescriptor[]
  /** Adapter's region descriptor list — used for region-id validation
   *  and forwarded as the layout output's region palette. */
  regions?: readonly RegionDescriptor[]
}

/* ────────────────────────────────────────────────────────────────────
 * Input / output types
 * ──────────────────────────────────────────────────────────────────── */

export interface LayoutInput {
  flow: FlowInstance
  nodes: readonly FlowNode[]
  relationships: readonly Relationship[]
  folded: ReadonlySet<string>
  hints?: LayoutHints
}

export interface PositionedNode {
  /** Stable id — equals FlowNode.id. */
  id: string
  /** Originating flow id. */
  flowId: string
  /** 0-based longest-path depth in the fold-respecting visible graph. */
  depth: number
  /** Dense topological-sort rank, [0, N-1]. Used by the canvas as
   *  the Y target so visual reading order matches dependency order. */
  depRank: number
  /** Resolved region id (may equal hint region or fall back to the
   *  first descriptor / `FALLBACK_REGION_ID`). */
  region: string
  /** Resolved family id. */
  family: string
  /** Display label — pass-through from FlowNode.label. */
  label: string
  /** Status — open string from the upstream FlowNode. */
  status: string
  /** True when this node represents a `contains`-parent (group). */
  isGroup: boolean
  /** True when this node is a folded group (children hidden). */
  isFolded: boolean
  /** Count of direct children — surfaced as a badge on folded groups. */
  childCount: number
  /** Forwarded for tooltip / click handlers. */
  node: FlowNode
}

export interface PositionedEdge {
  fromId: string
  toId: string
  /** The relationship type that produced this edge. `contains` edges
   *  are NEVER emitted here — `contains` is used to drive grouping
   *  only, the canvas does not render a `contains` edge. */
  relType: Exclude<RelationshipType, 'contains'>
  /** Source-node status — used for tone selection on the edge stroke. */
  fromStatus: string
  /** Cross-region edge — drawn with a different tone. */
  crossRegion: boolean
  /** Cross-flow edge — drawn dashed by convention. */
  crossFlow: boolean
  /** Edge condition (`on-success` is the canvas default; `on-failure`
   *  edges are drawn with a fail-path style and NOT counted for
   *  depth). */
  condition: 'on-success' | 'on-failure' | 'always'
  /** Optional temporal lag in seconds — annotation. */
  lag?: number
}

export interface ComponentInfo {
  /** Stable id of the component: smallest member id in the rank order. */
  id: string
  /** Members of this weakly-connected component (excluding `contains`
   *  edges — components are computed on the blocking-DAG graph). */
  memberIds: string[]
  /** Inclusive depRank range — used by hosts to draw component gutters. */
  depRankMin: number
  depRankMax: number
}

export interface LayoutOutput {
  positionedNodes: PositionedNode[]
  edges: PositionedEdge[]
  components: ComponentInfo[]
  maxDepth: number
  families: FamilyDescriptor[]
  regions: RegionDescriptor[]
}

/* ────────────────────────────────────────────────────────────────────
 * Constants
 * ──────────────────────────────────────────────────────────────────── */

/** Reserved id for the default region descriptor when adapters
 *  don't supply one. */
export const FALLBACK_REGION_ID = 'primary'

/** Defence-in-depth depth cap. Real provisioning graphs after
 *  parent-elision collapse to ~5; this is the safety net for
 *  malformed inputs. */
export const MAX_VISIBLE_DEPTH = 8

/* ────────────────────────────────────────────────────────────────────
 * Layout entry point
 * ──────────────────────────────────────────────────────────────────── */

export function layout(input: LayoutInput): LayoutOutput {
  const { flow, nodes, relationships, folded } = input
  const hints = input.hints ?? {}
  const perNode = hints.perNode ?? new Map<string, FlowLayoutHint>()
  const families = hints.families ?? []
  const regions = hints.regions ?? []

  /* ── 1. Resolve fallback region descriptor ────────────────────── */

  const fallbackRegion: RegionDescriptor =
    regions.find((r) => r.id === FALLBACK_REGION_ID) ??
    regions[0] ?? { id: FALLBACK_REGION_ID, label: 'Primary' }

  /* ── 2. Index nodes by id (last-write-wins for collisions, same
   *      semantics as the legacy byId.set loop). Cycle-safety is in
   *      the parent-chain walks below, not here. ──────────────────── */

  const byId = new Map<string, FlowNode>()
  for (const n of nodes) byId.set(n.id, n)

  /* ── 3. Partition relationships into hierarchy vs blocking. ──── */

  // Hierarchy: parent map (child id → parent id). `contains` edges
  // have toId=parent, fromId=child.
  const parentOf = new Map<string, string>()
  // childrenOf used for visibleChildCount + fan-out logic.
  const childrenOf = new Map<string, string[]>()
  // Blocking deps per node: nodeId → list of (predId, condition, type).
  type BlockingDep = { predId: string; condition: 'on-success' | 'on-failure' | 'always'; type: Exclude<RelationshipType, 'contains'>; lag?: number }
  const blockingDepsOf = new Map<string, BlockingDep[]>()
  // Outbound blocking deps per node (used for parent-elision lift).
  const blockingOutOf = new Map<string, BlockingDep[]>()

  for (const r of relationships) {
    if (r.type === 'contains') {
      // `contains`: toId contains fromId (parent = toId).
      parentOf.set(r.fromId, r.toId)
      const arr = childrenOf.get(r.toId) ?? []
      arr.push(r.fromId)
      childrenOf.set(r.toId, arr)
      continue
    }
    if (!isBlockingRelationship(r.type)) continue
    const cond = r.condition ?? 'on-success'
    const dep: BlockingDep = {
      predId: r.fromId,
      condition: cond,
      type: r.type,
      lag: r.lag,
    }
    const inArr = blockingDepsOf.get(r.toId) ?? []
    inArr.push(dep)
    blockingDepsOf.set(r.toId, inArr)
    const outArr = blockingOutOf.get(r.fromId) ?? []
    outArr.push({ ...dep, predId: r.toId })
    blockingOutOf.set(r.fromId, outArr)
  }
  // Synthesise edges from per-node hint extraDepIds — treat as
  // finish-to-start / on-success.
  for (const [nodeId, h] of perNode) {
    if (!h.extraDepIds) continue
    for (const pred of h.extraDepIds) {
      const dep: BlockingDep = {
        predId: pred,
        condition: 'on-success',
        type: 'finish-to-start',
      }
      const inArr = blockingDepsOf.get(nodeId) ?? []
      inArr.push(dep)
      blockingDepsOf.set(nodeId, inArr)
      const outArr = blockingOutOf.get(pred) ?? []
      outArr.push({ ...dep, predId: nodeId })
      blockingOutOf.set(pred, outArr)
    }
  }

  /* ── 4. isVisible: a node is visible when no ancestor is folded.
   *      Walks parentOf; cycle-safe via `seen` set (bug #476). ── */

  function isVisible(n: FlowNode): boolean {
    let pid = parentOf.get(n.id)
    const seen = new Set<string>([n.id])
    while (pid) {
      if (seen.has(pid)) return true
      seen.add(pid)
      if (folded.has(pid)) return false
      const parent = byId.get(pid)
      if (!parent) break
      pid = parentOf.get(pid)
    }
    return true
  }

  /* ── 5. Identify "group" nodes — a node IS a group iff it appears
   *      as the parent (toId) of any `contains` edge. ──────────── */

  function isGroupNode(nodeId: string): boolean {
    const ch = childrenOf.get(nodeId)
    return !!ch && ch.length > 0
  }

  /* ── 6. Build the visible node list. Reuses isVisible/folded
   *      semantics from the legacy. ──────────────────────────── */

  const allVisibleNodes: FlowNode[] = []
  for (const n of nodes) {
    if (isVisible(n)) allVisibleNodes.push(n)
  }

  /* ── 7. Parent-elision (bug #481 round 2): an unfolded group with
   *      at least one visible child disappears from the bubble set.
   *      Inbound deps rewire to visible children; outbound deps lift
   *      onto each visible child. ─────────────────────────────── */

  // A `contains` cycle between two nodes (a-contains-b, b-contains-a)
  // would make both eligible for elision; eliding both removes every
  // bubble from the canvas. To stay cycle-safe we mark "child" as
  // visible only when the child is not an ancestor of the candidate
  // group in the `contains` graph (walk parentOf with a visited set).
  function isAncestorOf(maybeAncestor: string, descendant: string): boolean {
    let cursor = parentOf.get(descendant)
    const seen = new Set<string>([descendant])
    while (cursor) {
      if (seen.has(cursor)) return false
      seen.add(cursor)
      if (cursor === maybeAncestor) return true
      const next: string | undefined = parentOf.get(cursor)
      if (!next) return false
      cursor = next
    }
    return false
  }
  const elidedIds = new Set<string>()
  for (const n of allVisibleNodes) {
    if (!isGroupNode(n.id)) continue
    if (folded.has(n.id)) continue
    if (byId.get(n.id) !== n) continue // last-wins collision protection
    const ch = childrenOf.get(n.id) ?? []
    let visibleChildCount = 0
    for (const childId of ch) {
      if (childId === n.id) continue
      const child = byId.get(childId)
      if (!child) continue
      if (child === n) continue
      if (!isVisible(child)) continue
      // Cycle guard: if "child" is also an ancestor of n (contains
      // cycle), don't count it — otherwise both nodes get elided and
      // the canvas renders nothing.
      if (isAncestorOf(childId, n.id)) continue
      visibleChildCount++
    }
    if (visibleChildCount > 0) elidedIds.add(n.id)
  }

  const visibleNodes: FlowNode[] = allVisibleNodes.filter((n) => {
    if (!elidedIds.has(n.id)) return true
    return byId.get(n.id) !== n
  })
  const visibleIdSet = new Set(visibleNodes.map((n) => n.id))

  /* ── 8. visibleRepresentative — resolve any dep target to the
   *      nearest visible-AND-not-elided ancestor. Cycle-safe. ── */

  function visibleRepresentative(id: string): string | null {
    let cursor: FlowNode | undefined = byId.get(id)
    const seen = new Set<string>()
    while (cursor) {
      if (seen.has(cursor.id)) return cursor.id
      seen.add(cursor.id)
      const nodeVisible = isVisible(cursor)
      const nodeIsElided = elidedIds.has(cursor.id)
      const isGroup = isGroupNode(cursor.id)
      if (nodeVisible && !nodeIsElided && (!isGroup || !folded.has(cursor.id))) {
        return cursor.id
      }
      if (nodeVisible && !nodeIsElided) return cursor.id
      const pid = parentOf.get(cursor.id)
      if (!pid) break
      cursor = byId.get(pid)
    }
    return null
  }

  /* ── 9. fanOutVisibleChildren — when an inbound dep targets an
   *      elided group, fan it across the elided group's visible
   *      (non-elided) leaves. Cycle-safe. ─────────────────────── */

  function fanOutVisibleChildren(nodeId: string): string[] {
    const root = byId.get(nodeId)
    if (!root) return []
    if (!elidedIds.has(nodeId)) return [nodeId]
    const out: string[] = []
    const seen = new Set<string>([nodeId])
    const stack: string[] = [...(childrenOf.get(nodeId) ?? [])]
    while (stack.length > 0) {
      const cid = stack.pop()!
      if (seen.has(cid)) continue
      seen.add(cid)
      const c = byId.get(cid)
      if (!c) continue
      if (!isVisible(c)) continue
      if (elidedIds.has(cid)) {
        for (const gc of childrenOf.get(cid) ?? []) stack.push(gc)
      } else {
        out.push(cid)
      }
    }
    return out
  }

  /* ── 10. Build the visible edge set. ──────────────────────────── */

  // Edge dedup: per (fromId, toId) we keep the strongest edge metadata —
  // mirroring the legacy `kind: 'depends-on' | 'parent-child'` priority
  // (depends-on dominated parent-child). With the new contract we keep
  // the FIRST non-failure edge per pair as the canonical render; failure
  // edges live separately as overlays.
  type EdgeKey = string
  const edgeMap = new Map<EdgeKey, PositionedEdge>()
  function key(from: string, to: string): EdgeKey {
    return `${from}${to}`
  }
  function addEdge(
    from: string,
    to: string,
    relType: Exclude<RelationshipType, 'contains'>,
    fromStatus: string,
    crossRegion: boolean,
    crossFlow: boolean,
    condition: 'on-success' | 'on-failure' | 'always',
    lag: number | undefined,
  ) {
    if (!visibleIdSet.has(from) || !visibleIdSet.has(to)) return
    if (from === to) return
    const k = key(from, to)
    const existing = edgeMap.get(k)
    // Promotion rule: an `on-success` non-failure edge dominates
    // `on-failure`; among non-failure edges the first wins to stay
    // deterministic. `triggers` is treated as ordinary blocking.
    if (existing) {
      if (existing.condition === 'on-failure' && condition !== 'on-failure') {
        // Replace failure-overlay with a true blocking edge.
        edgeMap.set(k, {
          fromId: from,
          toId: to,
          relType,
          fromStatus,
          crossRegion,
          crossFlow,
          condition,
          lag,
        })
      }
      return
    }
    edgeMap.set(k, {
      fromId: from,
      toId: to,
      relType,
      fromStatus,
      crossRegion,
      crossFlow,
      condition,
      lag,
    })
  }

  function statusOf(id: string): string {
    return byId.get(id)?.status ?? 'pending'
  }
  function regionOf(id: string): string {
    const n = byId.get(id)
    if (!n) return fallbackRegion.id
    const hintRegion = perNode.get(id)?.region
    const candidate = hintRegion ?? n.region
    if (candidate && (regions.length === 0 || regions.some((r) => r.id === candidate))) {
      return candidate
    }
    return fallbackRegion.id
  }
  function flowOf(id: string): string {
    return byId.get(id)?.flowId ?? flow.id
  }

  // Iterate every blocking dep entry; emit rewired/lifted edges.
  for (const node of visibleNodes) {
    const fromRep = visibleRepresentative(node.id)
    if (!fromRep) continue
    const deps = blockingDepsOf.get(node.id) ?? []
    for (const d of deps) {
      const predJob = byId.get(d.predId)
      // Elided source: fan out from each visible child of the elided
      // pred to this node.
      if (predJob && elidedIds.has(predJob.id)) {
        for (const fanned of fanOutVisibleChildren(predJob.id)) {
          if (fanned !== fromRep) {
            addEdge(
              fanned,
              fromRep,
              d.type,
              statusOf(fanned),
              regionOf(fanned) !== regionOf(fromRep),
              flowOf(fanned) !== flowOf(fromRep),
              d.condition,
              d.lag,
            )
          }
        }
        continue
      }
      const depRep = visibleRepresentative(d.predId)
      if (!depRep) continue
      if (depRep === fromRep) continue
      addEdge(
        depRep,
        fromRep,
        d.type,
        statusOf(depRep),
        regionOf(depRep) !== regionOf(fromRep),
        flowOf(depRep) !== flowOf(fromRep),
        d.condition,
        d.lag,
      )
    }
  }
  // Lift outbound deps from elided groups onto each visible child.
  for (const elidedId of elidedIds) {
    const elided = byId.get(elidedId)
    if (!elided) continue
    const outDeps = blockingOutOf.get(elidedId) ?? []
    if (outDeps.length === 0) continue
    const visibleChildren = fanOutVisibleChildren(elidedId)
    for (const childId of visibleChildren) {
      for (const d of outDeps) {
        // predId is the OTHER end of the original outbound dep.
        const targetRep = visibleRepresentative(d.predId)
        const targetCandidates =
          d.predId && byId.get(d.predId) && elidedIds.has(d.predId)
            ? fanOutVisibleChildren(d.predId)
            : targetRep
              ? [targetRep]
              : []
        for (const tgt of targetCandidates) {
          if (tgt === childId) continue
          addEdge(
            childId,
            tgt,
            d.type,
            statusOf(childId),
            regionOf(childId) !== regionOf(tgt),
            flowOf(childId) !== flowOf(tgt),
            d.condition,
            d.lag,
          )
        }
      }
    }
  }
  // Fan INBOUND deps that target elided groups onto each visible
  // child of the elided group. A "foundation → apps" edge with apps
  // elided becomes "foundation → c1", "foundation → c2", ...
  //
  // We iterate elidedIds and pull their inbound deps from
  // blockingDepsOf (the per-target map). Each dep's predId is the
  // edge source; resolve to its visible representative (or fan out
  // again if the source itself is elided).
  for (const elidedId of elidedIds) {
    const inDeps = blockingDepsOf.get(elidedId) ?? []
    if (inDeps.length === 0) continue
    const fannedChildren = fanOutVisibleChildren(elidedId)
    if (fannedChildren.length === 0) continue
    for (const d of inDeps) {
      const predJob = byId.get(d.predId)
      const sources: string[] = predJob && elidedIds.has(predJob.id)
        ? fanOutVisibleChildren(predJob.id)
        : (() => {
            const rep = visibleRepresentative(d.predId)
            return rep ? [rep] : []
          })()
      for (const src of sources) {
        for (const childId of fannedChildren) {
          if (src === childId) continue
          addEdge(
            src,
            childId,
            d.type,
            statusOf(src),
            regionOf(src) !== regionOf(childId),
            flowOf(src) !== flowOf(childId),
            d.condition,
            d.lag,
          )
        }
      }
    }
  }

  /* ── 11. Depth = longest-path from any root in the BLOCKING graph
   *       (failure-conditioned edges are NOT counted, per the
   *       founder-locked rule). ──────────────────────────────── */

  // Build adjacency excluding on-failure edges.
  const blockingIn = new Map<string, Set<string>>()
  for (const n of visibleNodes) blockingIn.set(n.id, new Set())
  for (const e of edgeMap.values()) {
    if (e.condition === 'on-failure') continue
    blockingIn.get(e.toId)!.add(e.fromId)
  }

  const depth = new Map<string, number>()
  for (const n of visibleNodes) depth.set(n.id, 0)
  let changed = true
  let iterations = 0
  const cap = visibleNodes.length + 2
  while (changed && iterations < cap) {
    changed = false
    iterations++
    for (const n of visibleNodes) {
      const ins = blockingIn.get(n.id)!
      let bestParent = -1
      for (const p of ins) bestParent = Math.max(bestParent, depth.get(p) ?? 0)
      const want = bestParent + 1
      if (want > 0 && want > (depth.get(n.id) ?? 0)) {
        depth.set(n.id, want)
        changed = true
      }
    }
  }
  for (const [id, d] of depth) {
    if (d > MAX_VISIBLE_DEPTH) depth.set(id, MAX_VISIBLE_DEPTH)
  }

  /* ── 12. depRank — dense topological-sort rank for Y-axis. ──── */

  const indexInVisible = new Map<string, number>()
  visibleNodes.forEach((n, i) => indexInVisible.set(n.id, i))
  const sortedByDep = visibleNodes
    .slice()
    .sort((a, b) => {
      const da = depth.get(a.id) ?? 0
      const db = depth.get(b.id) ?? 0
      if (da !== db) return da - db
      return (indexInVisible.get(a.id) ?? 0) - (indexInVisible.get(b.id) ?? 0)
    })
  const depRank = new Map<string, number>()
  sortedByDep.forEach((n, i) => depRank.set(n.id, i))

  /* ── 13. Emit positioned nodes. ─────────────────────────────── */

  const positionedNodes: PositionedNode[] = visibleNodes.map((n) => {
    const h = perNode.get(n.id)
    const familyId = h?.family ?? n.family ?? (families[0]?.id ?? 'platform')
    const isGroup = isGroupNode(n.id)
    const isFolded = isGroup && folded.has(n.id)
    const childCount = (childrenOf.get(n.id) ?? []).length
    return {
      id: n.id,
      flowId: n.flowId,
      depth: depth.get(n.id) ?? 0,
      depRank: depRank.get(n.id) ?? 0,
      region: regionOf(n.id),
      family: familyId,
      label: n.label,
      status: n.status,
      isGroup,
      isFolded,
      childCount,
      node: n,
    }
  })

  /* ── 14. Emit positioned edges (filtered by visible-id set). ── */

  const edges: PositionedEdge[] = []
  for (const e of edgeMap.values()) {
    if (!visibleIdSet.has(e.fromId)) continue
    if (!visibleIdSet.has(e.toId)) continue
    edges.push(e)
  }

  /* ── 15. Connected components (weak) on the blocking-DAG graph
   *       — used by hosts to draw component gutters. ─────────── */

  const components = computeComponents(positionedNodes, edges)

  const maxDepth = positionedNodes.reduce((m, n) => Math.max(m, n.depth), 0)

  return {
    positionedNodes,
    edges,
    components,
    maxDepth,
    families: [...families],
    regions: regions.length > 0 ? [...regions] : [fallbackRegion],
  }
}

/* ────────────────────────────────────────────────────────────────────
 * Connected-component helper
 * ──────────────────────────────────────────────────────────────────── */

function computeComponents(
  nodes: PositionedNode[],
  edges: PositionedEdge[],
): ComponentInfo[] {
  const parent = new Map<string, string>()
  for (const n of nodes) parent.set(n.id, n.id)
  function find(x: string): string {
    let r = x
    while (parent.get(r)! !== r) r = parent.get(r)!
    // Path compression.
    let cursor = x
    while (parent.get(cursor)! !== r) {
      const nxt = parent.get(cursor)!
      parent.set(cursor, r)
      cursor = nxt
    }
    return r
  }
  function union(a: string, b: string) {
    const ra = find(a)
    const rb = find(b)
    if (ra !== rb) parent.set(ra, rb)
  }
  for (const e of edges) {
    if (parent.has(e.fromId) && parent.has(e.toId)) union(e.fromId, e.toId)
  }
  const groups = new Map<string, PositionedNode[]>()
  for (const n of nodes) {
    const r = find(n.id)
    const arr = groups.get(r) ?? []
    arr.push(n)
    groups.set(r, arr)
  }
  const components: ComponentInfo[] = []
  for (const arr of groups.values()) {
    arr.sort((a, b) => a.depRank - b.depRank)
    components.push({
      id: arr[0]?.id ?? '',
      memberIds: arr.map((n) => n.id),
      depRankMin: arr[0]?.depRank ?? 0,
      depRankMax: arr[arr.length - 1]?.depRank ?? 0,
    })
  }
  components.sort((a, b) => a.depRankMin - b.depRankMin)
  return components
}

/* ────────────────────────────────────────────────────────────────────
 * Default fold helper — cycle-safe, walks parent chain.
 * ──────────────────────────────────────────────────────────────────── */

/**
 * Produce the default-fold set: every `contains`-parent at or below
 * `depth` in the parent tree is folded by default. depth=1 keeps top-
 * level groups folded; depth=all returns an empty set.
 */
export function defaultFoldedAtDepth(
  nodes: readonly FlowNode[],
  relationships: readonly Relationship[],
  depth: number | 'all',
): Set<string> {
  if (depth === 'all') return new Set()
  const containsRels = relationships.filter((r) => r.type === 'contains')
  const parentOf = new Map<string, string>()
  const childrenOf = new Map<string, string[]>()
  for (const r of containsRels) {
    parentOf.set(r.fromId, r.toId)
    const arr = childrenOf.get(r.toId) ?? []
    arr.push(r.fromId)
    childrenOf.set(r.toId, arr)
  }
  const byId = new Map<string, FlowNode>()
  for (const n of nodes) byId.set(n.id, n)
  // A node is a group iff it has at least one child.
  if (depth <= 0) {
    return new Set([...childrenOf.keys()])
  }
  const out = new Set<string>()
  for (const groupId of childrenOf.keys()) {
    let d = 1
    let pid = parentOf.get(groupId)
    const seen = new Set<string>([groupId])
    while (pid) {
      if (seen.has(pid)) break
      seen.add(pid)
      d++
      const ppid: string | undefined = parentOf.get(pid)
      if (!ppid) break
      pid = ppid
    }
    if (d >= depth) out.add(groupId)
  }
  return out
}
