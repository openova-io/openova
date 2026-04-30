/**
 * types.ts — wire types for the force-directed Architecture graph
 * widget (P2 of issue openova-io/openova#309).
 *
 * The graph widget is data-shape-agnostic: a higher-level adapter turns
 * the hierarchical infrastructure tree into these neutral
 * GraphNode / GraphEdge shapes, and the GraphCanvas only knows about
 * those.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target shape) — these types are the FINAL contract
 *      between the page-level orchestrator and the canvas.
 *   #4 (never hardcode) — node radius, color and edge colour are all
 *      derived from the type field.
 */

/**
 * Architecture node types — drawn from the hierarchical infrastructure
 * tree. Region / Cluster / vCluster come straight from the spec; the
 * other shapes (LoadBalancer, NodePool, WorkerNode, Network) surface
 * the leaves so the operator can see the whole picture in one canvas.
 */
export type ArchNodeType =
  | 'Cloud'
  | 'Region'
  | 'Cluster'
  | 'vCluster'
  | 'NodePool'
  | 'WorkerNode'
  | 'LoadBalancer'
  | 'Network'

/**
 * Edge relationship types. Containment is just one of these — the
 * founder spec verbatim: "forget about the containment, just show it
 * as another type of relation."
 */
export type ArchEdgeType =
  | 'contains'
  | 'runs-on'
  | 'routes-to'
  | 'attached-to'
  | 'depends-on'
  | 'peers-with'

export type ArchStatus = 'healthy' | 'degraded' | 'failed' | 'unknown'

/** A node on the graph canvas — composite id, type-tagged, with status. */
export interface GraphNode {
  /** Composite id: `${type}:${elementId}`. Stable across renders. */
  id: string
  type: ArchNodeType
  label: string
  /** Optional one-line subtext (e.g. SKU, IP). */
  sublabel?: string
  status?: ArchStatus
  /** Free-form per-type metadata shown in the detail panel. */
  metadata?: Record<string, string>
}

export interface GraphEdge {
  id: string
  source: string
  target: string
  type: ArchEdgeType
}

/* ── Canvas runtime shapes ───────────────────────────────────────── */

/**
 * Internal D3-force-augmented node (mutable x/y/fx/fy). The canvas
 * keeps these in a Map keyed by id so positions persist across
 * prop-driven re-renders. Exported because the page tests inspect
 * the same shape.
 */
export interface LiveNode extends GraphNode {
  x: number
  y: number
  vx: number
  vy: number
  /** Pinned x — set by drag-to-pin. null = unpinned (D3 will move it). */
  fx: number | null
  fy: number | null
  /** Cached degree (incoming + outgoing edges). */
  degree: number
}

/**
 * Internal edge — D3-force mutates `source` / `target` from string ids
 * to LiveNode references after the first simulation tick. Use the
 * `edgeNodeId()` helper everywhere you read these fields. We omit
 * source/target from the GraphEdge structural extension because their
 * runtime shape is `string | LiveNode` (string only on the very first
 * tick; node ref afterward).
 */
export interface LiveEdge extends Omit<GraphEdge, 'source' | 'target'> {
  source: string | LiveNode
  target: string | LiveNode
}

/**
 * D3-force-link mutates link.source / link.target from their initial
 * string id values to node-object references after the first tick. Any
 * code that reads either field MUST go through this helper to support
 * both shapes.
 *
 * Critical: the canonical bug pattern (and the reason this helper exists
 * as a single export) is `link.source === id` — that comparison is true
 * pre-tick and false post-tick. Always read via `edgeNodeId(link.source)`.
 */
export function edgeNodeId(v: string | { id: string }): string {
  return typeof v === 'object' ? v.id : v
}

/* ── Visual mapping ──────────────────────────────────────────────── */

/**
 * Per-type color palette. Each type is visually distinct on the
 * canvas. Per INVIOLABLE-PRINCIPLES #4 (never hardcode visible-only
 * tokens): these map onto the project's CSS variables where one
 * exists, and fall back to literal hex for the type-distinctive
 * accents the palette lacks.
 */
export const NODE_FILL: Record<ArchNodeType, string> = {
  Cloud: '#7048e8', // violet — provider tenant anchor
  Region: '#1c7ed6', // blue
  Cluster: '#0ca678', // teal — control-plane group
  vCluster: '#37b24d', // green — isolation scope
  NodePool: '#f59f00', // amber
  WorkerNode: '#fab005', // yellow
  LoadBalancer: '#e8590c', // orange
  Network: '#868e96', // grey
}

export const EDGE_STROKE: Record<ArchEdgeType, string> = {
  contains: '#4c6ef5', // solid blue
  'runs-on': '#15aabf', // solid cyan
  'routes-to': '#fa5252', // solid red
  'attached-to': '#868e96', // dashed grey
  'depends-on': '#fd7e14', // solid orange
  'peers-with': '#7950f2', // dashed violet
}

/** Edges that render dashed instead of solid. */
export const EDGE_DASHED: Record<ArchEdgeType, boolean> = {
  contains: false,
  'runs-on': false,
  'routes-to': false,
  'attached-to': true,
  'depends-on': false,
  'peers-with': true,
}
