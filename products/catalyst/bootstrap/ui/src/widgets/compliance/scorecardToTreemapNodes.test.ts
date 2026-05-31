/**
 * scorecardToTreemapNodes.test.ts — pure helper tests (slice U, #1096).
 */

import { describe, it, expect } from 'vitest'
import {
  categoryScoresToTreemapNodes,
  scorecardToTreemapNodes,
} from './scorecardToTreemapNodes'
import type { CategoryScore, Score } from '@/pages/admin/compliance/compliance.api'

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

describe('categoryScoresToTreemapNodes (G86b #2633 fallback)', () => {
  function cat(opts: Partial<CategoryScore> & { score?: number; denom?: number; num?: number; pc?: number }): CategoryScore {
    return {
      score: opts.score ?? 0,
      denominator: opts.denom ?? 0,
      numerator: opts.num ?? 0,
      policyCount: opts.pc ?? 0,
    }
  }
  it('returns [] when categoryScores is undefined', () => {
    expect(categoryScoresToTreemapNodes(undefined)).toEqual([])
  })
  it('returns [] when every category has zero denominator AND zero policyCount', () => {
    const nodes = categoryScoresToTreemapNodes({
      security: cat({}),
      sre: cat({}),
      baseline: cat({}),
    })
    expect(nodes).toEqual([])
  })
  it('synthesizes one parent + per-category leaves when at least one category is non-empty', () => {
    // Matches the hw86 G86 fixed state: baseline 14/18 passing,
    // security + sre cold-start (zero), Sovereign score 50%.
    const nodes = categoryScoresToTreemapNodes({
      security: cat({ pc: 0 }),
      sre: cat({ pc: 0 }),
      baseline: cat({ score: 77, denom: 18, num: 14, pc: 18 }),
    })
    expect(nodes.length).toBe(1)
    const parent = nodes[0]!
    expect(parent.name).toBe('Compliance categories')
    expect(parent.children?.length).toBe(1) // only baseline carried data
    const baseline = parent.children![0]!
    expect(baseline.name).toContain('Baseline')
    expect(baseline.name).toContain('18 policies')
    expect(baseline.total).toBe(77)
    expect(baseline.size).toBe(18)
    expect(baseline.score?.scope).toBe('application')
    expect(baseline.score?.id).toBe('category:baseline')
  })
  it('emits a category cell with greyed total when only policyCount > 0', () => {
    const nodes = categoryScoresToTreemapNodes({
      security: cat({ pc: 3 }), // 3 policies installed but zero verdicts yet
    })
    const leaf = nodes[0]?.children?.[0]
    expect(leaf?.name).toContain('3 policies')
    expect(leaf?.total).toBeNull()
    expect(leaf?.size).toBe(3)
  })
  it('renders categories in canonical order: Security, Reliability, Baseline', () => {
    const nodes = categoryScoresToTreemapNodes({
      baseline: cat({ score: 50, denom: 10, num: 5, pc: 10 }),
      sre: cat({ score: 30, denom: 8, num: 2, pc: 8 }),
      security: cat({ score: 90, denom: 5, num: 4, pc: 5 }),
    })
    const names = nodes[0]?.children?.map((c) => c.name.split(' ')[0])
    expect(names).toEqual(['Security', 'Reliability', 'Baseline'])
  })
})
