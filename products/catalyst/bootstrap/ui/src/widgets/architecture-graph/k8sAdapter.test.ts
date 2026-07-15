/**
 * k8sAdapter.test.ts — covers the K8s-side projection of the
 * architecture graph (issue #975).
 *
 * Each test seeds a minimal K8sSnapshot map by hand and asserts the
 * specific node + edge shapes the adapter must produce. We pin the
 * structural sources of every edge — owner-ref chain, Service
 * selector match, EndpointSlice membership, PV→Volume.hcloud
 * bridge — so a regression in one breaks one test, never silently
 * deletes an edge type from the graph.
 */

import { describe, expect, it } from 'vitest'
import { k8sToGraph, mergeGraphs } from './k8sAdapter'
import type { K8sObject, K8sSnapshot } from './useK8sCacheStream'
import type { GraphEdge, GraphNode } from './types'

function snap(...entries: Array<[string, K8sObject]>): K8sSnapshot {
  const m = new Map<string, K8sObject>()
  for (const [k, v] of entries) m.set(k, v)
  return m
}

function pod(ns: string, name: string, extra: Partial<K8sObject> = {}): K8sObject {
  return {
    apiVersion: 'v1',
    kind: 'Pod',
    metadata: { namespace: ns, name, ...(extra.metadata ?? {}) },
    spec: extra.spec ?? {},
    status: extra.status ?? {},
  }
}

function deployment(ns: string, name: string, replicas = 1): K8sObject {
  return {
    apiVersion: 'apps/v1',
    kind: 'Deployment',
    metadata: { namespace: ns, name },
    spec: { replicas },
    status: { readyReplicas: replicas, replicas },
  }
}

function replicaset(ns: string, name: string, deploymentName: string): K8sObject {
  return {
    apiVersion: 'apps/v1',
    kind: 'ReplicaSet',
    metadata: {
      namespace: ns,
      name,
      ownerReferences: [{ kind: 'Deployment', name: deploymentName }],
    },
    spec: {},
    status: {},
  }
}

