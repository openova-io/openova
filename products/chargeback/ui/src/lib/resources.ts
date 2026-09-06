import type { DimensionValues } from '../api/types'
import { presetWindow, windowFromParams, type Preset, type Window } from './dates'

/**
 * Resources list state ↔ URL (#6867, DESIGN.md §2.3 / §3.4). The list is
 * server-paged and server-sorted, so every control — window, kind, region,
 * status, search, sort, page — lives in the URL and maps 1:1 onto the query
 * string GET /resources reads.
 */

/** Mirrors store.KindLabel in the Go binary; the dimensions endpoint wins when it answers. */
export const KIND_LABELS: Readonly<Record<string, string>> = {
  ecs: 'Elastic Cloud Server',
  evs: 'Block storage (EVS)',
  eip: 'Elastic IP',
  elb: 'Load balancer',
  nat: 'NAT gateway',
  vpc: 'VPC',
  rds: 'Relational DB (RDS)',
  dds: 'Document DB (DDS)',
  gaussdb: 'GaussDB',
  cbr: 'Backup (CBR)',
  cce: 'Kubernetes cluster (CCE)',
  ims: 'Images (IMS)',
  dns: 'DNS',
  waf: 'Web application firewall',
  as: 'Auto scaling',
  vpcep: 'VPC endpoint',
  'k8s-pod': 'Kubernetes pods',
  'k8s-pvc': 'Kubernetes volumes',
}

/** Human name for a kind: the server's label for the window, else the local map, else the key. */
export function kindLabel(kind: string | null | undefined, dims?: DimensionValues | null): string {
  if (!kind) return '(none)'
  const fromServer = dims?.dimensions.kind?.find((v) => v.key === kind)?.label
  if (fromServer && fromServer !== kind) return fromServer
  return KIND_LABELS[kind] ?? fromServer ?? kind
}

export type ResourceStatus = 'all' | 'live' | 'stopped' | 'deleted'
export type ResourceSort = 'cost' | 'name' | 'kind' | 'first_seen' | 'last_seen'
export type SortOrder = 'asc' | 'desc'

export const STATUS_OPTIONS: ReadonlyArray<{ value: ResourceStatus; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'live', label: 'Live' },
  { value: 'stopped', label: 'Stopped' },
  { value: 'deleted', label: 'Deleted' },
]

export const PAGE_SIZES: readonly number[] = [25, 50, 100, 200]
export const DEFAULT_PAGE_SIZE = 50

const SORTS: readonly ResourceSort[] = ['cost', 'name', 'kind', 'first_seen', 'last_seen']
const STATUSES: readonly ResourceStatus[] = ['all', 'live', 'stopped', 'deleted']

export interface ResourcesState {
  preset: Preset
  window: Window
  kind: string
  region: string
  status: ResourceStatus
  q: string
  sort: ResourceSort
  order: SortOrder
  limit: number
  offset: number
}

export function defaultResourcesState(now = new Date()): ResourcesState {
  return { preset: '30d', window: presetWindow('30d', now), kind: '', region: '', status: 'all', q: '', sort: 'cost', order: 'desc', limit: DEFAULT_PAGE_SIZE, offset: 0 }
}

export function isSort(v: string | null): v is ResourceSort {
  return v !== null && (SORTS as readonly string[]).includes(v)
}

export function resourcesStateFromParams(params: URLSearchParams, now = new Date()): ResourcesState {
  const base = defaultResourcesState(now)
  const { window, preset } = windowFromParams(params, '30d', now)
  const status = params.get('status')
  const sort = params.get('sort')
  const order = params.get('order')
  const limit = params.get('limit')
  const offset = params.get('offset')
  const lim = limit !== null && /^\d+$/.test(limit) && Number(limit) > 0 ? Number(limit) : base.limit
  return {
    preset,
    window,
    kind: params.get('kind') ?? '',
    region: params.get('region') ?? '',
    status: status !== null && (STATUSES as readonly string[]).includes(status) ? (status as ResourceStatus) : 'all',
    q: params.get('q') ?? '',
    sort: isSort(sort) ? sort : base.sort,
    order: order === 'asc' || order === 'desc' ? order : sort && isSort(sort) && sort !== 'cost' ? 'asc' : 'desc',
    limit: lim,
    offset: offset !== null && /^\d+$/.test(offset) ? Math.floor(Number(offset) / lim) * lim : 0,
  }
}

