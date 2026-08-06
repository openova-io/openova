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
 *     - HTTPRoute → Gateway `flows-to`  via spec.parentRefs (#3987)
 *     - HTTPRoute → Service `routes-to` via spec.rules[].backendRefs
 *     - Gateway / NetworkPolicy → Namespace `member-of`
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
import { snapshotKey } from './useK8sCacheStream'
import type { ArchNodeType, ArchStatus, GraphEdge, GraphNode } from './types'

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
    // #5571: resolve the owning ReplicaSet in the SAME region as the
    // Pod. Snapshot keys are now cluster-suffixed, and on a 2-region
    // Sovereign both regions run the same Deployment names — an
    // unsuffixed lookup would either miss entirely or bind the Pod to
    // the OTHER region's ReplicaSet.
    const rs = snapshot.get(
      snapshotKey('replicaset', ns, ref.name, pod.clusterId ?? ''),
    )
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

  // 6b. Gateway-API Gateways (#3987 UAT row 200). The SSE snapshot
  //     carries `gateway:` keys (CLOUD_PAGE_K8S_KINDS ∪ GRAPH_K8S_KINDS)
  //     but no adapter section consumed them, so the Networking lens
  //     chips read 0/0 while the cluster served real Gateways / routes.
  //     Status from the Gateway's Programmed (fallback Accepted)
  //     condition.
  for (const [, gw] of iterByKind(snapshot, 'gateway')) {
    const ns = gw.metadata?.namespace ?? ''
    const name = gw.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('Gateway', ns, name)
    addNode({
      id,
      type: 'Gateway',
      label: name,
      sublabel: ns,
      status: gatewayStatus(gw),
      metadata: {
        namespace: ns,
        gatewayClassName: (gw.spec?.['gatewayClassName'] as string | undefined) ?? '',
      },
    })
    addEdge({
      id: `e:${id}->Namespace:${ns}`,
      source: id,
      target: compositeId('Namespace', '', ns),
      type: 'member-of',
    })
  }

  // 6c. HTTPRoutes — status from status.parents[].conditions Accepted.
  //     Edges: HTTPRoute → Gateway `flows-to` via spec.parentRefs
  //     (parent namespace defaults to the route's own), and
  //     HTTPRoute → Service `routes-to` via spec.rules[].backendRefs
  //     (kind omitted = Service per the Gateway-API default).
  for (const [, route] of iterByKind(snapshot, 'httproute')) {
    const ns = route.metadata?.namespace ?? ''
    const name = route.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('HTTPRoute', ns, name)
    addNode({
      id,
      type: 'HTTPRoute',
      label: name,
      sublabel: ns,
      status: httpRouteStatus(route),
      metadata: {
        namespace: ns,
        hostnames: ((route.spec?.['hostnames'] as string[] | undefined) ?? []).join(', '),
      },
    })
    addEdge({
      id: `e:${id}->Namespace:${ns}`,
      source: id,
      target: compositeId('Namespace', '', ns),
      type: 'member-of',
    })
    const parentRefs = (route.spec?.['parentRefs'] as Array<Record<string, unknown>> | undefined) ?? []
    for (const p of parentRefs) {
      const kind = (p['kind'] as string | undefined) ?? 'Gateway'
      if (kind !== 'Gateway') continue
      const pName = (p['name'] as string | undefined) ?? ''
      if (!pName) continue
      const pNs = (p['namespace'] as string | undefined) ?? ns
      addEdge({
        id: `e:${id}->Gateway:${pNs}/${pName}`,
        source: id,
        target: compositeId('Gateway', pNs, pName),
        type: 'flows-to',
      })
    }
    const routeRules = (route.spec?.['rules'] as Array<Record<string, unknown>> | undefined) ?? []
    for (const rule of routeRules) {
      const backends = (rule['backendRefs'] as Array<Record<string, unknown>> | undefined) ?? []
      for (const b of backends) {
        const kind = (b['kind'] as string | undefined) ?? 'Service'
        if (kind !== 'Service') continue
        const bName = (b['name'] as string | undefined) ?? ''
        if (!bName) continue
        const bNs = (b['namespace'] as string | undefined) ?? ns
        addEdge({
          id: `e:${id}->Service:${bNs}/${bName}`,
          source: id,
          target: compositeId('Service', bNs, bName),
          type: 'routes-to',
        })
      }
    }
  }

  // 6d. NetworkPolicies (networking.k8s.io) — no status subresource on
  //     the API; an existing policy is an enforced policy, so presence
  //     renders healthy. Edge: → Namespace.
  for (const [, np] of iterByKind(snapshot, 'networkpolicy')) {
    const ns = np.metadata?.namespace ?? ''
    const name = np.metadata?.name ?? ''
    if (!ns || !name) continue
    const id = compositeId('NetworkPolicy', ns, name)
    addNode({
      id,
      type: 'NetworkPolicy',
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

  // 6e. CiliumNetworkPolicies (cilium.io/v2) — L3-L7 micro-segmentation,
  //     rendered as a DISTINCT node type so the Networking lens no longer
  //     folds them into the vanilla NetworkPolicy count (#5129). Edge: → Namespace.
  for (const cnpKind of ['ciliumnetworkpolicy', 'ciliumclusterwidenetworkpolicy'] as const) {
    for (const [, cnp] of iterByKind(snapshot, cnpKind)) {
      const ns = cnp.metadata?.namespace ?? ''
      const name = cnp.metadata?.name ?? ''
      if (!name) continue
      const id = compositeId('CiliumNetworkPolicy', ns, name)
      addNode({
        id,
        type: 'CiliumNetworkPolicy',
        label: name,
        sublabel: ns || 'cluster-wide',
        status: 'healthy',
        metadata: { namespace: ns },
      })
      if (ns) {
        addEdge({
          id: `e:${id}->Namespace:${ns}`,
          source: id,
          target: compositeId('Namespace', '', ns),
          type: 'member-of',
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
      // #5571: bind to the PV in the PVC's OWN region — a PV name can
      // repeat across regions, and crossing the boundary here would
      // attribute a region-a claim to a region-b disk.
      const pv = snapshot.get(
        snapshotKey('persistentvolume', '', pvName, pvc.clusterId ?? ''),
      )
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

  // 7b. Volume — Crossplane `volume.hcloud` managed resource (#3987).
  //     The SSE snapshot carries `volume.hcloud:` keys (they are in both
  //     GRAPH_K8S_KINDS and the CloudPage CLOUD_PAGE_K8S_KINDS union; the
  //     kind is registered in api/internal/k8scache/kinds.go), and
  //     section 7 already emits a PVC → `Volume:${hcloudID}` `realizes`
  //     edge — but NO adapter ever created the Volume node. So the whole
  //     PVC→Volume.hcloud bridge that kinds.go / GRAPH_K8S_KINDS / the
  //     cloud-side adapter all scaffold rendered nothing: the Volume chip
  //     read empty on an hcloud Sovereign even with live volumes, and
  //     every `realizes` edge dangled (its target node absent → dropped
  //     by mergeGraphs's endpoint filter). Adapt the managed resource
  //     into a Volume node keyed by its hcloud id (the Crossplane
  //     external-name, which equals the CSI volumeHandle the edge keys
  //     on) so the chip populates from the live source AND the existing
  //     PVC→Volume edges resolve. A cluster with no volume.hcloud objects
  //     (e.g. a non-hcloud provider) still renders empty — no fabrication.
  for (const [, vol] of iterByKind(snapshot, 'volume.hcloud')) {
    const hcloudID = crossplaneExternalID(vol)
    if (!hcloudID) continue
    const forProvider = (vol.spec?.['forProvider'] as Record<string, unknown> | undefined) ?? {}
    const size = forProvider['size']
    const capacity = size != null && `${size}` !== '' ? `${size} GB` : ''
    const region = (forProvider['location'] as string | undefined) ?? ''
    const label = (forProvider['name'] as string | undefined) || vol.metadata?.name || hcloudID
    addNode({
      id: compositeId('Volume', '', hcloudID),
      type: 'Volume',
      label,
      sublabel: capacity || region,
      status: nodeStatus(vol),
      metadata: { capacity, region, hcloudID },
    })
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

/**
 * crossplaneExternalID resolves a Crossplane managed resource's stable
 * cloud-side id — the `crossplane.io/external-name` annotation, which
 * provider-hcloud sets to the numeric hcloud id (the SAME value the
 * hcloud-csi PV carries as its volumeHandle, so a Volume node keyed on
 * it matches the PVC→Volume `realizes` edge target). Falls back to
 * status.atProvider.id, then metadata.name. Empty string when none
 * resolve (the node is skipped rather than keyed on a blank id).
 */
function crossplaneExternalID(o: K8sObject): string {
  const ext = o.metadata?.annotations?.['crossplane.io/external-name']
  if (ext) return ext
  const atProvider = o.status?.['atProvider'] as { id?: unknown } | undefined
  if (atProvider?.id != null && `${atProvider.id}` !== '') return `${atProvider.id}`
  return o.metadata?.name ?? ''
}

/**
 * Gateway readiness — the Gateway-API status carries `Programmed`
 * (the implementation wired dataplane config) with `Accepted` as the
 * weaker fallback on older conditions sets.
 */
function gatewayStatus(o: K8sObject): ArchStatus {
  const conds = (o.status?.['conditions'] as Array<{ type?: string; status?: string }> | undefined) ?? []
  for (const want of ['Programmed', 'Accepted']) {
    const c = conds.find((x) => x.type === want)
    if (c) return c.status === 'True' ? 'healthy' : 'degraded'
  }
  return 'unknown'
}

/**
 * HTTPRoute readiness — per-parent `Accepted` conditions under
 * status.parents[]. Any accepted parent = the route is serving.
 */
function httpRouteStatus(o: K8sObject): ArchStatus {
  const parents = (o.status?.['parents'] as Array<Record<string, unknown>> | undefined) ?? []
  if (parents.length === 0) return 'unknown'
  let sawAcceptedCondition = false
  for (const p of parents) {
    const conds = (p['conditions'] as Array<{ type?: string; status?: string }> | undefined) ?? []
    const accepted = conds.find((c) => c.type === 'Accepted')
    if (accepted) {
      sawAcceptedCondition = true
      if (accepted.status === 'True') return 'healthy'
    }
  }
  return sawAcceptedCondition ? 'degraded' : 'unknown'
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
 * The collapse rules:
 *
 *   1. Cloud-side WorkerNode + K8s-side WorkerNode (originating from a
 *      Node object) get folded into one node when their ids match. The
 *      K8s-side payload wins for live status (Ready condition) while
 *      the cloud-side payload contributes the SKU / role metadata.
 *   2. #4732(4): when the ids DON'T match — the normal case on Huawei,
 *      where the cloud side names nodes from the RegionSpec
 *      ("worker-1-me-east-215-a") and the k8s side uses the real Node
 *      name ("catalyst-<fqdn>-...-w1d535a") — a WorkerNode pair is
 *      collapsed by InternalIP instead. Both adapters stamp
 *      `metadata.ip`, and a node's InternalIP is unique within the
 *      fleet, so IP equality IS node identity. Without this rule every
 *      node rendered twice (12 real nodes → "24/24" on the graph while
 *      the list view showed the true 12).
 *   3. #4814: rules 1 + 2 both FAIL when the declared side carries no
 *      identity that can match a live node. The topology_loader's
 *      declared `buildNodes` emits synthetic ids
 *      (`node-w-<i>-<region>` / `node-cp-<region>`) AND leaves worker
 *      `ip` EMPTY (only the control-plane gets an EIP, which is not the
 *      live InternalIP). So neither the id match nor the IP-keyed
 *      collapse fires, and a 2-region ×(1 cp + 5 worker) prov renders
 *      12 declared + 12 live = "24/24" — an exact 2× over-count of the
 *      12 physical nodes. There is no per-node key that can ever fold
 *      the declared placeholders onto their live twins (the live node
 *      name is a random hash, not the declared ordinal), so we count
 *      LIVE, not declared: when confirmed-live WorkerNodes are present
 *      (a standalone k8s Node WITH a non-empty InternalIP), the
 *      remaining declared WorkerNode leaves that did NOT collapse onto
 *      a live node are dropped — the live nodes are the leaves. The
 *      declared Region / Cluster / NodePool structural layer stays, so
 *      declared capacity is still visible (and a live-absent region
 *      still shows its NodePool). Pre-convergence, when no live Node
 *      has streamed yet, every declared placeholder is kept so the
 *      declared-only preview still renders.
 *
 * Edges referencing a collapsed k8s id are rewritten to the surviving
 * cloud id so neighbour lookups (Pod runs-on, Volume attached-to) stay
 * correct.
 */
export function mergeGraphs(cloud: AdaptResult, k8s: AdaptResult): AdaptResult {
  const cloudNodesById = new Map(cloud.nodes.map((n) => [n.id, n] as const))
  const merged: GraphNode[] = []
  const seen = new Set<string>()

  // #4732(4): index cloud-side WorkerNodes by InternalIP for the
  // IP-keyed collapse (rule 2). Only non-empty IPs participate — a
  // topology row without an IP can never falsely match.
  const cloudWorkerByIP = new Map<string, string>()
  for (const cn of cloud.nodes) {
    if (cn.type !== 'WorkerNode') continue
    const ip = String(cn.metadata?.['ip'] ?? '').trim()
    if (ip && !cloudWorkerByIP.has(ip)) cloudWorkerByIP.set(ip, cn.id)
  }
  // k8s id → surviving cloud id, for edge rewriting.
  const aliasedTo = new Map<string, string>()
  // #4814: cloud-side WorkerNode ids that DID absorb a live payload
  // (via rule 1 id-match or rule 2 IP-collapse). These represent a
  // confirmed physical node and must never be dropped by rule 3.
  const declaredWorkersMergedWithLive = new Set<string>()
  // #4814: k8s WorkerNodes that rendered standalone (no declared twin).
  // A non-empty InternalIP marks a CONFIRMED live node — the signal
  // that the declared placeholders for that fleet are now redundant.
  let confirmedLiveWorkerPresent = false

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
      if (kn.type === 'WorkerNode') declaredWorkersMergedWithLive.add(kn.id)
      continue
    }
    // Rule 2 — IP-keyed WorkerNode collapse.
    if (kn.type === 'WorkerNode') {
      const ip = String(kn.metadata?.['ip'] ?? '').trim()
      const cloudId = ip ? cloudWorkerByIP.get(ip) : undefined
      if (cloudId) {
        const idx = merged.findIndex((n) => n.id === cloudId)
        if (idx >= 0) {
          merged[idx] = mergeNodePayloads(merged[idx]!, kn)
        }
        declaredWorkersMergedWithLive.add(cloudId)
        aliasedTo.set(kn.id, cloudId)
        continue
      }
      // Standalone live WorkerNode — a real node with no declared twin.
      if (ip) confirmedLiveWorkerPresent = true
    }
    seen.add(kn.id)
    merged.push(kn)
  }

  // #4814 (rule 3) — count LIVE, not declared. When at least one
  // confirmed live WorkerNode is present, the declared WorkerNode
  // leaves that never collapsed onto a live node are the un-matchable
  // duplicates the topology_loader emitted (synthetic id + empty
  // worker IP); drop them so each physical node is counted ONCE. The
  // declared Cluster / NodePool structural layer is untouched, so
  // declared capacity stays visible. With no live node yet (declared-
  // only preview) nothing is dropped.
  let survivingNodes = merged
  if (confirmedLiveWorkerPresent) {
    survivingNodes = merged.filter((n) => {
      if (n.type !== 'WorkerNode') return true
      if (!cloudNodesById.has(n.id)) return true // live-originated leaf — keep
      return declaredWorkersMergedWithLive.has(n.id) // declared: keep only if it has a live twin
    })
  }
  const survivingIds = new Set(survivingNodes.map((n) => n.id))

  // Rewrite edges that referenced a collapsed k8s WorkerNode id to the
  // surviving cloud id, so Pod runs-on / Volume attached-to edges land
  // on the merged node instead of being dropped by the endpoint filter.
  const rewrite = (id: string) => aliasedTo.get(id) ?? id
  const edges = [...cloud.edges, ...k8s.edges].map((e) => {
    const s = rewrite(e.source)
    const t = rewrite(e.target)
    return s === e.source && t === e.target ? e : { ...e, source: s, target: t }
  })
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
  // Drop edges whose endpoints aren't in the surviving node set (this
  // also sheds the runs-on edges of any #4814-dropped declared leaf).
  const merge: AdaptResult = {
    nodes: survivingNodes,
    edges: dedupedEdges.filter((e) => survivingIds.has(e.source) && survivingIds.has(e.target)),
  }
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
