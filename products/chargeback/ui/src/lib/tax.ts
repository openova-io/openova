import { API_BASE } from '../api/client'
import type { Customer, EInvoiceState, TaxCategoryRule, TaxLine, TaxRule } from '../api/types'
import { fractionToPercent, percentToFraction } from './billing'
import { isDay, type Errors } from './forms'
import { formatNumber } from './money'
import { toNumber } from './num'

/**
 * Configure → Tax (DESIGN.md §17). Everything the tax surfaces decide is
 * pure and lives here: what a kind means, whether a rule is in force today,
 * what a form sends over the wire, and whether an exemption certificate has
 * lapsed. Rates are TYPED as percentages and TRAVEL as fractions, the same
 * bargain lib/billing.ts already strikes for the single Sovereign rate.
 */

// ── kinds ──────────────────────────────────────────────────────────────────

export const TAX_STANDARD = 'standard'

/**
 * What a rule DOES. The rate cannot say it: zero-rated and exempt are both
 * 0 % and are different lines on a tax return, and reverse charge is 0 % to
 * this issuer and taxable to the buyer.
 */
export const TAX_KINDS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: TAX_STANDARD, label: 'Standard', help: 'The ordinary rate for this country and category. The only kind that carries a rate above 0.' },
  { value: 'zero_rated', label: 'Zero-rated', help: 'Taxable at 0 %: the supply is INSIDE the tax system, and input tax on it is still reclaimable.' },
  { value: 'exempt', label: 'Exempt', help: 'Outside the tax system: no tax is charged and none is reclaimable.' },
  { value: 'reverse_charge', label: 'Reverse charge', help: 'The buyer accounts for the tax. Applied only to a registered BUSINESS in another country; the note below is the wording the invoice must carry.' },
  { value: 'out_of_state', label: 'Out of state', help: 'The supply falls outside the taxing jurisdiction of this Sovereign — an export of services, or a buyer in a state we are not registered in.' },
]

export function taxKindLabel(kind: string | null | undefined): string {
  const k = (kind ?? '').trim()
  return TAX_KINDS.find((x) => x.value === k)?.label ?? k ?? ''
}

export function taxKindHelp(kind: string | null | undefined): string {
  return TAX_KINDS.find((x) => x.value === (kind ?? '').trim())?.help ?? ''
}

/** Only a standard rule charges anything; every other kind is 0 by definition. */
export function chargesTax(kind: string | null | undefined): boolean {
  return (kind ?? '').trim() === TAX_STANDARD
}

/** The badge tone for a kind: standard is ordinary, reverse charge is the one to notice. */
export function kindTone(kind: string | null | undefined): 'ok' | 'warn' | 'info' | undefined {
  switch ((kind ?? '').trim()) {
    case TAX_STANDARD:
      return 'ok'
    case 'reverse_charge':
      return 'warn'
    case 'zero_rated':
    case 'exempt':
    case 'out_of_state':
      return 'info'
    default:
      return undefined
  }
}

// ── rates ──────────────────────────────────────────────────────────────────

/**
 * 0.05 → "5 %", 0.055 → "5.5 %", 0 → "0 %". The wire carries the FRACTION;
 * a reader is shown the percentage, with trailing zeros trimmed so a whole
 * rate does not read as "5.00 %".
 */
export function rateText(rate: number | string | null | undefined): string {
  return `${formatNumber(toNumber(rate) * 100, 4)} %`
}

// ── validity ───────────────────────────────────────────────────────────────

export type RulePhase = 'in force' | 'scheduled' | 'ended'

/**
 * Where a rule stands on a day. `effective_to` is EXCLUSIVE — the store's
 * own `ruleEffectiveAt` refuses a rule on the day its end names — so a rule
 * ending 2026-07-01 no longer applies on 2026-07-01.
 */
