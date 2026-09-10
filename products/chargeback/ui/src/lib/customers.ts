import type { Customer, PriceBook, Summary } from '../api/types'

/**
 * Readers over the customer list and detail documents (#6867). Pure, so the
 * joins the Customers page draws (MTD from the summary, price-book names,
 * verified/total sources) are unit-tested instead of eyeballed.
 */

export type StatusFilter = 'all' | 'active' | 'pending' | 'suspended'

export const STATUS_FILTERS: ReadonlyArray<{ value: StatusFilter; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'pending', label: 'Pending' },
  { value: 'suspended', label: 'Suspended' },
]

/**
 * The commercial model (DESIGN.md §8). showback / chargeback / real were
 * three labels standing in for three different questions, so a corporate
 * customer invoiced on terms fitted none of them. Three controls now answer
 * the three questions separately; billing_mode is derived server-side and is
 * never shown or sent.
 */
export const CHARGING_OPTIONS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'billed', label: 'Billed', help: 'Statements are invoices and are collected.' },
  { value: 'informational', label: 'Informational', help: 'Statements exist for visibility only; nothing is ever collected.' },
]

export const PAYMENT_MODELS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'prepaid', label: 'Prepaid', help: 'Pays ahead, or holds a balance that is debited when a statement is issued.' },
  { value: 'postpaid', label: 'Postpaid', help: 'Invoiced after the period and pays on terms, usually against a purchase order.' },
]

export const PAYMENT_METHODS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'gateway', label: 'Payment gateway', help: 'A payment gateway collects; choose which one below.' },
  { value: 'transfer', label: 'Bank transfer', help: 'Pays by transfer against the invoice; you record the payment when the bank shows it.' },
  { value: 'internal', label: 'Internal recharge', help: 'A cost-centre recharge. No external money moves.' },
]

/** The gateways this deployment can select. Stripe is the one that exists. */
export const GATEWAYS: ReadonlyArray<{ value: string; label: string }> = [{ value: 'stripe', label: 'Stripe' }]

export function gatewayLabel(name: string | null | undefined): string {
  if (!name) return ''
  return GATEWAYS.find((g) => g.value === name)?.label ?? name
}

/**
 * The compact badge the customer directory shows instead of a mode word:
 * "prepaid · Stripe", "postpaid · transfer", "internal recharge",
 * "informational".
 */
export function commercialLabel(c: Customer): string {
  const charging = c.charging ?? (c.billing_mode === 'showback' ? 'informational' : 'billed')
  if (charging !== 'billed') return 'informational'
  const method = c.payment_method ?? ''
  if (method === 'internal') return 'internal recharge'
  const model = c.payment_model ?? ''
  if (method === 'gateway') return [model, gatewayLabel(c.gateway_name)].filter(Boolean).join(' · ') || 'gateway'
  if (method === 'transfer') return [model, 'transfer'].filter(Boolean).join(' · ')
  return model || 'billed'
}

/** One line under the badge: the terms an invoiced customer is billed on. */
export function commercialDetail(c: Customer): string {
  if ((c.charging ?? '') !== 'billed' || (c.payment_method ?? '') === 'gateway') return ''
  const bits: string[] = []
  if (typeof c.payment_terms_days === 'number') bits.push(c.payment_terms_days === 0 ? 'due on receipt' : `net ${c.payment_terms_days}`)
  if (c.po_reference) bits.push(c.po_reference)
  return bits.join(' · ')
}

export const CUSTOMER_KINDS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'external', label: 'External', help: 'An external account billed for its own cloud projects.' },
  { value: 'organization', label: 'Organization', help: 'An Organization on this Sovereign; usage is allocated from the shared platform.' },
]

/**
 * The plan a customer is on, as the header says it (EPIC #6867, founder
 * direction 2026-09-10). The four sized plans are a committed bundle and are
 * named by their size; FLEXI is not a size at all — it is uncapped and billed
 * off its k8s.* meters — so it reads "pay per use" rather than pretending to
 * be a plan tier. "" is a customer with no plan (every external one).
 *
 * The slug is the same one `store.PlanFlexi` uses server-side.
 */
export const PLAN_FLEXI = 'flexi'

export function planLabel(slug: string | null | undefined): string {
  const s = (slug ?? '').trim().toLowerCase()
  if (!s) return ''
  if (s === PLAN_FLEXI) return 'pay per use'
  return `${s.toUpperCase()} plan`
}

