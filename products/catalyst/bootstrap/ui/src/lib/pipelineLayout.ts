/**
 * pipelineLayout.ts — pure two-level Sugiyama layered DAG layout for
 * the JobsPage Flow tab (founder spec, urgent).
 *
 * The Flow tab visualises the job dependency graph at TWO scales,
 * simultaneously:
 *
 *   • Outer (meta) — batches arranged as meta-stages, left → right.
 *     metaStage(B) = max(metaStage(B') for B' that B depends on) + 1,
 *     where B depends on B' iff ∃ job j∈B with a job j'∈B' in
 *     j.dependsOn.
 *
 *   • Inner — within each batch, jobs arranged as stages, left → right.
 *     stage(j) = max(stage(d) for d ∈ j.dependsOn that's in same batch)
 *     + 1. Cross-batch deps are tracked as meta-edges, not inner edges.
 *
 * Both axes flow left → right for consistent reading direction.
 *
 * This file owns ONE Sugiyama implementation that operates on a
 * generic node/edge pair; the same function is invoked twice — once
 * for the meta-DAG (batches), once per expanded batch for the inner
 * job DAG. Same algorithm, two scales.
 *
 * ──────────────────────────────────────────────────────────────────
 * Algorithm (per founder spec):
 *   1. Layer assignment — longest-path topological sort
 *      layer(n) = max(layer(d) + 1) for d in deps(n) or 0.
 *   2. Crossing minimization — barycenter heuristic, 4 sweep rounds
 *      (down-then-up = 1 round). Converges for our scales (≤14 jobs
 *      per batch, ≤8 batches).
 *   3. Coordinate assignment — x = layer × COLUMN_WIDTH; y = position
 *      × ROW_HEIGHT (with batch-relative offsets at the outer scale).
 *   4. Edge routing — straight line for span=1; cubic bezier with 2
 *      control points at (x1 + (x2-x1)/3, y1) and (x1 + 2(x2-x1)/3,
 *      y2) for span>1. Bezier produces a smooth curve over empty
 *      stage columns (e.g. 2→5 spanning empty stage-2).
 * ──────────────────────────────────────────────────────────────────
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target-state shape) — ships the full two-level
 *      contract, not a "single-level for now" MVP.
 *   #2 (no compromise) — pure function, no graph library, deterministic
 *      output. No simulation, no random tie-breaking.
 *   #4 (never hardcode) — every dimension is a configurable option;
 *      the COLUMN_WIDTH / ROW_HEIGHT defaults below can be overridden
 *      via `opts.geometry`.
 */

import type { Job } from './jobs.types'

/* ──────────────────────────────────────────────────────────────────
 * Public types
 * ────────────────────────────────────────────────────────────────── */

/** A node in the laid-out flow canvas (job card OR collapsed batch). */
export interface FlowNode {
  /** Stable id — same as jobs.types.Job#id for jobs; batch id for batches. */
  id: string
  /** Owning batch id. For `kind: 'batch'` this equals `id`. */
  batchId: string
  /** Top-left corner X (page coords, in pixels). */
  x: number
  /** Top-left corner Y (page coords, in pixels). */
  y: number
  /** Width in pixels. */
  width: number
  /** Height in pixels. */
  height: number
  /** What this node represents. */
  kind: 'job' | 'batch'
  /** Inner stage of a job within its batch (0-indexed). Undefined for batch supernodes. */
  stage?: number
  /** Owning Job for `kind: 'job'`. Undefined for `kind: 'batch'`. */
  job?: Job
}

/** A laid-out edge (inner-batch arrow OR cross-batch meta-edge). */
export interface FlowEdge {
  /** From-node id. */
  fromId: string
  /** To-node id. */
  toId: string
  /**
   * Polyline points for the edge path. ALWAYS at least 2 points
   * (start, end). Cubic-bezier edges carry exactly 4 points
   * (start, control1, control2, end) so the SVG renderer can emit
   * `M x0 y0 C cx1 cy1, cx2 cy2, x1 y1`.
   */
  points: { x: number; y: number }[]
  /** Edge category — drives stroke style + colour. */
  kind: 'within-batch' | 'meta' | 'cross-batch-job'
  /** True when the source batch has at least one failed job (renders dashed red). */
  blocked?: boolean
  /** Hover tooltip content (e.g. for cross-batch arrows). */
  tooltip?: string
}

