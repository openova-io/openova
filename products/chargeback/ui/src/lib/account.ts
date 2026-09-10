import type { AccountDocument, AccountEntry, AgingBucket, AgingReport, CreditNote, Payment, Statement } from '../api/types'
import { toNumber } from './num'

/**
 * Readers over the account, payments, credit notes and the aging report
 * (DESIGN.md §9). Pure, so the arithmetic the Account tab and the
 * Collections page show is unit-tested rather than eyeballed.
 */

/** The ledger kinds in display order, with the word the ledger shows. */
export const ENTRY_KINDS: ReadonlyArray<{ value: string; label: string; debit: boolean }> = [
  { value: 'invoice', label: 'Invoice', debit: true },
  { value: 'payment', label: 'Payment', debit: false },
  { value: 'top_up', label: 'Top-up', debit: false },
  { value: 'credit_note', label: 'Credit note', debit: false },
  { value: 'write_off', label: 'Write-off', debit: false },
  { value: 'refund', label: 'Refund', debit: true },
]

export function entryLabel(kind: string): string {
  return ENTRY_KINDS.find((k) => k.value === kind)?.label ?? kind.replace(/_/g, ' ')
}

/** Debit and credit columns from the signed amount: positive is owed. */
export function entryColumns(e: AccountEntry): { debit: number | null; credit: number | null } {
  const v = toNumber(e.amount)
  if (v > 0) return { debit: v, credit: null }
  if (v < 0) return { debit: null, credit: -v }
  return { debit: null, credit: null }
}

/** "in credit 150.000" / "owes 250.000" / "settled" for the account header. */
export function balanceWord(balance: number | string | null | undefined): 'owes' | 'in credit' | 'settled' {
  const v = toNumber(balance)
  if (v > 0) return 'owes'
  if (v < 0) return 'in credit'
  return 'settled'
}

export const PAYMENT_STATUS_LABELS: Record<string, string> = {
  received: 'settled',
  settled: 'settled',
  pending: 'pending',
  failed: 'failed',
  refunded: 'refunded',
}

export function paymentStatus(p: Payment): string {
  return PAYMENT_STATUS_LABELS[p.status ?? 'received'] ?? p.status ?? 'settled'
}

/** What a payment did: "300.000 to INV-2026-00001, 150.000 on account". */
export function allocationText(p: Payment, currency: string, money: (v: number, cur: string) => string): string {
  const parts: string[] = []
  for (const a of p.allocations ?? []) parts.push(`${money(toNumber(a.amount), currency)} to ${a.invoice_number || 'invoice'}`)
  const left = toNumber(p.unallocated)
  if (left > 0) parts.push(`${money(left, currency)} on account`)
  if (parts.length === 0 && (p.status === 'received' || p.status === 'settled' || !p.status)) parts.push('fully allocated')
  return parts.join(', ')
}

/** Whether the operator may still allocate more of this payment. */
export function canAllocate(p: Payment): boolean {
  return (p.status === 'received' || p.status === 'settled' || !p.status) && toNumber(p.unallocated) > 0
}

/** The open invoices credit could be applied to, oldest due first. */
export function openInvoices(rows: Statement[]): Statement[] {
  return rows
    .filter((s) => (s.effective_status || s.status) === 'issued' || (s.effective_status || s.status) === 'sent' || (s.effective_status || s.status) === 'overdue')
    .filter((s) => toNumber(s.balance ?? toNumber(s.total)) > 0)
    .sort((a, b) => (a.due_at ?? '').localeCompare(b.due_at ?? '') || (a.period_start ?? '').localeCompare(b.period_start ?? ''))
}

/** A credit note's effect in one line: "50.000 applied, 30.000 on account". */
export function creditNoteEffect(n: CreditNote, currency: string, money: (v: number, cur: string) => string): string {
  const applied = toNumber(n.applied)
  const left = toNumber(n.unapplied)
  const parts: string[] = []
  if (applied > 0) parts.push(`${money(applied, currency)} applied to the invoice`)
  if (left > 0) parts.push(`${money(left, currency)} on account`)
  return parts.join(', ') || 'nothing applied'
}

// ── The aging report ──────────────────────────────────────────────────────

export const AGING_BUCKETS: ReadonlyArray<{ value: AgingBucket; label: string; late: boolean }> = [
  { value: 'current', label: 'Current', late: false },
  { value: '1-30', label: '1–30 days', late: true },
  { value: '31-60', label: '31–60 days', late: true },
  { value: '61-90', label: '61–90 days', late: true },
  { value: 'over-90', label: 'Over 90', late: true },
]

/** The bucket a number of days past due falls in — the server's rule, for the row detail. */
export function bucketFor(daysPastDue: number): AgingBucket {
  if (daysPastDue <= 0) return 'current'
  if (daysPastDue <= 30) return '1-30'
  if (daysPastDue <= 60) return '31-60'
  if (daysPastDue <= 90) return '61-90'
  return 'over-90'
}

/** The share of the report's total that is overdue, or null when nothing is owed. */
export function overdueShare(rep: AgingReport): number | null {
  const total = toNumber(rep.total)
  if (total <= 0) return null
  return toNumber(rep.overdue) / total
}

/** "3 days before · on the due date · 7, 14, 30 days after". */
export function reminderScheduleText(days: number[]): string {
  const before = days.filter((d) => d < 0).map((d) => -d).sort((a, b) => a - b)
  const on = days.includes(0)
  const after = days.filter((d) => d > 0).sort((a, b) => a - b)
  const parts: string[] = []
  if (before.length) parts.push(`${before.join(', ')} day${before.length === 1 && before[0] === 1 ? '' : 's'} before`)
  if (on) parts.push('on the due date')
  if (after.length) parts.push(`${after.join(', ')} day${after.length === 1 && after[0] === 1 ? '' : 's'} after`)
  return parts.join(' · ') || 'no reminders'
}

/** "-3, 0, 7, 14, 30" ⇄ [-3, 0, 7, 14, 30]; sorted, de-duplicated, whole days in -365..3650. */
export function parseReminderDays(s: string): { values: number[]; error?: string } {
  const parts = s
    .split(/[\s,;]+/)
    .map((p) => p.trim())
    .filter(Boolean)
  const values: number[] = []
  for (const p of parts) {
    if (!/^-?\d+$/.test(p)) return { values: [], error: `"${p}" is not a whole number of days.` }
    const v = Number(p)
    if (v < -365 || v > 3650) return { values: [], error: `${v} is out of range (-365 before to 3650 after).` }
    if (!values.includes(v)) values.push(v)
  }
  values.sort((a, b) => a - b)
  return { values }
}

/** The account's headline figures as numbers, whatever the wire sent. */
export function accountFigures(a: AccountDocument): { balance: number; credit: number; outstanding: number; overdue: number } {
  return { balance: toNumber(a.balance), credit: toNumber(a.available_credit), outstanding: toNumber(a.outstanding), overdue: toNumber(a.overdue) }
}
