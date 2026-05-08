/**
 * scorecardToTreemapNodes.test.ts — pure helper tests (slice U, #1096).
 */

import { describe, it, expect } from 'vitest'
import { scorecardToTreemapNodes } from './scorecardToTreemapNodes'
import type { Score } from '@/pages/admin/compliance/compliance.api'

const NOW = '2026-05-09T00:00:00Z'

function appScore(opts: Partial<Score> & { id: string; org?: string; total?: number | null; denom?: number; results?: Record<string, string> }): Score {
  return {
    scope: 'application',
    id: opts.id,
    applicationRef: opts.id,
    organizationRef: opts.org,
    total: opts.total ?? 50,
    numerator: 0,
    denominator: opts.denom ?? 100,
    policyResults: opts.results,
    updatedAt: NOW,
  }
}

describe('scorecardToTreemapNodes', () => {
  it('groups apps by organizationRef', () => {
    const nodes = scorecardToTreemapNodes([
      appScore({ id: 'billing', org: 'acme' }),
      appScore({ id: 'orders', org: 'acme' }),
      appScore({ id: 'forum', org: 'beta' }),
    ])
    expect(nodes.length).toBe(2)
    const acme = nodes.find((n) => n.name === 'acme')!
    expect(acme.children?.length).toBe(2)
    const beta = nodes.find((n) => n.name === 'beta')!
    expect(beta.children?.length).toBe(1)
  })

  it('apps without organizationRef land under "—"', () => {
    const nodes = scorecardToTreemapNodes([appScore({ id: 'orphan' })])
    expect(nodes[0]?.name).toBe('—')
  })

  it('applies non-zero size baseline so cells are renderable', () => {
    const nodes = scorecardToTreemapNodes([
      appScore({ id: 'billing', org: 'acme', denom: 0 }),
    ])
    const billing = nodes[0]?.children?.[0]
    expect(billing?.size).toBeGreaterThanOrEqual(1)
  })

  it('skips non-application scopes', () => {
    const nodes = scorecardToTreemapNodes([
      appScore({ id: 'billing', org: 'acme' }),
      { ...appScore({ id: 'acme', org: 'acme' }), scope: 'organization' },
    ])
    expect(nodes.length).toBe(1)
    expect(nodes[0]?.children?.length).toBe(1)
  })

  it('policyDomainFilter keeps only apps whose policyResults touch the domain', () => {
    const nodes = scorecardToTreemapNodes(
      [
        appScore({ id: 'billing', org: 'acme', results: { 'cilium-l7-mtls': 'pass' } }),
        appScore({ id: 'orders', org: 'acme', results: { 'probes-present': 'fail' } }),
      ],
      new Set(['cilium-l7-mtls']),
    )
    expect(nodes[0]?.children?.length).toBe(1)
    expect(nodes[0]?.children?.[0]?.name).toBe('billing')
  })

  it('orgs sorted by total weight descending', () => {
    const nodes = scorecardToTreemapNodes([
      appScore({ id: 'a1', org: 'small', denom: 50 }),
      appScore({ id: 'b1', org: 'big', denom: 100 }),
      appScore({ id: 'b2', org: 'big', denom: 200 }),
    ])
    expect(nodes[0]?.name).toBe('big')
    expect(nodes[1]?.name).toBe('small')
  })
})