/** A laid-out swimlane container (batch background card). */
export interface FlowBatchLane {
  batchId: string
  x: number
  y: number
  width: number
  height: number
  metaStage: number
  /** Aggregate status — `mixed` when jobs straddle multiple buckets. */
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'mixed'
  /** finished / total across this batch's jobs. */
  finished: number
  total: number
  /** True when the batch is rendered as a single supernode. */
  collapsed: boolean
}

/** Full layout result. Width / height are the SVG canvas extents. */
export interface FlowLayoutResult {
  nodes: FlowNode[]
  edges: FlowEdge[]
  batches: FlowBatchLane[]
  width: number
  height: number
}

/** Geometry knobs — keep in lockstep with the JobsFlowView CSS. */
export interface FlowGeometry {
  /** Distance between adjacent stage columns (job inner). */
  jobColumnWidth: number
  /** Distance between adjacent rows within a batch (job inner). */
  jobRowHeight: number
  /** Job card dimensions (px). */
  jobWidth: number
  jobHeight: number
  /** Distance between adjacent meta-stages (batch outer). */
  metaColumnGap: number
  /** Distance between adjacent batch lanes vertically. */
  metaRowGap: number
  /** Inner padding between a batch's bounding box and its job grid. */
  batchPadX: number
  batchPadTop: number
  batchPadBottom: number
  /** Collapsed batch supernode height + width. */
  collapsedBatchWidth: number
  collapsedBatchHeight: number
  /** Outer canvas padding. */
  canvasPadding: number
}

export const DEFAULT_GEOMETRY: FlowGeometry = {
  jobColumnWidth: 200,
  jobRowHeight: 90,
  jobWidth: 170,
  jobHeight: 70,
  metaColumnGap: 60,
  metaRowGap: 32,
  batchPadX: 16,
  batchPadTop: 56,
  batchPadBottom: 16,
  collapsedBatchWidth: 220,
  collapsedBatchHeight: 90,
  canvasPadding: 24,
}

export interface PipelineLayoutOptions {
  /** Set of batchIds to render as collapsed supernodes (no inner job grid). */
  collapsedBatchIds?: ReadonlySet<string>
  /** Geometry overrides; sparse partial — unspecified keys fall back to defaults. */
  geometry?: Partial<FlowGeometry>
}

/* ──────────────────────────────────────────────────────────────────
 * Generic Sugiyama (used for both meta and inner scales)
 * ────────────────────────────────────────────────────────────────── */

/**
 * A directed edge in the generic Sugiyama input. We refer to nodes by
 * their id; the caller maintains the id → payload mapping.
 */
interface GenericEdge {
  from: string
  to: string
}

interface SugiyamaResult {
  /** Layer (column) of each node id. 0 = leftmost. */
  layer: Map<string, number>
  /** Within-layer position (row) of each node id. 0 = top. */
  position: Map<string, number>
  /** Total number of layers (max layer + 1). */
  layerCount: number
  /** Per-layer ordered node id arrays — useful for rendering. */
  byLayer: string[][]
}

/**
 * Run Sugiyama layout on a generic DAG.
 *
 *   1. Longest-path layer assignment.
 *   2. Barycenter crossing minimization (4 sweep rounds).
 *   3. Coordinate-friendly position assignment (compacted integer Y).
 *
 * Stable for repeated calls with identical input. Tied scores break
 * by node id (lex ascending) — never by Math.random.
 */
