import type { BelowBuyLine, BillTo, MarginReport, MarginRow, Partner, PriceBook, PriceItem, RetailOverride, RetailRule } from '../api/types'
import { toNumber } from './num'

/**
 * Partners — the console's side of DESIGN.md §11. Every figure here is
 * DERIVED, never entered: margin is customer net minus partner buy, and a
 * retail price is a base times a markup. The arithmetic mirrors the server's
 * (internal/rating/partners.go) so the preview a partner sees before saving
 * is the book it gets after.
 */

/** The two billing models, in the words the page uses. */
export const BILL_TO: ReadonlyArray<{ value: BillTo; label: string; help: string }> = [
  { value: 'partner', label: 'Resell', help: 'we invoice the partner its wholesale statement; the partner bills its own customers at its retail book' },
  { value: 'customer', label: 'Agent', help: 'we invoice the end customer at our books and credit the partner its commission' },
]

export function billToLabel(v: string | null | undefined): string {
  return BILL_TO.find((b) => b.value === v)?.label ?? String(v ?? '')
}

export function billToHelp(v: string | null | undefined): string {
  return BILL_TO.find((b) => b.value === v)?.help ?? ''
}

/** The bases a retail rule may be built on. */
export const RETAIL_BASES: ReadonlyArray<{ value: 'list' | 'buy'; label: string; help: string }> = [
  { value: 'buy', label: 'Buy price', help: 'the markup is on what the partner pays us, so the margin is the markup' },
  { value: 'list', label: 'List price', help: 'the markup is on the Sovereign list price, whatever the partner pays' },
]

/** A SKU's service: its first dot-separated segment (ecs.s6.large.2 → ecs). */
export function serviceOf(sku: string): string {
  const i = sku.indexOf('.')
  return i > 0 ? sku.slice(0, i) : sku
}

/**
 * The markup that applies to a SKU: the sku override, else the service
 * override, else the rule's default — most specific wins, like the discount
 * engine.
 */
export function markupFor(rule: Pick<RetailRule, 'markup_pct' | 'overrides'>, sku: string): number {
  const overrides = rule.overrides ?? []
  const bySKU = overrides.find((o) => o.scope === 'sku' && o.key === sku)
  if (bySKU) return toNumber(bySKU.markup_pct)
  const service = serviceOf(sku)
  const byService = overrides.find((o) => o.scope === 'service' && o.key.toLowerCase() === service.toLowerCase())
  if (byService) return toNumber(byService.markup_pct)
  return toNumber(rule.markup_pct)
}

/** One row of the retail preview: what the rule makes of one list price. */
export interface RetailPreviewRow {
  sku: string
  unit: string
  list: number
  buy: number
  retail: number
  /** retail − buy, what the partner keeps per unit. */
  margin: number
  /** Retail under the buy price: the partner would resell at a loss. */
  belowBuy: boolean
}

/**
 * previewRetail is the client-side derivation the rule editor shows BEFORE
 * saving: buy = list × (1 − tier %), retail = base × (1 + markup). The
 * server derives the book with the full combination engine; a single tier
 * percentage is the case the editor previews, and the saved response
 * replaces the preview with what was actually derived.
 */
export function previewRetail(items: PriceItem[], tierPct: number, rule: Pick<RetailRule, 'base' | 'markup_pct' | 'overrides'>): RetailPreviewRow[] {
  return items.map((it) => {
    const list = toNumber(it.unit_price)
    const buy = list * (1 - tierPct / 100)
    const base = rule.base === 'buy' ? buy : list
    const retail = Math.max(0, base * (1 + markupFor(rule, it.sku) / 100))
    return { sku: it.sku, unit: it.unit ?? '', list, buy, retail, margin: retail - buy, belowBuy: retail < buy }
  })
}

/** The below-buy rows of a preview, in the shape the API reports them. */
export function belowBuyOf(rows: RetailPreviewRow[]): BelowBuyLine[] {
  return rows
    .filter((r) => r.belowBuy)
    .map((r) => ({ sku: r.sku, unit: r.unit, list_unit_price: r.list, buy_unit_price: r.buy, retail_unit_price: r.retail }))
}

