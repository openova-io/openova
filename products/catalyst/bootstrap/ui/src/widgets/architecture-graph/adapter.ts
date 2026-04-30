/**
 * adapter.ts — turns the hierarchical infrastructure tree
 * (HierarchicalInfrastructure) into the neutral GraphNode/GraphEdge
 * shape consumed by GraphCanvas.
 *
 * Per founder spec: containment is just one of several edge types.
 * The adapter emits:
 *   • `contains`   — Cloud→Region, Region→Cluster, Cluster→vCluster
 *                    (the founder verbatim said "show it as another
 *                     type of relation" — so it stays, but rendered
 *                     identically to the others)
 *   • `runs-on`    — Cluster ←runs-on— NodePool / WorkerNode
 *   • `routes-to`  — LoadBalancer→Cluster
 *   • `attached-to`— Network→Region (dashed)
 *   • `peers-with` — Network↔Network (peering edges, dashed)
 *   • `depends-on` — reserved for future cross-tree dependencies
 *
 * Composite ids: ${type}:${elementId} so a Region with id "eu-central"
 * becomes "Region:eu-central" — no collision with cluster ids that
 * might happen to share an integer suffix.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — the type/edge palette lives in types.ts and
 *      this file owns ONLY the shape transform.
 */

import type {
  HierarchicalInfrastructure,
  ClusterSpec,
  RegionSpec,
} from '@/lib/infrastructure.types'
import type { GraphEdge, GraphNode } from './types'

