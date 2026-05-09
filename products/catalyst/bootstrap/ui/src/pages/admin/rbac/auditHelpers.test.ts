/**
 * auditHelpers.test.ts — unit coverage for the EPIC-3 #1098 slice U8
 * pure helpers. Verifies merge dedupe, row formatter, color palettes.
 */

import { describe, expect, it } from 'vitest'

import { actionPalette, formatAuditTimestamp, mergeAuditEvents, resultPalette } from './auditHelpers'
import type { AuditEvent } from './rbac.api'

const ev = (over: Partial<AuditEvent>): AuditEvent => ({
  auditType: 'rbac-grant-created',
  ts: '2026-05-09T00:00:00Z',
  ...over,
})

describe('mergeAuditEvents', () => {
  it('prepends streamed events on top of the REST baseline', () => {
    const baseline = [ev({ ts: '2026-05-09T00:00:00Z' })]
    const streamed = [ev({ ts: '2026-05-09T01:00:00Z' })]
    const merged = mergeAuditEvents(streamed, baseline)
    expect(merged).toHaveLength(2)
    expect(merged[0].ts).toBe('2026-05-09T01:00:00Z')
    expect(merged[1].ts).toBe('2026-05-09T00:00:00Z')
  })

  it('de-dupes by (auditType, ts, userAccessRef)', () => {
    const dup: AuditEvent = ev({ ts: '2026-05-09T00:00:00Z', userAccessRef: 'rbac-x-y' })
    const merged = mergeAuditEvents([dup], [dup])
    expect(merged).toHaveLength(1)
  })

  it('returns sorted newest-first across both sources', () => {
    const baseline = [
      ev({ ts: '2026-05-09T00:01:00Z', userAccessRef: 'a' }),
      ev({ ts: '2026-05-09T00:03:00Z', userAccessRef: 'c' }),
    ]
    const streamed = [ev({ ts: '2026-05-09T00:02:00Z', userAccessRef: 'b' })]
    const merged = mergeAuditEvents(streamed, baseline)
    expect(merged.map((e) => e.userAccessRef)).toEqual(['c', 'b', 'a'])
  })
})

describe('formatAuditTimestamp', () => {
  it('returns "Ns ago" for sub-minute deltas', () => {
    const now = Date.parse('2026-05-09T00:01:00Z')
    expect(formatAuditTimestamp('2026-05-09T00:00:30Z', now)).toBe('30s ago')
  })
  it('returns "Nm ago" for sub-hour deltas', () => {
    const now = Date.parse('2026-05-09T01:00:00Z')
    expect(formatAuditTimestamp('2026-05-09T00:50:00Z', now)).toBe('10m ago')
  })
  it('returns "Nh ago" for sub-day deltas', () => {
    const now = Date.parse('2026-05-09T05:00:00Z')
    expect(formatAuditTimestamp('2026-05-09T00:00:00Z', now)).toBe('5h ago')
  })
  it('returns ISO date for older deltas', () => {
    const now = Date.parse('2026-05-15T00:00:00Z')
    expect(formatAuditTimestamp('2026-05-09T00:00:00Z', now)).toBe('2026-05-09 00:00:00')
  })
  it('returns "" on empty input', () => {
    expect(formatAuditTimestamp('')).toBe('')
  })
  it('returns input on unparseable', () => {
    expect(formatAuditTimestamp('not-a-date')).toBe('not-a-date')
  })
})

describe('actionPalette', () => {
  it('uses green for grant-created', () => {
    expect(actionPalette('rbac-grant-created').fg).toBe('#86efac')
  })
  it('uses sky-blue for grant-updated', () => {
    expect(actionPalette('rbac-grant-updated').fg).toBe('#7dd3fc')
  })
  it('uses red for grant-deleted', () => {
    expect(actionPalette('rbac-grant-deleted').fg).toBe('#fca5a5')
  })
  it('uses amber for tier-changed', () => {
    expect(actionPalette('rbac-tier-changed').fg).toBe('#fcd34d')
  })
  it('falls back to grey on unknown', () => {
    expect(actionPalette('continuum-foo').fg).toBe('#cbd5e1')
  })
})

describe('resultPalette', () => {
  it('returns green palette for ok', () => {
    expect(resultPalette('ok').fg).toBe('#86efac')
  })
  it('returns red palette for denied', () => {
    expect(resultPalette('denied').fg).toBe('#fca5a5')
  })
  it('returns amber palette for error', () => {
    expect(resultPalette('error').fg).toBe('#fcd34d')
  })
})
