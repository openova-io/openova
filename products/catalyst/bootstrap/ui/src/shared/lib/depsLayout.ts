/**
 * depsLayout.ts — pure topological-layered DAG layout.
 *
 * Given a flat list of jobs with `id` + `dependsOn[]`, computes per-node
 * (x, y) coordinates and per-edge poly-line points suitable for an SVG
 * `<line>` / `<polyline>` render.
 *
 * RATIONALE (per docs/INVIOLABLE-PRINCIPLES.md):
 *   • #2 (never compromise quality) — we don't pull in reactflow /
 *     cytoscape / d3-dag for a 30-node chart. A deterministic layered
 *     layout is ~120 lines, has zero runtime deps, and stays mockable
 *     under jsdom/vitest with no `useRef` ceremony.
 *   • #4 (never hardcode) — every spacing / padding value is exposed as
 *     an option. Defaults match the wizard's existing card-grid padding
 *     so the graph matches the visual rhythm of the Job-detail panel.
 *
 * Algorithm (Kahn's topo sort + longest-path layering):
 *   1. Build adjacency from `dependsOn`. Edges that point at unknown
 *      ids (foreign-key into jobs not in this slice) are dropped — the
 *      caller decides whether to filter dangling deps upstream.
 *   2. Compute layer(node) = 1 + max(layer(p) for p in predecessors), 0
 *      if the node has no predecessors. Cycles are detected and broken
 *      by ignoring the back-edge (the layout still renders; the cycle
 *      is reported via the returned `cycles` array so the UI can
 *      surface a warning).
 *   3. Within each layer, sort nodes by (depCount desc, id asc) so
 *      heavily-depended-on nodes float to the top of their column —
 *      this minimises edge crossings without a full barycentric
 *      heuristic.
 *   4. Emit nodes at (layer * colWidth + paddingX,
 *                      indexInLayer * rowHeight + paddingY).
 *   5. Emit edges as a 4-point poly-line:
 *        from-right → mid-x (right of source) → mid-x (left of dest) →
 *        dest-left. This produces an orthogonal "step" routing that
 *        renders cleanly with `stroke-linejoin: miter` in SVG.
 */

export interface LayoutInput {
  /** Stable id — must be unique within the input array. */
  id: string
  /** IDs this node depends on. Unknown IDs are silently ignored. */
  dependsOn: readonly string[]
}

export interface LayoutNode {
  id: string
  x: number
  y: number
  /** 0-indexed layer (column). Surfaced so renderers can colour columns. */
  layer: number
  /** Index within the layer (0-indexed top-to-bottom). */
  indexInLayer: number
}

export interface LayoutPoint {
  x: number
  y: number
}

export interface LayoutEdge {
  from: string
  to: string
  /** Polyline points: 4 entries — exit, midRight, midLeft, entry. */
  points: LayoutPoint[]
}

export interface LayoutResult {
  nodes: LayoutNode[]
  edges: LayoutEdge[]
  /** Cycles broken during layering — array of (from, to) tuples. */
  cycles: Array<{ from: string; to: string }>
  /** Width / height of the bounding box, including padding. */
  width: number
  height: number
  /** Number of layers (max + 1). */
  layerCount: number
}

export interface LayoutOptions {
  /** Distance between layer columns. Default: 220. */
  colWidth?: number
  /** Distance between rows within a layer. Default: 90. */
  rowHeight?: number
  /** Horizontal padding around the whole graph. Default: 32. */
  paddingX?: number
  /** Vertical padding around the whole graph. Default: 32. */
  paddingY?: number
  /** Width of a node's bounding box (used to anchor edges). Default: 180. */
  nodeWidth?: number
  /** Height of a node's bounding box (used to anchor edges). Default: 56. */
  nodeHeight?: number
}

const DEFAULTS: Required<LayoutOptions> = {
  colWidth: 220,
  rowHeight: 90,
  paddingX: 32,
  paddingY: 32,
  nodeWidth: 180,
  nodeHeight: 56,
}

/**
 * Compute the layered layout for a graph defined by `LayoutInput[]`.
 * Pure function — same input always returns the same output. Safe to
 * memoize on input identity from React render code.
 */
