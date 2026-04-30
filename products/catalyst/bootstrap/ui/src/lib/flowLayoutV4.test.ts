/**
 * flowLayoutV4.test.ts — unit tests locking the multi-region circular
 * layout contract. Pure-function tests (no React, no DOM).
 */

import { describe, it, expect } from 'vitest'
import {
  flowLayoutV4,
  pointsToPath,
  routeBezier,
  jobProgress,
  DEFAULT_FAMILIES,
  FALLBACK_REGION_ID,
  FALLBACK_FAMILY_ID,
  type FlowFamily,
} from './flowLayoutV4'
import type { Job } from './jobs.types'

function makeJob(over: Partial<Job> & { id: string }): Job {
  return {
    id: over.id,
    jobName: over.jobName ?? over.id,
    appId: over.appId ?? 'app-x',
    batchId: over.batchId ?? 'b1',
    dependsOn: over.dependsOn ?? [],
    status: over.status ?? 'pending',
    startedAt: over.startedAt ?? null,
    finishedAt: over.finishedAt ?? null,
    durationMs: over.durationMs ?? 0,
  }
}

describe('flowLayoutV4 — basic contract', () => {
  it('returns an empty layout for no jobs', () => {
    const out = flowLayoutV4([])
    expect(out.nodes).toEqual([])
    expect(out.edges).toEqual([])
    expect(out.regions).toEqual([])
    expect(out.width).toBeGreaterThan(0)
    expect(out.height).toBeGreaterThan(0)
  })

  it('places a single root job at stage 1 in the fallback region', () => {
    const out = flowLayoutV4([makeJob({ id: 'a' })])
    expect(out.nodes).toHaveLength(1)
    expect(out.nodes[0]!.stage).toBe(1)
    expect(out.nodes[0]!.regionId).toBe(FALLBACK_REGION_ID)
    expect(out.regions).toHaveLength(1)
  })

  it('places a 3-node chain at stages 1, 2, 3', () => {
    const out = flowLayoutV4([
      makeJob({ id: 'a' }),
      makeJob({ id: 'b', dependsOn: ['a'] }),
      makeJob({ id: 'c', dependsOn: ['b'] }),
    ])
    const byId = Object.fromEntries(out.nodes.map((n) => [n.id, n]))
    expect(byId.a!.stage).toBe(1)
    expect(byId.b!.stage).toBe(2)
    expect(byId.c!.stage).toBe(3)
    // Stage 1 anchor radius must be larger than stage 2/3.
    expect(byId.a!.r).toBeGreaterThan(byId.b!.r)
  })

  it('emits an edge per dependsOn entry', () => {
    const out = flowLayoutV4([
      makeJob({ id: 'a' }),
      makeJob({ id: 'b', dependsOn: ['a'] }),
    ])
    expect(out.edges).toHaveLength(1)
    expect(out.edges[0]!.fromId).toBe('a')
    expect(out.edges[0]!.toId).toBe('b')
    expect(out.edges[0]!.kind).toBe('within-region')
  })
})

describe('flowLayoutV4 — multi-region', () => {
  const regions = [
    { id: 'fsn1', label: 'FSN1 · Falkenstein', meta: 'Hetzner · Primary' },
    { id: 'nbg1', label: 'NBG1 · Nuremberg', meta: 'Hetzner · Secondary' },
  ]

  it('partitions jobs into separate region bands', () => {
    const hints = new Map([
      ['a-fsn', { regionId: 'fsn1' }],
      ['a-nbg', { regionId: 'nbg1' }],
    ])
    const out = flowLayoutV4(
      [
        makeJob({ id: 'a-fsn', jobName: 'install-cilium' }),
        makeJob({ id: 'a-nbg', jobName: 'install-cilium' }),
      ],
      { regions, hints },
    )
    expect(out.regions).toHaveLength(2)
    expect(out.regions[0]!.regionId).toBe('fsn1')
    expect(out.regions[1]!.regionId).toBe('nbg1')
    // Each region has exactly one node.
    expect(out.regions[0]!.nodeCount).toBe(1)
    expect(out.regions[1]!.nodeCount).toBe(1)
    // Bottom region's band sits BELOW the top region's band.
    expect(out.regions[1]!.y).toBeGreaterThan(out.regions[0]!.y)
  })

  it('classifies cross-region edges separately', () => {
    const hints = new Map([
      ['a-fsn', { regionId: 'fsn1' }],
      ['a-nbg', { regionId: 'nbg1' }],
    ])
    const out = flowLayoutV4(
      [
        makeJob({ id: 'a-fsn', jobName: 'netbird' }),
        makeJob({ id: 'a-nbg', jobName: 'netbird-mirror', dependsOn: ['a-fsn'] }),
      ],
      { regions, hints },
    )
    expect(out.edges).toHaveLength(1)
    expect(out.edges[0]!.kind).toBe('cross-region')
    // Bezier (4 control points) for cross-region edges.
    expect(out.edges[0]!.points).toHaveLength(4)
  })

  it('renders only injected regions when no hints are provided', () => {
    const out = flowLayoutV4(
      [makeJob({ id: 'a' })],
      { regions: [regions[0]!] },
    )
    // Job lacks region hint -> falls into FALLBACK_REGION_ID, which is
    // appended to regionOrder. fsn1 is empty.
    expect(out.regions.find((r) => r.regionId === 'fsn1')!.nodeCount).toBe(0)
    expect(out.regions.find((r) => r.regionId === FALLBACK_REGION_ID)!.nodeCount).toBe(1)
  })
})

