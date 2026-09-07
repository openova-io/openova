import { describe, expect, it } from 'vitest'
import type { ReportSchedule } from '../api/types'
import { cadenceLabel, deliveryStats, emptyReportForm, hourLabel, nextRunText, ordinal, reportBody, reportForm, sectionLabels, validateReportForm, validateSections, windowLabel } from './reports'

const schedule = (over: Partial<ReportSchedule>): ReportSchedule => ({
  id: 'r-1',
  name: 'Weekly ops',
  customer_id: null,
  cadence: 'weekly',
  day_of_week: 1,
  day_of_month: null,
  hour_utc: 6,
  recipients: ['ops@nc.example', 'fin@nc.example'],
  sections: ['summary', 'services', 'customers', 'budgets', 'anomalies', 'recommendations'],
  active: true,
  last_sent_at: null,
  next_at: '2026-09-14T06:00:00Z',
  sent_30d: 4,
  failed_30d: 1,
  last_error: '550 relay refused',
  ...over,
})

describe('cadenceLabel / ordinal / hourLabel / windowLabel', () => {
  it('describes the three cadences in words', () => {
    expect(cadenceLabel(schedule({}))).toBe('Weekly on Monday at 06:00 UTC')
    expect(cadenceLabel(schedule({ cadence: 'daily', day_of_week: null, hour_utc: 0 }))).toBe('Daily at 00:00 UTC')
    expect(cadenceLabel(schedule({ cadence: 'monthly', day_of_week: null, day_of_month: 15, hour_utc: 23 }))).toBe('Monthly on the 15th at 23:00 UTC')
    expect(cadenceLabel(schedule({ cadence: 'weekly', day_of_week: 0 }))).toBe('Weekly on Sunday at 06:00 UTC')
    expect(cadenceLabel(schedule({ cadence: 'weekly', day_of_week: null }))).toBe('Weekly on Monday at 06:00 UTC')
  })
  it('ordinals: 1st 2nd 3rd 4th 11th 12th 13th 21st 22nd 23rd 28th', () => {
    expect([1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 28].map(ordinal)).toEqual(['1st', '2nd', '3rd', '4th', '11th', '12th', '13th', '21st', '22nd', '23rd', '28th'])
  })
  it('hour label pads and clamps', () => {
    expect(hourLabel(6)).toBe('06:00 UTC')
    expect(hourLabel(23)).toBe('23:00 UTC')
    expect(hourLabel(null)).toBe('00:00 UTC')
    expect(hourLabel(99)).toBe('23:00 UTC')
  })
  it('names the window a cadence covers', () => {
    expect(windowLabel('daily')).toBe('yesterday')
    expect(windowLabel('weekly')).toBe('the last 7 complete days')
    expect(windowLabel('monthly')).toBe('the previous calendar month')
  })
})

describe('nextRunText', () => {
  const now = new Date('2026-09-08T10:00:00Z')
  it('minutes, hours, days, due', () => {
    expect(nextRunText('2026-09-08T10:25:00Z', now)).toBe('in 25 min')
    expect(nextRunText('2026-09-08T13:00:00Z', now)).toBe('in 3 h')
    expect(nextRunText('2026-09-09T06:00:00Z', now)).toBe('in 20 h')
    expect(nextRunText('2026-09-14T06:00:00Z', now)).toBe('in 6 d')
    expect(nextRunText('2026-09-08T09:59:00Z', now)).toBe('due now')
    expect(nextRunText('2026-09-08T10:00:20Z', now)).toBe('in 1 min')
  })
  it('unknown input renders a dash, never NaN', () => {
    expect(nextRunText(null, now)).toBe('—')
    expect(nextRunText('not a date', now)).toBe('—')
  })
})

describe('validateSections / sectionLabels', () => {
  it('keeps known sections in render order without duplicates', () => {
    expect(validateSections(['budgets', 'summary', 'summary', 'anomalies'])).toEqual({ values: ['summary', 'budgets', 'anomalies'] })
  })
  it('names the unknown section and rejects an empty pick', () => {
    expect(validateSections(['summary', 'bogus']).error).toContain('bogus')
    expect(validateSections([]).error).toContain('at least one')
  })
  it('labels in render order', () => {
    expect(sectionLabels(['recommendations', 'summary'])).toBe('Summary · Recommendations')
    expect(sectionLabels([])).toBe('—')
  })
})