export function depsLayout(
  input: readonly LayoutInput[],
  opts: LayoutOptions = {},
): LayoutResult {
  const o = { ...DEFAULTS, ...opts }

  // Index by id for O(1) edge resolution.
  const byId = new Map<string, LayoutInput>()
  for (const n of input) byId.set(n.id, n)

  // Build forward adjacency (id -> ids that depend on it) so we can do
  // topological-order traversal. Unknown ids in `dependsOn` are dropped.
  const incoming = new Map<string, string[]>() // node -> known parents
  const outgoing = new Map<string, string[]>() // node -> children
  for (const n of input) {
    incoming.set(n.id, [])
    outgoing.set(n.id, [])
  }
  for (const n of input) {
    for (const dep of n.dependsOn) {
      if (!byId.has(dep)) continue
      incoming.get(n.id)!.push(dep)
      outgoing.get(dep)!.push(n.id)
    }
  }

  // Kahn's topological sort with cycle-breaking. We track in-degree on
  // copies of the incoming map so the layering pass can iterate in
  // topological order. If we run out of zero-in-degree nodes before
  // exhausting the input, we have a cycle: pick the lowest-id remaining
  // node, break ALL of its incoming edges, record them in `cycles`,
  // and continue.
  const inDeg = new Map<string, number>()
  for (const n of input) inDeg.set(n.id, incoming.get(n.id)!.length)

  const order: string[] = []
  const cycles: Array<{ from: string; to: string }> = []
  const ready: string[] = []
  for (const [id, d] of inDeg) {
    if (d === 0) ready.push(id)
  }
  ready.sort()

  while (order.length < input.length) {
    if (ready.length === 0) {
      // Cycle break: smallest remaining id.
      const remaining = input
        .map((n) => n.id)
        .filter((id) => !order.includes(id))
        .sort()
      const victim = remaining[0]!
      // Sever ALL incoming edges to `victim`.
      for (const parent of incoming.get(victim) ?? []) {
        cycles.push({ from: parent, to: victim })
      }
      incoming.set(victim, [])
      inDeg.set(victim, 0)
      ready.push(victim)
    }
    const next = ready.shift()!
    order.push(next)
    for (const child of outgoing.get(next) ?? []) {
      const d = (inDeg.get(child) ?? 0) - 1
      inDeg.set(child, d)
      if (d === 0) {
        ready.push(child)
        ready.sort()
      }
    }
  }

  // Layer assignment via longest-path from any source.
  const layer = new Map<string, number>()
  for (const id of order) {
    const parents = incoming.get(id) ?? []
    if (parents.length === 0) {
      layer.set(id, 0)
    } else {
      let max = 0
      for (const p of parents) {
        const lp = layer.get(p) ?? 0
        if (lp + 1 > max) max = lp + 1
      }
      layer.set(id, max)
    }
  }

  // Group by layer + sort within layer by (descendant-count desc, id asc).
  const descendantCount = new Map<string, number>()
  for (const id of [...order].reverse()) {
    let count = 0
    for (const child of outgoing.get(id) ?? []) {
      count += 1 + (descendantCount.get(child) ?? 0)
    }
    descendantCount.set(id, count)
  }

  const layers: string[][] = []
  for (const id of input.map((n) => n.id)) {
    const l = layer.get(id) ?? 0
    while (layers.length <= l) layers.push([])
    layers[l]!.push(id)
  }
  for (const l of layers) {
    l.sort((a, b) => {
      const da = descendantCount.get(a) ?? 0
      const db = descendantCount.get(b) ?? 0
      if (da !== db) return db - da
      return a < b ? -1 : a > b ? 1 : 0
    })
  }

  // Emit nodes.
  const nodes: LayoutNode[] = []
  const nodeXY = new Map<string, { x: number; y: number }>()
  let maxRow = 0
  for (let l = 0; l < layers.length; l++) {
    const col = layers[l]!
    for (let i = 0; i < col.length; i++) {
      const id = col[i]!
      const x = o.paddingX + l * o.colWidth
      const y = o.paddingY + i * o.rowHeight
      nodes.push({ id, x, y, layer: l, indexInLayer: i })
      nodeXY.set(id, { x, y })
      if (i > maxRow) maxRow = i
    }
  }

  // Emit edges as 4-point orthogonal poly-lines. Each node is anchored
  // at its visual centre-right (source) and centre-left (target).
  const edges: LayoutEdge[] = []
  for (const n of input) {
    for (const dep of n.dependsOn) {
      if (!byId.has(dep)) continue
      // If this edge was severed by cycle-breaking, skip it visually
      // (the cycle is reported via `cycles` for an out-of-band warning).
      if (cycles.some((c) => c.from === dep && c.to === n.id)) continue
      const src = nodeXY.get(dep)
      const dst = nodeXY.get(n.id)
      if (!src || !dst) continue
      const sx = src.x + o.nodeWidth
      const sy = src.y + o.nodeHeight / 2
      const dx = dst.x
      const dy = dst.y + o.nodeHeight / 2
      const midX = sx + (dx - sx) / 2
      edges.push({
        from: dep,
        to: n.id,
        points: [
          { x: sx, y: sy },
          { x: midX, y: sy },
          { x: midX, y: dy },
          { x: dx, y: dy },
        ],
      })
    }
  }

  const layerCount = layers.length
  const width =
    layerCount === 0
      ? o.paddingX * 2
      : o.paddingX * 2 + (layerCount - 1) * o.colWidth + o.nodeWidth
  const height = o.paddingY * 2 + maxRow * o.rowHeight + o.nodeHeight

  return { nodes, edges, cycles, width, height, layerCount }
}