export function sugiyama(
  nodeIds: readonly string[],
  edges: readonly GenericEdge[],
): SugiyamaResult {
  // Filter edges to those with both endpoints in the node set —
  // tolerates external edges (e.g. inner-DAG with cross-batch deps)
  // without crashing.
  const idSet = new Set(nodeIds)
  const validEdges = edges.filter((e) => idSet.has(e.from) && idSet.has(e.to))

  // 1. Longest-path layer assignment.
  // Build deps: incoming edges per node.
  const incoming = new Map<string, string[]>()
  const outgoing = new Map<string, string[]>()
  for (const id of nodeIds) {
    incoming.set(id, [])
    outgoing.set(id, [])
  }
  for (const e of validEdges) {
    incoming.get(e.to)!.push(e.from)
    outgoing.get(e.from)!.push(e.to)
  }

  const layer = new Map<string, number>()
  // Topological order via Kahn's algorithm + memoised longest path.
  const indeg = new Map<string, number>()
  for (const id of nodeIds) indeg.set(id, incoming.get(id)!.length)
  const queue: string[] = []
  // Pull roots in deterministic id-asc order.
  for (const id of [...nodeIds].sort((a, b) => a.localeCompare(b))) {
    if (indeg.get(id) === 0) {
      queue.push(id)
      layer.set(id, 0)
    }
  }
  // Standard Kahn — but at each pop, layer(child) is the max over
  // already-discovered parents.
  let head = 0
  while (head < queue.length) {
    const id = queue[head++]!
    const myLayer = layer.get(id)!
    const outs = [...outgoing.get(id)!].sort((a, b) => a.localeCompare(b))
    for (const child of outs) {
      const childLayer = Math.max(layer.get(child) ?? 0, myLayer + 1)
      layer.set(child, childLayer)
      indeg.set(child, indeg.get(child)! - 1)
      if (indeg.get(child) === 0) queue.push(child)
    }
  }

  // Defensive — a cycle leaves nodes with no layer entry. Floor them
  // to 0 so the renderer never produces NaN coords.
  for (const id of nodeIds) {
    if (!layer.has(id)) layer.set(id, 0)
  }

  // 2. Crossing minimization — barycenter heuristic with DUMMY nodes
  //    for long edges so the standard Sugiyama "edge-vs-edge crossings
  //    only between adjacent layers" assumption holds.
  //
  //    For every edge whose endpoints span more than one layer, we
  //    insert one dummy node per intermediate layer; the dummy
  //    participates in barycenter ordering and routes the long edge
  //    through the correct y-position at each stage.
  const layerCount = Math.max(0, ...layer.values()) + 1
  const byLayer: string[][] = Array.from({ length: layerCount }, () => [])
  for (const id of nodeIds) byLayer[layer.get(id)!]!.push(id)

  // Build per-layer adjacency (with dummies). Dummies get synthetic
  // ids `__dummy:<edgeId>:<layer>` and are stored alongside real ids
  // in byLayer / incoming / outgoing for the barycenter sweep.
  const augIncoming = new Map<string, string[]>()
  const augOutgoing = new Map<string, string[]>()
  for (const id of nodeIds) {
    augIncoming.set(id, [])
    augOutgoing.set(id, [])
  }

  let dummyCounter = 0
  for (const e of validEdges) {
    const lFrom = layer.get(e.from)!
    const lTo = layer.get(e.to)!
    const span = lTo - lFrom
    if (span <= 1) {
      augIncoming.get(e.to)!.push(e.from)
      augOutgoing.get(e.from)!.push(e.to)
    } else {
      // Chain of dummies: e.from → d1 → d2 → … → e.to
      let prev = e.from
      for (let l = lFrom + 1; l < lTo; l++) {
        const dummyId = `__dummy:${dummyCounter++}:${e.from}->${e.to}@${l}`
        byLayer[l]!.push(dummyId)
        augIncoming.set(dummyId, [prev])
        augOutgoing.set(dummyId, [])
        augOutgoing.get(prev)!.push(dummyId)
        prev = dummyId
      }
      augIncoming.get(e.to)!.push(prev)
      augOutgoing.get(prev)!.push(e.to)
    }
  }

  // Initial deterministic order — id ascending. Provides the same
  // starting point on every run so the test fixture is reproducible.
  for (const lst of byLayer) lst.sort((a, b) => a.localeCompare(b))

  const augPosition = new Map<string, number>()
  const refreshPositions = () => {
    for (const lst of byLayer) {
      lst.forEach((id, i) => augPosition.set(id, i))
    }
  }
  refreshPositions()

  const barycenter = (id: string, neighbours: readonly string[]): number => {
    if (neighbours.length === 0) return augPosition.get(id) ?? 0
    let sum = 0
    for (const n of neighbours) sum += augPosition.get(n) ?? 0
    return sum / neighbours.length
  }

  // Count crossings between two adjacent layers — pure pair-wise
  // inversion count over the augmented edge set. Used to pick the
  // best ordering across sweep rounds (the barycenter heuristic
  // alone is not optimal).
  const crossingsBetween = (lUpper: number, lLower: number): number => {
    const upper = byLayer[lUpper]!
    const lower = byLayer[lLower]!
    const upperIdx = new Map<string, number>()
    upper.forEach((id, i) => upperIdx.set(id, i))
    const lowerIdx = new Map<string, number>()
    lower.forEach((id, i) => lowerIdx.set(id, i))
    // Collect edges as (upperPos, lowerPos) pairs.
    const pairs: Array<[number, number]> = []
    for (const u of upper) {
      for (const v of augOutgoing.get(u) ?? []) {
        if (!lowerIdx.has(v)) continue
        pairs.push([upperIdx.get(u)!, lowerIdx.get(v)!])
      }
    }
    // Crossings = pairs (a, b) and (c, d) with a < c && b > d, plus
    // a > c && b < d. Equivalent to inversion count by lower index
    // when sorted by upper index, with stable secondary order on
    // lower asc.
    pairs.sort((p, q) => (p[0] !== q[0] ? p[0] - q[0] : p[1] - q[1]))
    let n = 0
    for (let i = 0; i < pairs.length; i++) {
      for (let j = i + 1; j < pairs.length; j++) {
        if (pairs[i]![0] === pairs[j]![0]) continue
        if (pairs[i]![1] > pairs[j]![1]) n++
      }
    }
    return n
  }

  const totalCrossings = (): number => {
    let n = 0
    for (let l = 0; l < layerCount - 1; l++) n += crossingsBetween(l, l + 1)
    return n
  }

  // Snapshot and restore byLayer state — used to revert when a sweep
  // makes things worse.
  const snapshot = (): string[][] => byLayer.map((lst) => [...lst])
  const restore = (snap: string[][]) => {
    for (let i = 0; i < snap.length; i++) byLayer[i] = snap[i]!
  }

  let bestSnapshot = snapshot()
  let bestCrossings = totalCrossings()

  const SWEEP_ROUNDS = 32
  for (let round = 0; round < SWEEP_ROUNDS; round++) {
    // Down sweep — order each layer by barycenter of parents.
    for (let l = 1; l < layerCount; l++) {
      const lst = byLayer[l]!
      const scored = lst.map((id) => ({
        id,
        bc: barycenter(id, augIncoming.get(id)!),
      }))
      // Stable sort by barycenter; tie-break by id ascending.
      scored.sort((a, b) => {
        if (a.bc !== b.bc) return a.bc - b.bc
        return a.id.localeCompare(b.id)
      })
      byLayer[l] = scored.map((s) => s.id)
    }
    refreshPositions()
    // Up sweep — order each layer by barycenter of children.
    for (let l = layerCount - 2; l >= 0; l--) {
      const lst = byLayer[l]!
      const scored = lst.map((id) => ({
        id,
        bc: barycenter(id, augOutgoing.get(id)!),
      }))
      scored.sort((a, b) => {
        if (a.bc !== b.bc) return a.bc - b.bc
        return a.id.localeCompare(b.id)
      })
      byLayer[l] = scored.map((s) => s.id)
    }
    refreshPositions()

    const c = totalCrossings()
    if (c < bestCrossings) {
      bestCrossings = c
      bestSnapshot = snapshot()
    }
  }

  // Restore the best ordering observed across all sweep rounds — the
  // barycenter heuristic isn't monotonically improving.
  restore(bestSnapshot)
  refreshPositions()

  // Median heuristic — for any pair of adjacent layers that still has
  // crossings, try swapping adjacent same-layer nodes that share a
  // crossing and accept the swap if total crossings drop. Bounded at
  // 64 attempts so the layout stays O(N²).
  for (let pass = 0; pass < 64; pass++) {
    let improved = false
    for (let l = 0; l < layerCount; l++) {
      const lst = byLayer[l]!
      for (let i = 0; i < lst.length - 1; i++) {
        const before = totalCrossings()
        const tmp = lst[i]!
        lst[i] = lst[i + 1]!
        lst[i + 1] = tmp
        refreshPositions()
        const after = totalCrossings()
        if (after < before) {
          improved = true
        } else {
          // Revert.
          lst[i + 1] = lst[i]!
          lst[i] = tmp
          refreshPositions()
        }
      }
    }
    if (!improved) break
  }

  // 3. Final position assignment — strip dummies, keep real-node
  //    positions contiguous within each layer.
  const realByLayer: string[][] = byLayer.map((lst) =>
    lst.filter((id) => !id.startsWith('__dummy:')),
  )
  const position = new Map<string, number>()
  for (const lst of realByLayer) {
    lst.forEach((id, i) => position.set(id, i))
  }

  return { layer, position, layerCount, byLayer: realByLayer }
}

