import { describe, expect, it } from 'vitest'
import type { DimensionValues } from '../api/types'
import {
  DEFAULT_PAGE_SIZE,
  defaultResourcesState,
  flattenAttrs,
  kindLabel,
  pageRange,
  paramsFromResourcesState,
  resourcesQuery,
  resourcesStateFromParams,
  transitionRows,
  unitCost,
} from './resources'

const now = new Date(Date.UTC(2026, 8, 7, 10))

describe('kindLabel', () => {
  const dims: DimensionValues = { from: '2026-08-09', to: '2026-09-08', dimensions: { kind: [{ key: 'ecs', label: 'Servers (from server)' }] } }
  it('prefers the label the dimensions endpoint sent', () => {
    expect(kindLabel('ecs', dims)).toBe('Servers (from server)')
  })
  it('falls back to the Go KindLabel names', () => {
    expect(kindLabel('ecs')).toBe('Elastic Cloud Server')
    expect(kindLabel('evs', dims)).toBe('Block storage (EVS)')
    expect(kindLabel('k8s-pvc')).toBe('Kubernetes volumes')
  })
  it('a server label that merely repeats the key does not hide the local name', () => {
    const bare: DimensionValues = { from: '', to: '', dimensions: { kind: [{ key: 'eip', label: 'eip' }, { key: 'obs', label: 'obs' }] } }
    expect(kindLabel('eip', bare)).toBe('Elastic IP')
    expect(kindLabel('obs', bare)).toBe('obs')
  })
  it('shows an unknown kind as itself and no kind as (none)', () => {
    expect(kindLabel('obs')).toBe('obs')
    expect(kindLabel('')).toBe('(none)')
    expect(kindLabel(null)).toBe('(none)')
  })
})

describe('resources state ↔ URL', () => {
  it('round-trips every control', () => {
    const s = {
      ...defaultResourcesState(now),
      preset: 'custom' as const,
      window: { from: '2026-09-01', to: '2026-09-05' },
      kind: 'ecs',
      region: 'me-east-215-a',
      status: 'stopped' as const,
      q: 'web',
      sort: 'name' as const,
      order: 'asc' as const,
      limit: 100,
      offset: 200,
    }
    expect(resourcesStateFromParams(paramsFromResourcesState(s), now)).toEqual(s)
  })
  it('defaults: last 30 days, all statuses, cost descending, first page of 50', () => {
    const s = resourcesStateFromParams(new URLSearchParams(''), now)
    expect(s).toEqual(defaultResourcesState(now))
    expect(s.window).toEqual({ from: '2026-08-09', to: '2026-09-08' })
    expect(s.limit).toBe(DEFAULT_PAGE_SIZE)
    expect(paramsFromResourcesState(s).toString()).toBe('preset=30d')
  })
  it('ignores junk instead of throwing', () => {
    const s = resourcesStateFromParams(new URLSearchParams('status=zombie&sort=colour&order=sideways&limit=-1&offset=abc'), now)
    expect(s.status).toBe('all')
    expect(s.sort).toBe('cost')
    expect(s.order).toBe('desc')
    expect(s.limit).toBe(DEFAULT_PAGE_SIZE)
    expect(s.offset).toBe(0)
  })
  it('a text sort without an order defaults ascending, cost descending', () => {
    expect(resourcesStateFromParams(new URLSearchParams('sort=name'), now).order).toBe('asc')
    expect(resourcesStateFromParams(new URLSearchParams('sort=cost'), now).order).toBe('desc')
  })
  it('snaps a stray offset to a page boundary', () => {
    expect(resourcesStateFromParams(new URLSearchParams('limit=50&offset=137'), now).offset).toBe(100)
  })
  it('produces the query GET /resources reads, trimming the search', () => {
    const q = new URLSearchParams(resourcesQuery({ ...defaultResourcesState(now), q: '  web  ', kind: 'evs' }))
    expect(q.get('from')).toBe('2026-08-09')
    expect(q.get('to')).toBe('2026-09-08')
    expect(q.get('kind')).toBe('evs')
    expect(q.get('status')).toBe('all')
    expect(q.get('q')).toBe('web')
    expect(q.get('sort')).toBe('cost')
    expect(q.get('order')).toBe('desc')
    expect(q.get('limit')).toBe('50')
    expect(q.get('offset')).toBe('0')
    expect(q.has('region')).toBe(false)
  })
  it('overrides let the count probes reuse the filters', () => {
    const q = new URLSearchParams(resourcesQuery({ ...defaultResourcesState(now), kind: 'ecs', offset: 100 }, { status: 'live', limit: 1, offset: 0 }))
    expect(q.get('kind')).toBe('ecs')
    expect(q.get('status')).toBe('live')
    expect(q.get('limit')).toBe('1')
    expect(q.get('offset')).toBe('0')
  })
})

describe('pageRange', () => {
  it('describes the visible slice', () => {
    expect(pageRange(0, 50, 312)).toBe('1–50 of 312')
    expect(pageRange(300, 12, 312)).toBe('301–312 of 312')
  })
  it('never says "1–0"', () => {
    expect(pageRange(0, 0, 0)).toBe('0 of 0')
  })
})

describe('flattenAttrs', () => {
  it('flattens nested objects, joins scalar arrays, skips transitions and empties', () => {
    const out = flattenAttrs({
      flavor: 'ecs.s6.large.2',
      transitions: [{ at: '2026-09-01T00:00:00Z', status: 'running' }],
      tags: ['a', 'b'],
      spec: { vcpus: 2, mem: { gib: 4 } },
      empty: '',
      nothing: null,
      stopped: false,
    })
    expect(out).toEqual([
      { key: 'flavor', value: 'ecs.s6.large.2' },
      { key: 'spec.mem.gib', value: '4' },
      { key: 'spec.vcpus', value: '2' },
      { key: 'stopped', value: 'no' },
      { key: 'tags', value: 'a, b' },
    ])
  })
  it('keeps arrays of objects readable as JSON', () => {
    expect(flattenAttrs({ nics: [{ ip: '10.0.0.1' }] })).toEqual([{ key: 'nics', value: '[{"ip":"10.0.0.1"}]' }])
  })
  it('handles no attrs', () => {
    expect(flattenAttrs(null)).toEqual([])
  })
})

describe('transitionRows', () => {
  it('reads window.Transition JSON newest first and drops rows without a time', () => {
    const rows = transitionRows([
      { at: '2026-09-01T10:00:00Z', status: 'running', source: 'created' },
      { at: '2026-09-03T10:00:00Z', status: 'stopped', source: 'cts' },
      { status: 'lost' },
    ])
    expect(rows.map((r) => r.status)).toEqual(['stopped', 'running'])
    expect(rows[0]).toEqual({ at: '2026-09-03T10:00:00Z', status: 'stopped', flavor: '', source: 'cts' })
  })
})

describe('unitCost', () => {
  it('divides, and refuses to divide by zero', () => {
    expect(unitCost(10, 4)).toBe(2.5)
    expect(unitCost(10, 0)).toBeNull()
    expect(unitCost(NaN, 4)).toBeNull()
  })
})
