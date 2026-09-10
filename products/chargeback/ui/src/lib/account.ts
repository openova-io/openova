import type { AccountDocument, AccountEntry, AgingBucket, AgingReport, CreditNote, Payment, PaymentIntent, Statement, Suspension } from '../api/types'
import { toNumber } from './num'
import { statementStatus } from './statements'

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

// ── The ledger as the Account tab shows it ────────────────────────────────

/** One ledger line: the entry, its debit/credit split, and the balance after it. */
export interface LedgerRow {
  entry: AccountEntry
  debit: number | null
  credit: number | null
  /** The running balance after this entry: positive owed, negative in credit. */
  running: number
}

/**
 * The ledger newest first, with the running balance after every line. The
 * balance is recomputed from the signed amounts in posting order; the
 * server's figure, when it sent one, wins — the two agree on a complete
 * ledger, and the recomputation is what carries a document that omits it.
 */
export function ledgerRows(entries: AccountEntry[]): LedgerRow[] {
  const ordered = [...entries].sort((a, b) => (a.entered_at ?? '').localeCompare(b.entered_at ?? '') || a.id - b.id)
  let running = 0
  const rows: LedgerRow[] = []
  for (const e of ordered) {
    running += toNumber(e.amount)
    const sent = e.balance !== null && e.balance !== undefined && e.balance !== '' && Number.isFinite(Number(e.balance))
    if (sent) running = toNumber(e.balance)
    rows.push({ entry: e, ...entryColumns(e), running })
  }
  return rows.reverse()
}

/** What a ledger line points at: the invoice, the credit note, or the payment reference. */
export function entryReference(e: AccountEntry): string {
  return e.invoice_number || e.credit_note_number || e.reference || ''
}

// ── Aging, as the Collections page reads it ───────────────────────────────

export function bucketLabel(bucket: string): string {
  return AGING_BUCKETS.find((b) => b.value === bucket)?.label ?? bucket
}

/** "nothing overdue" / "12 days overdue" from a row's oldest open invoice. */
export function oldestDueText(days: number): string {
  if (days <= 0) return 'nothing overdue'
  return `${days} day${days === 1 ? '' : 's'} overdue`
}

export interface AgingKPIs {
  total: number
  overdue: number
  customers: number
  customersOverdue: number
  invoices: number
  currency: string
}

/** The figures the Collections strip shows, as numbers whatever the wire sent. */
export function agingKPIs(rep: AgingReport | null | undefined): AgingKPIs {
  const rows = rep?.rows ?? []
  return {
    total: toNumber(rep?.total),
    overdue: toNumber(rep?.overdue),
    customers: rows.length,
    customersOverdue: rows.filter((r) => toNumber(r.overdue) > 0).length,
    invoices: rep?.invoices?.length ?? 0,
    currency: rows[0]?.currency ?? rep?.invoices?.[0]?.currency ?? '',
  }
}

/**
 * The directory's Balance column reads the customer document's `balance`,
 * accounting-signed like the ledger: positive is what the customer OWES,
 * negative is credit it holds. Null when the document did not carry it, so
 * the cell reads "—" and never 0.
 */
export function directoryBalance(v: number | string | null | undefined): number | null {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) ? n : null
}

// ── Payment intents, suspensions, credit notes on an invoice ──────────────

export const INTENT_STATUS_LABELS: Record<string, string> = {
  requested: 'requested',
  pending: 'pending',
  settled: 'settled',
  failed: 'failed',
  refused: 'refused',
  'awaiting-transfer': 'awaiting transfer',
}

export function intentStatus(i: PaymentIntent): string {
  return INTENT_STATUS_LABELS[i.status] ?? i.status
}

/** "suspended at the platform by collections since 2026-09-01 — 45 days overdue" for a header notice. */
export function suspensionText(s: { suspended_at: string; source: string; reason?: string | null }): string {
  const since = (s.suspended_at ?? '').slice(0, 10)
  const by = s.source ? `by ${s.source.replace(/_/g, ' ')}` : ''
  return ['suspended at the platform', by, since ? `since ${since}` : '', s.reason ? `— ${s.reason}` : ''].filter(Boolean).join(' ')
}

/** "suspended · collections" / "resumed · operator", with the platform's refusal when there was one. */
export function suspensionOutcome(s: Suspension): { label: string; ok: boolean; detail: string } {
  const label = `${s.action === 'resume' ? 'resumed' : 'suspended'} · ${(s.source || 'operator').replace(/_/g, ' ')}`
  return { label, ok: s.ok, detail: s.ok ? (s.reason ?? '') : `the platform refused: ${s.error || 'unknown error'}` }
}

/** How much of an invoice can still be credited: its total less the credit notes already issued. */
export function creditRoom(s: Statement): number {
  return Math.max(0, toNumber(s.total) - toNumber(s.credited_total))
}

/** Whether a credit note can be issued: the statement is an invoice (not a draft), not cancelled, and not fully credited. */
export function acceptsCreditNote(s: Statement): boolean {
  const st = statementStatus(s)
  return (st === 'issued' || st === 'sent' || st === 'paid' || st === 'overdue') && creditRoom(s) > 0
}
