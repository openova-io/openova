/**
 * cronSchedule.test.ts — correctness lock for the dependency-free cron
 * parser behind the /jobs Schedule timeline (P3-frontend, Refs #6703).
 *
 * An operator trusts "what fires at 12:00", so every field form (star, step,
 * range, list, numeric), the Vixie DOM/DOW AND-vs-OR rule, macros, fire-time
 * enumeration, next-fire, and describe() are pinned here.
 */

import { describe, expect, it } from 'vitest'
import {
  CronParseError,
  describeCron,
  fireMinutesOnDate,
  matchesDate,
  nextFireTime,
  parseCron,
  tryParseCron,
} from './cronSchedule'

/** A local date at (y, m0, d, hh, mm). Local time is what the parser reads. */
function at(y: number, m0: number, d: number, hh = 0, mm = 0): Date {
  return new Date(y, m0, d, hh, mm, 0, 0)
}

describe('parseCron — field expansion', () => {
  it('expands `*` to the full domain', () => {
    const p = parseCron('* * * * *')!
    expect(p.minute.values.size).toBe(60)
    expect(p.hour.values.size).toBe(24)
    expect(p.dom.values.size).toBe(31)
    expect(p.month.values.size).toBe(12)
    // dow: 0..7 with 7→0 ⇒ 0..6 = 7 distinct days.
    expect([...p.dow.values].sort((a, b) => a - b)).toEqual([0, 1, 2, 3, 4, 5, 6])
    expect(p.minute.star).toBe(true)
    expect(p.dom.star).toBe(true)
    expect(p.dow.star).toBe(true)
  })

  it('expands a step field `*/15`', () => {
    const p = parseCron('*/15 * * * *')!
    expect([...p.minute.values].sort((a, b) => a - b)).toEqual([0, 15, 30, 45])
    expect(p.minute.star).toBe(true)
  })

  it('expands a range `9-17`', () => {
    const p = parseCron('0 9-17 * * *')!
    expect([...p.hour.values].sort((a, b) => a - b)).toEqual([9, 10, 11, 12, 13, 14, 15, 16, 17])
    expect(p.hour.star).toBe(false)
  })

  it('expands a list `0,15,30,45`', () => {
    const p = parseCron('0,15,30,45 * * * *')!
    expect([...p.minute.values].sort((a, b) => a - b)).toEqual([0, 15, 30, 45])
  })

  it('expands a range+step `10-40/10`', () => {
    const p = parseCron('10-40/10 * * * *')!
    expect([...p.minute.values].sort((a, b) => a - b)).toEqual([10, 20, 30, 40])
  })

  it('expands a bare `n/step` from n to max', () => {
    const p = parseCron('5/15 * * * *')!
    expect([...p.minute.values].sort((a, b) => a - b)).toEqual([5, 20, 35, 50])
  })

  it('maps day-of-week 7 to 0 (Sunday)', () => {
    const p = parseCron('0 0 * * 7')!
    expect([...p.dow.values]).toEqual([0])
    expect(p.dow.star).toBe(false)
  })

  it('composes fields: weekday business hours', () => {
    const p = parseCron('0,30 9-17 * * 1-5')!
    expect([...p.minute.values].sort((a, b) => a - b)).toEqual([0, 30])
    expect(p.hour.values.has(9)).toBe(true)
    expect(p.hour.values.has(18)).toBe(false)
    expect([...p.dow.values].sort((a, b) => a - b)).toEqual([1, 2, 3, 4, 5])
  })
})

describe('parseCron — errors + macros', () => {
  it('rejects the wrong field count', () => {
    expect(() => parseCron('* * * *')).toThrow(CronParseError)
    expect(() => parseCron('* * * * * *')).toThrow(CronParseError)
  })
  it('rejects an out-of-range value', () => {
    expect(() => parseCron('60 * * * *')).toThrow(CronParseError)
    expect(() => parseCron('0 24 * * *')).toThrow(CronParseError)
    expect(() => parseCron('0 0 32 * *')).toThrow(CronParseError)
    expect(() => parseCron('0 0 * 13 *')).toThrow(CronParseError)
  })
  it('rejects non-numeric + inverted ranges + zero step', () => {
    expect(() => parseCron('x * * * *')).toThrow(CronParseError)
    expect(() => parseCron('30-10 * * * *')).toThrow(CronParseError)
    expect(() => parseCron('*/0 * * * *')).toThrow(CronParseError)
  })
  it('expands @daily / @hourly / @weekly macros', () => {
    expect(fireMinutesOnDate(parseCron('@daily')!, at(2026, 7, 24))).toEqual([0])
    expect(fireMinutesOnDate(parseCron('@hourly')!, at(2026, 7, 24)).length).toBe(24)
    // @weekly = Sunday 00:00; 2026-08-23 is a Sunday.
    expect(fireMinutesOnDate(parseCron('@weekly')!, at(2026, 7, 23))).toEqual([0])
    expect(fireMinutesOnDate(parseCron('@weekly')!, at(2026, 7, 24))).toEqual([])
  })
  it('treats @reboot as unschedulable (null)', () => {
    expect(parseCron('@reboot')).toBeNull()
    expect(tryParseCron('@reboot')).toBeNull()
  })
  it('tryParseCron swallows errors', () => {
    expect(tryParseCron('nonsense')).toBeNull()
    expect(tryParseCron('61 * * * *')).toBeNull()
  })
})