/* ──────────────────────────────────────────────────────────────────
 * Edge routing — bezier vs straight
 * ────────────────────────────────────────────────────────────────── */

/**
 * Compute the polyline / bezier control points for an edge between
 * two nodes. Span = number of stage columns spanned. For span ≤ 1 we
 * emit a 2-point straight line; for span ≥ 2 we emit a cubic-bezier
 * with 2 control points so the curve clears intermediate empty
 * columns smoothly.
 *
 * Anchor convention: edges leave the right-edge midpoint of the
 * source and enter the left-edge midpoint of the target.
 */
export function routeEdge(
  fromBox: { x: number; y: number; width: number; height: number },
  toBox: { x: number; y: number; width: number; height: number },
  span: number,
): { x: number; y: number }[] {
  const x0 = fromBox.x + fromBox.width
  const y0 = fromBox.y + fromBox.height / 2
  const x1 = toBox.x
  const y1 = toBox.y + toBox.height / 2
  // Span ≤ 1 → straight line. Span ≥ 2 → cubic bezier with 2
  // control points so the curve clears intermediate empty columns
  // smoothly. We do NOT collapse to a straight line when y0 === y1
  // for span ≥ 2 because the founder spec requires a visibly smooth
  // arc over the empty columns even when source + target sit on
  // the same row (otherwise long horizontal edges would zigzag
  // through fan-out targets in adjacent stages).
  if (span <= 1) {
    return [
      { x: x0, y: y0 },
      { x: x1, y: y1 },
    ]
  }
  const dx = x1 - x0
  // When y0 === y1 the bezier collapses to a horizontal line — the
  // operator wouldn't notice the curve. Add a small vertical wiggle
  // to the control points so the path always reads as a curve.
  const wiggle = y0 === y1 ? Math.max(8, Math.abs(dx) * 0.04) : 0
  return [
    { x: x0, y: y0 },
    { x: x0 + dx / 3, y: y0 - wiggle },
    { x: x0 + (dx * 2) / 3, y: y1 + wiggle },
    { x: x1, y: y1 },
  ]
}