/** One sentence about a derived book, for the card header. */
export function derivedFromText(book: PriceBook, listBookName: string): string {
  return book.derived_from_rule ? `derived from ${listBookName} — read-only; change the rule, the tier or the list book` : listBookName
}

/** Margin as a percentage of the customer net; null when the net is zero. */
export function marginPct(net: number, buy: number): number | null {
  if (!Number.isFinite(net) || net === 0) return null
  return ((net - buy) / net) * 100
}

/** One row of the margin table, with the figures as numbers. */
export interface MarginTableRow {
  customerId: string
  customer: string
  service: string
  net: number
  buy: number
  margin: number
  pct: number | null
}

/**
 * marginTable reads the report the server sends into the table the page
 * draws — margin recomputed from net and buy so a row can never show a
 * margin that does not equal the two figures beside it.
 */
export function marginTable(report: MarginReport | null | undefined): MarginTableRow[] {
  return (report?.rows ?? []).map((r: MarginRow) => {
    const net = toNumber(r.customer_net)
    const buy = toNumber(r.partner_buy)
    return { customerId: r.customer_id, customer: r.customer_name, service: r.service, net, buy, margin: net - buy, pct: marginPct(net, buy) }
  })
}

/** The totals line of the margin table, summed from the rows it shows. */
export function marginTotals(rows: MarginTableRow[]): { net: number; buy: number; margin: number; pct: number | null } {
  const net = rows.reduce((n, r) => n + r.net, 0)
  const buy = rows.reduce((n, r) => n + r.buy, 0)
  return { net, buy, margin: net - buy, pct: marginPct(net, buy) }
}

/** The customers of one margin report, for the per-customer roll-up. */
export function marginByCustomer(rows: MarginTableRow[]): MarginTableRow[] {
  const by = new Map<string, MarginTableRow>()
  for (const r of rows) {
    const cur = by.get(r.customerId)
    if (!cur) {
      by.set(r.customerId, { ...r, service: 'all services' })
      continue
    }
    cur.net += r.net
    cur.buy += r.buy
    cur.margin = cur.net - cur.buy
    cur.pct = marginPct(cur.net, cur.buy)
  }
  return [...by.values()].sort((a, b) => a.customer.localeCompare(b.customer))
}

/** The partner's balance in the accounting sign the directory shows. */
export function partnerBalance(p: Partner): number | null {
  if (p.balance === undefined || p.balance === null) return null
  const v = toNumber(p.balance)
  return Number.isFinite(v) ? v : null
}

/** What a partner's commercial line reads as in the directory. */
export function partnerTerms(p: Partner): string {
  const parts = [billToLabel(p.bill_to)]
  if (p.tier_name) parts.push(p.tier_name)
  else if (p.bill_to === 'customer' && p.commission_pct !== null && p.commission_pct !== undefined) parts.push(`${toNumber(p.commission_pct)} % commission`)
  else parts.push('no tier')
  return parts.join(' · ')
}

/** A retail override row the editor can add, edit and drop. */
export function emptyOverride(): RetailOverride {
  return { scope: 'service', key: '', markup_pct: 0 }
}

/** Validates a rule before it is sent; "" when it is sound. */
export function validateRetailRule(rule: Pick<RetailRule, 'base' | 'markup_pct' | 'overrides'>): string {
  if (rule.base !== 'list' && rule.base !== 'buy') return 'Choose a base: the list price or the buy price.'
  if (!Number.isFinite(toNumber(rule.markup_pct))) return 'The markup must be a number.'
  if (toNumber(rule.markup_pct) < -100) return 'A markup below −100 % would price below zero.'
  const seen = new Set<string>()
  for (const o of rule.overrides ?? []) {
    if (!o.key.trim()) return 'Every override needs a service or a SKU.'
    if (!Number.isFinite(toNumber(o.markup_pct))) return `The markup for ${o.key} must be a number.`
    const k = `${o.scope}:${o.key.trim()}`
    if (seen.has(k)) return `${o.key} is overridden twice.`
    seen.add(k)
  }
  return ''
}