const n = (v: unknown): number => {
  const x = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(x) ? x : 0
}

/** Client-side search over name / slug / admin email + status filter. */
export function filterCustomers(rows: Customer[], q: string, status: StatusFilter): Customer[] {
  const needle = q.trim().toLowerCase()
  return rows.filter((c) => {
    if (status !== 'all' && (c.status ?? '').toLowerCase() !== status) return false
    if (!needle) return true
    return [c.name, c.slug, c.admin_email].some((v) => (v ?? '').toLowerCase().includes(needle))
  })
}

export function customerCounts(rows: Customer[]): { active: number; pending: number; suspended: number; total: number } {
  const out = { active: 0, pending: 0, suspended: 0, total: rows.length }
  for (const c of rows) {
    const s = (c.status ?? '').toLowerCase()
    if (s === 'active') out.active++
    else if (s === 'pending') out.pending++
    else if (s === 'suspended') out.suspended++
  }
  return out
}

/**
 * verified / total sources. The list endpoint sends counts; the detail page
 * embeds the rows. `null` means the document did not say.
 */
export function sourceCounts(c: Customer): { verified: number | null; total: number | null } {
  if (Array.isArray(c.sources)) {
    return { verified: c.sources.filter((s) => s.status === 'verified').length, total: c.sources.length }
  }
  const total = typeof c.source_count === 'number' ? c.source_count : typeof c.sources === 'number' ? c.sources : null
  const verified = typeof c.verified_source_count === 'number' ? c.verified_source_count : null
  return { verified, total }
}

export function sourcesText(c: Customer): string {
  const { verified, total } = sourceCounts(c)
  if (total === null) return '—'
  if (verified === null) return String(total)
  return `${verified}/${total}`
}

/**
 * MTD cost per customer id from the summary's by_customer block. That block
 * is top-10 + "other", so a customer can legitimately be absent: the caller
 * renders "—", never 0, for an id that is not in the map.
 */
export function mtdByCustomer(s: Summary | null | undefined): Map<string, number> {
  const out = new Map<string, number>()
  for (const g of Array.isArray(s?.by_customer) ? s.by_customer : []) {
    const id = g.id ?? g.key
    if (!id || id === 'other') continue
    out.set(id, n(g.cost))
  }
  return out
}

export function mtdFor(map: Map<string, number>, id: string): number | null {
  return map.has(id) ? (map.get(id) as number) : null
}

export function priceBookName(books: PriceBook[], id: string | null | undefined): string | null {
  if (!id) return null
  return books.find((b) => b.id === id)?.name ?? null
}

export function priceBookCurrency(books: PriceBook[], id: string | null | undefined): string | null {
  if (!id) return null
  return books.find((b) => b.id === id)?.currency ?? null
}

/** "2026-08 (issued)" from whichever shape the endpoint used. */
export function lastStatementText(c: Customer): string {
  if (typeof c.last_statement_period === 'string' && c.last_statement_period) return c.last_statement_period.slice(0, 7)
  const s = c.last_statement
  if (!s) return '—'
  if (typeof s === 'string') return s
  const p = s.period_start ? s.period_start.slice(0, 7) : '—'
  return s.status ? `${p} (${s.status})` : p
}

/**
 * The fields PATCH /customers/{id} accepts, as the settings form edits them.
 * billing_mode is NOT among them (DESIGN.md §8): it is derived server-side
 * from charging + payment_method, and sending it would be ignored. The tax
 * profile and the account-credit switches (DESIGN.md §9.4–§9.5) are here
 * too; the tax rate is typed as a percentage and travels as a fraction.
 */
export interface CustomerSettings {
  name: string
  admin_email: string
  charging: string
  payment_model: string
  payment_method: string
  gateway_name: string
  po_reference: string
  payment_terms_days: string
  external_account_id: string
  start_date: string
  status: string
  org_slug: string
  auto_apply_credit: boolean
  suspend_at_zero: boolean
  tax_exempt: boolean
  tax_exempt_reason: string
  /** Percent as typed ("5"); "" leaves the Sovereign default in force. */
  tax_rate: string
  tax_registration_number: string
}

/** 0.05 → "5" for the override field; absent → "" (the Sovereign default applies). */
export function taxRatePercent(v: number | string | null | undefined): string {
  if (v === null || v === undefined || v === '') return ''
  const n = Number(v)
  if (!Number.isFinite(n)) return ''
  return String(Math.round(n * 100 * 1e6) / 1e6)
}