describe('deliveryStats', () => {
  it('sums attempts, failures and active schedules; sent excludes failures', () => {
    const rows = [schedule({}), schedule({ id: 'r-2', active: false, sent_30d: 2, failed_30d: 0 }), schedule({ id: 'r-3', sent_30d: 0, failed_30d: 0 })]
    expect(deliveryStats(rows)).toEqual({ schedules: 3, active: 2, sent30: 5, failed30: 1 })
    expect(deliveryStats([])).toEqual({ schedules: 0, active: 0, sent30: 0, failed30: 0 })
  })
})

describe('form ↔ body', () => {
  it('round-trips a schedule through the form', () => {
    const f = reportForm(schedule({ cadence: 'monthly', day_of_week: null, day_of_month: 15, sections: ['services', 'summary', 'bogus'] }))
    expect(f).toEqual({
      name: 'Weekly ops',
      customer_id: '',
      cadence: 'monthly',
      day_of_week: '1',
      day_of_month: '15',
      hour_utc: '6',
      recipients: 'ops@nc.example, fin@nc.example',
      sections: ['summary', 'services'],
      active: true,
    })
    expect(reportForm(schedule({ cadence: 'hourly' })).cadence).toBe('weekly')
  })
  it('validates name, day ranges, hour, recipients and sections', () => {
    const base = emptyReportForm()
    expect(validateReportForm({ ...base, recipients: 'a@x.example' }).name).toBeTruthy()
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example' })).toEqual({})
    expect(validateReportForm({ ...base, name: 'x', recipients: '' }).recipients).toContain('At least one')
    expect(validateReportForm({ ...base, name: 'x', recipients: 'nope' }).recipients).toContain('nope')
    expect(validateReportForm({ ...base, name: 'x', recipients: Array.from({ length: 21 }, (_, i) => `r${i}@x.example`).join(',') }).recipients).toContain('At most 20')
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example', cadence: 'weekly', day_of_week: '7' }).day_of_week).toBeTruthy()
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example', cadence: 'monthly', day_of_month: '29' }).day_of_month).toContain('1–28')
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example', cadence: 'monthly', day_of_month: '0' }).day_of_month).toBeTruthy()
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example', hour_utc: '24' }).hour_utc).toBeTruthy()
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example', sections: [] }).sections).toContain('at least one')
    // A weekly form does not care about a bad day_of_month and vice versa.
    expect(validateReportForm({ ...base, name: 'x', recipients: 'a@x.example', cadence: 'weekly', day_of_month: '99' })).toEqual({})
  })
  it('builds the wire body: the unused day field is null, emails normalized, sections ordered', () => {
    const f = { ...emptyReportForm(), name: ' Weekly ops ', recipients: 'Ops@NC.example; fin@nc.example ops@nc.example', sections: ['budgets', 'summary'] as ReportForm['sections'] }
    expect(reportBody(f)).toEqual({
      name: 'Weekly ops',
      customer_id: null,
      cadence: 'weekly',
      day_of_week: 1,
      day_of_month: null,
      hour_utc: 6,
      recipients: ['ops@nc.example', 'fin@nc.example'],
      sections: ['summary', 'budgets'],
      active: true,
    })
    const monthly = reportBody({ ...f, cadence: 'monthly', day_of_month: '15', customer_id: 'c-2' })
    expect(monthly.day_of_week).toBeNull()
    expect(monthly.day_of_month).toBe(15)
    expect(monthly.customer_id).toBe('c-2')
    // The customer lens forces its own customer whatever the form holds.
    expect(reportBody({ ...f, customer_id: 'c-2' }, 'c-1').customer_id).toBe('c-1')
    expect(reportBody({ ...f, cadence: 'daily' }).day_of_week).toBeNull()
  })
})

type ReportForm = ReturnType<typeof emptyReportForm>
