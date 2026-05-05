/**
 * k8sAdapter.ts — K8s-side companion to adapter.ts. Takes the live
 * cache snapshot fed by useK8sCacheStream and emits GraphNodes /
 * GraphEdges with the same composite-id convention the cloud-side
 * adapter uses.
 *
 * Per the issue #975 design:
 *
 *   • Composite ids
 *     - cluster-scoped: `${Type}:${name}`
 *     - namespaced:     `${Type}:${ns}/${name}`
 *
 *   • Edge inference (every edge has a structural source)
 *     - Pod → WorkerNode  `runs-on`     via .spec.nodeName
 *     - Pod → Namespace   `member-of`   via .metadata.namespace
 *     - Pod → Workload    `member-of`   via ownerReferences chain
 *                                       (collapse ReplicaSet hop to
 *                                        attribute Pods directly to
 *                                        their parent Deployment)
 *     - Service → Pod     `routes-to`   via spec.selector ⊆ pod.labels
 *                                       within the same namespace
 *                                       (EndpointSlice gives exact
 *                                        membership when present)
 *     - Ingress → Service `flows-to`    via spec.rules[].http.paths[]
 *                                       .backend.service.name
 *     - Pod → PVC         `attached-to` via spec.volumes[]
 *                                       .persistentVolumeClaim.claimName
 *     - PVC → Volume(.hcloud) `realizes` via PV csi.volumeAttributes
 *                                        hcloud-volume id
 *
 *   • Bridge — `WorkerNode:<name>` (cloud) and `Node:<name>` (k8s) get
 *     **collapsed** into one node when names match. The collapse pass
 *     lives in the page-level merge step (mergeGraphs), not this
 *     adapter — to keep this file pure and testable.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — every edge type lands here in one cut.
 *   #2 (quality)   — selector matching uses the EndpointSlice cache
 *                    when present (exact membership) and falls back
 *                    to label-subset only when not — never mock data.
 *   #4 (never hardcode) — the kind list lives in useK8sCacheStream so
 *                    this module reads it via the snapshot keys.
 */

import type { K8sObject, K8sSnapshot } from './useK8sCacheStream'
import type { ArchEdgeType, ArchNodeType, ArchStatus, GraphEdge, GraphNode } from './types'

