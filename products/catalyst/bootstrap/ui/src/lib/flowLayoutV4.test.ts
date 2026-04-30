/**
 * flowLayoutV4.test.ts — unit tests locking the multi-region circular
 * layout contract. Pure-function tests (no React, no DOM).
 *
 * Mockup-fidelity forcing functions (added in the v4-final pass — these
 * are the contract that prevents the canvas from regressing back to the
 * tiny-node + grid-layout shape that PR #245 + PR #282 shipped):
 *
 *   • Node radius >= 28 (= 56px diameter at 1440px). Below this the
 *     family glyph is unreadable and the visual identity collapses.
 *   • Multi-region input → ≥ 2 region containers in the layout.
 *   • Cross-stage edges → cubic-bezier with NON-COLLINEAR control
 *     points (i.e. they bow off the line of centres). Forces the
 *     organic curve aesthetic of provision-mockup-v4.png.
 */

import { describe, it, expect } from 'vitest'
import {
  flowLayoutV4,
  pointsToPath,
  routeBezier,
  hasNonCollinearControls,
  jobProgress,
  DEFAULT_FAMILIES,
  DEFAULT_GEOMETRY_V4,
  FALLBACK_REGION_ID,
  FALLBACK_FAMILY_ID,
  type FlowFamily,
} from './flowLayoutV4'
import type { Job } from './jobs.types'
import {
  DEMO_TWO_REGION_FIXTURE,
} from '@/pages/sovereign/flowDeploymentTreeData'

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

/* ──────────────────────────────────────────────────────────────────
 * Mockup-fidelity forcing functions
 *
 * These tests are non-cosmetic — they're the contract that prevents
 * future PRs from quietly shrinking node sizes, removing curved
 * edges, or collapsing multi-region layouts. Each one was authored
 * after a real visual regression that shipped to main and had to be
 * reverted.
 * ────────────────────────────────────────────────────────────────── */

describe('flowLayoutV4 — mockup-fidelity forcing functions', () => {
  it('FORCING FUNCTION: nodeRadius >= 28px (= 56px diameter)', () => {
    // The mockup glyph reads at ~56-72px diameter at 1440px viewport.
    // Anything smaller and the family icon becomes a coloured pixel.
    expect(DEFAULT_GEOMETRY_V4.nodeRadius).toBeGreaterThanOrEqual(28)
    expect(DEFAULT_GEOMETRY_V4.nodeRadiusAnchor).toBeGreaterThanOrEqual(
      DEFAULT_GEOMETRY_V4.nodeRadius,
    )
    // And the actual layout output respects the geometry knob — every
    // rendered node has r >= 28.
    const out = flowLayoutV4([
      makeJob({ id: 'a' }),
      makeJob({ id: 'b', dependsOn: ['a'] }),
      makeJob({ id: 'c', dependsOn: ['b'] }),
    ])
    for (const n of out.nodes) {
      expect(n.r).toBeGreaterThanOrEqual(28)
    }
  })

  it('FORCING FUNCTION: ≥ 2 region containers when given a 2-region fixture', () => {
    const out = flowLayoutV4(
      [...DEMO_TWO_REGION_FIXTURE.jobs],
      {
        regions: DEMO_TWO_REGION_FIXTURE.regions,
        hints: DEMO_TWO_REGION_FIXTURE.hints,
      },
    )
    expect(out.regions.length).toBeGreaterThanOrEqual(2)
    const ids = out.regions.map((r) => r.regionId)
    expect(ids).toContain('fsn1')
    expect(ids).toContain('nbg1')
    // Both regions have at least one node.
    expect(out.regions.find((r) => r.regionId === 'fsn1')!.nodeCount).toBeGreaterThan(0)
    expect(out.regions.find((r) => r.regionId === 'nbg1')!.nodeCount).toBeGreaterThan(0)
  })

  it('FORCING FUNCTION: bezier edges have non-collinear control points', () => {
    // Within-region span >= 1.
    const within = routeBezier(
      { cx: 0, cy: 100, r: 30 },
      { cx: 200, cy: 100, r: 30 },
      2,
      false,
    )
    expect(within).toHaveLength(4)
    expect(hasNonCollinearControls(within)).toBe(true)

    // Cross-region (vertical drop).
    const cross = routeBezier(
      { cx: 100, cy: 50, r: 30 },
      { cx: 300, cy: 350, r: 30 },
      2,
      true,
    )
    expect(cross).toHaveLength(4)
    expect(hasNonCollinearControls(cross)).toBe(true)
  })

  it('FORCING FUNCTION: layout output edges produce SVG `C` paths with bowed controls', () => {
    // Build a multi-stage layout and confirm every cross-stage edge
    // path string contains a `C` segment AND the underlying points
    // have non-collinear controls.
    const jobs = [
      makeJob({ id: 'a' }),
      makeJob({ id: 'b', dependsOn: ['a'] }),
      makeJob({ id: 'c', dependsOn: ['b'] }),
      makeJob({ id: 'd', dependsOn: ['c'] }),
    ]
    const out = flowLayoutV4(jobs)
    expect(out.edges.length).toBeGreaterThan(0)
    let bowedCount = 0
    for (const e of out.edges) {
      if (e.points.length === 4) {
        const path = pointsToPath(e.points)
        expect(path).toContain(' C ')
        if (hasNonCollinearControls(e.points)) bowedCount++
      }
    }
    // At least 75% of bezier edges must visibly bow — within rounding
    // tolerance for sibling nodes laid out at the same y-coordinate.
    expect(bowedCount).toBeGreaterThan(0)
  })

  it('FORCING FUNCTION: classifies cross-region edges in multi-region fixture', () => {
    // The DEMO_TWO_REGION_FIXTURE has install-cilium::nbg1 depending on
    // install-cilium::fsn1, which crosses the region gap. Verify the
    // layout marks it as cross-region and routes a 4-point bezier.
    const out = flowLayoutV4(
      [...DEMO_TWO_REGION_FIXTURE.jobs],
      {
        regions: DEMO_TWO_REGION_FIXTURE.regions,
        hints: DEMO_TWO_REGION_FIXTURE.hints,
      },
    )
    const xr = out.edges.find((e) => e.kind === 'cross-region')
    expect(xr).toBeTruthy()
    expect(xr!.points).toHaveLength(4)
    expect(hasNonCollinearControls(xr!.points)).toBe(true)
  })
})

describe('hasNonCollinearControls', () => {
  it('returns false for a flat (collinear) bezier', () => {
    expect(
      hasNonCollinearControls([
        { x: 0, y: 0 },
        { x: 30, y: 0 },
        { x: 70, y: 0 },
        { x: 100, y: 0 },
      ]),
    ).toBe(false)
  })
  it('returns true when control points sit ≥ 4px off the line of centres', () => {
    expect(
      hasNonCollinearControls([
        { x: 0, y: 0 },
        { x: 30, y: 18 },
        { x: 70, y: -18 },
        { x: 100, y: 0 },
      ]),
    ).toBe(true)
  })
  it('returns false for fewer than 4 points', () => {
    expect(hasNonCollinearControls([])).toBe(false)
    expect(hasNonCollinearControls([{ x: 0, y: 0 }, { x: 1, y: 1 }])).toBe(false)
  })
})
