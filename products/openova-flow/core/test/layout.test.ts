/**
 * @openova/flow-core — layout regression suite. Ports
 * `flowLayoutOrganic.test.ts` to the new contract:
 *
 *   • Legacy `Job` shape (parentId / dependsOn) → new
 *     FlowNode + Relationship[] (contains / finish-to-start).
 *   • Same cycle-protection, parent-elision, MAX_VISIBLE_DEPTH cap
 *     assertions.
 *
 * Every test exercises the founder-locked invariants documented in
 * the legacy: bug #476 (cycle hangs), bug #481 round 2 (parent
 * elision + lift + fan-out), MAX_VISIBLE_DEPTH defence-in-depth.
 */

import { describe, it, expect } from 'vitest'
import {
  layout,
  defaultFoldedAtDepth,
  FALLBACK_REGION_ID,
  MAX_VISIBLE_DEPTH,
  type FlowLayoutHint,
} from '../src/index'
import type { FlowInstance, FlowNode, Relationship } from '../src/index'

const FLOW: FlowInstance = {
  id: 'test-flow',
  status: 'running',
  startedAt: 0,
}

const REGIONS = [{ id: FALLBACK_REGION_ID, label: 'Primary' }]
const FAMILIES = [{ id: 'catalyst', label: 'Catalyst', color: '#fff' }]
const HINTS = new Map<string, FlowLayoutHint>()

function deadline<T>(label: string, fn: () => T, ms: number): T {
  const t0 = Date.now()
  const r = fn()
  const dt = Date.now() - t0
  if (dt > ms) {
    throw new Error(`${label} took ${dt}ms (budget ${ms}ms)`)
  }
  return r
}

function leaf(id: string): FlowNode {
  return { id, flowId: FLOW.id, label: id, status: 'pending' }
}

function group(id: string): FlowNode {
  return { id, flowId: FLOW.id, label: id, status: 'pending' }
}

function contains(parent: string, child: string): Relationship {
  return { fromId: child, toId: parent, type: 'contains' }
}

function fs(from: string, to: string): Relationship {
  return { fromId: from, toId: to, type: 'finish-to-start', condition: 'on-success' }
}

describe('@openova/flow-core layout — cycle protection (bug #476)', () => {
  it('returns within 100ms when a leaf has parentId === its own id (self-cycle)', () => {
    const nodes: FlowNode[] = [leaf('a')]
    // Synthesise the original pathology: a `contains` rel pointing
    // from a node to itself. The cycle guard in isVisible /
    // visibleRepresentative must terminate.
    const rels: Relationship[] = [{ fromId: 'a', toId: 'a', type: 'contains' }]
    const out = deadline(
      'self-cycle layout',
      () =>
        layout({
          flow: FLOW,
          nodes,
          relationships: rels,
          folded: new Set(),
          hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
        }),
      100,
    )
    expect(out.positionedNodes.length).toBe(1)
  })

  it('returns within 100ms when two ids collide and produce a self-reference (original #476 shape)', () => {
    const nodes: FlowNode[] = [
      { ...group('cluster-bootstrap'), label: 'Cluster Bootstrap' },
      leaf('cluster-bootstrap'),
    ]
    const rels: Relationship[] = [
      // The original group had childIds=['cluster-bootstrap'] AND the
      // leaf's parentId='cluster-bootstrap'. Both shapes encoded as
      // `contains` edges below — self-pointing on the collided id.
      { fromId: 'cluster-bootstrap', toId: 'cluster-bootstrap', type: 'contains' },
    ]
    const out = deadline(
      'collision layout',
      () =>
        layout({
          flow: FLOW,
          nodes,
          relationships: rels,
          folded: new Set(),
          hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
        }),
      100,
    )
    expect(out.positionedNodes.length).toBeGreaterThanOrEqual(1)
  })

  it('returns within 100ms when a leaf chain forms a multi-step cycle (a→b→a)', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b')]
    const rels: Relationship[] = [
      { fromId: 'a', toId: 'b', type: 'contains' },
      { fromId: 'b', toId: 'a', type: 'contains' },
    ]
    const out = deadline(
      'a-b-a cycle',
      () =>
        layout({
          flow: FLOW,
          nodes,
          relationships: rels,
          folded: new Set(),
          hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
        }),
      100,
    )
    expect(out.positionedNodes.length).toBe(2)
  })
})