interface AdaptResult {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

/**
 * Strict typing for the parts of the unstructured object the adapter
 * actually reads. We accept the broader K8sObject shape and narrow at
 * field-read time — Kubernetes evolves field shapes (notably
 * Ingress.spec.rules[].http.paths[].backend.service vs the older
 * .serviceName) and a strict generated type would be a lie.
 */
type Labels = Record<string, string>

function compositeId(kind: ArchNodeType, ns: string, name: string): string {
  if (ns) {
    return `${kind}:${ns}/${name}`
  }
  return `${kind}:${name}`
}

function snapshotKeyType(snapshotKey: string): string {
  const colon = snapshotKey.indexOf(':')
  if (colon < 0) {
    return ''
  }
  return snapshotKey.slice(0, colon)
}

function getLabels(o: K8sObject): Labels {
  return (o.metadata?.labels as Labels | undefined) ?? {}
}

function selectorMatches(selector: Labels, podLabels: Labels): boolean {
  for (const k of Object.keys(selector)) {
    if (podLabels[k] !== selector[k]) {
      return false
    }
  }
  return Object.keys(selector).length > 0
}

function podStatus(o: K8sObject): ArchStatus {
  const conds = (o.status?.conditions as Array<{ type?: string; status?: string }> | undefined) ?? []
  for (const c of conds) {
    if (c.type === 'Ready') {
      return c.status === 'True' ? 'healthy' : 'degraded'
    }
  }
  const phase = (o.status?.phase as string | undefined) ?? ''
  switch (phase) {
    case 'Running':
    case 'Succeeded':
      return 'healthy'
    case 'Failed':
      return 'failed'
    case 'Unknown':
      return 'unknown'
    default:
      return 'unknown'
  }
}

function workloadStatus(o: K8sObject, kind: 'Deployment' | 'StatefulSet' | 'DaemonSet'): ArchStatus {
  const status = o.status as Record<string, unknown> | undefined
  if (!status) return 'unknown'
  const ready = Number(status['readyReplicas'] ?? status['numberReady'] ?? 0)
  const desired = Number(
    kind === 'DaemonSet'
      ? status['desiredNumberScheduled'] ?? 0
      : (o.spec?.['replicas'] as number | undefined) ?? status['replicas'] ?? 0,
  )
  if (desired === 0) return 'unknown'
  if (ready >= desired) return 'healthy'
  if (ready === 0) return 'failed'
  return 'degraded'
}

/**
 * Walk a Pod's ownerReferences chain to find the highest-level
 * workload owner that the architecture graph projects (Deployment /
 * StatefulSet / DaemonSet). The ReplicaSet hop in front of a
 * Deployment is the most common — when an RS owns the Pod, we look
 * up the RS in the snapshot and take its first ownerRef.
 *
 * Returns null when the chain doesn't terminate at a known kind
 * (e.g. bare Pods, Pods owned by Job / CronJob — out of scope today).
 */
function resolveTopOwner(
  pod: K8sObject,
  snapshot: K8sSnapshot,
): { kind: 'Deployment' | 'StatefulSet' | 'DaemonSet'; namespace: string; name: string } | null {
  const refs = pod.metadata?.ownerReferences ?? []
  if (refs.length === 0) return null
  const ns = pod.metadata?.namespace ?? ''
  const ref = refs[0]!
  if (ref.kind === 'StatefulSet' && ref.name) {
    return { kind: 'StatefulSet', namespace: ns, name: ref.name }
  }
  if (ref.kind === 'DaemonSet' && ref.name) {
    return { kind: 'DaemonSet', namespace: ns, name: ref.name }
  }
  if (ref.kind === 'ReplicaSet' && ref.name) {
    const rs = snapshot.get(`replicaset:${ns}/${ref.name}`)
    if (!rs) {
      return null
    }
    const rsOwner = rs.metadata?.ownerReferences?.[0]
    if (rsOwner?.kind === 'Deployment' && rsOwner.name) {
      return { kind: 'Deployment', namespace: ns, name: rsOwner.name }
    }
    return null
  }
  return null
}

/**
 * Filter a snapshot key set down to one kind. Allocation-light;
 * called repeatedly during edge inference.
 */
function* iterByKind(snapshot: K8sSnapshot, kind: string): Generator<[string, K8sObject]> {
  for (const [key, obj] of snapshot) {
    if (snapshotKeyType(key) === kind) {
      yield [key, obj]
    }
  }
}

/* ── Adapter ─────────────────────────────────────────────────────── */

export function k8sToGraph(snapshot: K8sSnapshot): AdaptResult {
  const nodes: GraphNode[] = []
  const edges: GraphEdge[] = []
  const nodeIds = new Set<string>()
  const addNode = (n: GraphNode) => {
    if (nodeIds.has(n.id)) return
    nodeIds.add(n.id)
    nodes.push(n)
  }
  const addEdge = (e: GraphEdge) => {
    // Endpoint validation is deferred to mergeGraphs's final filter
    // so cross-side bridges (e.g. K8s PVC → cloud-side Volume.hcloud)
    // survive: the K8s pass emits the edge, the cloud pass adds the
    // Volume node, mergeGraphs unifies them.
    edges.push(e)
  }

  // 1. Namespace nodes.
  for (const [, obj] of iterByKind(snapshot, 'namespace')) {
    const name = obj.metadata?.name ?? ''
    if (!name) continue
    addNode({
      id: compositeId('Namespace', '', name),
      type: 'Namespace',
      label: name,
      status: 'healthy',
      metadata: { name },
    })
  }

  // 2. WorkerNode (k8s Node) — emitted under the cloud-side type so
  //    the merge pass can collapse name-matched cloud nodes onto
  //    these. The id includes the type prefix so when the cloud-side
  //    adapter ALSO emits `WorkerNode:<id>` with the same name, the
  //    de-dupe in addNode collapses the pair into one. (Cloud-side
  //    ids today are `WorkerNode:node-1`/`WorkerNode:abc` from the
  //    deployment record's RegionSpec; k8s-side ids are
  //    `WorkerNode:<node-name>`. When provisioning is deterministic
  //    they match by construction; when they don't, the page renders
  //    both and the operator sees the gap.)
  for (const [, obj] of iterByKind(snapshot, 'node')) {
    const name = obj.metadata?.name ?? ''
    if (!name) continue
    const status = nodeStatus(obj)
    const addresses = (obj.status?.addresses as Array<{ type?: string; address?: string }> | undefined) ?? []
    const internalIP = addresses.find((a) => a.type === 'InternalIP')?.address ?? ''
    const sublabel = internalIP || (obj.status?.nodeInfo as { kubeletVersion?: string } | undefined)?.kubeletVersion || ''
    addNode({
      id: compositeId('WorkerNode', '', name),
      type: 'WorkerNode',
      label: name,
      sublabel,
      status,
      metadata: {
        ip: internalIP,
        kubeletVersion: (obj.status?.nodeInfo as { kubeletVersion?: string } | undefined)?.kubeletVersion ?? '',
      },
    })
  }

  // 3. Workload owners — Deployment / StatefulSet / DaemonSet.
  for (const kind of ['deployment', 'statefulset', 'daemonset'] as const) {
    const archType = kindToArchType(kind)
    for (const [, obj] of iterByKind(snapshot, kind)) {
      const ns = obj.metadata?.namespace ?? ''
      const name = obj.metadata?.name ?? ''
      if (!ns || !name) continue
      addNode({
        id: compositeId(archType, ns, name),
        type: archType,
        label: name,
        sublabel: ns,
        status: workloadStatus(obj, archType as 'Deployment' | 'StatefulSet' | 'DaemonSet'),
        metadata: {
          namespace: ns,
          replicas: String((obj.spec?.['replicas'] as number | undefined) ?? ''),
        },
      })
      addEdge({
        id: `e:${archType}:${ns}/${name}->Namespace:${ns}`,
        source: compositeId(archType, ns, name),
        target: compositeId('Namespace', '', ns),
        type: 'member-of',
      })
    }
  }

  // 4. Pods.
  for (const [, pod] of iterByKind(snapshot, 'pod')) {
    const ns = pod.metadata?.namespace ?? ''
    const name = pod.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('Pod', ns, name)
    addNode({
      id,
      type: 'Pod',
      label: name,
      sublabel: ns,
      status: podStatus(pod),
      metadata: { namespace: ns },
    })
    // Pod → Namespace
    addEdge({
      id: `e:${id}->Namespace:${ns}`,
      source: id,
      target: compositeId('Namespace', '', ns),
      type: 'member-of',
    })
    // Pod → WorkerNode
    const nodeName = (pod.spec?.['nodeName'] as string | undefined) ?? ''
    if (nodeName) {
      addEdge({
        id: `e:${id}->WorkerNode:${nodeName}`,
        source: id,
        target: compositeId('WorkerNode', '', nodeName),
        type: 'runs-on',
      })
    }
    // Pod → Workload (via owner-ref chain).
    const top = resolveTopOwner(pod, snapshot)
    if (top) {
      addEdge({
        id: `e:${id}->${top.kind}:${top.namespace}/${top.name}`,
        source: id,
        target: compositeId(top.kind, top.namespace, top.name),
        type: 'member-of',
      })
    }
    // Pod → PVC (attached-to).
    const volumes = (pod.spec?.['volumes'] as Array<Record<string, unknown>> | undefined) ?? []
    for (const v of volumes) {
      const pvcRef = v?.['persistentVolumeClaim'] as { claimName?: string } | undefined
      if (pvcRef?.claimName) {
        addEdge({
          id: `e:${id}->PVC:${ns}/${pvcRef.claimName}`,
          source: id,
          target: compositeId('PVC', ns, pvcRef.claimName),
          type: 'attached-to',
        })
      }
    }
  }

  // 5. Services + selector matching.
  //    Prefer EndpointSlice when present — exact membership without
  //    walking the pod labelset.
  const slicesByService = new Map<string, K8sObject[]>() // ns/name → slices
  for (const [, slice] of iterByKind(snapshot, 'endpointslice')) {
    const ns = slice.metadata?.namespace ?? ''
    const svcName = (slice.metadata?.labels?.['kubernetes.io/service-name'] as string | undefined) ?? ''
    if (!ns || !svcName) continue
    const key = `${ns}/${svcName}`
    if (!slicesByService.has(key)) {
      slicesByService.set(key, [])
    }
    slicesByService.get(key)!.push(slice)
  }
  for (const [, svc] of iterByKind(snapshot, 'service')) {
    const ns = svc.metadata?.namespace ?? ''
    const name = svc.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('Service', ns, name)
    addNode({
      id,
      type: 'Service',
      label: name,
      sublabel: ns,
      status: 'healthy',
      metadata: { namespace: ns },
    })
    addEdge({
      id: `e:${id}->Namespace:${ns}`,
      source: id,
      target: compositeId('Namespace', '', ns),
      type: 'member-of',
    })
    // Try EndpointSlice first.
    const slices = slicesByService.get(`${ns}/${name}`) ?? []
    if (slices.length > 0) {
      const seen = new Set<string>()
      for (const slice of slices) {
        const eps = (slice['endpoints'] as Array<Record<string, unknown>> | undefined) ?? []
        for (const ep of eps) {
          const target = (ep['targetRef'] as { kind?: string; name?: string } | undefined) ?? {}
          if (target.kind !== 'Pod' || !target.name) continue
          const podKey = compositeId('Pod', ns, target.name)
          if (seen.has(podKey)) continue
          seen.add(podKey)
          addEdge({
            id: `e:${id}->${podKey}`,
            source: id,
            target: podKey,
            type: 'routes-to',
          })
        }
      }
      continue
    }
    // Fall back to label-selector match.
    const selector = (svc.spec?.['selector'] as Labels | undefined) ?? {}
    if (Object.keys(selector).length === 0) continue
    for (const [, pod] of iterByKind(snapshot, 'pod')) {
      if ((pod.metadata?.namespace ?? '') !== ns) continue
      if (!selectorMatches(selector, getLabels(pod))) continue
      const podName = pod.metadata?.name ?? ''
      if (!podName) continue
      addEdge({
        id: `e:${id}->Pod:${ns}/${podName}`,
        source: id,
        target: compositeId('Pod', ns, podName),
        type: 'routes-to',
      })
    }
  }

  // 6. Ingresses.
  for (const [, ing] of iterByKind(snapshot, 'ingress')) {
    const ns = ing.metadata?.namespace ?? ''
    const name = ing.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('Ingress', ns, name)
    addNode({
      id,
      type: 'Ingress',
      label: name,
      sublabel: ns,
      status: 'healthy',
      metadata: { namespace: ns },
    })
    addEdge({
      id: `e:${id}->Namespace:${ns}`,
      source: id,
      target: compositeId('Namespace', '', ns),
      type: 'member-of',
    })
    const rules = (ing.spec?.['rules'] as Array<Record<string, unknown>> | undefined) ?? []
    for (const rule of rules) {
      const http = rule['http'] as { paths?: Array<Record<string, unknown>> } | undefined
      const paths = http?.paths ?? []
      for (const p of paths) {
        const backend = p['backend'] as { service?: { name?: string } } | undefined
        const svcName = backend?.service?.name
        if (!svcName) continue
        addEdge({
          id: `e:${id}->Service:${ns}/${svcName}`,
          source: id,
          target: compositeId('Service', ns, svcName),
          type: 'flows-to',
        })
      }
    }
  }

  // 7. PVCs.
  for (const [, pvc] of iterByKind(snapshot, 'persistentvolumeclaim')) {
    const ns = pvc.metadata?.namespace ?? ''
    const name = pvc.metadata?.name ?? ''
    if (!ns || !name) continue
    const phase = (pvc.status?.['phase'] as string | undefined) ?? ''
    addNode({
      id: compositeId('PVC', ns, name),
      type: 'PVC',
      label: name,
      sublabel: ns,
      status: phase === 'Bound' ? 'healthy' : phase === 'Lost' ? 'failed' : 'unknown',
      metadata: {
        namespace: ns,
        capacity: ((pvc.spec?.['resources'] as { requests?: { storage?: string } } | undefined)?.requests?.storage) ?? '',
        storageClass: ((pvc.spec?.['storageClassName'] as string | undefined)) ?? '',
        phase,
      },
    })
    // PVC → Volume.hcloud via PV csi.volumeAttributes (best-effort —
    // hcloud-csi sets `volumeID` on the PV's csi block).
    const pvName = (pvc.spec?.['volumeName'] as string | undefined) ?? ''
    if (pvName) {
      const pv = snapshot.get(`persistentvolume:${pvName}`)
      const csi = pv?.spec?.['csi'] as { volumeAttributes?: Record<string, string>; volumeHandle?: string } | undefined
      const hcloudID = csi?.volumeAttributes?.['volumeID'] ?? csi?.volumeHandle ?? ''
      if (hcloudID) {
        addEdge({
          id: `e:PVC:${ns}/${name}->Volume:${hcloudID}`,
          source: compositeId('PVC', ns, name),
          target: compositeId('Volume', '', hcloudID),
          type: 'realizes',
        })
      }
    }
  }

  // 8. ConfigMaps — opt-in chip; rendered only when the operator
  //    enables the type. Edge: ConfigMap → Namespace.
  for (const [, cm] of iterByKind(snapshot, 'configmap')) {
    const ns = cm.metadata?.namespace ?? ''
    const name = cm.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('ConfigMap', ns, name)
    addNode({
      id,
      type: 'ConfigMap',
      label: name,
      sublabel: ns,
      status: 'healthy',
      metadata: { namespace: ns },
    })
    addEdge({
      id: `e:${id}->Namespace:${ns}`,
      source: id,
      target: compositeId('Namespace', '', ns),
      type: 'member-of',
    })
  }

  return { nodes, edges }
}

function nodeStatus(o: K8sObject): ArchStatus {
  const conds = (o.status?.conditions as Array<{ type?: string; status?: string }> | undefined) ?? []
  for (const c of conds) {
    if (c.type === 'Ready') {
      return c.status === 'True' ? 'healthy' : 'degraded'
    }
  }
  return 'unknown'
}

function kindToArchType(kind: 'deployment' | 'statefulset' | 'daemonset'): ArchNodeType {
  switch (kind) {
    case 'deployment':
      return 'Deployment'
    case 'statefulset':
      return 'StatefulSet'
    case 'daemonset':
      return 'DaemonSet'
  }
}

/* ── Bridge: collapse cloud-side WorkerNode ↔ k8s Node ──────────── */

/**
 * mergeGraphs unions the cloud-side and K8s-side adapter outputs.
 * The collapse rule:
 *
 *   Cloud-side WorkerNode + K8s-side WorkerNode (originating from a
 *   Node object) get folded into one node when their ids match. The
 *   K8s-side payload wins for live status (Ready condition) while
 *   the cloud-side payload contributes the SKU / role metadata.
 *
 * Edges referencing either id are rewritten to point at the merged
 * node so neighbour lookups stay correct.
 */
export function mergeGraphs(cloud: AdaptResult, k8s: AdaptResult): AdaptResult {
  const cloudNodesById = new Map(cloud.nodes.map((n) => [n.id, n] as const))
  const merged: GraphNode[] = []
  const seen = new Set<string>()

  // Emit cloud nodes first (provenance). When a k8s node has the same
  // id, fold the k8s payload onto the cloud node.
  for (const cn of cloud.nodes) {
    seen.add(cn.id)
    merged.push(cn)
  }
  for (const kn of k8s.nodes) {
    if (seen.has(kn.id)) {
      const idx = merged.findIndex((n) => n.id === kn.id)
      if (idx >= 0) {
        merged[idx] = mergeNodePayloads(merged[idx]!, kn)
      }
      continue
    }
    seen.add(kn.id)
    merged.push(kn)
  }

  const edges = [...cloud.edges, ...k8s.edges]
  // De-duplicate edges by (source,target,type) so cloud + k8s emitters
  // don't produce parallel edges for the same relation.
  const edgeKey = (e: GraphEdge) => `${e.source}|${e.target}|${e.type}`
  const seenEdges = new Set<string>()
  const dedupedEdges: GraphEdge[] = []
  for (const e of edges) {
    const k = edgeKey(e)
    if (seenEdges.has(k)) continue
    seenEdges.add(k)
    dedupedEdges.push(e)
  }
  // Drop edges whose endpoints aren't in the merged node set.
  const merge: AdaptResult = {
    nodes: merged,
    edges: dedupedEdges.filter((e) => seen.has(e.source) && seen.has(e.target)),
  }
  // unused but keeps the variable referenced under strict TS noUnusedLocals
  void cloudNodesById
  return merge
}

function mergeNodePayloads(cloud: GraphNode, k8s: GraphNode): GraphNode {
  // Prefer K8s status when meaningful — the cloud side often shows
  // 'unknown' because the topology loader doesn't have the live
  // Ready condition.
  const status: ArchStatus = k8s.status && k8s.status !== 'unknown' ? k8s.status : cloud.status ?? 'unknown'
  return {
    ...cloud,
    status,
    sublabel: cloud.sublabel || k8s.sublabel,
    metadata: { ...(k8s.metadata ?? {}), ...(cloud.metadata ?? {}) },
  }
}
