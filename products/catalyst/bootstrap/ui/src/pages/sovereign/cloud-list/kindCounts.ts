/**
 * kindCounts.ts — the per-kind count map behind the Cloud list chip
 * badges.
 *
 * Extracted verbatim out of CloudPage's `kindCounts` useMemo (#5611) so
 * the derivation is testable as PRODUCTION code. A guard that renders
 * CloudKindChips with a hand-built `counts` prop proves nothing about
 * what the operator sees — it asserts on a value production never
 * produced. CloudPage now calls this function, so a test that calls it
 * exercises the same path.
 *
 * Semantics (unchanged):
 *   • `null`  → data unavailable; the chip renders the "—" marker.
 *   • `0`     → genuinely empty (stream connected, nothing of this kind).
 *   • Topology-projected kinds seed from the topology snapshot.
 *   • Every kind in KIND_TO_REGISTRY is overridden from the live SSE
 *     snapshot once initialState has arrived.
 */

import type { K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { KIND_IDS, KIND_TO_REGISTRY, type CloudListKind } from './kinds'

export function deriveKindCounts(
  data: HierarchicalInfrastructure | undefined | null,
  snapshot: K8sSnapshot | null | undefined,
): Record<CloudListKind, number | null> {
  const c = {} as Record<CloudListKind, number | null>
  for (const id of KIND_IDS) {
    // K8s-backed kinds start at null (until the stream connects);
    // topology-projected kinds start at 0 (the snapshot is sync).
    c[id] = id in KIND_TO_REGISTRY ? null : 0
  }
  if (data) {
    let clusters = 0
    let vclusters = 0
    let nodePools = 0
    let workerNodes = 0
    let lb = 0
    for (const region of data.topology.regions ?? []) {
      for (const cluster of region.clusters ?? []) {
        clusters += 1
        vclusters += cluster.vclusters?.length ?? 0
        nodePools += cluster.nodePools?.length ?? 0
        workerNodes += cluster.nodes?.length ?? 0
        lb += cluster.loadBalancers?.length ?? 0
      }
    }
    c['clusters'] = clusters
    c['vclusters'] = vclusters
    c['node-pools'] = nodePools
    c['worker-nodes'] = workerNodes
    c['load-balancers'] = lb
    c['pvcs'] = data.storage?.pvcs?.length ?? 0
    c['buckets'] = data.storage?.buckets?.length ?? 0
    c['volumes'] = data.storage?.volumes?.length ?? 0
  }
  // Override every K8s-backed count from the live snapshot. Counts stay
  // null until the SSE connection delivers initialState=1.
  if (snapshot && snapshot.size > 0) {
    const liveCounts: Partial<Record<CloudListKind, number>> = {}
    for (const key of snapshot.keys()) {
      const kind = key.split(':', 1)[0]
      for (const [chipId, registryKind] of Object.entries(KIND_TO_REGISTRY)) {
        if (registryKind === kind) {
          const id = chipId as CloudListKind
          liveCounts[id] = (liveCounts[id] ?? 0) + 1
        }
      }
    }
    // Always set to 0 (not null) for K8s-backed kinds once the stream
    // has any data — initialState=1 has arrived, so a 0-count kind is
    // genuinely empty, not unconnected.
    for (const chipId of Object.keys(KIND_TO_REGISTRY) as CloudListKind[]) {
      c[chipId] = liveCounts[chipId] ?? 0
    }
  }
  return c
}