describe('flowLayoutV4 — family colours', () => {
  it('maps known family ids to the default palette', () => {
    const hints = new Map([
      ['a', { familyId: 'spine' }],
      ['b', { familyId: 'guardian' }],
    ])
    const out = flowLayoutV4(
      [makeJob({ id: 'a' }), makeJob({ id: 'b' })],
      { hints },
    )
    expect(out.nodes.find((n) => n.id === 'a')!.familyId).toBe('spine')
    expect(out.nodes.find((n) => n.id === 'b')!.familyId).toBe('guardian')
  })

  it('falls back to platform when family is unknown', () => {
    const hints = new Map([['a', { familyId: 'mystery-family' }]])
    const out = flowLayoutV4([makeJob({ id: 'a' })], { hints })
    expect(out.nodes[0]!.familyId).toBe(FALLBACK_FAMILY_ID)
  })

  it('honours caller-supplied family palette', () => {
    const families: FlowFamily[] = [
      { id: 'custom', label: 'CUSTOM', color: '#FF00FF' },
    ]
    const hints = new Map([['a', { familyId: 'custom' }]])
    const out = flowLayoutV4([makeJob({ id: 'a' })], { families, hints })
    expect(out.nodes[0]!.familyId).toBe('custom')
  })
})

describe('flowLayoutV4 — highlighting', () => {
  it('marks the highlightJobId node as highlighted', () => {
    const out = flowLayoutV4(
      [makeJob({ id: 'a' }), makeJob({ id: 'b' })],
      { highlightJobId: 'b' },
    )
    expect(out.nodes.find((n) => n.id === 'a')!.highlighted).toBe(false)
    expect(out.nodes.find((n) => n.id === 'b')!.highlighted).toBe(true)
  })
})

describe('routeBezier', () => {
  it('returns a 2-point line for span=0 within-region', () => {
    const pts = routeBezier(
      { cx: 0, cy: 0, r: 10 },
      { cx: 0, cy: 60, r: 10 },
      0,
      false,
    )
    expect(pts).toHaveLength(2)
  })

  it('returns a 4-point bezier for span>=1 within-region', () => {
    const pts = routeBezier(
      { cx: 0, cy: 0, r: 10 },
      { cx: 100, cy: 0, r: 10 },
      1,
      false,
    )
    expect(pts).toHaveLength(4)
  })

  it('returns a 4-point bezier for cross-region edges', () => {
    const pts = routeBezier(
      { cx: 0, cy: 0, r: 10 },
      { cx: 0, cy: 200, r: 10 },
      0,
      true,
    )
    expect(pts).toHaveLength(4)
  })
})

describe('pointsToPath', () => {
  it('returns empty for fewer than 2 points', () => {
    expect(pointsToPath([])).toBe('')
    expect(pointsToPath([{ x: 0, y: 0 }])).toBe('')
  })

  it('emits an SVG line for 2 points', () => {
    expect(pointsToPath([{ x: 0, y: 0 }, { x: 10, y: 5 }])).toBe(
      'M 0.0 0.0 L 10.0 5.0',
    )
  })

  it('emits a cubic bezier for 4 points', () => {
    const d = pointsToPath([
      { x: 0, y: 0 },
      { x: 30, y: 0 },
      { x: 70, y: 5 },
      { x: 100, y: 5 },
    ])
    expect(d).toContain('M 0.0 0.0')
    expect(d).toContain(' C ')
    expect(d).toContain('30.0 0.0')
    expect(d).toContain('70.0 5.0')
    expect(d).toContain('100.0 5.0')
  })
})

describe('jobProgress', () => {
  it('returns 1 for succeeded / failed', () => {
    expect(jobProgress(makeJob({ id: 'a', status: 'succeeded' }))).toBe(1)
    expect(jobProgress(makeJob({ id: 'a', status: 'failed' }))).toBe(1)
  })
  it('returns 0 for pending', () => {
    expect(jobProgress(makeJob({ id: 'a', status: 'pending' }))).toBe(0)
  })
  it('returns a fraction (0,1) for running with durationMs', () => {
    const p = jobProgress(makeJob({ id: 'a', status: 'running', durationMs: 30_000 }))
    expect(p).toBeGreaterThan(0)
    expect(p).toBeLessThan(1)
  })
})

describe('DEFAULT_FAMILIES', () => {
  it('includes all 10 product families plus catalyst + platform', () => {
    const ids = DEFAULT_FAMILIES.map((f) => f.id)
    for (const expected of [
      'catalyst', 'pilot', 'spine', 'surge', 'silo', 'guardian',
      'insights', 'fabric', 'cortex', 'relay', 'platform',
    ]) {
      expect(ids).toContain(expected)
    }
  })
})
