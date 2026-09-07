import { exploreQuery, type ExploreParams, type Granularity, type GroupBy, type Metric } from '../api/types'
import { emptyFilters, filterDims, type Dim, type Filters } from '../components/FilterChips'
import { compareWindow, defaultGranularity, fitGranularity, presetWindow, windowFromParams, type CompareMode, type Preset, type Window } from './dates'
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
  /**
   * What `previous` is measured against. `previous` and the relative modes
   * follow the window (the URL keeps the mode, the window is derived when the
   * query is built); `custom` pins compareWindow.
   */
  compare: CompareMode
  /** The custom compare window; read only when compare === 'custom'. */
  compareWindow: Window | null
}

export const DEFAULT_LIMIT = 10

export function defaultExploreState(now = new Date()): ExploreState {
  const window = presetWindow('30d', now)
  return {
    preset: '30d',
    window,
    granularity: defaultGranularity(window),
    groupBy: 'kind',
    metric: 'cost',
    limit: DEFAULT_LIMIT,
    chart: 'stacked',
    filters: emptyFilters(),
    compare: 'previous',
    compareWindow: null,
  }
}

const GROUPS: GroupBy[] = ['none', 'customer', 'source', 'kind', 'sku', 'region', 'resource', 'tier', 'namespace', 'enterprise_project']
const DIMS: Dim[] = ['customer', 'source', 'kind', 'sku', 'region', 'resource', 'tier', 'namespace', 'enterprise_project']

/** A group_by value the server accepts: a static dimension or a `tag:<key>` with a valid key. */
export function isGroupBy(v: string | null | undefined): v is GroupBy {
  return !!v && ((GROUPS as string[]).includes(v) || isTagDim(v))
}
const DAY_RE = /^\d{4}-\d{2}-\d{2}$/

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
  // Compare: a relative mode stands alone; custom needs a valid window
  // (compare_from/compare_to alone also means custom). Anything else is the
  // automatic previous period.
  const cm = params.get('compare')
  const cf = params.get('compare_from')
  const ct = params.get('compare_to')
  const customWindow = cf && ct && DAY_RE.test(cf) && DAY_RE.test(ct) && ct > cf ? { from: cf, to: ct } : null
  let compare: CompareMode = 'previous'
  let compareWin: Window | null = null
  if (cm === 'last-month' || cm === 'last-year') compare = cm
  else if ((cm === 'custom' || cm === null) && customWindow) {
    compare = 'custom'
    compareWin = customWindow
  }
  return {
    preset,
    window,
    // An hourly URL over a window that has since grown (a relative preset a
    // fortnight later) falls back to daily instead of a 400.
    granularity: g === 'month' || g === 'day' || g === 'hour' ? fitGranularity(g, window) : defaultGranularity(window),
    groupBy: isGroupBy(gb) ? gb : base.groupBy,
    metric: m === 'usage' ? 'usage' : 'cost',
    limit: lim !== null && /^\d+$/.test(lim) ? Number(lim) : base.limit,
    chart: ch === 'line' || ch === 'area' ? ch : 'stacked',
    filters,
    compare,
    compareWindow: compareWin,
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
  if (s.compare !== 'previous') p.set('compare', s.compare)
  if (s.compare === 'custom' && s.compareWindow) {
    p.set('compare_from', s.compareWindow.from)
    p.set('compare_to', s.compareWindow.to)
  }
  return p
}

/** The compare window the state sends, or null for the API's automatic previous period. */
export function resolvedCompare(s: ExploreState): Window | null {
  return compareWindow(s.window, s.compare, s.compareWindow)
}

/** The API parameters for the state (window resolved, filters flattened). */
export function apiParams(s: ExploreState): ExploreParams {
  const cw = resolvedCompare(s)
  return {
    from: s.window.from,
    to: s.window.to,
    granularity: s.granularity,
    group_by: s.groupBy,
    metric: s.metric,
    limit: s.limit,
    include: s.filters.include,
    exclude: s.filters.exclude,
    ...(cw ? { compare_from: cw.from, compare_to: cw.to } : {}),
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