describe('@openova/flow-core layout — parent-elision (bug #481 round 2)', () => {
  it('elides an unfolded group when its children are visible', () => {
    const nodes: FlowNode[] = [
      group('apps'),
      leaf('c1'),
      leaf('c2'),
      leaf('c3'),
    ]
    const rels: Relationship[] = [
      contains('apps', 'c1'),
      contains('apps', 'c2'),
      contains('apps', 'c3'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const ids = new Set(out.positionedNodes.map((n) => n.id))
    expect(ids.has('apps')).toBe(false)
    expect(ids.has('c1')).toBe(true)
    expect(ids.has('c2')).toBe(true)
    expect(ids.has('c3')).toBe(true)
    expect(out.positionedNodes.length).toBe(3)
    for (const e of out.edges) {
      expect(e.fromId).not.toBe('apps')
      expect(e.toId).not.toBe('apps')
    }
  })

  it('keeps a folded group as a single node and hides its children', () => {
    const nodes: FlowNode[] = [group('apps'), leaf('c1'), leaf('c2')]
    const rels: Relationship[] = [contains('apps', 'c1'), contains('apps', 'c2')]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(['apps']),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const ids = new Set(out.positionedNodes.map((n) => n.id))
    expect(ids.has('apps')).toBe(true)
    expect(ids.has('c1')).toBe(false)
    expect(ids.has('c2')).toBe(false)
  })

  it('rewires inbound deps to children: foundation→apps becomes foundation→{c1,c2}', () => {
    const nodes: FlowNode[] = [
      leaf('foundation'),
      group('apps'),
      leaf('c1'),
      leaf('c2'),
    ]
    const rels: Relationship[] = [
      contains('apps', 'c1'),
      contains('apps', 'c2'),
      fs('foundation', 'apps'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const edgeKeys = new Set(out.edges.map((e) => `${e.fromId}→${e.toId}`))
    expect(edgeKeys.has('foundation→c1')).toBe(true)
    expect(edgeKeys.has('foundation→c2')).toBe(true)
    for (const e of out.edges) {
      expect(e.fromId).not.toBe('apps')
      expect(e.toId).not.toBe('apps')
    }
  })

  it('fans an inbound depends-on edge across all visible children of an elided group', () => {
    const nodes: FlowNode[] = [
      group('apps'),
      leaf('c1'),
      leaf('c2'),
      leaf('sentinel'),
    ]
    const rels: Relationship[] = [
      contains('apps', 'c1'),
      contains('apps', 'c2'),
      fs('apps', 'sentinel'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const edgeKeys = new Set(out.edges.map((e) => `${e.fromId}→${e.toId}`))
    expect(edgeKeys.has('c1→sentinel')).toBe(true)
    expect(edgeKeys.has('c2→sentinel')).toBe(true)
    for (const e of out.edges) {
      expect(e.fromId).not.toBe('apps')
      expect(e.toId).not.toBe('apps')
    }
  })

  it('caps depth at MAX_VISIBLE_DEPTH for malformed deep chains (defence-in-depth)', () => {
    const CHAIN = 50
    const nodes: FlowNode[] = []
    const rels: Relationship[] = []
    for (let i = 0; i < CHAIN; i++) {
      nodes.push(leaf(`n${i}`))
      if (i > 0) rels.push(fs(`n${i - 1}`, `n${i}`))
    }
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    for (const n of out.positionedNodes) {
      expect(n.depth).toBeLessThanOrEqual(MAX_VISIBLE_DEPTH)
    }
    expect(out.maxDepth).toBeLessThanOrEqual(MAX_VISIBLE_DEPTH)
  })

  it('collapses a real-shape graph (foundation → apps[c1..c9] → sentinel) to ≤4 visible depths', () => {
    const nodes: FlowNode[] = [
      leaf('foundation'),
      group('apps'),
      ...Array.from({ length: 10 }, (_, i) => leaf(`c${i}`)),
      leaf('sentinel'),
    ]
    const rels: Relationship[] = [
      ...Array.from({ length: 10 }, (_, i) => contains('apps', `c${i}`)),
      fs('foundation', 'apps'),
      fs('apps', 'sentinel'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    // 1 (foundation) + 10 (children) + 1 (sentinel) = 12 nodes.
    expect(out.positionedNodes.length).toBe(12)
    expect(out.maxDepth).toBeLessThanOrEqual(2)
  })

  it('handles an empty group gracefully (no children → group still renders)', () => {
    const nodes: FlowNode[] = [group('orphan-group')]
    const rels: Relationship[] = []
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    // No children → no elision; the group still renders even though
    // technically it has no `contains` edges (and thus isn't a
    // "group" under the new contract). The node MUST still appear.
    expect(out.positionedNodes.map((n) => n.id)).toEqual(['orphan-group'])
  })
})

describe('@openova/flow-core layout — relationship-type edge tagging', () => {
  it('preserves FS / SS / FF / SF / triggers as separate edge tags', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b'), leaf('c'), leaf('d'), leaf('e'), leaf('f')]
    const rels: Relationship[] = [
      { fromId: 'a', toId: 'b', type: 'finish-to-start' },
      { fromId: 'b', toId: 'c', type: 'start-to-start' },
      { fromId: 'c', toId: 'd', type: 'finish-to-finish' },
      { fromId: 'd', toId: 'e', type: 'start-to-finish' },
      { fromId: 'e', toId: 'f', type: 'triggers' },
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const byKey = new Map(out.edges.map((e) => [`${e.fromId}→${e.toId}`, e.relType]))
    expect(byKey.get('a→b')).toBe('finish-to-start')
    expect(byKey.get('b→c')).toBe('start-to-start')
    expect(byKey.get('c→d')).toBe('finish-to-finish')
    expect(byKey.get('d→e')).toBe('start-to-finish')
    expect(byKey.get('e→f')).toBe('triggers')
  })

  it('does NOT count on-failure edges toward depth', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b'), leaf('c')]
    const rels: Relationship[] = [
      { fromId: 'a', toId: 'b', type: 'finish-to-start', condition: 'on-success' },
      // failure overlay from b → c — must not push c past depth 1
      // (since b is the only blocking pred of c is a failure-only edge).
      { fromId: 'b', toId: 'c', type: 'finish-to-start', condition: 'on-failure' },
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const depthOf = new Map(out.positionedNodes.map((n) => [n.id, n.depth]))
    expect(depthOf.get('a')).toBe(0)
    expect(depthOf.get('b')).toBe(1)
    expect(depthOf.get('c')).toBe(0)
  })

  it('tags cross-region edges with crossRegion=true', () => {
    const regions = [
      { id: 'eu', label: 'EU' },
      { id: 'us', label: 'US' },
    ]
    const nodes: FlowNode[] = [
      { ...leaf('a'), region: 'eu' },
      { ...leaf('b'), region: 'us' },
    ]
    const rels: Relationship[] = [fs('a', 'b')]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions, families: FAMILIES },
    })
    expect(out.edges.length).toBe(1)
    expect(out.edges[0].crossRegion).toBe(true)
  })

  it('tags cross-flow edges with crossFlow=true', () => {
    const nodes: FlowNode[] = [
      { id: 'a', flowId: 'flow-1', label: 'a', status: 'pending' },
      { id: 'b', flowId: 'flow-2', label: 'b', status: 'pending' },
    ]
    const rels: Relationship[] = [
      {
        fromId: 'a',
        toId: 'b',
        fromFlowId: 'flow-1',
        toFlowId: 'flow-2',
        type: 'triggers',
      },
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    expect(out.edges.length).toBe(1)
    expect(out.edges[0].crossFlow).toBe(true)
  })
})

describe('@openova/flow-core layout — component metadata', () => {
  it('emits connected-component info covering every node', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b'), leaf('c'), leaf('d')]
    const rels: Relationship[] = [
      fs('a', 'b'),
      // c, d are isolated from a/b.
      fs('c', 'd'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    expect(out.components.length).toBe(2)
    const memberCounts = out.components.map((c) => c.memberIds.length).sort()
    expect(memberCounts).toEqual([2, 2])
  })
})

describe('@openova/flow-core defaultFoldedAtDepth — cycle protection (bug #476)', () => {
  it('returns within 100ms when a group references its own id as parent', () => {
    const nodes: FlowNode[] = [group('g1')]
    const rels: Relationship[] = [
      // Self-pointing contains edge: g1 contains itself.
      { fromId: 'g1', toId: 'g1', type: 'contains' },
    ]
    const out = deadline(
      'self-cycle defaultFoldedAtDepth',
      () => defaultFoldedAtDepth(nodes, rels, 1),
      100,
    )
    expect(out.has('g1')).toBe(true)
  })

  it('depth=all returns an empty fold set', () => {
    const nodes: FlowNode[] = [group('a'), leaf('b')]
    const rels: Relationship[] = [contains('a', 'b')]
    const out = defaultFoldedAtDepth(nodes, rels, 'all')
    expect(out.size).toBe(0)
  })
})

/* ────────────────────────────────────────────────────────────────────
 * Agent #9 — lanes + recursive descendantCount.
 * ──────────────────────────────────────────────────────────────────── */

describe('@openova/flow-core layout — lanes (Agent #9)', () => {
  function laneGroup(id: string, layoutHint: 'lane-horizontal' | 'lane-vertical', sortKey?: number): FlowNode {
    return {
      id,
      flowId: FLOW.id,
      label: id,
      status: 'pending',
      meta: typeof sortKey === 'number'
        ? { layout: layoutHint, isGroup: true, sortKey }
        : { layout: layoutHint, isGroup: true },
    }
  }

  it('emits a LaneDescriptor for each contains-parent whose meta.layout is set', () => {
    const nodes: FlowNode[] = [
      laneGroup('fsn1', 'lane-vertical'),
      laneGroup('hel1', 'lane-vertical'),
      leaf('hr-a'),
      leaf('hr-b'),
    ]
    const rels: Relationship[] = [
      contains('fsn1', 'hr-a'),
      contains('hel1', 'hr-b'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    expect(out.lanes.length).toBe(2)
    const fsn = out.lanes.find((l) => l.id === 'fsn1')
    expect(fsn?.axis).toBe('vertical')
    expect(fsn?.laneDepth).toBe(0)
    expect(fsn?.parentLaneId).toBeNull()
    expect(fsn?.childIds).toContain('hr-a')
  })

  it('nests phase lanes inside region lanes with laneDepth=1', () => {
    const nodes: FlowNode[] = [
      laneGroup('fsn1', 'lane-vertical'),
      laneGroup('fsn1/phase-1', 'lane-horizontal', 1),
      laneGroup('fsn1/phase-2', 'lane-horizontal', 2),
      leaf('hr-a'),
    ]
    const rels: Relationship[] = [
      contains('fsn1', 'fsn1/phase-1'),
      contains('fsn1', 'fsn1/phase-2'),
      contains('fsn1/phase-1', 'hr-a'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const region = out.lanes.find((l) => l.id === 'fsn1')
    const phase1 = out.lanes.find((l) => l.id === 'fsn1/phase-1')
    const phase2 = out.lanes.find((l) => l.id === 'fsn1/phase-2')
    expect(region?.laneDepth).toBe(0)
    expect(phase1?.laneDepth).toBe(1)
    expect(phase2?.laneDepth).toBe(1)
    expect(phase1?.parentLaneId).toBe('fsn1')
    // Phase sortKey ordering: phase-1 before phase-2.
    const phasesInOrder = out.lanes.filter((l) => l.laneDepth === 1).map((l) => l.id)
    expect(phasesInOrder).toEqual(['fsn1/phase-1', 'fsn1/phase-2'])
  })

  it('returns empty lanes when no group declares meta.layout', () => {
    const nodes: FlowNode[] = [leaf('a'), leaf('b')]
    const rels: Relationship[] = [fs('a', 'b')]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    expect(out.lanes).toEqual([])
  })
})

describe('@openova/flow-core layout — descendantCount (Agent #9)', () => {
  it('computes recursive descendant count through contains edges', () => {
    // fsn1 → phase-1 → hr-a, hr-b
    // descendantCount(fsn1) = 3 (phase-1 + hr-a + hr-b)
    // descendantCount(phase-1) = 2 (hr-a + hr-b)
    const nodes: FlowNode[] = [group('fsn1'), group('phase-1'), leaf('hr-a'), leaf('hr-b')]
    const rels: Relationship[] = [
      contains('fsn1', 'phase-1'),
      contains('phase-1', 'hr-a'),
      contains('phase-1', 'hr-b'),
    ]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: rels,
      folded: new Set(['fsn1']),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    const fsn = out.positionedNodes.find((n) => n.id === 'fsn1')
    expect(fsn?.descendantCount).toBe(3)
  })

  it('descendantCount is 0 for leaf nodes', () => {
    const nodes: FlowNode[] = [leaf('a')]
    const out = layout({
      flow: FLOW,
      nodes,
      relationships: [],
      folded: new Set(),
      hints: { perNode: HINTS, regions: REGIONS, families: FAMILIES },
    })
    expect(out.positionedNodes[0]?.descendantCount).toBe(0)
  })
})
