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

export const BILLING_MODES: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'showback', label: 'Showback', help: 'Statements are informational; nothing is invoiced.' },
  { value: 'chargeback', label: 'Chargeback', help: 'Internal recharge — statements are issued to the customer.' },
  { value: 'real', label: 'Real', help: 'The customer is invoiced at price-book rates.' },
]

export const CUSTOMER_KINDS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'external', label: 'External', help: 'A tenant billed for its own cloud projects.' },
  { value: 'organization', label: 'Organization', help: 'An Organization on this Sovereign; usage is allocated from the shared platform.' },
]

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

/** The fields PATCH /customers/{id} accepts, as the settings form edits them. */
export interface CustomerSettings {
  name: string
  admin_email: string
  billing_mode: string
  price_book_id: string
  start_date: string
  status: string
  org_slug: string
}

export function settingsFrom(c: Customer): CustomerSettings {
  return {
    name: c.name ?? '',
    admin_email: c.admin_email ?? '',
    billing_mode: c.billing_mode ?? 'showback',
    price_book_id: c.price_book_id ?? '',
    start_date: c.start_date ? c.start_date.slice(0, 10) : '',
    status: c.status ?? 'pending',
    org_slug: c.org_slug ?? '',
  }
}

/**
 * Only the fields that changed. The server treats an absent key as "leave
 * it" and an empty string as "clear it" for the nullable columns, so a
 * cleared price book is sent as "" and an untouched one is not sent at all.
 */
export function customerPatch(orig: Customer, form: CustomerSettings): Partial<CustomerSettings> {
  const before = settingsFrom(orig)
  const out: Partial<CustomerSettings> = {}
  for (const k of Object.keys(form) as Array<keyof CustomerSettings>) {
    const v = form[k].trim()
    if (v !== before[k]) out[k] = k === 'admin_email' ? v.toLowerCase() : v
  }
  return out
}
