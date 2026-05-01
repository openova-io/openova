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
