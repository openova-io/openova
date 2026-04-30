/**
 * infrastructure.types.test.ts — pure-function tests for the topology
 * layered layout. Mirrors the depsLayout.test.ts pattern: no jsdom,
 * no React render — just assert the layout function's invariants.
 */

import { describe, it, expect } from 'vitest'
import {
  topologyLayout,
  type TopologyEdge,
  type TopologyNode,
} from './infrastructure.types'

describe('topologyLayout', () => {
  it('returns an empty graph for empty input', () => {
    const result = topologyLayout([], [])
    expect(result.nodes).toEqual([])
    expect(result.edges).toEqual([])
    // padding * 2 = 64, paddingY*2 + maxRow*rowHeight + nodeHeight = 64+0+64 = 128
    expect(result.width).toBe(64)
    expect(result.height).toBe(128)
  })

  it('places nodes on layers keyed off NodeKind', () => {
    const nodes: TopologyNode[] = [
      { id: 'cloud-hetzner', kind: 'cloud', label: 'Hetzner', status: 'healthy', metadata: {} },
      { id: 'region-eu', kind: 'region', label: 'eu-central', status: 'healthy', metadata: {} },
      { id: 'cluster-1', kind: 'cluster', label: 'omantel', status: 'healthy', metadata: {} },
      { id: 'node-w-0', kind: 'node', label: 'worker-1', status: 'healthy', metadata: {} },
    ]
    const result = topologyLayout(nodes, [])
    expect(result.nodes).toHaveLength(4)

    const byId = new Map(result.nodes.map((n) => [n.id, n]))
    expect(byId.get('cloud-hetzner')!.layer).toBe(0)
    expect(byId.get('region-eu')!.layer).toBe(1)
    expect(byId.get('cluster-1')!.layer).toBe(2)
    expect(byId.get('node-w-0')!.layer).toBe(3)
  })

  it('lays nodes on the same layer left-aligned to the same X', () => {
    const nodes: TopologyNode[] = [
      { id: 'node-a', kind: 'node', label: 'a', status: 'healthy', metadata: {} },
      { id: 'node-b', kind: 'node', label: 'b', status: 'healthy', metadata: {} },
      { id: 'node-c', kind: 'node', label: 'c', status: 'healthy', metadata: {} },
    ]
    const result = topologyLayout(nodes, [])
    expect(result.nodes).toHaveLength(3)
    const xs = new Set(result.nodes.map((n) => n.x))
    expect(xs.size).toBe(1) // all same X (same layer)
    const ys = result.nodes.map((n) => n.y).sort((a, b) => a - b)
    expect(ys[1] - ys[0]).toBeGreaterThan(0)
    expect(ys[2] - ys[1]).toBeGreaterThan(0)
  })

  it('emits 4-point orthogonal poly-lines for edges between known nodes', () => {
    const nodes: TopologyNode[] = [
      { id: 'cloud', kind: 'cloud', label: 'h', status: 'healthy', metadata: {} },
      { id: 'cluster', kind: 'cluster', label: 'c', status: 'healthy', metadata: {} },
    ]
    const edges: TopologyEdge[] = [{ from: 'cloud', to: 'cluster', relation: 'contains' }]
    const result = topologyLayout(nodes, edges)
    expect(result.edges).toHaveLength(1)
    const e = result.edges[0]!
    expect(e.from).toBe('cloud')
    expect(e.to).toBe('cluster')
    expect(e.points).toHaveLength(4)
    // First point exits the source's right edge, last point enters
    // the destination's left edge, mid points share a vertical x.
    expect(e.points[1]!.x).toBe(e.points[2]!.x)
  })

  it('drops edges that reference unknown node ids', () => {
    const nodes: TopologyNode[] = [
      { id: 'a', kind: 'cluster', label: 'a', status: 'healthy', metadata: {} },
    ]
    const edges: TopologyEdge[] = [
      { from: 'a', to: 'missing', relation: 'contains' },
      { from: 'ghost', to: 'a', relation: 'contains' },
    ]
    const result = topologyLayout(nodes, edges)
    expect(result.edges).toEqual([])
  })

  it('produces a deterministic layout for the same input', () => {
    const nodes: TopologyNode[] = [
      { id: 'b-cluster', kind: 'cluster', label: 'b', status: 'healthy', metadata: {} },
      { id: 'a-cluster', kind: 'cluster', label: 'a', status: 'healthy', metadata: {} },
    ]
    const r1 = topologyLayout(nodes, [])
    const r2 = topologyLayout(nodes, [])
    expect(r1).toEqual(r2)
    // Sort-by-id within layer means a-cluster precedes b-cluster.
    const ids = r1.nodes.map((n) => n.id)
    expect(ids).toEqual(['a-cluster', 'b-cluster'])
  })

  it('honours custom layout options', () => {
    const nodes: TopologyNode[] = [
      { id: 'n', kind: 'cluster', label: 'n', status: 'healthy', metadata: {} },
    ]
    const result = topologyLayout(nodes, [], {
      nodeWidth: 100,
      nodeHeight: 40,
      paddingX: 10,
      paddingY: 10,
      colWidth: 150,
      rowHeight: 60,
    })
    expect(result.nodes[0]!.x).toBe(10 + 2 * 150) // layer 2 (cluster) * colWidth + paddingX
    expect(result.nodes[0]!.y).toBe(10) // top of column + paddingY
    expect(result.width).toBe(10 * 2 + 2 * 150 + 100) // padding*2 + (layers-1)*colW + nodeW = 420
  })
})
