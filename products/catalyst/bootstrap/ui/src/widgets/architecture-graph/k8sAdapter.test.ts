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