export function settingsFrom(c: Customer): CustomerSettings {
  const charging = c.charging ?? (c.billing_mode === 'showback' ? 'informational' : 'billed')
  return {
    name: c.name ?? '',
    admin_email: c.admin_email ?? '',
    charging,
    payment_model: c.payment_model ?? '',
    payment_method: c.payment_method ?? '',
    gateway_name: c.gateway_name ?? '',
    po_reference: c.po_reference ?? '',
    payment_terms_days: typeof c.payment_terms_days === 'number' ? String(c.payment_terms_days) : '',
    external_account_id: c.external_account_id ?? '',
    start_date: c.start_date ? c.start_date.slice(0, 10) : '',
    status: c.status ?? 'pending',
    org_slug: c.org_slug ?? '',
    auto_apply_credit: c.auto_apply_credit === true,
    suspend_at_zero: c.suspend_at_zero === true,
    tax_exempt: c.tax_exempt === true,
    tax_exempt_reason: c.tax_exempt_reason ?? '',
    tax_rate: taxRatePercent(c.tax_rate),
    tax_registration_number: c.tax_registration_number ?? '',
  }
}

/**
 * The settings-form field names as the saved notice reads them: the known
 * keys get the label the form shows, anything else loses its underscores.
 * String.replace with a string pattern swaps only the first one, which is how
 * the notice read "saved price book_id" on hw307.
 */
const FIELD_LABELS: ReadonlyMap<string, string> = new Map([
  ['price_book_id', 'price book'],
  ['admin_email', 'admin email'],
  ['start_date', 'start date'],
  ['org_slug', 'Organization slug'],
  ['plan_slug', 'plan'],
  ['charging', 'charging'],
  ['payment_model', 'payment model'],
  ['payment_method', 'payment method'],
  ['gateway_name', 'payment gateway'],
  ['po_reference', 'purchase order'],
  ['payment_terms_days', 'payment terms'],
  ['external_account_id', 'billing account id'],
  ['auto_apply_credit', 'auto-apply credit'],
  ['suspend_at_zero', 'suspend at zero'],
  ['tax_exempt', 'tax exemption'],
  ['tax_exempt_reason', 'exemption reason'],
  ['tax_rate', 'tax rate'],
  ['tax_registration_number', 'tax registration number'],
])

export function fieldLabel(key: string): string {
  return FIELD_LABELS.get(key) ?? key.replace(/_/g, ' ')
}

const BOOLEAN_FIELDS: ReadonlyArray<keyof CustomerSettings> = ['auto_apply_credit', 'suspend_at_zero', 'tax_exempt']

/**
 * Only the fields that changed. The server treats an absent key as "leave
 * it" and an empty string as "clear it" for the nullable columns, so a
 * cleared tax-rate override is sent as "" and an untouched one is not sent
 * at all. The switches travel as booleans, the tax rate as a fraction.
 */
export function customerPatch(orig: Customer, form: CustomerSettings): Record<string, string | number | boolean> {
  const before = settingsFrom(orig)
  const out: Record<string, string | number | boolean> = {}
  for (const k of Object.keys(form) as Array<keyof CustomerSettings>) {
    if (BOOLEAN_FIELDS.includes(k)) {
      if (form[k] !== before[k]) out[k] = form[k] === true
      continue
    }
    const v = String(form[k]).trim()
    if (v === before[k]) continue
    // payment_terms_days is a whole number on the wire, not a string: 0 is
    // "due on receipt" and must not read as "not given".
    if (k === 'payment_terms_days') {
      if (v !== '') out[k] = Number(v)
      continue
    }
    if (k === 'tax_rate') {
      out[k] = v === '' ? '' : String(Math.round((Number(v) / 100) * 1e8) / 1e8)
      continue
    }
    out[k] = k === 'admin_email' ? v.toLowerCase() : v
  }
  // Switching charging off makes the other three meaningless; the server
  // clears them, so they are not sent as contradictions alongside it.
  if (out.charging === 'informational') {
    delete out.payment_model
    delete out.payment_method
    delete out.gateway_name
  } else if (out.payment_method && out.payment_method !== 'gateway') {
    delete out.gateway_name
  }
  // An exemption that is switched off takes its reason with it.
  if (out.tax_exempt === false && !('tax_exempt_reason' in out) && before.tax_exempt_reason) out.tax_exempt_reason = ''
  return out
}
