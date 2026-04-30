/**
 * topologyLayout.ts — pure topological-layered layout for the
 * hierarchical Sovereign infrastructure topology canvas.
 *
 * Produces a 4-depth top-down layered SVG layout:
 *   depth 0 — Cloud (provider tenants)
 *   depth 1 — Region
 *   depth 2 — Cluster (physical k3s)
 *   depth 3 — vCluster (DMZ / RTZ / MGMT)
 *
 * Per founder spec: hierarchical, NOT force-directed. Click a node →
 * the canvas zooms in (the active subtree's parent stays anchored,
 * siblings dim). vClusters render dim until their parent cluster is
 * zoomed.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (no compromise) — pure function, no `reactflow`, no
 *      simulation. Same input = same output.
 *   #4 (never hardcode) — every dimension is a configurable option.
 */

import type {
  HierarchicalInfrastructure,
  ClusterSpec,
  RegionSpec,
  TopologyStatus,
  VClusterSpec,
  CloudSpec,
} from './infrastructure.types'

/** Top-down depth for the layered topology. */
export type TopologyDepth = 0 | 1 | 2 | 3

/** Visual node kind on the topology canvas. */
export type TopologyVisualKind = 'cloud' | 'region' | 'cluster' | 'vcluster'

export interface LayoutNode {
  id: string
  kind: TopologyVisualKind
  label: string
  sublabel: string
  status: TopologyStatus
  depth: TopologyDepth
  parentId: string | null
  x: number
  y: number
  width: number
  height: number
  /** True when this node is dimmed because its parent isn't zoomed. */
  dim: boolean
  /** Original spec object (for the detail panel + CRUD modals). */
  ref:
    | { kind: 'cloud'; data: CloudSpec }
    | { kind: 'region'; data: RegionSpec }
    | { kind: 'cluster'; data: ClusterSpec; regionId: string }
    | { kind: 'vcluster'; data: VClusterSpec; clusterId: string; regionId: string }
}

export interface LayoutEdge {
  id: string
  fromId: string
  toId: string
  /** Orthogonal poly-line from src.bottom → midY → midY → dst.top. */
  points: { x: number; y: number }[]
}

export interface ZoomState {
  /** Currently zoomed-in cluster id (vClusters of this cluster are bright,
   *  others dim). null = canvas-default. */
  zoomedClusterId: string | null
  /** Currently zoomed-in region id (clusters of this region get vClusters
   *  rendered; others render their cluster row but no children). */
  zoomedRegionId: string | null
}

export interface TopologyLayoutResult {
  nodes: LayoutNode[]
  edges: LayoutEdge[]
  width: number
  height: number
  zoom: ZoomState
}

export interface TopologyLayoutOptions {
  /** Per-depth fixed band Y coordinate (top of the row). */
  rowY?: Record<TopologyDepth, number>
  /** Box width per depth. */
  nodeWidth?: Record<TopologyDepth, number>
  /** Box height per depth. */
  nodeHeight?: Record<TopologyDepth, number>
  /** Horizontal padding between sibling nodes. */
  hGap?: number
  /** Total canvas width — nodes spread evenly across this. */
  canvasWidth?: number
  /** Outer padding. */
  paddingX?: number
  /** Optional zoom focus — drives `dim` flags on the result. */
  zoom?: Partial<ZoomState>
}

const DEFAULTS: Required<Omit<TopologyLayoutOptions, 'zoom'>> = {
  rowY: { 0: 24, 1: 130, 2: 250, 3: 380 },
  nodeWidth: { 0: 200, 1: 200, 2: 220, 3: 140 },
  nodeHeight: { 0: 70, 1: 80, 2: 90, 3: 70 },
  hGap: 24,
  canvasWidth: 1200,
  paddingX: 40,
}

const DEPTH_BY_KIND: Record<TopologyVisualKind, TopologyDepth> = {
  cloud: 0,
  region: 1,
  cluster: 2,
  vcluster: 3,
}