export interface AdaptResult {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

function compositeId(type: string, id: string): string {
  return `${type}:${id}`
}

export function hierarchyToGraph(tree: HierarchicalInfrastructure | null): AdaptResult {
  const nodes: GraphNode[] = []
  const edges: GraphEdge[] = []
  if (!tree) return { nodes, edges }

  // 1. Cloud anchors.
  for (const c of tree.cloud) {
    const id = compositeId('Cloud', c.id)
    nodes.push({
      id,
      type: 'Cloud',
      label: c.name || c.provider,
      sublabel: `${c.provider}`,
      status: 'healthy',
      metadata: {
        provider: c.provider,
        regions: String(c.regionCount),
        quota: `${c.quotaUsed}/${c.quotaLimit}`,
      },
    })
  }

  // 2. Regions, then their clusters / vclusters / pools / nodes / lbs / networks.
  for (const region of tree.topology.regions) {
    addRegion(region, nodes, edges, tree)
  }

  // 3. Network peering edges — networks across regions.
  // We collect peering ids once after all networks have been emitted.
  const networkIds = new Set(
    tree.topology.regions.flatMap((r) => r.networks ?? []).map((n) => compositeId('Network', n.id)),
  )
  for (const region of tree.topology.regions) {
    for (const net of region.networks ?? []) {
      for (const peer of net.peerings ?? []) {
        // Best-effort: the peer's vpcPair string holds "from → to".
        // We don't have a structured peer-id, so skip cross-network edges
        // that don't resolve cleanly. If both ends exist in our
        // network set, draw a peers-with edge.
        const parts = peer.vpcPair?.split(/→|->/).map((s) => s.trim()) ?? []
        if (parts.length === 2) {
          const a = compositeId('Network', parts[0]!)
          const b = compositeId('Network', parts[1]!)
          if (networkIds.has(a) && networkIds.has(b) && a !== b) {
            edges.push({
              id: `peer:${peer.id}`,
              source: a,
              target: b,
              type: 'peers-with',
            })
          }
        }
      }
    }
  }

  return { nodes, edges }
}

function addRegion(
  region: RegionSpec,
  nodes: GraphNode[],
  edges: GraphEdge[],
  tree: HierarchicalInfrastructure,
): void {
  const regionId = compositeId('Region', region.id)
  nodes.push({
    id: regionId,
    type: 'Region',
    label: region.name || region.providerRegion,
    sublabel: `${region.provider} · ${region.providerRegion}`,
    status: region.status,
    metadata: {
      provider: region.provider,
      providerRegion: region.providerRegion,
      skuCp: region.skuCp,
      skuWorker: region.skuWorker,
      workers: String(region.workerCount),
    },
  })

  // Cloud → Region.
  const cloudMatch = tree.cloud.find((c) => c.provider === region.provider)
  if (cloudMatch) {
    edges.push({
      id: `e:${compositeId('Cloud', cloudMatch.id)}->${regionId}`,
      source: compositeId('Cloud', cloudMatch.id),
      target: regionId,
      type: 'contains',
    })
  }

  // Networks under the region (attached-to, dashed).
  for (const net of region.networks ?? []) {
    const netId = compositeId('Network', net.id)
    nodes.push({
      id: netId,
      type: 'Network',
      label: `vpc-${net.id.slice(0, 6)}`,
      sublabel: net.cidr,
      status: 'healthy',
      metadata: {
        cidr: net.cidr,
        region: net.region,
      },
    })
    edges.push({
      id: `e:${netId}->${regionId}`,
      source: netId,
      target: regionId,
      type: 'attached-to',
    })
  }

  for (const cluster of region.clusters ?? []) {
    addCluster(cluster, region, nodes, edges)
  }
}

function addCluster(
  cluster: ClusterSpec,
  region: RegionSpec,
  nodes: GraphNode[],
  edges: GraphEdge[],
): void {
  const regionId = compositeId('Region', region.id)
  const clusterId = compositeId('Cluster', cluster.id)
  nodes.push({
    id: clusterId,
    type: 'Cluster',
    label: cluster.name,
    sublabel: cluster.version,
    status: cluster.status,
    metadata: {
      version: cluster.version,
      nodes: String(cluster.nodeCount),
      vclusters: String(cluster.vclusters.length),
    },
  })
  edges.push({
    id: `e:${regionId}->${clusterId}`,
    source: regionId,
    target: clusterId,
    type: 'contains',
  })

  // vClusters.
  for (const vc of cluster.vclusters) {
    const vcId = compositeId('vCluster', vc.id)
    nodes.push({
      id: vcId,
      type: 'vCluster',
      label: vc.name,
      sublabel: vc.isolationMode,
      status: vc.status,
      metadata: { isolationMode: vc.isolationMode },
    })
    edges.push({
      id: `e:${clusterId}->${vcId}`,
      source: clusterId,
      target: vcId,
      type: 'contains',
    })
  }

  // Node pools.
  for (const pool of cluster.nodePools) {
    const pId = compositeId('NodePool', pool.id)
    nodes.push({
      id: pId,
      type: 'NodePool',
      label: pool.id,
      sublabel: `${pool.sku} ×${pool.replicas}`,
      status: pool.status,
      metadata: { sku: pool.sku, replicas: String(pool.replicas) },
    })
    edges.push({
      id: `e:${pId}->${clusterId}`,
      source: pId,
      target: clusterId,
      type: 'runs-on',
    })
  }

  // Worker nodes.
  for (const node of cluster.nodes) {
    const nId = compositeId('WorkerNode', node.id)
    nodes.push({
      id: nId,
      type: 'WorkerNode',
      label: node.name,
      sublabel: `${node.sku} · ${node.role}`,
      status: node.status,
      metadata: { sku: node.sku, role: node.role, ip: node.ip },
    })
    edges.push({
      id: `e:${nId}->${clusterId}`,
      source: nId,
      target: clusterId,
      type: 'runs-on',
    })
  }

  // Load balancers.
  for (const lb of cluster.loadBalancers) {
    const lbId = compositeId('LoadBalancer', lb.id)
    nodes.push({
      id: lbId,
      type: 'LoadBalancer',
      label: lb.name,
      sublabel: lb.publicIP,
      status: lb.status,
      metadata: {
        publicIP: lb.publicIP,
        listeners: lb.listeners.map((l) => `${l.port}/${l.protocol}`).join(','),
      },
    })
    edges.push({
      id: `e:${lbId}->${clusterId}`,
      source: lbId,
      target: clusterId,
      type: 'routes-to',
    })
  }
}
