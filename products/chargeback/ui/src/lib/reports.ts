import { REPORT_SECTIONS, type ReportCadence, type ReportSchedule, type ReportSection } from '../api/types'
import { parseEmails } from './budgets'
import type { Errors } from './forms'

/**
 * Scheduled-report helpers (#6867 follow-up) — pure, unit-tested in
 * reports.test.ts. The API owns the calendar arithmetic (next_at); these
 * only describe a schedule in words and shape the form ↔ wire body.
 */

export const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'] as const

export const CADENCES: ReadonlyArray<{ value: ReportCadence; label: string; window: string }> = [
  { value: 'daily', label: 'Daily', window: 'yesterday' },
  { value: 'weekly', label: 'Weekly', window: 'the last 7 complete days' },
  { value: 'monthly', label: 'Monthly', window: 'the previous calendar month' },
]

export const MAX_RECIPIENTS = 20

/** 1 → "1st", 2 → "2nd", 3 → "3rd", 11 → "11th", 21 → "21st". */
export function ordinal(n: number): string {
  const mod100 = n % 100
  if (mod100 >= 11 && mod100 <= 13) return `${n}th`
  switch (n % 10) {
    case 1:
      return `${n}st`
    case 2:
      return `${n}nd`
    case 3:
      return `${n}rd`
    default:
      return `${n}th`
  }
}

/** 6 → "06:00 UTC". */
export function hourLabel(h: number | null | undefined): string {
  const v = typeof h === 'number' && Number.isFinite(h) ? Math.max(0, Math.min(23, Math.trunc(h))) : 0
  return `${String(v).padStart(2, '0')}:00 UTC`
}

/** What the report covers, for a cadence. */
export function windowLabel(cadence: string): string {
  return CADENCES.find((c) => c.value === cadence)?.window ?? cadence
}

/**
 * "Daily at 06:00 UTC" · "Weekly on Monday at 06:00 UTC" ·
 * "Monthly on the 15th at 06:00 UTC".
 */
export function cadenceLabel(s: Pick<ReportSchedule, 'cadence' | 'day_of_week' | 'day_of_month' | 'hour_utc'>): string {
  const at = `at ${hourLabel(s.hour_utc)}`
  switch (s.cadence) {
    case 'daily':
      return `Daily ${at}`
    case 'weekly': {
      const d = s.day_of_week ?? 1
      return `Weekly on ${WEEKDAYS[d] ?? 'Monday'} ${at}`
    }
    case 'monthly':
      return `Monthly on the ${ordinal(s.day_of_month ?? 1)} ${at}`
    default:
      return `${s.cadence} ${at}`
  }
}

/**
 * Relative due text: "in 25 min", "in 3 h", "in 2 d", "due now" once
 * passed (the scheduler polls every 5 minutes), "—" when unknown.
 */
export function nextRunText(nextAt: string | null | undefined, now: Date = new Date()): string {
  if (!nextAt) return '—'
  const t = new Date(nextAt).getTime()
  if (!Number.isFinite(t)) return '—'
  const ms = t - now.getTime()
  if (ms <= 0) return 'due now'
  const min = Math.round(ms / 60000)
  if (min < 60) return `in ${Math.max(1, min)} min`
  const h = Math.round(ms / 3600000)
  if (h < 48) return `in ${h} h`
  return `in ${Math.round(ms / 86400000)} d`
}

const SECTION_VALUES = REPORT_SECTIONS.map((s) => s.value)

/** The known sections of `list`, in render order, de-duplicated; unknown names dropped. */
export function knownSections(list: readonly string[]): ReportSection[] {
  return SECTION_VALUES.filter((s) => list.includes(s))
}

/** Known sections of `list`, in render order, de-duplicated; empty is an error. */
export function validateSections(list: readonly string[]): { values: ReportSection[]; error?: string } {
  const unknown = list.filter((s) => !(SECTION_VALUES as readonly string[]).includes(s))
  if (unknown.length) return { values: [], error: `Unknown section${unknown.length > 1 ? 's' : ''}: ${unknown.join(', ')}` }
  const values = SECTION_VALUES.filter((s) => list.includes(s))
  if (values.length === 0) return { values: [], error: 'Pick at least one section' }
  return { values }
}

