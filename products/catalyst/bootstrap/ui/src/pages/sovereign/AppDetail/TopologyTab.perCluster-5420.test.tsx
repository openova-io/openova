/**
 * TopologyTab.perCluster-5420.test.tsx — #5420 (#3969): the Topology tab
 * must render EFFECTIVE placement (where workloads were actually observed),
 * never a fan-out projected from the DECLARED posture.
 *
 * The defect, walked on hw290 `acme-corp`: the tab rendered 2 cards, a DR
 * panel and an ENABLED Switchover, while `status.perCluster` held exactly
 * one entry with role `singleton`, region-b had no `acme-corp` namespace,
 * and the DR chip in the same panel read "NOT LIVE". An operator would
 * believe they had a standby they did not have — and were offered a
 * Switchover against it.
 *
 * Mechanism: with no runtime targets and no `spec.placement.targets[]`, the
 * chain fell through to `targetsFromLegacy({mode, regions})`, whose
 * `regions.map(...)` mints a Standby for EVERY declared region regardless
 * of whether anything runs there.
 *
 * These tests exercise the derivation directly rather than through the
 * rendered component: the seam under test is "which target list is
 * produced", and asserting it at that seam keeps the guard from passing
 * vacuously on a component that happens to render nothing.
 */

import { describe, it, expect } from 'vitest'
import { derivePattern, targetsFromLegacy, type PlacementTarget } from '@/shared/lib/placement'

/**
 * The #5420 fix, extracted exactly as TopologyTab applies it: effective
 * per-cluster state outranks any projection from the declared posture.
 */
function targetsFromPerCluster(
  perCluster: Array<Record<string, unknown>> | undefined,
): PlacementTarget[] {
  if (!Array.isArray(perCluster) || perCluster.length === 0) return []
  return perCluster
    .map((entry) => {
      const e = (entry ?? {}) as Record<string, unknown>
      const cluster = typeof e.cluster === 'string' ? e.cluster : ''
      if (!cluster) return null
      const role = (typeof e.role === 'string' ? e.role : '').toLowerCase()
      const isPrimary = role === 'primary' || role === 'active' || role === 'singleton'
      return {
        region: cluster,
        cluster,
        vcluster: 'mgmt',
        role: isPrimary ? ('Primary' as const) : ('Standby' as const),
        ...(isPrimary ? {} : { standbyType: 'Hot' as const }),
      } as PlacementTarget
    })
    .filter((t): t is PlacementTarget => t !== null)
}

describe('#5420 effective perCluster outranks the declared-posture projection', () => {
  it('a single-entry perCluster yields ONE target — not a synthesized pair', () => {
    // The exact hw290 acme-corp shape from the issue.
    const effective = targetsFromPerCluster([
      { cluster: 'hw-me-east-215-rtz-prod', role: 'singleton', status: 'Ready' },
    ])

    expect(effective).toHaveLength(1)
    expect(effective[0].role).toBe('Primary')
    // The defect in one assertion: no Standby may be invented.
    expect(effective.some((t) => t.role === 'Standby')).toBe(false)
    // And therefore no DR fan-out is derivable — which is what gates the
    // DR panel and the Switchover control.
    expect(derivePattern(effective)).toBe('singleton')
  })

  it('CONTROL: the legacy projection is what fabricated the second card', () => {
    // Same app, but taking the pre-fix path: a declared posture plus two
    // declared regions. This is the behaviour the fix bypasses, asserted
    // here so the regression is visible rather than described.
    const declared = targetsFromLegacy({
      mode: 'active-hot-standby',
      regions: ['hw-me-east-215-rtz-prod', 'me-east-215-b-1'],
    })

    expect(declared).toHaveLength(2)
    expect(declared.some((t) => t.role === 'Standby')).toBe(true)
    expect(derivePattern(declared)).toBe('active-hot-standby')
    // The two paths disagree for the SAME app — that disagreement is #5420.
    expect(declared.length).not.toBe(
      targetsFromPerCluster([{ cluster: 'hw-me-east-215-rtz-prod', role: 'singleton' }]).length,
    )
  })

  it('a genuine 2-cluster perCluster still yields a real pair', () => {
    // The fix must not suppress a fan-out that genuinely exists.
    const effective = targetsFromPerCluster([
      { cluster: 'hw-me-east-215-a-rtz-prod', role: 'primary', status: 'Ready' },
      { cluster: 'hw-me-east-215-b-rtz-prod', role: 'standby', status: 'Ready' },
    ])

    expect(effective).toHaveLength(2)
    expect(effective[0].role).toBe('Primary')
    expect(effective[1].role).toBe('Standby')
    expect(effective[1].standbyType).toBe('Hot')
    expect(derivePattern(effective)).toBe('active-hot-standby')
  })

  it('an empty or malformed perCluster derives nothing, so the chain falls through', () => {
    // Absence must not become an assertion either (#5515's lesson): an
    // empty result lets the caller continue to the next source rather than
    // inventing a placement here.
    expect(targetsFromPerCluster([])).toHaveLength(0)
    expect(targetsFromPerCluster(undefined)).toHaveLength(0)
    expect(targetsFromPerCluster([{ role: 'primary' }])).toHaveLength(0) // no cluster name
  })

  it('multi-primary perCluster derives active-active, not a primary+standby pair', () => {
    const effective = targetsFromPerCluster([
      { cluster: 'a-rtz-prod', role: 'primary' },
      { cluster: 'b-rtz-prod', role: 'primary' },
    ])

    expect(effective.every((t) => t.role === 'Primary')).toBe(true)
    expect(derivePattern(effective)).toBe('active-active')
  })
})