describe('k8sToGraph', () => {
  it('emits a Namespace node for each watched namespace', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      ['namespace:bar', { metadata: { name: 'bar' } }],
    )
    const { nodes } = k8sToGraph(s)
    const namespaces = nodes.filter((n) => n.type === 'Namespace').map((n) => n.id)
    expect(namespaces.sort()).toEqual(['Namespace:bar', 'Namespace:foo'])
  })

  it('emits Pod→Namespace member-of edge', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      ['pod:foo/p1', pod('foo', 'p1')],
    )
    const { edges } = k8sToGraph(s)
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Pod:foo/p1',
        target: 'Namespace:foo',
        type: 'member-of',
      }),
    )
  })

  it('emits Pod→WorkerNode runs-on edge via spec.nodeName', () => {
    const s = snap(
      ['node:n1', { metadata: { name: 'n1' } }],
      ['namespace:foo', { metadata: { name: 'foo' } }],
      ['pod:foo/p1', pod('foo', 'p1', { spec: { nodeName: 'n1' } })],
    )
    const { edges } = k8sToGraph(s)
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Pod:foo/p1',
        target: 'WorkerNode:n1',
        type: 'runs-on',
      }),
    )
  })

  it('chases ReplicaSet owner-ref to Deployment, dropping the RS hop', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      ['deployment:foo/d1', deployment('foo', 'd1', 1)],
      ['replicaset:foo/rs1', replicaset('foo', 'rs1', 'd1')],
      [
        'pod:foo/p1',
        pod('foo', 'p1', {
          metadata: {
            namespace: 'foo',
            name: 'p1',
            ownerReferences: [{ kind: 'ReplicaSet', name: 'rs1' }],
          },
        }),
      ],
    )
    const { edges } = k8sToGraph(s)
    // Pod must point at the Deployment (not the RS).
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Pod:foo/p1',
        target: 'Deployment:foo/d1',
        type: 'member-of',
      }),
    )
    // No edge points at any ReplicaSet:* target.
    for (const e of edges) {
      expect(e.target.startsWith('ReplicaSet:')).toBe(false)
    }
  })

  it('emits StatefulSet member-of for direct STS ownerRef', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      [
        'statefulset:foo/s1',
        {
          apiVersion: 'apps/v1',
          kind: 'StatefulSet',
          metadata: { namespace: 'foo', name: 's1' },
          spec: { replicas: 1 },
          status: { readyReplicas: 1, replicas: 1 },
        },
      ],
      [
        'pod:foo/p1',
        pod('foo', 'p1', {
          metadata: {
            namespace: 'foo',
            name: 'p1',
            ownerReferences: [{ kind: 'StatefulSet', name: 's1' }],
          },
        }),
      ],
    )
    const { edges } = k8sToGraph(s)
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Pod:foo/p1',
        target: 'StatefulSet:foo/s1',
        type: 'member-of',
      }),
    )
  })

  it('matches Service to Pod by label selector when no EndpointSlice is present', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      [
        'pod:foo/p1',
        pod('foo', 'p1', { metadata: { namespace: 'foo', name: 'p1', labels: { app: 'web' } } }),
      ],
      [
        'pod:foo/p2',
        pod('foo', 'p2', { metadata: { namespace: 'foo', name: 'p2', labels: { app: 'web' } } }),
      ],
      [
        'pod:foo/q1',
        pod('foo', 'q1', { metadata: { namespace: 'foo', name: 'q1', labels: { app: 'other' } } }),
      ],
      [
        'service:foo/svc',
        {
          apiVersion: 'v1',
          kind: 'Service',
          metadata: { namespace: 'foo', name: 'svc' },
          spec: { selector: { app: 'web' } },
        },
      ],
    )
    const { edges } = k8sToGraph(s)
    const routesTo = edges.filter((e) => e.source === 'Service:foo/svc' && e.type === 'routes-to')
    const targets = routesTo.map((e) => e.target).sort()
    expect(targets).toEqual(['Pod:foo/p1', 'Pod:foo/p2'])
  })

  it('uses EndpointSlice for Service→Pod when present (overrides selector match)', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      [
        'pod:foo/p1',
        pod('foo', 'p1', { metadata: { namespace: 'foo', name: 'p1', labels: { app: 'web' } } }),
      ],
      [
        'pod:foo/p2',
        pod('foo', 'p2', { metadata: { namespace: 'foo', name: 'p2', labels: { app: 'web' } } }),
      ],
      [
        'service:foo/svc',
        {
          apiVersion: 'v1',
          kind: 'Service',
          metadata: { namespace: 'foo', name: 'svc' },
          spec: { selector: { app: 'web' } },
        },
      ],
      [
        'endpointslice:foo/svc-abc',
        {
          apiVersion: 'discovery.k8s.io/v1',
          kind: 'EndpointSlice',
          metadata: {
            namespace: 'foo',
            name: 'svc-abc',
            labels: { 'kubernetes.io/service-name': 'svc' },
          },
          // Only p1 listed in slice — p2 should NOT get an edge even
          // though its labels would otherwise match.
          endpoints: [{ targetRef: { kind: 'Pod', name: 'p1' } }],
        },
      ],
    )
    const { edges } = k8sToGraph(s)
    const routesTo = edges.filter((e) => e.source === 'Service:foo/svc' && e.type === 'routes-to')
    expect(routesTo.map((e) => e.target)).toEqual(['Pod:foo/p1'])
  })

  it('emits Ingress→Service flows-to from spec.rules backend', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      [
        'service:foo/svc',
        {
          apiVersion: 'v1',
          kind: 'Service',
          metadata: { namespace: 'foo', name: 'svc' },
          spec: { selector: {} },
        },
      ],
      [
        'ingress:foo/ing',
        {
          apiVersion: 'networking.k8s.io/v1',
          kind: 'Ingress',
          metadata: { namespace: 'foo', name: 'ing' },
          spec: {
            rules: [
              {
                host: 'example.local',
                http: { paths: [{ backend: { service: { name: 'svc' } } }] },
              },
            ],
          },
        },
      ],
    )
    const { edges } = k8sToGraph(s)
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Ingress:foo/ing',
        target: 'Service:foo/svc',
        type: 'flows-to',
      }),
    )
  })

  it('emits Pod→PVC attached-to via spec.volumes', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      [
        'persistentvolumeclaim:foo/data-0',
        {
          apiVersion: 'v1',
          kind: 'PersistentVolumeClaim',
          metadata: { namespace: 'foo', name: 'data-0' },
          spec: { resources: { requests: { storage: '1Gi' } } },
          status: { phase: 'Bound' },
        },
      ],
      [
        'pod:foo/p1',
        pod('foo', 'p1', {
          spec: {
            volumes: [{ name: 'data', persistentVolumeClaim: { claimName: 'data-0' } }],
          },
        }),
      ],
    )
    const { edges } = k8sToGraph(s)
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Pod:foo/p1',
        target: 'PVC:foo/data-0',
        type: 'attached-to',
      }),
    )
  })

  it('bridges PVC→Volume.hcloud via PV csi.volumeAttributes', () => {
    const s = snap(
      ['namespace:foo', { metadata: { name: 'foo' } }],
      [
        'persistentvolumeclaim:foo/data-0',
        {
          apiVersion: 'v1',
          kind: 'PersistentVolumeClaim',
          metadata: { namespace: 'foo', name: 'data-0' },
          spec: { volumeName: 'pv-1', resources: { requests: { storage: '1Gi' } } },
          status: { phase: 'Bound' },
        },
      ],
      [
        'persistentvolume:pv-1',
        {
          apiVersion: 'v1',
          kind: 'PersistentVolume',
          metadata: { name: 'pv-1' },
          spec: { csi: { volumeAttributes: { volumeID: 'hcloud-12345' } } },
        },
      ],
    )
    const { edges } = k8sToGraph(s)
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'PVC:foo/data-0',
        target: 'Volume:hcloud-12345',
        type: 'realizes',
      }),
    )
  })

  /* ── #3987 (UAT row 200) — Gateway-API + NetworkPolicy projection.
   * The snapshot carried gateway:/httproute:/networkpolicy: keys but
   * the adapter had no section for them, so the Networking lens chips
   * rendered 0/0 against a cluster full of live routes. */

  it('#3987: emits a Gateway node with Programmed-condition status + Namespace edge', () => {
    const s = snap(
      ['namespace:kube-system', { metadata: { name: 'kube-system' } }],
      [
        'gateway:kube-system/cilium-gateway',
        {
          apiVersion: 'gateway.networking.k8s.io/v1',
          kind: 'Gateway',
          metadata: { namespace: 'kube-system', name: 'cilium-gateway' },
          spec: { gatewayClassName: 'cilium' },
          status: { conditions: [{ type: 'Programmed', status: 'True' }] },
        },
      ],
    )
    const { nodes, edges } = k8sToGraph(s)
    const gw = nodes.find((n) => n.id === 'Gateway:kube-system/cilium-gateway')
    expect(gw).toBeDefined()
    expect(gw?.type).toBe('Gateway')
    expect(gw?.status).toBe('healthy')
    expect(gw?.metadata?.['gatewayClassName']).toBe('cilium')
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'Gateway:kube-system/cilium-gateway',
        target: 'Namespace:kube-system',
        type: 'member-of',
      }),
    )
  })

  it('#3987: emits HTTPRoute node + flows-to Gateway (parentRefs) + routes-to Service (backendRefs)', () => {
    const s = snap(
      ['namespace:catalyst-system', { metadata: { name: 'catalyst-system' } }],
      [
        'gateway:kube-system/cilium-gateway',
        {
          apiVersion: 'gateway.networking.k8s.io/v1',
          kind: 'Gateway',
          metadata: { namespace: 'kube-system', name: 'cilium-gateway' },
          spec: { gatewayClassName: 'cilium' },
        },
      ],
      [
        'httproute:catalyst-system/console',
        {
          apiVersion: 'gateway.networking.k8s.io/v1',
          kind: 'HTTPRoute',
          metadata: { namespace: 'catalyst-system', name: 'console' },
          spec: {
            hostnames: ['console.hw255.omani.works'],
            parentRefs: [{ name: 'cilium-gateway', namespace: 'kube-system' }],
            rules: [{ backendRefs: [{ name: 'catalyst-console', port: 80 }] }],
          },
          status: {
            parents: [{ conditions: [{ type: 'Accepted', status: 'True' }] }],
          },
        },
      ],
    )
    const { nodes, edges } = k8sToGraph(s)
    const route = nodes.find((n) => n.id === 'HTTPRoute:catalyst-system/console')
    expect(route).toBeDefined()
    expect(route?.type).toBe('HTTPRoute')
    expect(route?.status).toBe('healthy')
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'HTTPRoute:catalyst-system/console',
        target: 'Gateway:kube-system/cilium-gateway',
        type: 'flows-to',
      }),
    )
    // backendRef without namespace defaults to the route's own ns.
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'HTTPRoute:catalyst-system/console',
        target: 'Service:catalyst-system/catalyst-console',
        type: 'routes-to',
      }),
    )
  })

  it('#3987: HTTPRoute with no accepted parent renders degraded', () => {
    const s = snap([
      'httproute:foo/r1',
      {
        apiVersion: 'gateway.networking.k8s.io/v1',
        kind: 'HTTPRoute',
        metadata: { namespace: 'foo', name: 'r1' },
        spec: {},
        status: {
          parents: [{ conditions: [{ type: 'Accepted', status: 'False' }] }],
        },
      },
    ])
    const { nodes } = k8sToGraph(s)
    expect(nodes.find((n) => n.id === 'HTTPRoute:foo/r1')?.status).toBe('degraded')
  })

  it('#3987: emits a NetworkPolicy node + Namespace edge', () => {
    const s = snap(
      ['namespace:catalyst-system', { metadata: { name: 'catalyst-system' } }],
      [
        'networkpolicy:catalyst-system/plane-isolation',
        {
          apiVersion: 'networking.k8s.io/v1',
          kind: 'NetworkPolicy',
          metadata: { namespace: 'catalyst-system', name: 'plane-isolation' },
          spec: { podSelector: {} },
        },
      ],
    )
    const { nodes, edges } = k8sToGraph(s)
    const np = nodes.find((n) => n.id === 'NetworkPolicy:catalyst-system/plane-isolation')
    expect(np).toBeDefined()
    expect(np?.type).toBe('NetworkPolicy')
    expect(np?.status).toBe('healthy')
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'NetworkPolicy:catalyst-system/plane-isolation',
        target: 'Namespace:catalyst-system',
        type: 'member-of',
      }),
    )
  })
})