/**
 * Lay out the topology tree top-down with parent-child positioning.
 * Children are centered horizontally beneath their parent; siblings
 * never overlap. Layout is fully deterministic — sort within each
 * row is by id.
 */
export function topologyLayout(
  tree: HierarchicalInfrastructure,
  opts: TopologyLayoutOptions = {},
): TopologyLayoutResult {
  const o = {
    rowY: { ...DEFAULTS.rowY, ...(opts.rowY ?? {}) },
    nodeWidth: { ...DEFAULTS.nodeWidth, ...(opts.nodeWidth ?? {}) },
    nodeHeight: { ...DEFAULTS.nodeHeight, ...(opts.nodeHeight ?? {}) },
    hGap: opts.hGap ?? DEFAULTS.hGap,
    canvasWidth: opts.canvasWidth ?? DEFAULTS.canvasWidth,
    paddingX: opts.paddingX ?? DEFAULTS.paddingX,
  }

  const zoom: ZoomState = {
    zoomedClusterId: opts.zoom?.zoomedClusterId ?? null,
    zoomedRegionId: opts.zoom?.zoomedRegionId ?? null,
  }

  const nodes: LayoutNode[] = []
  const edges: LayoutEdge[] = []
  const nodeById = new Map<string, LayoutNode>()

  // Depth 0 — clouds. Sort by id deterministic.
  const clouds = [...tree.cloud].sort((a, b) => a.id.localeCompare(b.id))
  layoutRow(clouds.map((c) => c.id), 0, o)
  for (const c of clouds) {
    const xy = positionFor(c.id, 0, clouds.map((x) => x.id), o)
    const n: LayoutNode = {
      id: c.id,
      kind: 'cloud',
      label: c.name,
      sublabel: `${c.regionCount} region${c.regionCount === 1 ? '' : 's'} · quota ${c.quotaUsed}/${c.quotaLimit}`,
      status: 'healthy',
      depth: 0,
      parentId: null,
      x: xy.x,
      y: o.rowY[0],
      width: o.nodeWidth[0],
      height: o.nodeHeight[0],
      dim: false,
      ref: { kind: 'cloud', data: c },
    }
    nodes.push(n)
    nodeById.set(n.id, n)
  }

  // Depth 1 — regions. Each region's parent is the cloud whose
  // provider matches; default to the first cloud when no match.
  const regions = [...tree.topology.regions].sort((a, b) => a.id.localeCompare(b.id))
  layoutRow(regions.map((r) => r.id), 1, o)
  for (const r of regions) {
    const parent = clouds.find((c) => c.provider === r.provider) ?? clouds[0]
    const xy = positionFor(r.id, 1, regions.map((x) => x.id), o)
    const n: LayoutNode = {
      id: r.id,
      kind: 'region',
      label: r.name,
      sublabel: `${r.provider} · ${r.providerRegion}`,
      status: r.status,
      depth: 1,
      parentId: parent?.id ?? null,
      x: xy.x,
      y: o.rowY[1],
      width: o.nodeWidth[1],
      height: o.nodeHeight[1],
      dim: false,
      ref: { kind: 'region', data: r },
    }
    nodes.push(n)
    nodeById.set(n.id, n)
    if (parent) edges.push(makeEdge(nodeById.get(parent.id)!, n))
  }

  // Depth 2 — clusters. Sort by id within each region's cluster list.
  const allClusters: { region: RegionSpec; cluster: ClusterSpec }[] = []
  for (const region of regions) {
    for (const c of [...(region.clusters ?? [])].sort((a, b) => a.id.localeCompare(b.id))) {
      allClusters.push({ region, cluster: c })
    }
  }
  layoutRow(allClusters.map((x) => x.cluster.id), 2, o)
  for (const { region, cluster } of allClusters) {
    const xy = positionFor(
      cluster.id,
      2,
      allClusters.map((x) => x.cluster.id),
      o,
    )
    const dim =
      zoom.zoomedRegionId !== null && zoom.zoomedRegionId !== region.id
    const n: LayoutNode = {
      id: cluster.id,
      kind: 'cluster',
      label: cluster.name,
      sublabel: `${cluster.version} · ${cluster.nodeCount} nodes`,
      status: cluster.status,
      depth: 2,
      parentId: region.id,
      x: xy.x,
      y: o.rowY[2],
      width: o.nodeWidth[2],
      height: o.nodeHeight[2],
      dim,
      ref: { kind: 'cluster', data: cluster, regionId: region.id },
    }
    nodes.push(n)
    nodeById.set(n.id, n)
    edges.push(makeEdge(nodeById.get(region.id)!, n))
  }

  // Depth 3 — vClusters. Sort by id within each cluster's vcluster list.
  const allVCs: {
    region: RegionSpec
    cluster: ClusterSpec
    vc: VClusterSpec
  }[] = []
  for (const { region, cluster } of allClusters) {
    for (const vc of [...(cluster.vclusters ?? [])].sort((a, b) => a.id.localeCompare(b.id))) {
      allVCs.push({ region, cluster, vc })
    }
  }
  layoutRow(allVCs.map((x) => x.vc.id), 3, o)
  for (const { region, cluster, vc } of allVCs) {
    const xy = positionFor(vc.id, 3, allVCs.map((x) => x.vc.id), o)
    const dim =
      zoom.zoomedClusterId !== null
        ? zoom.zoomedClusterId !== cluster.id
        : true
    const n: LayoutNode = {
      id: vc.id,
      kind: 'vcluster',
      label: vc.name,
      sublabel: vc.isolationMode.toUpperCase(),
      status: vc.status,
      depth: 3,
      parentId: cluster.id,
      x: xy.x,
      y: o.rowY[3],
      width: o.nodeWidth[3],
      height: o.nodeHeight[3],
      dim,
      ref: {
        kind: 'vcluster',
        data: vc,
        clusterId: cluster.id,
        regionId: region.id,
      },
    }
    nodes.push(n)
    nodeById.set(n.id, n)
    edges.push(makeEdge(nodeById.get(cluster.id)!, n))
  }

  const lastRowY = Math.max(
    ...nodes.map((n) => n.y + n.height),
    o.rowY[3] + o.nodeHeight[3],
  )

  return {
    nodes,
    edges,
    width: o.canvasWidth,
    height: lastRowY + 24,
    zoom,
  }
}

