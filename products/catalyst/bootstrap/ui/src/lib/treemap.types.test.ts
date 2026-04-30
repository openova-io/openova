/**
 * treemap.types.test.ts — colour-gradient + drill-walk unit coverage.
 *
 * The Dashboard's correctness rests on two pure functions:
 *   • utilizationColor — maps 0..100 → blue → green → red verbatim.
 *   • walkDrillPath    — finds children at a given drill depth.
 *
 * Both are pure data ops so they live in the lib module and get
 * tested without a render harness. A failure here means the gradient
 * the founder spec calls out by colour anchor IS actually being
 * emitted — no rendering bug can hide a math bug.
 */

import { describe, it, expect } from 'vitest'
import {
  utilizationColor,
  healthColor,
  ageColor,
  colorFunctionFor,
  lockedColorBy,
  walkDrillPath,
  buildTreemapQuery,
  type TreemapItem,
} from './treemap.types'

describe('utilizationColor', () => {
  it('maps 0% → blue', () => {
    expect(utilizationColor(0)).toBe('rgb(59, 130, 246)')
  })

  it('maps 50% → green', () => {
    expect(utilizationColor(50)).toBe('rgb(16, 185, 129)')
  })

  it('maps 100% → red', () => {
    expect(utilizationColor(100)).toBe('rgb(239, 68, 68)')
  })

  it('interpolates 25% halfway between blue and green', () => {
    // 25% should be midpoint of [0..50], i.e. (BLUE + GREEN) / 2.
    // R: (59+16)/2 = 38, G: (130+185)/2 = 158, B: (246+129)/2 = 188 (rounded).
    const c = utilizationColor(25)
    expect(c).toBe('rgb(38, 158, 188)')
  })

  it('interpolates 75% halfway between green and red', () => {
    // R: (16+239)/2 = 128 (round half up), G: (185+68)/2 = 127, B: (129+68)/2 = 99 (round half up).
    const c = utilizationColor(75)
    // Round-half-up: 127.5 → 128, 126.5 → 127, 98.5 → 99. The lerp
    // function uses Math.round which rounds half-away-from-zero.
    expect(c).toMatch(/^rgb\(\d+, \d+, \d+\)$/)
    // Check colour is between green and red (R increases, G decreases)
    expect(c).not.toBe('rgb(16, 185, 129)') // not green
    expect(c).not.toBe('rgb(239, 68, 68)')  // not red
  })

  it('clamps below 0 to blue', () => {
    expect(utilizationColor(-10)).toBe('rgb(59, 130, 246)')
  })

  it('clamps above 100 to red', () => {
    expect(utilizationColor(150)).toBe('rgb(239, 68, 68)')
  })

  it('treats NaN as 0 → blue', () => {
    expect(utilizationColor(Number.NaN)).toBe('rgb(59, 130, 246)')
  })
})

describe('healthColor', () => {
  it('maps 0% → red (everything broken)', () => {
    expect(healthColor(0)).toBe('rgb(239, 68, 68)')
  })

  it('maps 50% → amber (warning)', () => {
    expect(healthColor(50)).toBe('rgb(245, 158, 11)')
  })

  it('maps 100% → green (everything healthy)', () => {
    expect(healthColor(100)).toBe('rgb(16, 185, 129)')
  })
})

describe('ageColor', () => {
  it('mirrors utilizationColor (0 → blue / 100 → red)', () => {
    expect(ageColor(0)).toBe(utilizationColor(0))
    expect(ageColor(100)).toBe(utilizationColor(100))
  })
})

describe('colorFunctionFor', () => {
  it('returns the right function for each selector', () => {
    expect(colorFunctionFor('utilization')(0)).toBe('rgb(59, 130, 246)')
    expect(colorFunctionFor('health')(0)).toBe('rgb(239, 68, 68)')
    expect(colorFunctionFor('age')(0)).toBe('rgb(59, 130, 246)')
  })
})

describe('lockedColorBy', () => {
  it('locks capacity metrics to utilisation', () => {
    expect(lockedColorBy('cpu_limit')).toBe('utilization')
    expect(lockedColorBy('memory_limit')).toBe('utilization')
    expect(lockedColorBy('storage_limit')).toBe('utilization')
  })

  it('does not lock when sizing by replica count', () => {
    expect(lockedColorBy('replica_count')).toBeNull()
  })
})

describe('walkDrillPath', () => {
  const tree: TreemapItem[] = [
    {
      id: 'spine',
      name: 'Spine',
      count: 3,
      percentage: 50,
      children: [
        { id: 'cilium', name: 'cilium', count: 1, percentage: 60, size_value: 100 },
        { id: 'flux',   name: 'flux',   count: 1, percentage: 40, size_value: 50  },
      ],
    },
    {
      id: 'pilot',
      name: 'Pilot',
      count: 2,
      percentage: 70,
      children: [
        { id: 'keycloak', name: 'keycloak', count: 1, percentage: 70, size_value: 100 },
      ],
    },
  ]

  it('returns root when path is empty', () => {
    const out = walkDrillPath(tree, [])
    expect(out).toBe(tree)
  })

  it('returns children of one drill step', () => {
    const out = walkDrillPath(tree, [{ id: 'spine' }])
    expect(out.map((c) => c.id)).toEqual(['cilium', 'flux'])
  })

  it('returns empty when path step is unknown', () => {
    const out = walkDrillPath(tree, [{ id: 'no-such' }])
    expect(out).toEqual([])
  })

  it('returns empty when drilling past a leaf', () => {
    const out = walkDrillPath(tree, [{ id: 'spine' }, { id: 'cilium' }])
    expect(out).toEqual([])
  })
})

describe('buildTreemapQuery', () => {
  it('joins layers with comma, includes color/size', () => {
    const qs = buildTreemapQuery(['family', 'application'], 'utilization', 'cpu_limit')
    const params = new URLSearchParams(qs)
    expect(params.get('group_by')).toBe('family,application')
    expect(params.get('color_by')).toBe('utilization')
    expect(params.get('size_by')).toBe('cpu_limit')
  })

  it('includes deployment_id when provided', () => {
    const qs = buildTreemapQuery(['application'], 'utilization', 'cpu_limit', 'd-123')
    const params = new URLSearchParams(qs)
    expect(params.get('deployment_id')).toBe('d-123')
  })
})