/** URL params for the state — only what differs from the default, so links stay short. */
export function paramsFromResourcesState(s: ResourcesState): URLSearchParams {
  const p = new URLSearchParams()
  p.set('preset', s.preset)
  if (s.preset === 'custom') {
    p.set('from', s.window.from)
    p.set('to', s.window.to)
  }
  if (s.kind) p.set('kind', s.kind)
  if (s.region) p.set('region', s.region)
  if (s.status !== 'all') p.set('status', s.status)
  if (s.q) p.set('q', s.q)
  if (s.sort !== 'cost' || s.order !== 'desc') {
    p.set('sort', s.sort)
    p.set('order', s.order)
  }
  if (s.limit !== DEFAULT_PAGE_SIZE) p.set('limit', String(s.limit))
  if (s.offset) p.set('offset', String(s.offset))
  return p
}

/** The query string GET /resources (and /resources.csv) reads — DESIGN.md §3.4. */
export function resourcesQuery(s: ResourcesState, override?: Partial<ResourcesState>): string {
  const x = { ...s, ...override }
  const q = new URLSearchParams({ from: x.window.from, to: x.window.to })
  if (x.kind) q.set('kind', x.kind)
  if (x.region) q.set('region', x.region)
  q.set('status', x.status)
  if (x.q.trim()) q.set('q', x.q.trim())
  q.set('sort', x.sort)
  q.set('order', x.order)
  q.set('limit', String(x.limit))
  q.set('offset', String(x.offset))
  return q.toString()
}

/** "1–50 of 312" style page description. */
export function pageRange(offset: number, shown: number, total: number): string {
  if (total === 0 || shown === 0) return '0 of 0'
  return `${(offset + 1).toLocaleString()}–${(offset + shown).toLocaleString()} of ${total.toLocaleString()}`
}

export interface AttrEntry {
  key: string
  value: string
}

/**
 * Flattens a resource's attrs into display pairs: nested objects become
 * dotted keys, arrays and remaining objects are shown as JSON, `transitions`
 * is left out (it gets its own table). Sorted by key so two resources of the
 * same kind read alike.
 */
export function flattenAttrs(attrs: Record<string, unknown> | null | undefined, skip: readonly string[] = ['transitions']): AttrEntry[] {
  const out: AttrEntry[] = []
  const walk = (prefix: string, v: unknown) => {
    if (v === null || v === undefined || v === '') return
    if (Array.isArray(v)) {
      out.push({ key: prefix, value: v.every((x) => typeof x !== 'object' || x === null) ? v.map(String).join(', ') : JSON.stringify(v) })
      return
    }
    if (typeof v === 'object') {
      const o = v as Record<string, unknown>
      const keys = Object.keys(o)
      if (keys.length === 0) return
      for (const k of keys) walk(prefix ? `${prefix}.${k}` : k, o[k])
      return
    }
    out.push({ key: prefix, value: typeof v === 'boolean' ? (v ? 'yes' : 'no') : String(v) })
  }
  for (const [k, v] of Object.entries(attrs ?? {})) {
    if (skip.includes(k)) continue
    walk(k, v)
  }
  return out.sort((a, b) => a.key.localeCompare(b.key))
}

export interface TransitionRow {
  at: string
  status: string
  flavor: string
  source: string
}

/** Reads `transitions` off attrs or the detail document (window.Transition JSON: at, status, flavor, source). */
export function transitionRows(list: Array<Record<string, unknown>> | null | undefined): TransitionRow[] {
  const str = (v: unknown) => (v === null || v === undefined ? '' : String(v))
  return (list ?? [])
    .filter((t) => t && typeof t === 'object')
    .map((t) => ({ at: str(t.at), status: str(t.status), flavor: str(t.flavor), source: str(t.source) }))
    .filter((t) => t.at)
    .sort((a, b) => b.at.localeCompare(a.at))
}

/** Unit cost of a SKU line — null when the quantity is 0 so the table shows a dash, not Infinity. */
export function unitCost(cost: number, quantity: number): number | null {
  if (!Number.isFinite(cost) || !Number.isFinite(quantity) || quantity === 0) return null
  return cost / quantity
}
