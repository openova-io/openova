/**
 * flowLayoutOrganic — regression tests focused on cycle-protection
 * (bug #476).
 *
 * The browser hung indefinitely when the operator clicked any job in
 * the Jobs table. Root cause: `adaptDerivedJobsToFlat` synthesised a
 * "Cluster Bootstrap" group whose slug equalled the bare leaf id, so
 * `byId.set(j.id, j)` was last-wins and the leaf overwrote the group.
 * The leaf's `parentId` then pointed at itself and `isVisible()`
 * walked the chain forever.
 *
 * The fix has two layers — both locked here:
 *   1. The group slug was renamed (`phase-1-bootstrap`) so it cannot
 *      collide with any leaf id.
 *   2. The layout's parent-chain walks now track visited ids so a
 *      malformed input degrades gracefully rather than freezing the
 *      browser.
 */

import { describe, it, expect } from 'vitest'
import {
  flowLayoutOrganic,
  defaultFoldedAtDepth,
  FALLBACK_REGION_ID,
} from './flowLayoutOrganic'
import type { Job } from './jobs.types'

const REGIONS = [{ id: FALLBACK_REGION_ID, label: 'Primary' }]
const FAMILIES = [{ id: 'catalyst', label: 'Catalyst', color: '#fff' }]
const HINTS = new Map()

function deadline<T>(label: string, fn: () => T, ms: number): T {
  const t0 = Date.now()
  const r = fn()
  const dt = Date.now() - t0
  if (dt > ms) {
    throw new Error(`${label} took ${dt}ms (budget ${ms}ms)`)
  }
  return r
}