/* ──────────────────────────────────────────────────────────────────
 * Public layout function — two-level (batches + jobs)
 * ────────────────────────────────────────────────────────────────── */

/**
 * Compute the full Flow-tab layout from a flat job list.
 *
 * Steps:
 *   1. Group jobs by batchId. Compute the meta-DAG (B depends on B'
 *      iff there's any cross-batch job dep).
 *   2. Sugiyama-layout the meta-DAG → batch supernode positions.
 *   3. For each expanded batch, Sugiyama-layout its inner-batch DAG
 *      to position jobs within the lane.
 *   4. For each collapsed batch, render as a single supernode with
 *      summary geometry.
 *   5. Route every edge (within-batch + cross-batch + meta).
 *
 * Pure function — same input + opts produces the same output.
 */
export function pipelineLayout(
  jobs: readonly Job[],
  opts: PipelineLayoutOptions = {},
): FlowLayoutResult {
  const collapsed = opts.collapsedBatchIds ?? new Set<string>()
  const geom: FlowGeometry = { ...DEFAULT_GEOMETRY, ...opts.geometry }

  if (jobs.length === 0) {
    return {
      nodes: [],
      edges: [],
      batches: [],
      width: geom.canvasPadding * 2 + 100,
      height: geom.canvasPadding * 2 + 100,
    }
  }

  // 1. Group jobs by batch. Preserve discovery order so that, when
  //    the meta-DAG has no cross-batch edges, the meta-Sugiyama
  //    position tie-breaker (id ascending) yields a stable layout.
  const jobById = new Map<string, Job>()
  for (const j of jobs) jobById.set(j.id, j)

  const batchIds: string[] = []
  const jobsByBatch = new Map<string, Job[]>()
  for (const j of jobs) {
    if (!jobsByBatch.has(j.batchId)) {
      batchIds.push(j.batchId)
      jobsByBatch.set(j.batchId, [])
    }
    jobsByBatch.get(j.batchId)!.push(j)
  }

  // 2. Build the meta-DAG.
  const metaEdgesSet = new Set<string>()
  const metaEdgesList: GenericEdge[] = []
  // Track which (sourceJob, targetJob) pair drove each meta-edge for
  // the cross-batch tooltip. Multiple driving pairs are joined by ", ".
  const metaEdgeDrivers = new Map<string, Array<{ from: string; to: string }>>()
  for (const j of jobs) {
    for (const dep of j.dependsOn) {
      const depJob = jobById.get(dep)
      if (!depJob) continue
      if (depJob.batchId === j.batchId) continue
      const k = `${depJob.batchId}→${j.batchId}`
      if (!metaEdgesSet.has(k)) {
        metaEdgesSet.add(k)
        metaEdgesList.push({ from: depJob.batchId, to: j.batchId })
      }
      const drivers = metaEdgeDrivers.get(k) ?? []
      drivers.push({ from: dep, to: j.id })
      metaEdgeDrivers.set(k, drivers)
    }
  }

  const metaSugi = sugiyama(batchIds, metaEdgesList)

  // 3. Lay out each batch's inner DAG. We need the batch's content
  //    bounding box to size the swimlane.
  interface InnerLayout {
    /** Inner job nodes positioned RELATIVE to the batch's content origin. */
    relNodes: Map<string, { x: number; y: number; width: number; height: number; stage: number }>
    /** Inner edges. Coordinates ALSO relative to the batch content origin. */
    innerEdges: Array<{ fromId: string; toId: string; span: number }>
    /** Content width (max relative right edge). */
    contentWidth: number
    /** Content height (max relative bottom edge). */
    contentHeight: number
  }

  const innerLayouts = new Map<string, InnerLayout>()

  for (const batchId of batchIds) {
    const batchJobs = jobsByBatch.get(batchId)!
    if (collapsed.has(batchId)) {
      // Collapsed batch: skip inner layout entirely.
      continue
    }
    const ids = batchJobs.map((j) => j.id)
    const inner: GenericEdge[] = []
    for (const j of batchJobs) {
      for (const dep of j.dependsOn) {
        const depJob = jobById.get(dep)
        if (!depJob) continue
        if (depJob.batchId !== batchId) continue
        inner.push({ from: dep, to: j.id })
      }
    }
    const innerSugi = sugiyama(ids, inner)
    const relNodes = new Map<
      string,
      { x: number; y: number; width: number; height: number; stage: number }
    >()
    let maxRight = 0
    let maxBottom = 0
    for (const id of ids) {
      const lay = innerSugi.layer.get(id) ?? 0
      const pos = innerSugi.position.get(id) ?? 0
      const x = lay * geom.jobColumnWidth
      const y = pos * geom.jobRowHeight
      relNodes.set(id, {
        x,
        y,
        width: geom.jobWidth,
        height: geom.jobHeight,
        stage: lay,
      })
      maxRight = Math.max(maxRight, x + geom.jobWidth)
      maxBottom = Math.max(maxBottom, y + geom.jobHeight)
    }

    const innerEdges = inner.map((e) => {
      const fromLayer = innerSugi.layer.get(e.from) ?? 0
      const toLayer = innerSugi.layer.get(e.to) ?? 0
      return { fromId: e.from, toId: e.to, span: toLayer - fromLayer }
    })

    innerLayouts.set(batchId, {
      relNodes,
      innerEdges,
      contentWidth: maxRight,
      contentHeight: maxBottom,
    })
  }

  // 4. Compute per-batch outer geometry — width/height per batch lane,
  //    then per-meta-stage column width = max lane width, per-row Y
  //    = sum of preceding lane heights (in same meta-stage).

  interface BatchOuter {
    batchId: string
    metaStage: number
    metaPos: number
    /** Width of the batch lane (collapsed: fixed; expanded: derived from inner content). */
    width: number
    /** Height of the batch lane. */
    height: number
  }

  const outerBoxes = new Map<string, BatchOuter>()
  for (const batchId of batchIds) {
    const inner = innerLayouts.get(batchId)
    let width: number
    let height: number
    if (collapsed.has(batchId) || !inner) {
      width = geom.collapsedBatchWidth
      height = geom.collapsedBatchHeight
    } else {
      width = inner.contentWidth + geom.batchPadX * 2
      height = inner.contentHeight + geom.batchPadTop + geom.batchPadBottom
    }
    outerBoxes.set(batchId, {
      batchId,
      metaStage: metaSugi.layer.get(batchId) ?? 0,
      metaPos: metaSugi.position.get(batchId) ?? 0,
      width,
      height,
    })
  }

  // Per-meta-column max width.
  const colWidth: number[] = Array.from({ length: metaSugi.layerCount }, () => 0)
  for (const ob of outerBoxes.values()) {
    if (ob.width > colWidth[ob.metaStage]!) colWidth[ob.metaStage] = ob.width
  }

  // Compute X offsets for each meta-column.
  const colX: number[] = Array.from({ length: metaSugi.layerCount }, () => 0)
  for (let i = 0; i < metaSugi.layerCount; i++) {
    if (i === 0) {
      colX[i] = geom.canvasPadding
    } else {
      colX[i] = (colX[i - 1] ?? 0) + (colWidth[i - 1] ?? 0) + geom.metaColumnGap
    }
  }

  // Within each meta-column, walk the column entries in `metaPos` order
  // and stack them vertically. Lane heights vary, so we accumulate Y.
  const placedBatches: FlowBatchLane[] = []
  for (let l = 0; l < metaSugi.layerCount; l++) {
    const colIds = metaSugi.byLayer[l]!
    let cursorY = geom.canvasPadding
    for (const id of colIds) {
      const ob = outerBoxes.get(id)!
      const inner = innerLayouts.get(id)
      const isCollapsed = collapsed.has(id) || !inner
      const lane: FlowBatchLane = {
        batchId: id,
        x: colX[l]!,
        y: cursorY,
        width: ob.width,
        height: ob.height,
        metaStage: l,
        status: aggregateStatus(jobsByBatch.get(id)!),
        finished: jobsByBatch.get(id)!.filter((j) => j.status === 'succeeded' || j.status === 'failed').length,
        total: jobsByBatch.get(id)!.length,
        collapsed: isCollapsed,
      }
      placedBatches.push(lane)
      cursorY += ob.height + geom.metaRowGap
    }
  }

  // Compute total canvas extents.
  let canvasW = 0
  let canvasH = 0
  for (const b of placedBatches) {
    canvasW = Math.max(canvasW, b.x + b.width)
    canvasH = Math.max(canvasH, b.y + b.height)
  }
  canvasW += geom.canvasPadding
  canvasH += geom.canvasPadding

  // Index placed lanes for the edge-routing phase.
  const laneById = new Map<string, FlowBatchLane>()
  for (const b of placedBatches) laneById.set(b.batchId, b)

  // 5. Emit nodes — jobs (expanded batches) and batch supernodes
  //    (collapsed batches).
  const nodes: FlowNode[] = []
  const jobNodeAbs = new Map<string, FlowNode>()

  for (const b of placedBatches) {
    if (b.collapsed) {
      const supernode: FlowNode = {
        id: b.batchId,
        batchId: b.batchId,
        x: b.x,
        y: b.y,
        width: b.width,
        height: b.height,
        kind: 'batch',
      }
      nodes.push(supernode)
      jobNodeAbs.set(b.batchId, supernode)
    } else {
      const inner = innerLayouts.get(b.batchId)!
      const contentX = b.x + geom.batchPadX
      const contentY = b.y + geom.batchPadTop
      for (const j of jobsByBatch.get(b.batchId)!) {
        const rel = inner.relNodes.get(j.id)!
        const absNode: FlowNode = {
          id: j.id,
          batchId: b.batchId,
          x: contentX + rel.x,
          y: contentY + rel.y,
          width: rel.width,
          height: rel.height,
          kind: 'job',
          stage: rel.stage,
          job: j,
        }
        nodes.push(absNode)
        jobNodeAbs.set(j.id, absNode)
      }
    }
  }

  // 6. Emit edges.
  const edges: FlowEdge[] = []

  // 6a. Within-batch edges (only for expanded batches).
  for (const [batchId, inner] of innerLayouts.entries()) {
    const lane = laneById.get(batchId)
    if (!lane || lane.collapsed) continue
    for (const e of inner.innerEdges) {
      const from = jobNodeAbs.get(e.fromId)
      const to = jobNodeAbs.get(e.toId)
      if (!from || !to) continue
      edges.push({
        fromId: e.fromId,
        toId: e.toId,
        points: routeEdge(from, to, e.span),
        kind: 'within-batch',
      })
    }
  }

  // 6b. Cross-batch edges (job-level when both batches expanded;
  //     meta when either side is collapsed).
  for (const e of metaEdgesList) {
    const fromLane = laneById.get(e.from)!
    const toLane = laneById.get(e.to)!
    const drivers = metaEdgeDrivers.get(`${e.from}→${e.to}`) ?? []
    const blocked = jobsByBatch.get(e.from)!.some((j) => j.status === 'failed')
    const tooltip =
      drivers.length > 0
        ? `via ${drivers.map((d) => `${d.from} → ${d.to}`).join(', ')}`
        : undefined

    if (fromLane.collapsed || toLane.collapsed) {
      // Render as a meta arrow lane→lane.
      edges.push({
        fromId: e.from,
        toId: e.to,
        points: routeEdge(
          { x: fromLane.x, y: fromLane.y, width: fromLane.width, height: fromLane.height },
          { x: toLane.x, y: toLane.y, width: toLane.width, height: toLane.height },
          Math.max(1, toLane.metaStage - fromLane.metaStage),
        ),
        kind: 'meta',
        blocked,
        tooltip,
      })
    } else {
      // Both expanded — render a job→job arrow per driver.
      for (const d of drivers) {
        const from = jobNodeAbs.get(d.from)
        const to = jobNodeAbs.get(d.to)
        if (!from || !to) continue
        edges.push({
          fromId: d.from,
          toId: d.to,
          points: routeEdge(
            from,
            to,
            // Meta-edges always span ≥ 1 meta-column → use a bezier so
            // the curve clears lane boundaries cleanly.
            2,
          ),
          kind: 'cross-batch-job',
          blocked,
          tooltip,
        })
      }
    }
  }

  return {
    nodes,
    edges,
    batches: placedBatches,
    width: canvasW,
    height: canvasH,
  }
}