export function rulePhase(r: Pick<TaxRule, 'effective_from' | 'effective_to'>, on: string): RulePhase {
  const from = dayOf(r.effective_from)
  const to = dayOf(r.effective_to)
  if (from && on < from) return 'scheduled'
  if (to && on >= to) return 'ended'
  return 'in force'
}

export function phaseTone(p: RulePhase): 'ok' | 'warn' | undefined {
  return p === 'in force' ? 'ok' : p === 'scheduled' ? 'warn' : undefined
}

/** "from 2026-01-01" · "2026-01-01 → 2027-01-01 (excluded)". */
export function validityText(r: Pick<TaxRule, 'effective_from' | 'effective_to'>): string {
  const from = dayOf(r.effective_from) || '—'
  const to = dayOf(r.effective_to)
  return to ? `${from} → ${to}` : `from ${from}`
}

/** "OM · Muscat · storage" — the supply a rule governs, in the operator's words. */
export function scopeText(r: Pick<TaxRule, 'country' | 'region' | 'category'>): string {
  return [r.country || '—', (r.region ?? '').trim() || 'every region', (r.category ?? '').trim() || 'every category'].join(' · ')
}

// ── the rule form ──────────────────────────────────────────────────────────

export interface TaxRuleForm {
  name: string
  country: string
  region: string
  category: string
  /** Percent as typed: "5" or "5.5". The wire carries the fraction. */
  rate: string
  kind: string
  note: string
  effective_from: string
  effective_to: string
}

export function emptyTaxRuleForm(from: string, country = ''): TaxRuleForm {
  return { name: '', country, region: '', category: '', rate: '', kind: TAX_STANDARD, note: '', effective_from: from, effective_to: '' }
}

export function taxRuleFormFrom(r: TaxRule): TaxRuleForm {
  return {
    name: r.name ?? '',
    country: r.country ?? '',
    region: r.region ?? '',
    category: r.category ?? '',
    rate: fractionToPercent(r.rate),
    kind: (r.kind ?? TAX_STANDARD) as string,
    note: r.note ?? '',
    effective_from: dayOf(r.effective_from),
    effective_to: dayOf(r.effective_to),
  }
}

/**
 * Switching the kind. A rule that is not standard charges nothing, and the
 * API refuses one that carries a rate — so the form holds the rate at 0
 * rather than sending something it knows will come back 400.
 */
export function withKind(f: TaxRuleForm, kind: string): TaxRuleForm {
  return { ...f, kind, rate: chargesTax(kind) ? f.rate : '0' }
}

const PCT = /^\d+(\.\d+)?$/

/** The same rules store.TaxRule.Validate applies, seen before the round trip. */
export function validateTaxRule(f: TaxRuleForm): Errors<TaxRuleForm> {
  const e: Errors<TaxRuleForm> = {}
  if (!f.name.trim()) e.name = 'Name is required — it is what the rules table and the invoice summary call this rule.'
  if (!/^[A-Za-z]{2}$/.test(f.country.trim())) e.country = 'A two-letter ISO 3166-1 alpha-2 code, e.g. OM.'
  if (!TAX_KINDS.some((k) => k.value === f.kind)) e.kind = `Choose one of ${TAX_KINDS.map((k) => k.label).join(', ')}.`
  const rate = f.rate.trim()
  if (chargesTax(f.kind)) {
    if (!rate) e.rate = 'A percentage is required, e.g. 5.'
    else if (!PCT.test(rate)) e.rate = 'A percentage, e.g. 5 or 5.5.'
    else if (Number(rate) > 100) e.rate = 'At most 100 %.'
  } else if (rate && Number(rate) !== 0) {
    e.rate = `A ${taxKindLabel(f.kind).toLowerCase()} rule charges nothing, so its rate must be 0.`
  }
  if (!f.effective_from.trim()) e.effective_from = 'The day the rule starts applying is required.'
  else if (!isDay(f.effective_from.trim())) e.effective_from = 'Use YYYY-MM-DD.'
  const to = f.effective_to.trim()
  if (to && !isDay(to)) e.effective_to = 'Use YYYY-MM-DD.'
  else if (to && f.effective_from.trim() && to <= f.effective_from.trim()) e.effective_to = 'The end must be after the start.'
  return e
}

