import { describe, expect, it } from 'vitest'
import { apiQuery, defaultExploreState, drillInto, isGroupBy, nextGroupBy, paramsFromState, resolvedCompare, stateFromParams } from './exploreState'

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
      compare: 'custom' as const,
      compareWindow: { from: '2026-08-01', to: '2026-08-05' },
    }
    const p = paramsFromState(s)
    expect(p.get('kind')).toBe('ecs,evs')
    expect(p.get('exclude_customer')).toBe('c-2')
    expect(p.get('compare')).toBe('custom')
    expect(p.get('compare_from')).toBe('2026-08-01')
    expect(p.get('compare_to')).toBe('2026-08-05')
    expect(stateFromParams(p, now)).toEqual(s)
  })
  it('defaults are last 30 days, daily, by service, top 10, stacked, vs previous period', () => {
    const s = stateFromParams(new URLSearchParams(''), now)
    expect(s).toEqual(defaultExploreState(now))
    expect(s.window).toEqual({ from: '2026-08-09', to: '2026-09-08' })
    expect(s.groupBy).toBe('kind')
    expect(s.granularity).toBe('day')
    expect(s.compare).toBe('previous')
    expect(s.compareWindow).toBeNull()
    const p = paramsFromState(s)
    expect(p.has('compare')).toBe(false)
    expect(p.has('compare_from')).toBe(false)
  })
  it('ignores junk values instead of throwing', () => {
    const s = stateFromParams(new URLSearchParams('group_by=colour&metric=weight&limit=abc&chart=pie&granularity=week&compare=yesterday'), now)
    expect(s.groupBy).toBe('kind')
    expect(s.metric).toBe('cost')
    expect(s.limit).toBe(10)
    expect(s.chart).toBe('stacked')
    expect(s.granularity).toBe('day')
    expect(s.compare).toBe('previous')
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
    // Previous period is the API default: nothing is sent for it.
    expect(q.has('compare_from')).toBe(false)
    expect(q.has('compare_to')).toBe(false)
  })
  it('keeps hourly grain on a short window and falls back to daily when the window is too long', () => {
    const short = stateFromParams(new URLSearchParams('preset=7d&granularity=hour'), now)
    expect(short.granularity).toBe('hour')
    expect(new URLSearchParams(apiQuery(short)).get('granularity')).toBe('hour')
    expect(paramsFromState(short).get('granularity')).toBe('hour')
    const long = stateFromParams(new URLSearchParams('preset=30d&granularity=hour'), now)
    expect(long.granularity).toBe('day')
    const edge = stateFromParams(new URLSearchParams('from=2026-09-01&to=2026-09-15&granularity=hour'), now)
    expect(edge.granularity).toBe('hour')
    const over = stateFromParams(new URLSearchParams('from=2026-09-01&to=2026-09-16&granularity=hour'), now)
    expect(over.granularity).toBe('day')
  })
  it('relative compare modes follow the window and round-trip by mode only', () => {
    const s = stateFromParams(new URLSearchParams('preset=custom&from=2026-09-01&to=2026-09-08&compare=last-month'), now)
    expect(s.compare).toBe('last-month')
    expect(s.compareWindow).toBeNull()
    expect(resolvedCompare(s)).toEqual({ from: '2026-08-01', to: '2026-08-08' })
    const q = new URLSearchParams(apiQuery(s))
    expect(q.get('compare_from')).toBe('2026-08-01')
    expect(q.get('compare_to')).toBe('2026-08-08')
    const p = paramsFromState(s)
    expect(p.get('compare')).toBe('last-month')
    expect(p.has('compare_from')).toBe(false)
    expect(stateFromParams(p, now)).toEqual(s)
    // Moving the window moves the compare window with it.
    const moved = { ...s, window: { from: '2026-03-31', to: '2026-04-01' } }
    expect(resolvedCompare(moved)).toEqual({ from: '2026-02-28', to: '2026-03-01' })
    const year = stateFromParams(new URLSearchParams('preset=mtd&compare=last-year'), now)
    expect(resolvedCompare(year)).toEqual({ from: '2025-09-01', to: '2025-09-08' })
  })
  it('custom compare needs a valid window; compare_from/to alone imply custom', () => {
    const implied = stateFromParams(new URLSearchParams('compare_from=2026-07-01&compare_to=2026-08-01'), now)
    expect(implied.compare).toBe('custom')
    expect(implied.compareWindow).toEqual({ from: '2026-07-01', to: '2026-08-01' })
    expect(new URLSearchParams(apiQuery(implied)).get('compare_from')).toBe('2026-07-01')
    for (const bad of ['compare=custom', 'compare=custom&compare_from=2026-07-01', 'compare=custom&compare_from=2026-08-01&compare_to=2026-07-01', 'compare=custom&compare_from=1/7/2026&compare_to=2026-08-01']) {
      const s = stateFromParams(new URLSearchParams(bad), now)
      expect(s.compare).toBe('previous')
      expect(s.compareWindow).toBeNull()
      expect(new URLSearchParams(apiQuery(s)).has('compare_from')).toBe(false)
    }
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