/** "Summary · Top services · Budgets" — the short labels in render order. */
export function sectionLabels(list: readonly string[]): string {
  const labels = REPORT_SECTIONS.filter((s) => list.includes(s.value)).map((s) => s.label)
  return labels.length ? labels.join(' · ') : '—'
}

/** Sum of the list view's delivery aggregates. */
export function deliveryStats(rows: readonly ReportSchedule[]): { schedules: number; active: number; sent30: number; failed30: number } {
  let active = 0
  let attempts = 0
  let failed30 = 0
  for (const r of rows) {
    if (r.active) active++
    attempts += Number(r.sent_30d) || 0
    failed30 += Number(r.failed_30d) || 0
  }
  return { schedules: rows.length, active, sent30: Math.max(0, attempts - failed30), failed30 }
}

// ── form ──────────────────────────────────────────────────────────────────

export interface ReportForm {
  name: string
  /** '' = all customers (operator only). */
  customer_id: string
  cadence: ReportCadence
  day_of_week: string
  day_of_month: string
  hour_utc: string
  recipients: string
  sections: ReportSection[]
  active: boolean
}

export function emptyReportForm(customerId = ''): ReportForm {
  return { name: '', customer_id: customerId, cadence: 'weekly', day_of_week: '1', day_of_month: '1', hour_utc: '6', recipients: '', sections: [...SECTION_VALUES], active: true }
}

export function reportForm(r: ReportSchedule): ReportForm {
  const cadence: ReportCadence = r.cadence === 'daily' || r.cadence === 'monthly' ? r.cadence : 'weekly'
  return {
    name: r.name,
    customer_id: r.customer_id ?? '',
    cadence,
    day_of_week: String(r.day_of_week ?? 1),
    day_of_month: String(r.day_of_month ?? 1),
    hour_utc: String(r.hour_utc ?? 6),
    recipients: (Array.isArray(r.recipients) ? r.recipients : []).join(', '),
    // A row edited by hand may carry a section this build does not know:
    // keep the known ones so the form still opens.
    sections: knownSections(Array.isArray(r.sections) ? r.sections : []),
    active: r.active,
  }
}

const INT = /^\d+$/

export function validateReportForm(f: ReportForm): Errors<ReportForm> {
  const e: Errors<ReportForm> = {}
  if (!f.name.trim()) e.name = 'Name is required'
  else if (f.name.trim().length > 120) e.name = 'Name must be 120 characters or fewer'
  if (!CADENCES.some((c) => c.value === f.cadence)) e.cadence = 'Pick daily, weekly or monthly'
  if (f.cadence === 'weekly' && (!INT.test(f.day_of_week) || Number(f.day_of_week) > 6)) e.day_of_week = 'Pick a weekday'
  if (f.cadence === 'monthly' && (!INT.test(f.day_of_month) || Number(f.day_of_month) < 1 || Number(f.day_of_month) > 28)) e.day_of_month = 'Day of month must be 1–28 so every month has it'
  if (!INT.test(f.hour_utc) || Number(f.hour_utc) > 23) e.hour_utc = 'Hour must be 0–23 (UTC)'
  const em = parseEmails(f.recipients)
  if (em.error) e.recipients = em.error
  else if (em.values.length === 0) e.recipients = 'At least one recipient is required'
  else if (em.values.length > MAX_RECIPIENTS) e.recipients = `At most ${MAX_RECIPIENTS} recipients`
  const sec = validateSections(f.sections)
  if (sec.error) e.sections = sec.error
  return e
}

/**
 * The body POST/PUT /reports/schedules decode. `forcedCustomerId` is the
 * customer lens: the server forces it anyway, the form just does not lie.
 * The day field the cadence does not use is sent as null so an edit that
 * changes the cadence clears the stale one.
 */
export function reportBody(f: ReportForm, forcedCustomerId?: string | null): Record<string, unknown> {
  const customer = forcedCustomerId ?? (f.customer_id || null)
  return {
    name: f.name.trim(),
    customer_id: customer,
    cadence: f.cadence,
    day_of_week: f.cadence === 'weekly' ? Number(f.day_of_week) : null,
    day_of_month: f.cadence === 'monthly' ? Number(f.day_of_month) : null,
    hour_utc: Number(f.hour_utc),
    recipients: parseEmails(f.recipients).values,
    sections: validateSections(f.sections).values,
    active: f.active,
  }
}