describe('fireMinutesOnDate', () => {
  it('0 12 * * * fires once at 12:00 (720)', () => {
    expect(fireMinutesOnDate(parseCron('0 12 * * *')!, at(2026, 7, 24))).toEqual([720])
  })
  it('*/15 * * * * fires 96 times, first at 0 and last at 1425', () => {
    const fires = fireMinutesOnDate(parseCron('*/15 * * * *')!, at(2026, 7, 24))
    expect(fires.length).toBe(96)
    expect(fires[0]).toBe(0)
    expect(fires[fires.length - 1]).toBe(23 * 60 + 45)
  })
  it('0 0 * * 0 fires only on Sundays', () => {
    const p = parseCron('0 0 * * 0')!
    // 2026-08-23 is a Sunday, 2026-08-24 a Monday.
    expect(fireMinutesOnDate(p, at(2026, 7, 23))).toEqual([0])
    expect(fireMinutesOnDate(p, at(2026, 7, 24))).toEqual([])
  })
  it('produces the wall-clock minute = hour*60 + minute', () => {
    // 30 8,20 * * * ⇒ 08:30 (510) and 20:30 (1230).
    expect(fireMinutesOnDate(parseCron('30 8,20 * * *')!, at(2026, 7, 24))).toEqual([510, 1230])
  })
})

describe('matchesDate — Vixie DOM/DOW rule', () => {
  it('ORs DOM and DOW when both are restricted', () => {
    // "1st of the month OR any Sunday". 2026-08-01 is a Saturday (dom match),
    // 2026-08-23 is a Sunday (dow match), 2026-08-10 is neither.
    const p = parseCron('0 0 1 * 0')!
    expect(matchesDate(p, at(2026, 7, 1))).toBe(true) // 1st
    expect(matchesDate(p, at(2026, 7, 23))).toBe(true) // Sunday
    expect(matchesDate(p, at(2026, 7, 10))).toBe(false) // neither
  })
  it('ANDs when DOM is a star', () => {
    // "* * * * 1" (Mondays only). 2026-08-24 is a Monday.
    const p = parseCron('0 0 * * 1')!
    expect(matchesDate(p, at(2026, 7, 24))).toBe(true)
    expect(matchesDate(p, at(2026, 7, 25))).toBe(false)
  })
  it('respects the month gate', () => {
    const p = parseCron('0 0 * 12 *')!
    expect(matchesDate(p, at(2026, 11, 5))).toBe(true) // December
    expect(matchesDate(p, at(2026, 7, 5))).toBe(false) // August
  })
})

describe('nextFireTime', () => {
  it('finds the next daily fire later the same day', () => {
    const next = nextFireTime(parseCron('0 12 * * *')!, at(2026, 7, 24, 9, 0))
    expect(next).toEqual(at(2026, 7, 24, 12, 0))
  })
  it('rolls to the next day when today has passed', () => {
    const next = nextFireTime(parseCron('0 12 * * *')!, at(2026, 7, 24, 13, 0))
    expect(next).toEqual(at(2026, 7, 25, 12, 0))
  })
  it('advances to the next matching weekday', () => {
    // Sundays only; from Monday 2026-08-24 the next Sunday is 2026-08-30.
    const next = nextFireTime(parseCron('0 0 * * 0')!, at(2026, 7, 24, 0, 0))
    expect(next).toEqual(at(2026, 7, 30, 0, 0))
  })
  it('picks the immediate next step within the hour', () => {
    const next = nextFireTime(parseCron('*/15 * * * *')!, at(2026, 7, 24, 10, 7))
    expect(next).toEqual(at(2026, 7, 24, 10, 15))
  })
  it('returns null for an impossible date (Feb 30)', () => {
    expect(nextFireTime(parseCron('0 0 30 2 *')!, at(2026, 0, 1))).toBeNull()
  })
})

describe('describeCron', () => {
  it('0 12 * * * → Every day at 12:00', () => {
    expect(describeCron('0 12 * * *')).toBe('Every day at 12:00')
  })
  it('0 0 * * 0 → Every Sunday at 00:00', () => {
    expect(describeCron('0 0 * * 0')).toBe('Every Sunday at 00:00')
  })
  it('*/15 * * * * → Every 15 minutes', () => {
    expect(describeCron('*/15 * * * *')).toBe('Every 15 minutes')
  })
  it('* * * * * → Every minute', () => {
    expect(describeCron('* * * * *')).toBe('Every minute')
  })
  it('0 */2 * * * → Every 2 hours', () => {
    expect(describeCron('0 */2 * * *')).toBe('Every 2 hours')
  })
  it('0 0 * * 1-5 → Monday–Friday at 00:00', () => {
    expect(describeCron('0 0 * * 1-5')).toBe('Monday–Friday at 00:00')
  })
  it('15 * * * * → Every hour at :15', () => {
    expect(describeCron('15 * * * *')).toBe('Every hour at :15')
  })
  it('0 3 1 * * → On day 1 of the month at 03:00', () => {
    expect(describeCron('0 3 1 * *')).toBe('On day 1 of the month at 03:00')
  })
  it('@daily → Every day at 00:00', () => {
    expect(describeCron('@daily')).toBe('Every day at 00:00')
  })
  it('@reboot → At startup', () => {
    expect(describeCron('@reboot')).toBe('At startup')
  })
  it('falls back to the raw expression for exotic shapes', () => {
    // Two hours + two minutes with a weekday gate — no canned phrase.
    expect(describeCron('0,30 8,20 * * 1-5')).toBe('0,30 8,20 * * 1-5')
  })
})