/**
 * The body POST /tax/rules and PUT /tax/rules/{id} decode. The rate travels
 * as a FRACTION — 5 typed here is 0.05 on the wire — and a kind that charges
 * nothing sends 0 whatever the field last held.
 */
export function taxRuleBody(f: TaxRuleForm): Record<string, string> {
  return {
    name: f.name.trim(),
    country: f.country.trim().toUpperCase(),
    region: f.region.trim(),
    category: f.category.trim(),
    rate: chargesTax(f.kind) ? percentToFraction(f.rate) || '0' : '0',
    kind: f.kind,
    note: f.note.trim(),
    effective_from: f.effective_from.trim(),
    effective_to: f.effective_to.trim(),
  }
}

// ── the category form ──────────────────────────────────────────────────────

export interface TaxCategoryForm {
  sku: string
  category: string
  note: string
}

export function emptyTaxCategoryForm(): TaxCategoryForm {
  return { sku: '', category: '', note: '' }
}

export function taxCategoryFormFrom(c: TaxCategoryRule): TaxCategoryForm {
  return { sku: c.sku ?? '', category: c.category ?? '', note: c.note ?? '' }
}

/** A pattern row covers a whole SKU family: `evs.*` is every block-storage meter. */
export function isSKUFamily(sku: string | null | undefined): boolean {
  return (sku ?? '').trim().endsWith('*')
}

export function validateTaxCategory(f: TaxCategoryForm): Errors<TaxCategoryForm> {
  const e: Errors<TaxCategoryForm> = {}
  const sku = f.sku.trim()
  if (!sku) e.sku = 'A SKU is required: an exact SKU (k8s.vcpu) or a prefix ending in * (evs.*).'
  else if (/\s/.test(sku)) e.sku = 'A SKU has no spaces.'
  else if (sku.includes('*') && !sku.endsWith('*')) e.sku = 'A * may only END the pattern: evs.* is the whole storage family.'
  else if (sku === '*') e.sku = 'Every SKU is already the default category — delete a row instead of writing *.'
  if (!f.category.trim()) e.category = 'A category is required; delete the row to return the SKU to the default category.'
  return e
}

/** The body PUT /tax/categories decodes. */
export function taxCategoryBody(f: TaxCategoryForm): Record<string, string> {
  return { sku: f.sku.trim(), category: f.category.trim(), note: f.note.trim() }
}

/**
 * The categories a rule can name: every category placed by a SKU row, plus
 * whatever the rules already use. The default category is "" and is offered
 * as the catch-all rather than as a name.
 */
export function knownCategories(cats: TaxCategoryRule[], rules: TaxRule[]): string[] {
  const out: string[] = []
  for (const c of cats) {
    const v = (c.category ?? '').trim()
    if (v && !out.includes(v)) out.push(v)
  }
  for (const r of rules) {
    const v = (r.category ?? '').trim()
    if (v && !out.includes(v)) out.push(v)
  }
  return out.sort((a, b) => a.localeCompare(b))
}

// ── the customer's exemption certificate ───────────────────────────────────

/** The day part of a wire value: a customer document sends a timestamp, a PATCH takes YYYY-MM-DD. */
export function dayOf(v: string | null | undefined): string {
  return (v ?? '').slice(0, 10)
}

export type CertificateState = 'none' | 'open-ended' | 'valid' | 'expired'

/**
 * Whether the exemption certificate still holds on a day. It is valid
 * THROUGH the day it names and has lapsed the day after — the same day
 * comparison store.TaxEngine.Resolve makes, which is what keeps this badge
 * and the rate a statement is actually charged at from disagreeing. Once it
 * has lapsed the rating engine falls back to the STANDARD RATE, and silently
 * falling back is how an issuer under-charges tax and carries the liability,
 * which is why this is shown.
 */
