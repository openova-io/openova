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
  statusColor,
  aggregateStatusKinds,
  isJobSourcedStack,
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

  it('#4731 — status keeps the (pct)=>string contract total: every pct → neutral pending tint', () => {
    // Status is CATEGORICAL (cells colour from statusKind, not pct);
    // the gradient contract must still be safe for generic callers.
    const fn = colorFunctionFor('status')
    for (const pct of [0, 50, 100]) {
      expect(fn(pct)).toBe(statusColor(undefined))
    }
  })
})

describe('statusColor (#4731 categorical channel)', () => {
  it('tints each kind from its ONE statusColors theme token — no new hex', () => {
    expect(statusColor('success')).toContain('var(--color-success)')
    expect(statusColor('in-progress')).toContain('var(--color-accent)')
    expect(statusColor('warning')).toContain('var(--color-warn)')
    expect(statusColor('failed')).toContain('var(--color-danger)')
    expect(statusColor('pending')).toContain('var(--color-text-dim)')
    for (const k of ['success', 'in-progress', 'warning', 'failed', 'pending'] as const) {
      expect(statusColor(k)).toMatch(/^color-mix\(in srgb, var\(--color-/)
      expect(statusColor(k)).not.toMatch(/#[0-9a-fA-F]{3,8}/)
    }
  })

  it('renders an absent statusKind as the pending tint — never success', () => {
    expect(statusColor(undefined)).toBe(statusColor('pending'))
  })

  it('#4731 — dormant tints from a DISTINCT dimmer grey token, not pending', () => {
    // A distinct dimmer grey token + a lower mix — the parked cutover tether
    // reads "asleep", never a genuine warning hue and never the same as
    // pending queued work.
    expect(statusColor('dormant')).toContain('var(--color-text-dimmer)')
    expect(statusColor('dormant')).toMatch(/^color-mix\(in srgb, var\(--color-/)
    expect(statusColor('dormant')).not.toMatch(/#[0-9a-fA-F]{3,8}/)
    expect(statusColor('dormant')).not.toBe(statusColor('pending'))
  })
})

describe('aggregateStatusKinds (#4731 bucket rollup)', () => {
  it('rollup precedence: failed > warning > in-progress > success > pending', () => {
    expect(aggregateStatusKinds(['success', 'failed', 'in-progress'])).toBe('failed')
    expect(aggregateStatusKinds(['success', 'warning'])).toBe('warning')
    expect(aggregateStatusKinds(['success', 'in-progress'])).toBe('in-progress')
    // Partially-done (mixed success+pending) reads as in-flight.
    expect(aggregateStatusKinds(['success', 'pending'])).toBe('in-progress')
    expect(aggregateStatusKinds(['success', 'success'])).toBe('success')
    expect(aggregateStatusKinds(['pending', 'pending'])).toBe('pending')
    expect(aggregateStatusKinds([])).toBe('pending')
  })

  it('#4731 — an all-dormant bucket rolls up dormant; any real work wins', () => {
    expect(aggregateStatusKinds(['dormant', 'dormant'])).toBe('dormant')
    // A dormant leaf mixed with genuinely queued work reads pending, never
    // dormant — real work is never hidden behind a parked tether.
    expect(aggregateStatusKinds(['dormant', 'pending'])).toBe('pending')
    expect(aggregateStatusKinds(['dormant', 'failed'])).toBe('failed')
  })
})

describe('isJobSourcedStack (#4731 data-source rule)', () => {
  it('true when the stack contains progress or kind — anywhere', () => {
    expect(isJobSourcedStack(['progress', 'kind'])).toBe(true)
    expect(isJobSourcedStack(['kind'])).toBe(true)
    expect(isJobSourcedStack(['progress', 'application'])).toBe(true)
    expect(isJobSourcedStack(['organization', 'application', 'kind'])).toBe(true)
  })

  it('false for every resource stack (getDashboardTreemap path untouched)', () => {
    expect(isJobSourcedStack(['organization', 'application'])).toBe(false)
    expect(isJobSourcedStack(['family', 'application'])).toBe(false)
    expect(isJobSourcedStack(['cluster'])).toBe(false)
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