describe('flowLayoutOrganic — cycle protection (bug #476)', () => {
  it('returns within 100ms when a leaf has parentId === its own id (self-cycle)', () => {
    const flat: Job[] = [
      {
        id: 'a',
        jobName: 'a',
        type: 'install',
        appId: 'x',
        parentId: 'a', // self-reference — pathological but possible after id collisions
        dependsOn: [],
        childIds: [],
        status: 'pending',
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
    ]
    const layout = deadline(
      'self-cycle layout',
      () =>
        flowLayoutOrganic(flat, {
          hints: HINTS,
          regions: REGIONS,
          families: FAMILIES,
          folded: new Set<string>(),
        }),
      100,
    )
    expect(layout.nodes.length).toBe(1)
  })

  it('returns within 100ms when two ids collide and produce a self-reference (the original #476 shape)', () => {
    // Reproduces the exact byId.set last-wins shape: one synthesised
    // group + one leaf, both id='cluster-bootstrap'. Leaf's parentId
    // points at the slug, byId.get(parentId) returns the leaf itself.
    const flat: Job[] = [
      {
        id: 'cluster-bootstrap',
        jobName: 'cluster-bootstrap',
        displayName: 'Cluster Bootstrap',
        type: 'group',
        appId: '',
        parentId: '',
        dependsOn: [],
        childIds: ['cluster-bootstrap'],
        status: 'pending',
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
      {
        id: 'cluster-bootstrap',
        jobName: 'cluster-bootstrap',
        type: 'install',
        appId: 'cluster-bootstrap',
        parentId: 'cluster-bootstrap',
        dependsOn: [],
        childIds: [],
        status: 'pending',
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
    ]
    const layout = deadline(
      'collision layout',
      () =>
        flowLayoutOrganic(flat, {
          hints: HINTS,
          regions: REGIONS,
          families: FAMILIES,
          folded: new Set<string>(),
        }),
      100,
    )
    // We don't assert on node count — byId.set is last-wins so the
    // emitted set is implementation-defined. The behavioural contract
    // is "do not hang"; if we got here the contract holds.
    expect(layout.nodes.length).toBeGreaterThanOrEqual(1)
  })

  it('returns within 100ms when a leaf chain forms a multi-step cycle (a→b→a)', () => {
    const flat: Job[] = [
      {
        id: 'a',
        jobName: 'a',
        type: 'install',
        appId: '',
        parentId: 'b',
        dependsOn: [],
        childIds: [],
        status: 'pending',
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
      {
        id: 'b',
        jobName: 'b',
        type: 'install',
        appId: '',
        parentId: 'a',
        dependsOn: [],
        childIds: [],
        status: 'pending',
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
    ]
    const layout = deadline(
      'a-b-a cycle',
      () =>
        flowLayoutOrganic(flat, {
          hints: HINTS,
          regions: REGIONS,
          families: FAMILIES,
          folded: new Set<string>(),
        }),
      100,
    )
    expect(layout.nodes.length).toBe(2)
  })
})

describe('flowLayoutOrganic — parent-elision (bug #481 round 2)', () => {
  // The founder's directive (2026-05-02): when a group is unfolded
  // and its children are visible, the group itself disappears from
  // the canvas. Its inbound deps are rewired to its children; its
  // outbound deps lift onto each child ("parent calling their
  // parents").
  function leaf(id: string, parentId: string, deps: string[] = []): Job {
    return {
      id,
      jobName: id,
      type: 'install',
      appId: id,
      parentId,
      dependsOn: deps,
      childIds: [],
      status: 'pending',
      startedAt: null,
      finishedAt: null,
      durationMs: 0,
    }
  }

  function group(id: string, parentId: string, childIds: string[], deps: string[] = []): Job {
    return {
      id,
      jobName: id,
      displayName: id,
      type: 'group',
      appId: '',
      parentId,
      dependsOn: deps,
      childIds,
      status: 'pending',
      startedAt: null,
      finishedAt: null,
      durationMs: 0,
    }
  }

  it('elides an unfolded group when its children are visible', () => {
    const flat: Job[] = [
      group('apps', '', ['c1', 'c2', 'c3']),
      leaf('c1', 'apps'),
      leaf('c2', 'apps'),
      leaf('c3', 'apps'),
    ]
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(),
    })
    const ids = new Set(layout.nodes.map((n) => n.id))
    // Group is elided: only the children render.
    expect(ids.has('apps')).toBe(false)
    expect(ids.has('c1')).toBe(true)
    expect(ids.has('c2')).toBe(true)
    expect(ids.has('c3')).toBe(true)
    expect(layout.nodes.length).toBe(3)
    // No parent→child edges from the elided group.
    for (const e of layout.edges) {
      expect(e.fromId).not.toBe('apps')
      expect(e.toId).not.toBe('apps')
    }
  })

  it('keeps a folded group as a single node and hides its children', () => {
    const flat: Job[] = [
      group('apps', '', ['c1', 'c2']),
      leaf('c1', 'apps'),
      leaf('c2', 'apps'),
    ]
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(['apps']),
    })
    const ids = new Set(layout.nodes.map((n) => n.id))
    expect(ids.has('apps')).toBe(true)
    expect(ids.has('c1')).toBe(false)
    expect(ids.has('c2')).toBe(false)
  })

  it('rewires inbound deps to children: foundation→apps becomes foundation→c1, foundation→c2', () => {
    const flat: Job[] = [
      leaf('foundation', ''),
      group('apps', '', ['c1', 'c2'], ['foundation']),
      leaf('c1', 'apps'),
      leaf('c2', 'apps'),
    ]
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(),
    })
    const edgeKeys = new Set(layout.edges.map((e) => `${e.fromId}→${e.toId}`))
    // The "apps depends on foundation" relationship must be honoured by
    // each visible child once apps is elided.
    expect(edgeKeys.has('foundation→c1')).toBe(true)
    expect(edgeKeys.has('foundation→c2')).toBe(true)
    // No edge points at the elided apps node.
    for (const e of layout.edges) {
      expect(e.fromId).not.toBe('apps')
      expect(e.toId).not.toBe('apps')
    }
  })

  it('fans an inbound depends-on edge across all visible children of an elided group', () => {
    const flat: Job[] = [
      group('apps', '', ['c1', 'c2']),
      leaf('c1', 'apps'),
      leaf('c2', 'apps'),
      leaf('sentinel', '', ['apps']),
    ]
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(),
    })
    const edgeKeys = new Set(layout.edges.map((e) => `${e.fromId}→${e.toId}`))
    // sentinel depended on the elided apps → fans out to every child.
    expect(edgeKeys.has('c1→sentinel')).toBe(true)
    expect(edgeKeys.has('c2→sentinel')).toBe(true)
    // No edge mentions apps.
    for (const e of layout.edges) {
      expect(e.fromId).not.toBe('apps')
      expect(e.toId).not.toBe('apps')
    }
  })

  it('caps depth at MAX_VISIBLE_DEPTH for malformed deep chains (defence-in-depth)', () => {
    // 50-leaf chain — each depends on the previous. Even without
    // elision-eligible groups, the depth cap kicks in.
    const CHAIN = 50
    const flat: Job[] = []
    for (let i = 0; i < CHAIN; i++) {
      flat.push(leaf(`n${i}`, '', i > 0 ? [`n${i - 1}`] : []))
    }
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(),
    })
    // Every depth must be ≤ MAX_VISIBLE_DEPTH (8).
    for (const n of layout.nodes) {
      expect(n.depth).toBeLessThanOrEqual(8)
    }
    expect(layout.maxDepth).toBeLessThanOrEqual(8)
  })

  it('collapses a real-shape graph (foundation → apps[c1..c10] → sentinel) to ≤4 visible depths', () => {
    // Mirrors the live otech17 shape that broke #481: a long
    // dependency chain through an unfolded "applications" group.
    const flat: Job[] = [
      leaf('foundation', ''),
      group('apps', '', Array.from({ length: 10 }, (_, i) => `c${i}`), ['foundation']),
      ...Array.from({ length: 10 }, (_, i) => leaf(`c${i}`, 'apps')),
      leaf('sentinel', '', ['apps']),
    ]
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(),
    })
    // 1 (foundation) + 10 (children) + 1 (sentinel) = 12 nodes.
    expect(layout.nodes.length).toBe(12)
    // Depth: foundation=0, c0..c9=1, sentinel=2 → maxDepth=2.
    expect(layout.maxDepth).toBeLessThanOrEqual(2)
  })

  it('handles an empty group gracefully (no children → group still renders)', () => {
    const flat: Job[] = [group('orphan-group', '', [])]
    const layout = flowLayoutOrganic(flat, {
      hints: HINTS,
      regions: REGIONS,
      families: FAMILIES,
      folded: new Set<string>(),
    })
    // No children means no elision: operator still sees the group.
    expect(layout.nodes.map((n) => n.id)).toEqual(['orphan-group'])
  })
})

describe('defaultFoldedAtDepth — cycle protection (bug #476)', () => {
  it('returns within 100ms when a group references its own id as parent', () => {
    const flat: Job[] = [
      {
        id: 'g1',
        jobName: 'g1',
        type: 'group',
        appId: '',
        parentId: 'g1',
        dependsOn: [],
        childIds: [],
        status: 'pending',
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
    ]
    const folded = deadline(
      'self-cycle defaultFoldedAtDepth',
      () => defaultFoldedAtDepth(flat, 1),
      100,
    )
    expect(folded.has('g1')).toBe(true)
  })
})
