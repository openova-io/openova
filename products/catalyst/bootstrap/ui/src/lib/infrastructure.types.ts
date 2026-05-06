/**
 * infrastructure.types.ts — wire types for the Sovereign Infrastructure
 * surface (issue #227). The Topology canvas + Compute / Storage /
 * Network tabs all consume these shapes.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target shape) — every type below is the FINAL shape.
 *      Backend returns a well-shaped empty response when the live
 *      cluster query isn't implemented yet; the UI handles empty
 *      gracefully (the canvas renders with a "Provisioning…" overlay
 *      rather than placeholder data).
 *   #4 (never hardcode) — every URL is derived from API_BASE; every
 *      colour comes from the canonical status palette in the renderer.
 */

import { API_BASE } from '@/shared/config/urls'

/* ── Topology ──────────────────────────────────────────────────── */

/**
 * NodeKind enumerates every shape that can appear on the topology
 * canvas. Mirrored verbatim by the backend's Go enum.
 *
 *   cloud   — provider account anchor (e.g. "Hetzner — eu-central")
 *   region  — cloud region grouping
 *   cluster — k3s control-plane group
 *   node    — worker / control-plane VM
 *   lb      — load balancer
 *   pvc     — Persistent Volume Claim
 *   volume  — cloud block volume (Hetzner Cloud volume etc.)
 *   network — VPC / subnet / DRG / peering edge anchor
 */
export type TopologyNodeKind =
  | 'cloud'
  | 'region'
  | 'cluster'
  | 'node'
  | 'lb'
  | 'pvc'
  | 'volume'
  | 'network'

export type TopologyStatus = 'healthy' | 'degraded' | 'failed' | 'unknown'

export interface TopologyNode {
  id: string
  kind: TopologyNodeKind
  label: string
  status: TopologyStatus
  /** Free-form key/value strings shown in the detail panel. */
  metadata: Record<string, string>
}

export type TopologyRelation = 'contains' | 'attached-to' | 'depends-on'

export interface TopologyEdge {
  from: string
  to: string
  relation: TopologyRelation
}

export interface TopologyResponse {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
}

/* ── Hierarchical topology (issue #228) ───────────────────────────
 *
 * The new /infrastructure/topology contract delivers ONE hierarchical
 * tree (cloud → region → cluster → vcluster | node | lb | network)
 * that powers all four tabs (Topology / Compute / Storage / Network).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target shape) —
 * this is the FINAL shape. Backend returns a well-shaped empty
 * response when the live cluster query isn't implemented yet.
 *
 * Founder-locked spec verbatim:
 *   Org/Sovereign
 *     └─ Topology pattern (SOLO | HA-PAIR | MULTI-REGION | AIR-GAP add-on)
 *         └─ Region(s)
 *             ├─ Provider tenant
 *             ├─ SKU
 *             ├─ Worker count
 *             └─ Physical Cluster(s)
 *                 ├─ vClusters [DMZ · RTZ · MGMT]
 *                 ├─ LBs / peerings / firewalls
 *                 ├─ PVCs / buckets / volumes
 *                 └─ Worker nodes
 */

export type TopologyPattern = 'solo' | 'ha-pair' | 'multi-region' | 'air-gap'

export type IsolationMode = 'dmz' | 'rtz' | 'mgmt'

export interface VClusterSpec {
  id: string
  name: string
  isolationMode: IsolationMode
  status: TopologyStatus
}

export interface NodePoolSpec {
  id: string
  sku: string
  replicas: number
  status: TopologyStatus
}

export interface NodeSpec {
  id: string
  name: string
  sku: string
  role: string
  ip: string
  status: TopologyStatus
}

export interface LBListener {
  port: number
  protocol: string
}

export interface LBTarget {
  id: string
  ip: string
  status: TopologyStatus
}

export interface LoadBalancerSpec {
  id: string
  name: string
  publicIP: string
  listeners: LBListener[]
  targets: LBTarget[]
  region: string
  status: TopologyStatus
}

export interface FirewallRule {
  id: string
  protocol: string
  port: string
  source: string
  action: 'allow' | 'deny'
}

export interface FirewallSpec {
  id: string
  name: string
  rules: FirewallRule[]
  status: TopologyStatus
}

export interface PeeringSpec {
  id: string
  name: string
  vpcPair: string
  subnets: string
  status: TopologyStatus
}

