import { exploreQuery, type ExploreParams, type Granularity, type GroupBy, type Metric } from '../api/types'
import { emptyFilters, filterDims, type Dim, type Filters } from '../components/FilterChips'
import { defaultGranularity, presetWindow, windowFromParams, type Preset, type Window } from './dates'
import { isTagDim, tagKeyOf } from './tags'

/**
 * Explorer state ↔ URL search params (#6867). Every control of the cost
 * explorer lives in the URL so a view is a link: shareable, bookmarkable,
 * and what a saved view stores (`params`).
 */
export type ChartKind = 'stacked' | 'line' | 'area'

export interface ExploreState {
  preset: Preset
  window: Window
  granularity: Granularity
  groupBy: GroupBy
  metric: Metric
  limit: number
  chart: ChartKind
  filters: Filters
}

export const DEFAULT_LIMIT = 10

export function defaultExploreState(now = new Date()): ExploreState {
  const window = presetWindow('30d', now)
  return { preset: '30d', window, granularity: defaultGranularity(window), groupBy: 'kind', metric: 'cost', limit: DEFAULT_LIMIT, chart: 'stacked', filters: emptyFilters() }
}

const GROUPS: GroupBy[] = ['none', 'customer', 'source', 'kind', 'sku', 'region', 'resource', 'tier', 'namespace', 'enterprise_project']
const DIMS: Dim[] = ['customer', 'source', 'kind', 'sku', 'region', 'resource', 'tier', 'namespace', 'enterprise_project']

/** A group_by value the server accepts: a static dimension or a `tag:<key>` with a valid key. */
export function isGroupBy(v: string | null | undefined): v is GroupBy {
  return !!v && ((GROUPS as string[]).includes(v) || isTagDim(v))
}

export function stateFromParams(params: URLSearchParams, now = new Date()): ExploreState {
  const base = defaultExploreState(now)
  const { window, preset } = windowFromParams(params, '30d', now)
  const g = params.get('granularity')
  const gb = params.get('group_by')
  const m = params.get('metric')
  const lim = params.get('limit')
  const ch = params.get('chart')
  const filters = emptyFilters()
  for (const d of DIMS) {
    const inc = params.get(d)
    if (inc) filters.include[d] = inc.split(',').filter(Boolean)
    const exc = params.get('exclude_' + d)
    if (exc) filters.exclude[d] = exc.split(',').filter(Boolean)
  }
  // Tag filters: `tag:<key>` / `exclude_tag:<key>`; a key the server would
  // refuse is dropped here rather than sent to fail.
  for (const [name, raw] of params.entries()) {
    const exclude = name.startsWith('exclude_')
    const dim = exclude ? name.slice('exclude_'.length) : name
    if (!isTagDim(dim)) continue
    const vals = raw.split(',').filter(Boolean)
    if (!vals.length) continue
    const side = exclude ? filters.exclude : filters.include
    side[dim] = [...(side[dim] ?? []), ...vals.filter((v) => !(side[dim] ?? []).includes(v))]
  }
  return {
    preset,
    window,
    granularity: g === 'month' || g === 'day' ? g : defaultGranularity(window),
    groupBy: isGroupBy(gb) ? gb : base.groupBy,
    metric: m === 'usage' ? 'usage' : 'cost',
    limit: lim !== null && /^\d+$/.test(lim) ? Number(lim) : base.limit,
    chart: ch === 'line' || ch === 'area' ? ch : 'stacked',
    filters,
  }
}

export function paramsFromState(s: ExploreState): URLSearchParams {
  const p = new URLSearchParams()
  p.set('preset', s.preset)
  if (s.preset === 'custom') {
    p.set('from', s.window.from)
    p.set('to', s.window.to)
  }
  p.set('granularity', s.granularity)
  p.set('group_by', s.groupBy)
  if (s.metric !== 'cost') p.set('metric', s.metric)
  if (s.limit !== DEFAULT_LIMIT) p.set('limit', String(s.limit))
  if (s.chart !== 'stacked') p.set('chart', s.chart)
  for (const d of filterDims(s.filters)) {
    const inc = s.filters.include[d]
    if (inc?.length) p.set(d, inc.join(','))
    const exc = s.filters.exclude[d]
    if (exc?.length) p.set('exclude_' + d, exc.join(','))
  }
  return p
}

/** The API parameters for the state (window resolved, filters flattened). */
export function apiParams(s: ExploreState): ExploreParams {
  return {
    from: s.window.from,
    to: s.window.to,
    granularity: s.granularity,
    group_by: s.groupBy,
    metric: s.metric,
    limit: s.limit,
    include: s.filters.include,
    exclude: s.filters.exclude,
  }
}

export function apiQuery(s: ExploreState): string {
  return exploreQuery(apiParams(s))
}

/** The grouping one level below `gb` — what a drill-in regroups by. */
export function nextGroupBy(gb: GroupBy): GroupBy {
  if (tagKeyOf(gb) !== null) return 'resource'
  switch (gb) {
    case 'customer':
    case 'source':
    case 'region':
    case 'enterprise_project':
      return 'kind'
    case 'kind':
      return 'sku'
    case 'sku':
    case 'namespace':
    case 'resource':
      return 'resource'
    case 'tier':
      return 'namespace'
    default:
      return 'none'
  }
}

/** Drill-in: clicking a group adds it as a filter and regroups one level down. */
export function drillInto(s: ExploreState, key: string): ExploreState {
  if (s.groupBy === 'none' || key === 'other') return s
  const dim = s.groupBy as Dim
  const include = { ...s.filters.include, [dim]: [key] }
  return { ...s, groupBy: nextGroupBy(s.groupBy), filters: { ...s.filters, include } }
}