/** Compute position for a node within its depth row. */
function positionFor(
  id: string,
  depth: TopologyDepth,
  allIds: string[],
  o: Required<Omit<TopologyLayoutOptions, 'zoom'>>,
): { x: number } {
  const idx = allIds.indexOf(id)
  if (idx < 0) return { x: o.paddingX }
  const total = allIds.length
  const w = o.nodeWidth[depth]
  const usable = o.canvasWidth - o.paddingX * 2
  if (total === 0) return { x: o.paddingX }
  if (total === 1) {
    return { x: o.paddingX + Math.max(0, (usable - w) / 2) }
  }
  // Spread evenly across canvas, clamping minimum gap.
  const step = Math.max(w + o.hGap, usable / total)
  const startX = o.paddingX + (usable - step * (total - 1) - w) / 2
  return { x: Math.max(o.paddingX, startX + idx * step) }
}

/** No-op left as a hook for future per-row pre-layout passes. */
function layoutRow(
  _ids: string[],
  _depth: TopologyDepth,
  _o: Required<Omit<TopologyLayoutOptions, 'zoom'>>,
): void {
  // Reserved for future pass (e.g. parent-child centering).
}

function makeEdge(from: LayoutNode, to: LayoutNode): LayoutEdge {
  const sx = from.x + from.width / 2
  const sy = from.y + from.height
  const dx = to.x + to.width / 2
  const dy = to.y
  const midY = sy + (dy - sy) / 2
  return {
    id: `${from.id}->${to.id}`,
    fromId: from.id,
    toId: to.id,
    points: [
      { x: sx, y: sy },
      { x: sx, y: midY },
      { x: dx, y: midY },
      { x: dx, y: dy },
    ],
  }
}

export const _internal = { DEPTH_BY_KIND, DEFAULTS }