export interface NetworkSpec {
  id: string
  cidr: string
  region: string
  peerings: PeeringSpec[]
  firewalls: FirewallSpec[]
}

export interface ClusterSpec {
  id: string
  name: string
  version: string
  status: TopologyStatus
  nodeCount: number
  vclusters: VClusterSpec[]
  loadBalancers: LoadBalancerSpec[]
  nodePools: NodePoolSpec[]
  nodes: NodeSpec[]
}

export interface RegionSpec {
  id: string
  name: string
  provider: string
  providerRegion: string
  skuCp: string
  skuWorker: string
  workerCount: number
  status: TopologyStatus
  clusters: ClusterSpec[]
  networks: NetworkSpec[]
}

export interface CloudSpec {
  id: string
  name: string
  provider: string
  regionCount: number
  quotaUsed: number
  quotaLimit: number
}

export interface TopologyTree {
  pattern: TopologyPattern
  regions: RegionSpec[]
}

export interface InfrastructureStorageBlock {
  pvcs: PVCItem[]
  buckets: BucketItem[]
  volumes: VolumeItem[]
}

export interface HierarchicalInfrastructure {
  cloud: CloudSpec[]
  topology: TopologyTree
  storage: InfrastructureStorageBlock
}

export interface DeploymentSummary {
  id: string
  sovereignFQDN: string
  status: string
}

/* ── Compute ───────────────────────────────────────────────────── */

export interface ClusterItem {
  id: string
  name: string
  /** k3s / k8s / etc. */
  controlPlane: string
  version: string
  region: string
  /** Node count including control plane. */
  nodeCount: number
  status: TopologyStatus
}

export interface NodeItem {
  id: string
  name: string
  /** Provider SKU string — "cx32", "cpx41", etc. */
  sku: string
  region: string
  /** "control-plane" | "worker" — kept open-string for future roles. */
  role: string
  /** Public or VPC IP, whichever the cluster uses for kubectl. */
  ip: string
  status: TopologyStatus
}

export interface ComputeResponse {
  clusters: ClusterItem[]
  nodes: NodeItem[]
}

/* ── Storage ──────────────────────────────────────────────────── */

export interface PVCItem {
  id: string
  name: string
  namespace: string
  /** "10Gi" / "500Mi" — Kubernetes capacity string verbatim. */
  capacity: string
  /** Used capacity, same units. Empty when metrics-server isn't on. */
  used: string
  storageClass: string
  status: TopologyStatus
}

export interface BucketItem {
  id: string
  name: string
  /** SeaweedFS S3 endpoint or provider-specific bucket FQDN. */
  endpoint: string
  /** Allocated quota string (e.g. "100Gi"). */
  capacity: string
  /** Used capacity string. */
  used: string
  /** Retention policy in days, or empty for "indefinite". */
  retentionDays: string
}

export interface VolumeItem {
  id: string
  name: string
  /** Hetzner Cloud volume size in GB, e.g. "50Gi". */
  capacity: string
  region: string
  /** Node id this volume is attached to, or empty when detached. */
  attachedTo: string
  status: TopologyStatus
}

export interface StorageResponse {
  pvcs: PVCItem[]
  buckets: BucketItem[]
  volumes: VolumeItem[]
}

/* ── Network ──────────────────────────────────────────────────── */

export interface LoadBalancerItem {
  id: string
  name: string
  /** Public IPv4 (or v6) the LB listens on. */
  publicIP: string
  /** Comma-separated listener ports — "80,443,6443". */
  ports: string
  /** "n/m healthy" or "—" when unknown. */
  targetHealth: string
  region: string
  status: TopologyStatus
}

export interface DRGItem {
  id: string
  name: string
  /** "10.0.0.0/16" etc. */
  cidr: string
  region: string
  /** Comma-separated FQDN/id list of peered DRGs/VPCs. */
  peers: string
  status: TopologyStatus
}

export interface PeeringItem {
  id: string
  name: string
  /** "vpc-a -> vpc-b" — direction is informational, peering is bidirectional. */
  vpcPair: string
  /** Comma-separated subnet CIDRs covered by the peering. */
  subnets: string
  status: TopologyStatus
}

export interface NetworkResponse {
  loadBalancers: LoadBalancerItem[]
  drgs: DRGItem[]
  peerings: PeeringItem[]
}

/* ── Fetchers ─────────────────────────────────────────────────── */

