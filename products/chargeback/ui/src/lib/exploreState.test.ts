import { describe, expect, it } from 'vitest'
import { apiQuery, defaultExploreState, drillInto, isGroupBy, nextGroupBy, paramsFromState, stateFromParams } from './exploreState'

const now = new Date(Date.UTC(2026, 8, 7, 10))

describe('explorer state ↔ URL', () => {
  it('round-trips every control', () => {
    const s = {
      ...defaultExploreState(now),
      preset: 'custom' as const,
      window: { from: '2026-09-01', to: '2026-09-05' },
      granularity: 'month' as const,
      groupBy: 'sku' as const,
      metric: 'usage' as const,
      limit: 25,
      chart: 'line' as const,
      filters: { include: { kind: ['ecs', 'evs'] }, exclude: { customer: ['c-2'] } },
    }
    const p = paramsFromState(s)
    expect(p.get('kind')).toBe('ecs,evs')
    expect(p.get('exclude_customer')).toBe('c-2')
    expect(stateFromParams(p, now)).toEqual(s)
  })
  it('defaults are last 30 days, daily, by service, top 10, stacked', () => {
    const s = stateFromParams(new URLSearchParams(''), now)
    expect(s).toEqual(defaultExploreState(now))
    expect(s.window).toEqual({ from: '2026-08-09', to: '2026-09-08' })
    expect(s.groupBy).toBe('kind')
    expect(s.granularity).toBe('day')
  })
  it('ignores junk values instead of throwing', () => {
    const s = stateFromParams(new URLSearchParams('group_by=colour&metric=weight&limit=abc&chart=pie&granularity=week'), now)
    expect(s.groupBy).toBe('kind')
    expect(s.metric).toBe('cost')
    expect(s.limit).toBe(10)
    expect(s.chart).toBe('stacked')
    expect(s.granularity).toBe('day')
  })
  it('produces the API query the server parses', () => {
    const s = { ...defaultExploreState(now), filters: { include: { kind: ['ecs'] }, exclude: { sku: ['eip'] } } }
    const q = new URLSearchParams(apiQuery(s))
    expect(q.get('from')).toBe('2026-08-09')
    expect(q.get('to')).toBe('2026-09-08')
    expect(q.get('group_by')).toBe('kind')
    expect(q.get('kind')).toBe('ecs')
    expect(q.get('exclude_sku')).toBe('eip')
    expect(q.get('limit')).toBe('10')
  })
  it('round-trips a tag grouping and tag filters', () => {
    const s = {
      ...defaultExploreState(now),
      groupBy: 'tag:team' as const,
      filters: { include: { kind: ['ecs'], 'tag:team': ['platform', '(untagged)'] }, exclude: { 'tag:env': ['dev'], 'tag:cost-centre': ['CC-1'] } },
    }
    const p = paramsFromState(s)
    expect(p.get('group_by')).toBe('tag:team')
    expect(p.get('tag:team')).toBe('platform,(untagged)')
    expect(p.get('exclude_tag:env')).toBe('dev')
    expect(p.get('exclude_tag:cost-centre')).toBe('CC-1')
    expect(stateFromParams(p, now)).toEqual(s)
    // The API query carries the same names (URLSearchParams encodes the colon; the server decodes it).
    const q = new URLSearchParams(apiQuery(s))
    expect(q.get('group_by')).toBe('tag:team')
    expect(q.get('tag:team')).toBe('platform,(untagged)')
    expect(q.get('exclude_tag:env')).toBe('dev')
    // A saved view's params object rebuilds the same state.
    expect(stateFromParams(new URLSearchParams(Object.fromEntries(p.entries())), now)).toEqual(s)
  })
  it('round-trips enterprise_project as a static dimension', () => {
    const s = { ...defaultExploreState(now), groupBy: 'enterprise_project' as const, filters: { include: { enterprise_project: ['ep-1'] }, exclude: {} } }
    const p = paramsFromState(s)
    expect(p.get('enterprise_project')).toBe('ep-1')
    expect(stateFromParams(p, now)).toEqual(s)
  })
  it('drops a tag key the server would refuse instead of sending it', () => {
    const s = stateFromParams(new URLSearchParams("group_by=tag:te'am&tag:a%20b=1&exclude_tag:=x&tag:ok=1"), now)
    expect(s.groupBy).toBe('kind')
    expect(s.filters).toEqual({ include: { 'tag:ok': ['1'] }, exclude: {} })
    expect(isGroupBy('tag:team')).toBe(true)
    expect(isGroupBy('tag:')).toBe(false)
    expect(isGroupBy('enterprise_project')).toBe(true)
    expect(isGroupBy('colour')).toBe(false)
  })
  it('drills a tag group into its resources and an enterprise project into services', () => {
    const s0 = { ...defaultExploreState(now), groupBy: 'tag:team' as const }
    const s1 = drillInto(s0, 'platform')
    expect(s1.groupBy).toBe('resource')
    expect(s1.filters.include['tag:team']).toEqual(['platform'])
    const e1 = drillInto({ ...defaultExploreState(now), groupBy: 'enterprise_project' }, 'ep-1')
    expect(e1.groupBy).toBe('kind')
    expect(e1.filters.include.enterprise_project).toEqual(['ep-1'])
    expect(nextGroupBy('tag:anything')).toBe('resource')
    expect(nextGroupBy('none')).toBe('none')
  })
  it('drills kind → sku → resource, keeping the clicked value as a filter', () => {
    const s0 = defaultExploreState(now)
    const s1 = drillInto(s0, 'ecs')
    expect(s1.groupBy).toBe('sku')
    expect(s1.filters.include.kind).toEqual(['ecs'])
    const s2 = drillInto(s1, 'ecs.m7n.xlarge.8')
    expect(s2.groupBy).toBe('resource')
    expect(s2.filters.include.sku).toEqual(['ecs.m7n.xlarge.8'])
    expect(drillInto(s2, 'other')).toBe(s2)
  })
})
