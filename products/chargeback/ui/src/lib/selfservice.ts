import type { Dispute, PaymentMethod, Statement } from '../api/types'
import { API_BASE } from '../api/client'

/**
 * Customer self-service (DESIGN.md §16) — the pure part of the three things
 * a paying customer does without the operator: download the invoice, keep a
 * payment method, dispute a bill.
 */

/** The download URL of an invoice. Same route, same scope check, as reading it. */
export function invoiceDownloadURL(statementID: string): string {
  return `${API_BASE}/statements/${statementID}.pdf`
}

/**
 * How a saved method is named in the list: "Visa ···· 4242", falling back to
 * the customer's own label and then to the gateway. There is nothing else to
 * show — and nothing else is held.
 */
export function methodLabel(m: Pick<PaymentMethod, 'brand' | 'last4' | 'label' | 'gateway'>): string {
  const brand = (m.brand ?? '').trim()
  const last4 = (m.last4 ?? '').trim()
  if (brand && last4) return `${titleCase(brand)} ···· ${last4}`
  if (last4) return `···· ${last4}`
  if ((m.label ?? '').trim()) return (m.label ?? '').trim()
  if (brand) return titleCase(brand)
  return (m.gateway ?? '').trim() ? `Held by ${m.gateway}` : 'Payment method'
}

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** "11 / 2030", or empty when the gateway reported no expiry. */
export function expiryLabel(m: Pick<PaymentMethod, 'exp_month' | 'exp_year'>): string {
  const month = Number(m.exp_month ?? 0)
  const year = Number(m.exp_year ?? 0)
  if (!month || !year) return ''
  return `${String(month).padStart(2, '0')} / ${year}`
}

/**
 * A method whose expiry is in the past cannot be charged. Compared at MONTH
 * granularity: a card expiring in November is good for all of November.
 */
export function isExpired(m: Pick<PaymentMethod, 'exp_month' | 'exp_year'>, now = new Date()): boolean {
  const month = Number(m.exp_month ?? 0)
  const year = Number(m.exp_year ?? 0)
  if (!month || !year) return false
  return year * 100 + month < (now.getUTCFullYear() * 100 + now.getUTCMonth() + 1)
}

/** The methods the console lists: everything the server returned, removed ones excluded. */
export function activeMethods(list: PaymentMethod[]): PaymentMethod[] {
  return list.filter((m) => m.status !== 'removed')
}

/** A method the customer must finish on the gateway's page. */
export function needsCompletion(m: Pick<PaymentMethod, 'status' | 'setup_url'>): boolean {
  return m.status === 'pending' && Boolean((m.setup_url ?? '').trim())
}

/**
 * Whether this invoice can be disputed: it must be a real invoice (not a
 * draft), not cancelled, and not already under dispute. A paid invoice can
 * still be disputed — being charged for something wrongly is exactly when a
 * customer notices.
 */
export function canDispute(s: Pick<Statement, 'status' | 'disputed_at'>): boolean {
  if (s.disputed_at) return false
  return s.status !== 'draft' && s.status !== 'cancelled'
}

/** Whether the invoice is under dispute right now. */
export function isDisputed(s: Pick<Statement, 'disputed_at'>): boolean {
  return Boolean(s.disputed_at)
}

/** The open dispute of a list, if any — the one an operator resolves. */
export function openDispute(list: Dispute[]): Dispute | null {
  return list.find((d) => d.status === 'open') ?? null
}

/** One line saying where a dispute stands, for the badge's title. */
export function disputeState(d: Dispute): string {
  switch (d.status) {
    case 'open':
      return 'under review by the operator; this invoice is not chased while it is open'
    case 'upheld':
      return 'upheld — a credit note was issued for the disputed amount'
    case 'rejected':
      return 'rejected — the invoice stands and collections have resumed'
  }
  return d.status
}

/** The two outcomes an operator may record, with the consequence of each. */
export const DISPUTE_OUTCOMES: ReadonlyArray<{ outcome: 'upheld' | 'rejected'; label: string; help: string }> = [
  { outcome: 'upheld', label: 'Uphold', help: 'issues a credit note for the disputed amount' },
  { outcome: 'rejected', label: 'Reject', help: 'the invoice stands and collections resume' },
]