/**
 * Fetch the topology graph for a deployment. Throws on non-2xx so
 * React Query surfaces the error via `query.isError`.
 */
export async function getTopology(deploymentId: string): Promise<TopologyResponse> {
  const res = await fetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/infrastructure/topology`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`topology fetch failed: ${res.status}`)
  }
  return (await res.json()) as TopologyResponse
}

export async function getCompute(deploymentId: string): Promise<ComputeResponse> {
  const res = await fetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/infrastructure/compute`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`compute fetch failed: ${res.status}`)
  }
  return (await res.json()) as ComputeResponse
}

export async function getStorage(deploymentId: string): Promise<StorageResponse> {
  const res = await fetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/infrastructure/storage`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`storage fetch failed: ${res.status}`)
  }
  return (await res.json()) as StorageResponse
}

export async function getNetwork(deploymentId: string): Promise<NetworkResponse> {
  const res = await fetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/infrastructure/network`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`network fetch failed: ${res.status}`)
  }
  return (await res.json()) as NetworkResponse
}

/**
 * Fetch the hierarchical infrastructure tree for a deployment. Single
 * round-trip, all four tabs render filtered views off the same response.
 */
export async function getHierarchicalInfrastructure(
  deploymentId: string,
): Promise<HierarchicalInfrastructure> {
  const res = await fetch(
    `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/infrastructure/topology`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`topology fetch failed: ${res.status}`)
  }
  return (await res.json()) as HierarchicalInfrastructure
}

/** List all known deployments — feeds the per-Sovereign header switcher. */
export async function listDeployments(): Promise<DeploymentSummary[]> {
  const res = await fetch(`${API_BASE}/v1/deployments`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`deployments fetch failed: ${res.status}`)
  }
  const body = (await res.json()) as { deployments?: DeploymentSummary[] } | DeploymentSummary[]
  if (Array.isArray(body)) return body
  return body.deployments ?? []
}

/* ── Tree → flat-table projections ─────────────────────────────── */

export function flattenCompute(tree: HierarchicalInfrastructure): ComputeResponse {
  const clusters: ClusterItem[] = []
  const nodes: NodeItem[] = []
  for (const region of tree.topology.regions) {
    for (const cluster of region.clusters) {
      clusters.push({
        id: cluster.id,
        name: cluster.name,
        controlPlane: 'k3s',
        version: cluster.version,
        region: region.providerRegion,
        nodeCount: cluster.nodeCount,
        status: cluster.status,
      })
      for (const node of cluster.nodes) {
        nodes.push({
          id: node.id,
          name: node.name,
          sku: node.sku,
          region: region.providerRegion,
          role: node.role,
          ip: node.ip,
          status: node.status,
        })
      }
    }
  }
  return { clusters, nodes }
}

export function flattenStorage(tree: HierarchicalInfrastructure): StorageResponse {
  return {
    pvcs: tree.storage.pvcs,
    buckets: tree.storage.buckets,
    volumes: tree.storage.volumes,
  }
}

export function flattenNetwork(tree: HierarchicalInfrastructure): NetworkResponse {
  const loadBalancers: LoadBalancerItem[] = []
  const drgs: DRGItem[] = []
  const peerings: PeeringItem[] = []
  for (const region of tree.topology.regions) {
    for (const cluster of region.clusters) {
      for (const lb of cluster.loadBalancers) {
        loadBalancers.push({
          id: lb.id,
          name: lb.name,
          publicIP: lb.publicIP,
          ports: lb.listeners.map((l) => String(l.port)).join(','),
          targetHealth:
            lb.targets.length === 0
              ? '—'
              : `${lb.targets.filter((t) => t.status === 'healthy').length}/${lb.targets.length} healthy`,
          region: lb.region || region.providerRegion,
          status: lb.status,
        })
      }
    }
    for (const network of region.networks) {
      drgs.push({
        id: network.id,
        name: `vpc-${network.id.slice(0, 6)}`,
        cidr: network.cidr,
        region: network.region,
        peers: network.peerings.map((p) => p.name).join(','),
        status: 'healthy',
      })
      for (const p of network.peerings) {
        peerings.push({
          id: p.id,
          name: p.name,
          vpcPair: p.vpcPair,
          subnets: p.subnets,
          status: p.status,
        })
      }
    }
  }
  return { loadBalancers, drgs, peerings }
}

