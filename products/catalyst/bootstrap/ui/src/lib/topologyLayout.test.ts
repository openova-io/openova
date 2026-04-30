/**
 * topologyLayout.test.ts — pure-function tests for the hierarchical
 * topology layout (issue #228). Mirrors the existing
 * infrastructure.types.test.ts pattern.
 */

import { describe, it, expect } from 'vitest'
import { topologyLayout } from './topologyLayout'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from './infrastructure.types'

const EMPTY: HierarchicalInfrastructure = {
  cloud: [],
  topology: { pattern: 'solo', regions: [] },
  storage: { pvcs: [], buckets: [], volumes: [] },
}

describe('topologyLayout — empty', () => {
  it('returns no nodes / edges for an empty tree', () => {
    const r = topologyLayout(EMPTY)
    expect(r.nodes).toEqual([])
    expect(r.edges).toEqual([])
    expect(r.width).toBeGreaterThan(0)
    expect(r.height).toBeGreaterThan(0)
  })
})

describe('topologyLayout — fixture render', () => {
  const r = topologyLayout(infrastructureTopologyFixture)

  it('produces a node per cloud / region / cluster / vcluster', () => {
    // Fixture: 1 cloud, 2 regions, 2 clusters, 4 vclusters (3 + 1) = 9
    expect(r.nodes.length).toBe(9)
  })

  it('places nodes on 4 distinct depth rows', () => {
    const depths = new Set(r.nodes.map((n) => n.depth))
    expect(Array.from(depths).sort()).toEqual([0, 1, 2, 3])
  })

  it('parent-child edges are emitted between adjacent depths', () => {
    // 2 cloud→region + 2 region→cluster + 4 cluster→vcluster = 8 edges
    expect(r.edges.length).toBe(8)
  })

  it('no overlapping nodes within the same depth row', () => {
    const byDepth = new Map<number, { x: number; width: number }[]>()
    for (const n of r.nodes) {
      const arr = byDepth.get(n.depth) ?? []
      arr.push({ x: n.x, width: n.width })
      byDepth.set(n.depth, arr)
    }
    for (const [, rects] of byDepth) {
      const sorted = [...rects].sort((a, b) => a.x - b.x)
      for (let i = 1; i < sorted.length; i++) {
        const prev = sorted[i - 1]!
        const cur = sorted[i]!
        expect(prev.x + prev.width).toBeLessThanOrEqual(cur.x + 1)
      }
    }
  })

  it('preserves parent-child relationships in node.parentId', () => {
    for (const n of r.nodes) {
      if (n.depth === 0) {
        expect(n.parentId).toBeNull()
      } else {
        expect(n.parentId).toBeTruthy()
        // The parent id must exist in the same layout result.
        expect(r.nodes.find((p) => p.id === n.parentId)).toBeTruthy()
      }
    }
  })

  it('marks vClusters dim by default (no zoom)', () => {
    const vc = r.nodes.filter((n) => n.kind === 'vcluster')
    expect(vc.length).toBeGreaterThan(0)
    for (const n of vc) {
      expect(n.dim).toBe(true)
    }
  })

  it('un-dims vClusters whose parent cluster is zoomed in', () => {
    const r2 = topologyLayout(infrastructureTopologyFixture, {
      zoom: { zoomedClusterId: 'cluster-eu-central-primary' },
    })
    const dmz = r2.nodes.find((n) => n.id === 'vc-eu-central-dmz')
    expect(dmz?.dim).toBe(false)
    // vClusters of a different cluster stay dim.
    const helVc = r2.nodes.find((n) => n.id === 'vc-hel-rtz')
    expect(helVc?.dim).toBe(true)
  })

  it('produces a deterministic layout for the same input', () => {
    const a = topologyLayout(infrastructureTopologyFixture)
    const b = topologyLayout(infrastructureTopologyFixture)
    expect(a).toEqual(b)
  })

  it('emits orthogonal poly-line edges with 4 points each', () => {
    for (const e of r.edges) {
      expect(e.points).toHaveLength(4)
      // First point exits parent.bottom, last enters child.top — same x columns.
      expect(e.points[1]!.y).toBe(e.points[2]!.y)
    }
  })
})

describe('topologyLayout — synthetic 4-depth graph', () => {
  it('renders a no-overlap layout for a 1×1×1×1 tree', () => {
    const tiny: HierarchicalInfrastructure = {
      cloud: [
        {
          id: 'c',
          name: 'cloud',
          provider: 'hetzner',
          regionCount: 1,
          quotaUsed: 0,
          quotaLimit: 10,
        },
      ],
      topology: {
        pattern: 'solo',
        regions: [
          {
            id: 'r',
            name: 'region',
            provider: 'hetzner',
            providerRegion: 'fsn1',
            skuCp: 'cpx32',
            skuWorker: 'cpx32',
            workerCount: 0,
            status: 'healthy',
            clusters: [
              {
                id: 'k',
                name: 'cluster',
                version: '1',
                status: 'healthy',
                nodeCount: 1,
                vclusters: [
                  { id: 'v', name: 'vc', isolationMode: 'rtz', status: 'healthy' },
                ],
                loadBalancers: [],
                nodePools: [],
                nodes: [],
              },
            ],
            networks: [],
          },
        ],
      },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    const r = topologyLayout(tiny)
    expect(r.nodes.map((n) => n.id).sort()).toEqual(['c', 'k', 'r', 'v'])
    expect(r.edges.map((e) => `${e.fromId}->${e.toId}`).sort()).toEqual([
      'c->r',
      'k->v',
      'r->k',
    ])
  })
})
