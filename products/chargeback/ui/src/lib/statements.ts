import type { Statement } from '../api/types'
import { describeWindow, toExclusive } from './dates'
import { toNumber } from './num'

const DAY = /^\d{4}-\d{2}-\d{2}$/

/**
 * "Aug 2026" for a statement's period. The wire sends period_end INCLUSIVE
 * (internal/rating/run.go: to − 1 day), so it is widened by a day before
 * describeWindow, which takes an exclusive end. Anything malformed is shown
 * as sent rather than mis-described.
 */
export function statementPeriod(s: { period_start: string; period_end: string }): string {
  const from = (s.period_start ?? '').slice(0, 10)
  const end = (s.period_end ?? '').slice(0, 10)
  if (!DAY.test(from) || !DAY.test(end) || end < from) return `${from || '?'} → ${end || '?'}`
  return describeWindow({ from, to: toExclusive(end) })
}

// ── The invoice lifecycle (DESIGN.md §8) ──────────────────────────────────

/** Every state a statement can be shown in, in lifecycle order. */
export const STATEMENT_STATUSES = ['draft', 'issued', 'sent', 'paid', 'overdue', 'cancelled'] as const

export type StatementStatus = (typeof STATEMENT_STATUSES)[number]
export type StatementStatusFilter = 'all' | StatementStatus

export const STATEMENT_STATUS_FILTERS: ReadonlyArray<{ value: StatementStatusFilter; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'draft', label: 'Draft' },
  { value: 'issued', label: 'Issued' },
  { value: 'sent', label: 'Sent' },
  { value: 'overdue', label: 'Overdue' },
  { value: 'paid', label: 'Paid' },
  { value: 'cancelled', label: 'Cancelled' },
]

export function isStatementStatus(v: string | null | undefined): v is StatementStatus {
  return !!v && (STATEMENT_STATUSES as readonly string[]).includes(v)
}

/**
 * The status to SHOW. The server derives "overdue" from the due date and the
 * outstanding balance and sends it as `effective_status`; a document written
 * before invoicing has only `status`, so that is the fallback.
 */
export function statementStatus(s: Statement): string {
  return s.effective_status || s.status
}

/** What is still owed: the server's `balance`, or total − paid. */
export function statementBalance(s: Statement): number {
  if (s.balance !== undefined && s.balance !== null) return toNumber(s.balance)
  return toNumber(s.total) - toNumber(s.paid_total ?? 0)
}

export function statementPaid(s: Statement): number {
  return toNumber(s.paid_total ?? 0)
}

/** Whether an operator may still record a payment against this statement. */
export function acceptsPayment(s: Statement): boolean {
  const st = statementStatus(s)
  return st === 'issued' || st === 'sent' || st === 'overdue'
}

/** The statuses this one may legally become — the store enforces the same. */
export function nextStatuses(status: string): string[] {
  switch (status) {
    case 'draft':
      return ['issued', 'cancelled']
    case 'issued':
      return ['sent', 'paid', 'cancelled']
    case 'sent':
      return ['paid', 'overdue']
    case 'overdue':
      return ['paid']
    default:
      return []
  }
}

/** "in 12 days" / "9 days ago" for a due date, or "" when there is none. */
export function dueLabel(s: Statement, now: Date = new Date()): string {
  if (!s.due_at) return ''
  const due = new Date(s.due_at)
  if (Number.isNaN(due.getTime())) return ''
  const days = Math.round((due.getTime() - now.getTime()) / 86_400_000)
  if (days === 0) return 'due today'
  if (days > 0) return `due in ${days} day${days === 1 ? '' : 's'}`
  return `${-days} day${days === -1 ? '' : 's'} overdue`
}
