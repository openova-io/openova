import { describe, expect, it } from 'vitest'
import { keyOfUsageRow } from './usageKey'

describe('keyOfUsageRow', () => {
  // The live API sends only `key`. Reading day/resource_id rendered '—' in
  // every row of the day and resource views (#6866).
  it('uses key, which is what the server actually sends', () => {
    expect(keyOfUsageRow({ key: '2026-08-07', sku: 'ecs.x' }, 'day')).toBe('2026-08-07')
    expect(keyOfUsageRow({ key: 'res-1' }, 'resource')).toBe('res-1')
    expect(keyOfUsageRow({ key: 'ecs.x' }, 'sku')).toBe('ecs.x')
  })

  it('falls back to the legacy fields if a server sends them', () => {
    expect(keyOfUsageRow({ day: '2026-08-07' }, 'day')).toBe('2026-08-07')
    expect(keyOfUsageRow({ resource_id: 'res-9' }, 'resource')).toBe('res-9')
  })

  // A dash is the honest render for a row with no grouped value; it must not
  // silently become "undefined".
  it('renders a dash when there is nothing', () => {
    expect(keyOfUsageRow({}, 'day')).toBe('—')
  })
})
