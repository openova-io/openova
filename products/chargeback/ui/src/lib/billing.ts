import type { BillingSettings } from '../api/types'
import { parseReminderDays } from './account'
import type { Errors } from './forms'
import { round } from './num'

/**
 * The Billing settings form (DESIGN.md §8 invoice prefix, §9 tax identity,
 * credit-note prefix and collections schedule) over PUT /billing-settings.
 * The server treats an absent key as "leave it" and REQUIRES discount_rule,
 * so the body is the saved rule plus only what changed. Rates are typed as
 * percentages and travel as fractions (5 → 0.05).
 */
export interface BillingForm {
  invoice_prefix: string
  credit_note_prefix: string
  /** Percent, as typed: "5" or "5.5". */
  tax_rate: string
  tax_registration_number: string
  legal_name: string
  address: string
  /** "-3, 0, 7, 14, 30" — negative days are before the due date. */
  reminder_days: string
  escalation_days: string
  escalation_action: string
}

export const ESCALATION_ACTIONS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'notify', label: 'Notify the operator', help: 'An invoice past the escalation day is reported; nothing is suspended.' },
  { value: 'suspend', label: 'Suspend at the platform', help: 'The Organization is suspended at the platform until the invoice is settled, then resumed.' },
]

/** 0.05 → "5"; "0.055" → "5.5"; absent → "". */
export function fractionToPercent(v: number | string | null | undefined): string {
  if (v === null || v === undefined || v === '') return ''
  const n = Number(v)
  if (!Number.isFinite(n)) return ''
  return String(round(n * 100, 6))
}

/** "5" → "0.05"; "5.5" → "0.055"; "" → "". */
export function percentToFraction(s: string): string {
  const t = s.trim()
  if (!t) return ''
  return String(round(Number(t) / 100, 8))
}

const PREFIX = /^[A-Z0-9][A-Z0-9-]{0,11}$/

export function billingFormFrom(s: BillingSettings | null | undefined): BillingForm {
  return {
    invoice_prefix: s?.invoice_prefix ?? '',
    credit_note_prefix: s?.credit_note_prefix ?? '',
    tax_rate: fractionToPercent(s?.tax_rate),
    tax_registration_number: s?.tax_registration_number ?? '',
    legal_name: s?.legal_name ?? '',
    address: s?.address ?? '',
    reminder_days: (s?.reminder_days ?? []).join(', '),
    escalation_days: s?.escalation_days === undefined || s?.escalation_days === null ? '' : String(s.escalation_days),
    escalation_action: s?.escalation_action ?? '',
  }
}

/** The same rules store.BillingSettings.Validate applies, seen before the round trip. */
export function validateBilling(f: BillingForm): Errors<BillingForm> {
  const e: Errors<BillingForm> = {}
  const inv = f.invoice_prefix.trim()
  const cn = f.credit_note_prefix.trim()
  if (inv && !PREFIX.test(inv)) e.invoice_prefix = '1–12 upper-case letters, digits or dashes, starting with a letter or digit.'
  if (cn && !PREFIX.test(cn)) e.credit_note_prefix = '1–12 upper-case letters, digits or dashes, starting with a letter or digit.'
  if (inv && cn && inv === cn) e.credit_note_prefix = 'Must differ from the invoice prefix, so a credit note is never mistaken for an invoice.'
  const rate = f.tax_rate.trim()
  if (rate) {
    if (!/^\d+(\.\d+)?$/.test(rate)) e.tax_rate = 'A percentage, e.g. 5 or 5.5.'
    else if (Number(rate) > 100) e.tax_rate = 'At most 100 %.'
  }
  const days = parseReminderDays(f.reminder_days)
  if (days.error) e.reminder_days = days.error
  const esc = f.escalation_days.trim()
  if (esc) {
    if (!/^\d+$/.test(esc)) e.escalation_days = 'A whole number of days after the due date.'
    else if (Number(esc) > 3650) e.escalation_days = 'At most 3650 days.'
  }
  if (f.escalation_action && !ESCALATION_ACTIONS.some((a) => a.value === f.escalation_action)) e.escalation_action = 'Choose notify or suspend.'
  return e
}

/**
 * The PUT body: discount_rule (the server requires it) plus only the keys
 * that changed. Null when nothing changed. An empty tax rate is sent as 0 —
 * the Sovereign default is a rate, never "unset".
 */
export function billingBody(saved: BillingSettings, f: BillingForm): Record<string, unknown> | null {
  const before = billingFormFrom(saved)
  const out: Record<string, unknown> = {}
  const strings: Array<keyof BillingForm> = ['invoice_prefix', 'credit_note_prefix', 'tax_registration_number', 'legal_name', 'address', 'escalation_action']
  for (const k of strings) {
    const v = f[k].trim()
    if (v !== before[k]) out[k] = v
  }
  if (f.tax_rate.trim() !== before.tax_rate) out.tax_rate = percentToFraction(f.tax_rate) || '0'
  const days = parseReminderDays(f.reminder_days)
  if (!days.error && days.values.join(',') !== (saved.reminder_days ?? []).join(',')) out.reminder_days = days.values
  if (f.escalation_days.trim() !== before.escalation_days) out.escalation_days = Number(f.escalation_days.trim() || 0)
  if (Object.keys(out).length === 0) return null
  return { discount_rule: saved.discount_rule, ...out }
}

/** "5 %" / "no tax" for a settings summary line. */
export function taxRateText(v: number | string | null | undefined): string {
  const pct = fractionToPercent(v)
  return pct && Number(pct) > 0 ? `${pct} %` : 'no tax'
}
