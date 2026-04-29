/**
 * depsLayout.test.ts — lock-in for the topological-layered DAG layout.
 *
 *   • 5-job graph (FIVE_JOB_GRAPH) → 4 layers, no overlapping coordinates,
 *     topological order respected (every dep's layer < dependent's layer).
 *   • Cycle break → reported in `cycles[]`, layout still emits.
 *   • Unknown deps → silently dropped (no crash, no edge emitted).
 */
import { describe, it, expect } from 'vitest'
import { depsLayout } from './depsLayout'
import {
  FIVE_JOB_GRAPH,
  THREE_NODE_CHAIN,
} from '@/test/fixtures/deps-graph.fixture'

describe('depsLayout — five-job graph', () => {
  const result = depsLayout(FIVE_JOB_GRAPH)

  it('emits one node per input job', () => {
    expect(result.nodes.length).toBe(FIVE_JOB_GRAPH.length)
  })

  it('emits one edge per resolvable dep', () => {
    // 1 (init→plan) + 1 (plan→apply) + 1 (apply→bootstrap) + 1 (apply→cilium) = 4
    expect(result.edges.length).toBe(4)
  })

  it('produces 4 layers (longest path is init→plan→apply→bootstrap)', () => {
    expect(result.layerCount).toBe(4)
  })

  it('respects topological order — every parent is to the left of its child', () => {
    const layerOf = new Map(result.nodes.map((n) => [n.id, n.layer]))
    for (const job of FIVE_JOB_GRAPH) {
      const childLayer = layerOf.get(job.id)!
      for (const dep of job.dependsOn) {
        const parentLayer = layerOf.get(dep)!
        expect(parentLayer).toBeLessThan(childLayer)
      }
    }
  })

  it('produces no overlapping (x, y) coordinates', () => {
    const seen = new Set<string>()
    for (const n of result.nodes) {
      const key = `${n.x},${n.y}`
      expect(seen.has(key)).toBe(false)
      seen.add(key)
    }
  })

  it('reports zero cycles for the acyclic graph', () => {
    expect(result.cycles).toEqual([])
  })

  it('places source nodes (no deps) in layer 0', () => {
    const sources = FIVE_JOB_GRAPH.filter((j) => j.dependsOn.length === 0)
    for (const s of sources) {
      const node = result.nodes.find((n) => n.id === s.id)!
      expect(node.layer).toBe(0)
    }
  })

  it('emits edges with 4-point orthogonal poly-lines', () => {
    for (const e of result.edges) {
      expect(e.points.length).toBe(4)
      // First two points share the same y (exit horizontal); last two share y (enter horizontal)
      expect(e.points[0]!.y).toBe(e.points[1]!.y)
      expect(e.points[2]!.y).toBe(e.points[3]!.y)
      // Middle two points share the same x (vertical drop).
      expect(e.points[1]!.x).toBe(e.points[2]!.x)
    }
  })
})

describe('depsLayout — three-node chain', () => {
  const result = depsLayout(THREE_NODE_CHAIN)

  it('emits 3 nodes + 2 edges', () => {
    expect(result.nodes.length).toBe(3)
    expect(result.edges.length).toBe(2)
  })

  it('places the chain in 3 distinct layers', () => {
    expect(result.layerCount).toBe(3)
    const layers = result.nodes.map((n) => n.layer).sort()
    expect(layers).toEqual([0, 1, 2])
  })
})

describe('depsLayout — edge cases', () => {
  it('returns an empty result for an empty input', () => {
    const r = depsLayout([])
    expect(r.nodes).toEqual([])
    expect(r.edges).toEqual([])
    expect(r.layerCount).toBe(0)
  })

  it('drops edges that point at unknown ids', () => {
    const r = depsLayout([
      { id: 'a', dependsOn: ['ghost'] },
      { id: 'b', dependsOn: ['a'] },
    ])
    expect(r.nodes.length).toBe(2)
    expect(r.edges.length).toBe(1)
    expect(r.edges[0]!.from).toBe('a')
    expect(r.edges[0]!.to).toBe('b')
  })

  it('breaks cycles and records them in cycles[]', () => {
    const r = depsLayout([
      { id: 'a', dependsOn: ['b'] },
      { id: 'b', dependsOn: ['a'] },
    ])
    expect(r.nodes.length).toBe(2)
    expect(r.cycles.length).toBeGreaterThan(0)
    // Layout still emits coordinates for both nodes.
    const ids = r.nodes.map((n) => n.id).sort()
    expect(ids).toEqual(['a', 'b'])
  })

  it('respects custom layout options', () => {
    const r = depsLayout(THREE_NODE_CHAIN, {
      colWidth: 300,
      rowHeight: 100,
      paddingX: 50,
      paddingY: 50,
    })
    const a = r.nodes.find((n) => n.id === 'a')!
    expect(a.x).toBe(50) // paddingX
    expect(a.y).toBe(50) // paddingY (single row)
    const c = r.nodes.find((n) => n.id === 'c')!
    expect(c.x).toBe(50 + 2 * 300) // paddingX + 2 * colWidth (layer 2)
  })
})