describe('mergeGraphs', () => {
  it('collapses cloud-side WorkerNode and k8s Node by id, K8s status wins', () => {
    const cloud = {
      nodes: [
        {
          id: 'Cluster:c',
          type: 'Cluster' as const,
          label: 'c',
          status: 'healthy' as const,
        },
        {
          id: 'WorkerNode:n1',
          type: 'WorkerNode' as const,
          label: 'n1',
          status: 'unknown' as const,
          metadata: { sku: 'cpx21' },
        },
      ],
      edges: [
        {
          id: 'e:Cluster:c->WorkerNode:n1',
          source: 'Cluster:c',
          target: 'WorkerNode:n1',
          type: 'runs-on' as const,
        },
      ],
    }
    const k8s = {
      nodes: [
        {
          id: 'WorkerNode:n1',
          type: 'WorkerNode' as const,
          label: 'n1',
          status: 'healthy' as const,
          metadata: { ip: '10.0.0.1' },
        },
        // Pod must exist so the runs-on edge survives mergeGraphs's
        // dangling-endpoint filter.
        {
          id: 'Pod:foo/p',
          type: 'Pod' as const,
          label: 'p',
          status: 'healthy' as const,
        },
      ],
      edges: [
        {
          id: 'e:Pod:foo/p->WorkerNode:n1',
          source: 'Pod:foo/p',
          target: 'WorkerNode:n1',
          type: 'runs-on' as const,
        },
      ],
    }
    const merged = mergeGraphs(cloud, k8s)
    const node = merged.nodes.find((n) => n.id === 'WorkerNode:n1')!
    expect(node.status).toBe('healthy')
    // sku from cloud + ip from k8s both retained
    expect(node.metadata).toEqual(expect.objectContaining({ sku: 'cpx21', ip: '10.0.0.1' }))
    // Both inbound edges retained (cluster + pod), no parallel duplicate.
    expect(merged.edges.filter((e) => e.target === 'WorkerNode:n1')).toHaveLength(2)
  })

  it('drops edges whose endpoints are not present in either graph', () => {
    const cloud = {
      nodes: [{ id: 'A:1', type: 'Cloud' as const, label: 'A' }],
      edges: [{ id: 'e1', source: 'A:1', target: 'B:2', type: 'contains' as const }],
    }
    const k8s = { nodes: [], edges: [] }
    const merged = mergeGraphs(cloud, k8s)
    expect(merged.edges).toEqual([])
  })

  it('#4732(4): collapses cloud/k8s WorkerNodes by InternalIP when ids differ (Huawei naming), rewriting edges', () => {
    // Cloud side names nodes from the RegionSpec; k8s side uses the real
    // Node name — ids NEVER match on Huawei. Same InternalIP = same node.
    const cloud = {
      nodes: [
        { id: 'Cluster:c', type: 'Cluster' as const, label: 'c', status: 'healthy' as const },
        {
          id: 'WorkerNode:worker-1-me-east-215-a',
          type: 'WorkerNode' as const,
          label: 'worker-1-me-east-215-a',
          status: 'unknown' as const,
          metadata: { sku: 's3.large', ip: '192.168.0.44' },
        },
      ],
      edges: [
        {
          id: 'e:Cluster:c->WorkerNode:worker-1-me-east-215-a',
          source: 'Cluster:c',
          target: 'WorkerNode:worker-1-me-east-215-a',
          type: 'runs-on' as const,
        },
      ],
    }
    const k8s = {
      nodes: [
        {
          id: 'WorkerNode:catalyst-hw220-907629bc-me-east-215-a-w1d535a',
          type: 'WorkerNode' as const,
          label: 'catalyst-hw220-907629bc-me-east-215-a-w1d535a',
          status: 'healthy' as const,
          metadata: { ip: '192.168.0.44', kubeletVersion: 'v1.31.4+k3s1' },
        },
        { id: 'Pod:foo/p', type: 'Pod' as const, label: 'p', status: 'healthy' as const },
      ],
      edges: [
        {
          id: 'e:Pod:foo/p->WorkerNode:catalyst',
          source: 'Pod:foo/p',
          target: 'WorkerNode:catalyst-hw220-907629bc-me-east-215-a-w1d535a',
          type: 'runs-on' as const,
        },
      ],
    }
    const merged = mergeGraphs(cloud, k8s)
    // ONE WorkerNode survives — the double-render (12 real → 24 shown) is dead.
    const workers = merged.nodes.filter((n) => n.type === 'WorkerNode')
    expect(workers).toHaveLength(1)
    const node = workers[0]!
    expect(node.id).toBe('WorkerNode:worker-1-me-east-215-a')
    // K8s live status wins; cloud sku + k8s kubeletVersion both retained.
    expect(node.status).toBe('healthy')
    expect(node.metadata).toEqual(
      expect.objectContaining({ sku: 's3.large', kubeletVersion: 'v1.31.4+k3s1' }),
    )
    // The Pod runs-on edge is REWRITTEN to the surviving cloud id, not dropped.
    const podEdge = merged.edges.find((e) => e.source === 'Pod:foo/p')!
    expect(podEdge.target).toBe('WorkerNode:worker-1-me-east-215-a')
    expect(merged.edges.filter((e) => e.target === 'WorkerNode:worker-1-me-east-215-a')).toHaveLength(2)
  })

  it('#4732(4): WorkerNodes without an IP never collapse (no false identity)', () => {
    const cloud = {
      nodes: [
        { id: 'WorkerNode:a', type: 'WorkerNode' as const, label: 'a', status: 'unknown' as const, metadata: {} },
      ],
      edges: [],
    }
    const k8s = {
      nodes: [
        { id: 'WorkerNode:b', type: 'WorkerNode' as const, label: 'b', status: 'healthy' as const, metadata: { ip: '' } },
      ],
      edges: [],
    }
    const merged = mergeGraphs(cloud, k8s)
    expect(merged.nodes.filter((n) => n.type === 'WorkerNode')).toHaveLength(2)
  })

  // ── #4814: declared-vs-live WorkerNode over-count ─────────────────
  // Builds the exact hw232 shape: the declared side (topology_loader
  // buildNodes) emits synthetic ids + EMPTY worker IPs, so neither the
  // id match nor the #4732 IP-collapse can fold a declared leaf onto its
  // live twin. Without rule 3 the graph doubles (declared + live).
  function declaredRegion(region: string, workers: number, cpEip: string) {
    const clusterId = `Cluster:cl-${region}`
    const nodes = [
      { id: clusterId, type: 'Cluster' as const, label: `cl-${region}`, status: 'healthy' as const },
      {
        id: `WorkerNode:node-cp-${region}`,
        type: 'WorkerNode' as const,
        label: `node-cp-${region}`,
        status: 'unknown' as const,
        // Declared CP carries the public EIP — NOT the live InternalIP.
        metadata: { sku: 's3.large', role: 'control-plane', ip: cpEip },
      },
    ]
    const edges = [
      { id: `e:${nodes[1]!.id}->${clusterId}`, source: nodes[1]!.id, target: clusterId, type: 'runs-on' as const },
    ]
    for (let i = 0; i < workers; i++) {
      const id = `WorkerNode:node-w-${i}-${region}`
      nodes.push({
        id,
        type: 'WorkerNode' as const,
        label: `node-w-${i}-${region}`,
        status: 'unknown' as const,
        // Declared WORKER ip is EMPTY (topology_loader buildNodes).
        metadata: { sku: 's3.large', role: 'worker', ip: '' } as Record<string, string>,
      } as (typeof nodes)[number])
      edges.push({ id: `e:${id}->${clusterId}`, source: id, target: clusterId, type: 'runs-on' as const })
    }
    return { nodes, edges }
  }

  function liveRegion(region: string, workers: number, ipBase: string) {
    const nodes = [
      {
        id: `WorkerNode:catalyst-hw232-${region}-cp1`,
        type: 'WorkerNode' as const,
        label: `catalyst-hw232-${region}-cp1`,
        status: 'healthy' as const,
        metadata: { ip: `${ipBase}.10`, kubeletVersion: 'v1.31.4+k3s1' } as Record<string, string>,
      },
    ]
    for (let i = 0; i < workers; i++) {
      nodes.push({
        id: `WorkerNode:catalyst-hw232-${region}-w${i}abc`,
        type: 'WorkerNode' as const,
        label: `catalyst-hw232-${region}-w${i}abc`,
        status: 'healthy' as const,
        metadata: { ip: `${ipBase}.${20 + i}`, kubeletVersion: 'v1.31.4+k3s1' } as Record<string, string>,
      })
    }
    return { nodes, edges: [] as GraphEdge[] }
  }

  function concat(...gs: Array<{ nodes: GraphNode[]; edges: GraphEdge[] }>) {
    return { nodes: gs.flatMap((g) => g.nodes), edges: gs.flatMap((g) => g.edges) }
  }

  it('#4814: two live regions ×(1 cp + 5 workers) collapse 24→12 (count live, not declared)', () => {
    // hw232: BOTH regions fully live. 12 declared (synthetic id, empty
    // worker IP) + 12 live (real name + IP) → must render 12, not 24.
    const cloud = concat(
      declaredRegion('me-east-215-a', 5, '203.0.113.1'),
      declaredRegion('me-east-215-b', 5, '203.0.113.2'),
    )
    const k8s = concat(
      liveRegion('me-east-215-a', 5, '10.209.1'),
      liveRegion('me-east-215-b', 5, '10.219.1'),
    )
    // Before the fix: 12 + 12 = 24.
    expect(cloud.nodes.filter((n) => n.type === 'WorkerNode')).toHaveLength(12)
    expect(k8s.nodes.filter((n) => n.type === 'WorkerNode')).toHaveLength(12)

    const merged = mergeGraphs(cloud, k8s)
    const workers = merged.nodes.filter((n) => n.type === 'WorkerNode')
    expect(workers).toHaveLength(12)
    // Every surviving WorkerNode is a live leaf (real name), carrying
    // the live Ready status — no synthetic declared placeholder remains.
    expect(workers.every((n) => n.id.startsWith('WorkerNode:catalyst-hw232-'))).toBe(true)
    expect(workers.every((n) => n.status === 'healthy')).toBe(true)
    // The declared Cluster structural layer is preserved.
    expect(merged.nodes.filter((n) => n.type === 'Cluster')).toHaveLength(2)
  })

  it('#4814: declared-only preview (no live nodes) keeps every declared WorkerNode leaf', () => {
    // Pre-convergence: the k8s stream is empty. Nothing is suppressed —
    // the declared topology still renders in full.
    const cloud = concat(declaredRegion('me-east-215-a', 5, '203.0.113.1'))
    const merged = mergeGraphs(cloud, { nodes: [], edges: [] })
    // 1 cp + 5 workers = 6 declared leaves, all kept.
    expect(merged.nodes.filter((n) => n.type === 'WorkerNode')).toHaveLength(6)
    // Their runs-on → Cluster edges survive too.
    expect(merged.edges.filter((e) => e.type === 'runs-on')).toHaveLength(6)
  })

  it('#4814: a live-absent secondary region is counted by its live nodes only (no fabricated leaves)', () => {
    // region-a live (1 cp + 5 workers = 6 real), region-b declared but
    // NOT provisioned (0 live). The over-count fix reports the 6 live
    // nodes — region-b contributes no fabricated WorkerNode leaves.
    const cloud = concat(
      declaredRegion('me-east-215-a', 5, '203.0.113.1'),
      declaredRegion('me-east-215-b', 5, '203.0.113.2'),
    )
    const k8s = concat(liveRegion('me-east-215-a', 5, '10.209.1'))
    const merged = mergeGraphs(cloud, k8s)
    expect(merged.nodes.filter((n) => n.type === 'WorkerNode')).toHaveLength(6)
    // Both declared Clusters remain so region-b's declared capacity is
    // still structurally visible (degraded, live-empty).
    expect(merged.nodes.filter((n) => n.type === 'Cluster')).toHaveLength(2)
  })

  it('deduplicates edges that appear in both adapters with the same (source,target,type)', () => {
    const cloud = {
      nodes: [
        { id: 'X:1', type: 'Cloud' as const, label: 'x' },
        { id: 'Y:1', type: 'Region' as const, label: 'y' },
      ],
      edges: [{ id: 'e:cloud', source: 'X:1', target: 'Y:1', type: 'contains' as const }],
    }
    const k8s = {
      nodes: [],
      edges: [{ id: 'e:k8s', source: 'X:1', target: 'Y:1', type: 'contains' as const }],
    }
    const merged = mergeGraphs(cloud, k8s)
    expect(merged.edges).toHaveLength(1)
  })
})
