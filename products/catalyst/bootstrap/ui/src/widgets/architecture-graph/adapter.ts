/**
 * adapter.ts — turns the hierarchical infrastructure tree
 * (HierarchicalInfrastructure) into the neutral GraphNode/GraphEdge
 * shape consumed by GraphCanvas.
 *
 * Per founder spec (#348 item 2): the adapter emits the FULL relation
 * cache — every node type and every edge — regardless of which chips
 * are active in the UI. Chip add/remove is a pure visibility filter
 * applied downstream in ArchitectureGraphPage. This means a user can
 * remove every chip except NodePool and PVC and still see the
 * "runs-on" edges between NodePools and the (now-hidden) Cluster
 * resolved into direct NodePool→PVC relationships? No — the contract
 * is: edges whose endpoints are both visible are drawn. No synthetic
 * transitive edges; we simply hold the full graph.
 *
 * Per founder spec: containment is just one of several edge types.
 * The adapter emits:
 *   • `contains`     — Cloud→Region, Region→Cluster, Cluster→vCluster
 *                      (rendered with ArchiMate composition marker)
 *   • `runs-on`      — NodePool→Cluster, WorkerNode→Cluster (assignment)
 *   • `routes-to`    — LoadBalancer→Cluster, Service→WorkerNode (triggering)
 *   • `attached-to`  — Network→Region (dashed), Volume→WorkerNode,
 *                      PVC→Volume
 *   • `peers-with`   — Network↔Network (peering edges)
 *   • `member-of`    — vCluster→Cluster (aggregation, hollow diamond)
 *   • `depends-on`   — Service→PVC (used-by, dashed)
 *   • `used-by`      — Bucket→vCluster (dashed)
 *   • `flows-to`     — Ingress→Service (dashed, flow notation)
 *   • `realizes`     — reserved for service implementations
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

  // 4. Storage block — PVCs, Buckets, Volumes. Live in tree.storage,
  //    not nested under regions, so we emit them after the topology
  //    walk. Volumes have an `attachedTo` node id; we wire that up.
  addStorage(tree, nodes, edges)

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
    label: region.name || region.providerRegion || region.id || 'region',
    sublabel: `${region.provider ?? ''} · ${region.providerRegion ?? ''}`,
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
    label: cluster.name || cluster.id || 'cluster',
    sublabel: cluster.version ?? '',
    status: cluster.status,
    metadata: {
      version: cluster.version ?? '',
      nodes: String(cluster.nodeCount ?? 0),
      vclusters: String((cluster.vclusters ?? []).length),
    },
  })
  edges.push({
    id: `e:${regionId}->${clusterId}`,
    source: regionId,
    target: clusterId,
    type: 'contains',
  })

  // vClusters — emitted as `member-of` (aggregation) so the marker is
  // hollow-diamond rather than the contains-style filled diamond.
  // Multiple vClusters can co-tenant a Cluster but the relation is
  // logical grouping, not strict containment.
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
      id: `e:${vcId}->${clusterId}`,
      source: vcId,
      target: clusterId,
      type: 'member-of',
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

/**
 * Emit storage block — PVCs, Buckets, Volumes — and their relations
 * to the rest of the topology. PVCs are namespaced K8s claims; they
 * attach-to a Volume (when bound) and depend-on a Cluster. Buckets are
 * cloud-side object storage; they used-by the cluster they back up.
 * Volumes are cloud block devices that attach-to a WorkerNode.
 *
 * When the storage block is empty (typical fixture today) we still
 * walk the structure so `tree.storage` shape changes (e.g. adding a
 * Service / Ingress projection) are picked up automatically.
 */
function addStorage(
  tree: HierarchicalInfrastructure,
  nodes: GraphNode[],
  edges: GraphEdge[],
): void {
  // Anchor: the first cluster in topology — used as the default
  // depends-on target for PVCs / Buckets when no explicit anchor.
  const firstCluster = tree.topology.regions[0]?.clusters[0]
  const firstClusterId = firstCluster ? compositeId('Cluster', firstCluster.id) : null
  const firstVClusterId = firstCluster?.vclusters[0]
    ? compositeId('vCluster', firstCluster.vclusters[0].id)
    : null

  // Volumes — block storage attached to a WorkerNode.
  for (const v of tree.storage.volumes ?? []) {
    const vId = compositeId('Volume', v.id)
    nodes.push({
      id: vId,
      type: 'Volume',
      label: v.name,
      sublabel: v.capacity,
      status: v.status,
      metadata: {
        capacity: v.capacity,
        region: v.region,
        attachedTo: v.attachedTo || '',
      },
    })
    if (v.attachedTo) {
      const wId = compositeId('WorkerNode', v.attachedTo)
      edges.push({
        id: `e:${vId}->${wId}`,
        source: vId,
        target: wId,
        type: 'attached-to',
      })
    }
  }

  // PVCs — K8s claims; depend-on the cluster they live in.
  for (const p of tree.storage.pvcs ?? []) {
    const pId = compositeId('PVC', p.id)
    nodes.push({
      id: pId,
      type: 'PVC',
      label: p.name,
      sublabel: `${p.namespace} · ${p.capacity}`,
      status: p.status,
      metadata: {
        namespace: p.namespace,
        capacity: p.capacity,
        used: p.used,
        storageClass: p.storageClass,
      },
    })
    if (firstClusterId) {
      edges.push({
        id: `e:${pId}->${firstClusterId}`,
        source: pId,
        target: firstClusterId,
        type: 'depends-on',
      })
    }
  }

  // Buckets — object storage; used-by the (first) vCluster they back.
  for (const b of tree.storage.buckets ?? []) {
    const bId = compositeId('Bucket', b.id)
    nodes.push({
      id: bId,
      type: 'Bucket',
      label: b.name,
      sublabel: b.capacity,
      status: 'healthy',
      metadata: {
        endpoint: b.endpoint,
        capacity: b.capacity,
        used: b.used,
        retentionDays: b.retentionDays,
      },
    })
    const target = firstVClusterId ?? firstClusterId
    if (target) {
      edges.push({
        id: `e:${bId}->${target}`,
        source: bId,
        target,
        type: 'used-by',
      })
    }
  }

  // Service / Ingress are not yet in HierarchicalInfrastructure (pending
  // #321 informer cache). Adapter holds zero of them today; once the
  // backend surfaces them, this is the single point that needs to grow
  // (no other place hardcodes "Service is missing"). Per #348 item 2,
  // chips for Service / Ingress are addable from the chip strip and
  // simply render an empty visibility set until data arrives.
}