/* ──────────────────────────────────────────────────────────────────
 * Helpers
 * ────────────────────────────────────────────────────────────────── */

/**
 * Roll up a batch's job statuses into a single bucket for the lane
 * tint. Single-bucket batches return that bucket; mixed batches
 * return `mixed` so the renderer can paint the amber background.
 */
export function aggregateStatus(
  jobs: readonly Job[],
): 'pending' | 'running' | 'succeeded' | 'failed' | 'mixed' {
  if (jobs.length === 0) return 'pending'
  const buckets = new Set(jobs.map((j) => j.status))
  if (buckets.size === 1) {
    return [...buckets][0] as 'pending' | 'running' | 'succeeded' | 'failed'
  }
  if (buckets.has('failed')) {
    // Any failure — surface it. UI will pick this over mixed/running so
    // the operator's eye is drawn to the failed lane first.
    return buckets.has('running') || buckets.has('pending') ? 'mixed' : 'failed'
  }
  return 'mixed'
}

/**
 * Default-collapse policy: every batch where ALL jobs are in a
 * terminal-success state is collapsed; the rest stay expanded.
 *
 * Per founder spec: "batches with ≥1 running/pending job → expanded.
 * All-succeeded batches → collapsed."
 */
export function defaultCollapsedBatchIds(jobs: readonly Job[]): Set<string> {
  const byBatch = new Map<string, Job[]>()
  for (const j of jobs) {
    const arr = byBatch.get(j.batchId) ?? []
    arr.push(j)
    byBatch.set(j.batchId, arr)
  }
  const out = new Set<string>()
  for (const [batchId, arr] of byBatch.entries()) {
    if (arr.length > 0 && arr.every((j) => j.status === 'succeeded')) {
      out.add(batchId)
    }
  }
  return out
}

/**
 * Render a polyline / bezier edge as an SVG path "d" attribute.
 *
 *   • 2-point segments → `M x0 y0 L x1 y1`
 *   • 4-point bezier   → `M x0 y0 C cx1 cy1, cx2 cy2, x1 y1`
 *   • 3-point fallback → straight L through the middle point
 *
 * Pure helper, exported so tests can lock the contract and the
 * JobsFlowView doesn't have to repeat the if-ladder inline.
 */
export function edgeToPath(points: readonly { x: number; y: number }[]): string {
  if (points.length < 2) return ''
  const [p0, ...rest] = points
  const head = `M ${p0!.x} ${p0!.y}`
  if (points.length === 4) {
    const [, c1, c2, p1] = points
    return `${head} C ${c1!.x} ${c1!.y}, ${c2!.x} ${c2!.y}, ${p1!.x} ${p1!.y}`
  }
  return `${head}${rest.map((p) => ` L ${p.x} ${p.y}`).join('')}`
}