export function certificateState(c: Pick<Customer, 'tax_exempt' | 'tax_exemption_number' | 'tax_exemption_expires_on'>, on: string): CertificateState {
  const expires = dayOf(c.tax_exemption_expires_on)
  if (!expires) return c.tax_exempt === true || (c.tax_exemption_number ?? '').trim() ? 'open-ended' : 'none'
  return expires < on ? 'expired' : 'valid'
}

export function certificateText(state: CertificateState, expires: string): string {
  switch (state) {
    case 'expired':
      return `expired on ${expires} — invoices are rated at the standard rate until a current certificate is recorded`
    case 'valid':
      return `in force through ${expires}`
    case 'open-ended':
      return 'no expiry recorded — the exemption does not lapse'
    default:
      return 'no certificate recorded'
  }
}

export function certificateTone(state: CertificateState): 'ok' | 'warn' | 'bad' | undefined {
  return state === 'expired' ? 'bad' : state === 'valid' ? 'ok' : state === 'open-ended' ? 'warn' : undefined
}

// ── the tax summary on a statement ─────────────────────────────────────────

/** What the per-rule summary adds up to, for the reader who checks it against the invoice's tax. */
export function taxLinesTotal(lines: TaxLine[] | null | undefined): number {
  return (lines ?? []).reduce((n, l) => n + toNumber(l.tax), 0)
}

/** The taxable base the summary covers — several rates each tax their own slice of the net. */
export function taxBaseTotal(lines: TaxLine[] | null | undefined): number {
  return (lines ?? []).reduce((n, l) => n + toNumber(l.base), 0)
}

/** One line per rate on the invoice: how many distinct rates the reader has to reconcile. */
export function distinctRates(lines: TaxLine[] | null | undefined): number {
  return new Set((lines ?? []).map((l) => `${l.kind}:${toNumber(l.rate)}`)).size
}

// ── e-invoicing ────────────────────────────────────────────────────────────

export const EINVOICE_STATES: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'built', label: 'Built', help: 'The structured document exists and has been validated; it is not signed yet.' },
  { value: 'signed', label: 'Signed', help: 'Signed, and not yet written to the archive.' },
  { value: 'archived', label: 'Archived', help: 'Signed and archived here with its hash. A complete outcome where the authority publishes no endpoint to submit to.' },
  { value: 'submitted', label: 'Submitted', help: 'Accepted by the tax authority, under the reference below.' },
  { value: 'not_submitted', label: 'Not submitted', help: 'Signed and archived, but the submission did not complete. The reason is recorded in full.' },
]

export function einvoiceStateLabel(state: string | null | undefined): string {
  const s = (state ?? '').trim()
  return EINVOICE_STATES.find((x) => x.value === s)?.label ?? s
}

export function einvoiceStateHelp(state: string | null | undefined): string {
  return EINVOICE_STATES.find((x) => x.value === (state ?? '').trim())?.help ?? ''
}

export function einvoiceTone(state: string | null | undefined): 'ok' | 'warn' | 'info' | undefined {
  switch ((state ?? '').trim()) {
    case 'submitted':
      return 'ok'
    case 'archived':
    case 'signed':
      return 'info'
    case 'not_submitted':
      return 'warn'
    default:
      return undefined
  }
}

/** Whether the signed archival copy is worth offering: only an archived document has one. */
export function hasArchivedXML(e: EInvoiceState | null | undefined): boolean {
  if (!e) return false
  return ['archived', 'submitted', 'not_submitted'].includes((e.state ?? '').trim())
}

/** The signed archival XML — a file download, served with Content-Disposition. */
export function einvoiceXMLURL(statementID: string): string {
  return `${API_BASE}/statements/${statementID}/einvoice.xml`
}

/** The structured document behind the state. */
export function einvoiceDocumentURL(statementID: string): string {
  return `${API_BASE}/statements/${statementID}/einvoice`
}