/* ── Topology layout ──────────────────────────────────────────── */

/**
 * Compute a layered topology layout adapted for the heterogeneous node
 * set (the dependency-graph layout in @/shared/lib/depsLayout assumes
 * a homogeneous DAG of jobs; the topology canvas instead groups by
 * NodeKind so cloud > region > cluster > node reads top-down).
 *
 * The layout is pure (same input = same output) so React's `useMemo`
 * is the right caching primitive. Per INVIOLABLE-PRINCIPLES #2 we own
 * a thin layout function rather than dragging in `reactflow` for a
 * <50-node graph.
 */
export interface LaidOutNode {
  id: string
  x: number
  y: number
  layer: number
  indexInLayer: number
}

export interface LaidOutEdge {
  from: string
  to: string
  /** 4-point orthogonal poly-line. */
  points: { x: number; y: number }[]
}

export interface LaidOutGraph {
  nodes: LaidOutNode[]
  edges: LaidOutEdge[]
  width: number
  height: number
}

export interface LayoutOptions {
  colWidth?: number
  rowHeight?: number
  paddingX?: number
  paddingY?: number
  nodeWidth?: number
  nodeHeight?: number
}

const KIND_TO_LAYER: Record<TopologyNodeKind, number> = {
  cloud: 0,
  region: 1,
  cluster: 2,
  node: 3,
  lb: 3,
  pvc: 4,
  volume: 4,
  network: 4,
}

const DEFAULTS = {
  colWidth: 240,
  rowHeight: 90,
  paddingX: 32,
  paddingY: 32,
  nodeWidth: 200,
  nodeHeight: 64,
}

/**
 * Layered layout keyed off NodeKind. Layer assignment is deterministic
 * so the canvas always reads cloud → region → cluster → node | lb →
 * pvc | volume | network.
 */
export function topologyLayout(
  nodes: readonly TopologyNode[],
  edges: readonly TopologyEdge[],
  opts: LayoutOptions = {},
): LaidOutGraph {
  const o = { ...DEFAULTS, ...opts }

  // Bucket nodes by layer.
  const layerBuckets: string[][] = []
  const nodeLayer = new Map<string, number>()
  for (const n of nodes) {
    const l = KIND_TO_LAYER[n.kind] ?? 0
    nodeLayer.set(n.id, l)
    while (layerBuckets.length <= l) layerBuckets.push([])
    layerBuckets[l]!.push(n.id)
  }

  // Within each layer, sort by id so the layout is deterministic.
  for (const b of layerBuckets) b.sort()

  const nodeXY = new Map<string, { x: number; y: number }>()
  const laidOut: LaidOutNode[] = []
  let maxRow = 0
  for (let l = 0; l < layerBuckets.length; l++) {
    const col = layerBuckets[l]!
    for (let i = 0; i < col.length; i++) {
      const id = col[i]!
      const x = o.paddingX + l * o.colWidth
      const y = o.paddingY + i * o.rowHeight
      nodeXY.set(id, { x, y })
      laidOut.push({ id, x, y, layer: l, indexInLayer: i })
      if (i > maxRow) maxRow = i
    }
  }

  // Emit edges. We treat every edge as an orthogonal poly-line from
  // src.right → midX → midX → dst.left. Edges between nodes at the
  // same layer route through a vertical mid-band so they don't run
  // through the node rectangles.
  const laidOutEdges: LaidOutEdge[] = []
  for (const e of edges) {
    const src = nodeXY.get(e.from)
    const dst = nodeXY.get(e.to)
    if (!src || !dst) continue
    const sx = src.x + o.nodeWidth
    const sy = src.y + o.nodeHeight / 2
    const dx = dst.x
    const dy = dst.y + o.nodeHeight / 2
    const midX = sx + (dx - sx) / 2
    laidOutEdges.push({
      from: e.from,
      to: e.to,
      points: [
        { x: sx, y: sy },
        { x: midX, y: sy },
        { x: midX, y: dy },
        { x: dx, y: dy },
      ],
    })
  }

  const layerCount = layerBuckets.length
  const width =
    layerCount === 0
      ? o.paddingX * 2
      : o.paddingX * 2 + (layerCount - 1) * o.colWidth + o.nodeWidth
  const height = o.paddingY * 2 + maxRow * o.rowHeight + o.nodeHeight

  return { nodes: laidOut, edges: laidOutEdges, width, height }
}
