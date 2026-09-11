import type { Estimate, PublicCatalog, PublicCatalogPlan, PublicCatalogRate, PublicCatalogSKU } from '../api/types'

/**
 * The public cost calculator's cart (DESIGN.md §11).
 *
 * This module holds the cart STATE and the request it becomes — never money
 * arithmetic. Every figure the page shows comes from the server, which
 * prices through the one rating function the invoices use
 * (POST /public/estimates?preview=1). A total computed here would be a
 * second pricing path, which is exactly what §11 forbids.
 */

/** One row of the cart: a catalog SKU by the hour, or a plan by the month. */
export interface CartLine {
  /** Stable identity of the row — the SKU, or `plan:<slug>`. */
  key: string
  kind: 'sku' | 'plan'
  sku?: string
  plan?: string
  label: string
  unit: string
  /** As typed, so a half-finished number never becomes 0 behind the user. */
  quantity: string
  hours: string
  months: string
}

/** The default month of an estimate: 8760 / 12, as the platform books use. */
export const HOURS_PER_MONTH = '730'
export const MAX_HOURS = 744
export const MAX_MONTHS = 12
export const MAX_QUANTITY = 1e9
export const MAX_LINES = 200

/** A catalog SKU (or a pay-per-use rate) as a cart row. */
export function lineForSKU(item: PublicCatalogSKU | PublicCatalogRate): CartLine {
  const label = 'service' in item && item.service ? `${item.sku}` : item.sku
  return { key: item.sku, kind: 'sku', sku: item.sku, label, unit: item.unit, quantity: '1', hours: HOURS_PER_MONTH, months: '1' }
}

/** A catalog plan as a cart row: a whole month, so hours do not apply. */
export function lineForPlan(plan: PublicCatalogPlan): CartLine {
  return { key: `plan:${plan.slug}`, kind: 'plan', plan: plan.slug, label: `${plan.name} plan`, unit: plan.unit, quantity: '1', hours: HOURS_PER_MONTH, months: '1' }
}

/** Adds a row, or bumps the quantity of the one already there. */
export function addLine(lines: CartLine[], line: CartLine): CartLine[] {
  const at = lines.findIndex((l) => l.key === line.key)
  if (at < 0) return [...lines, line]
  const copy = [...lines]
  const current = Number(copy[at].quantity)
  copy[at] = { ...copy[at], quantity: String(Number.isFinite(current) && current > 0 ? current + 1 : 1) }
  return copy
}

export function updateLine(lines: CartLine[], key: string, patch: Partial<CartLine>): CartLine[] {
  return lines.map((l) => (l.key === key ? { ...l, ...patch } : l))
}

export function removeLine(lines: CartLine[], key: string): CartLine[] {
  return lines.filter((l) => l.key !== key)
}

/**
 * Why a row cannot be priced yet, in the words the server would use — so a
 * half-typed quantity is shown as a hint here instead of as a 400 from the
 * API. Empty string = the row is ready.
 */
export function lineError(line: CartLine): string {
  const qty = Number(line.quantity)
  if (!line.quantity.trim() || !Number.isFinite(qty) || qty <= 0) return 'quantity must be more than 0'
  if (qty > MAX_QUANTITY) return `quantity must be at most ${MAX_QUANTITY.toLocaleString('en-US')}`
  if (line.kind === 'plan') {
    const months = Number(line.months)
    if (!Number.isInteger(months) || months < 1 || months > MAX_MONTHS) return `months must be between 1 and ${MAX_MONTHS}`
    return ''
  }
  const hours = Number(line.hours)
  if (!line.hours.trim() || !Number.isFinite(hours) || hours <= 0 || hours > MAX_HOURS) return `hours must be more than 0 and at most ${MAX_HOURS}`
  return ''
}

/** The rows that can be priced right now. */
export function priceableLines(lines: CartLine[]): CartLine[] {
  return lines.filter((l) => !lineError(l))
}

export interface EstimateRequest {
  region?: string
  contact_email?: string
  lines: Array<{ sku?: string; plan?: string; quantity: string; hours_per_month?: string; months?: number }>
}

/**
 * The request body for POST /public/estimates. A plan line carries months
 * and never hours (the API refuses hours on a plan); an SKU line carries
 * hours and never months.
 */
export function estimateBody(lines: CartLine[], region?: string, contactEmail?: string): EstimateRequest {
  const body: EstimateRequest = {
    lines: priceableLines(lines).map((l) =>
      l.kind === 'plan'
        ? { plan: l.plan, quantity: l.quantity.trim(), months: Number(l.months) }
        : { sku: l.sku, quantity: l.quantity.trim(), hours_per_month: l.hours.trim() },
    ),
  }
  if (region) body.region = region
  const email = (contactEmail ?? '').trim()
  if (email) body.contact_email = email
  return body
}

/** A cart is sendable when it has at least one priceable row and no more than the cap. */
export function cartReady(lines: CartLine[]): boolean {
  const n = priceableLines(lines).length
  return n > 0 && n <= MAX_LINES
}

/** The catalog rows matching a search over the SKU, its service and its description. */
export function searchCatalog(catalog: PublicCatalog | null, query: string): PublicCatalogSKU[] {
  if (!catalog) return []
  const q = query.trim().toLowerCase()
  if (!q) return catalog.skus
  return catalog.skus.filter((s) => `${s.sku} ${s.service} ${s.description ?? ''}`.toLowerCase().includes(q))
}

/** The SKUs grouped by service, services in alphabetical order. */
export function byService(skus: PublicCatalogSKU[]): Array<{ service: string; skus: PublicCatalogSKU[] }> {
  const groups = new Map<string, PublicCatalogSKU[]>()
  for (const s of skus) {
    const list = groups.get(s.service) ?? []
    list.push(s)
    groups.set(s.service, list)
  }
  return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([service, list]) => ({ service, skus: list }))
}

// ── embed mode ─────────────────────────────────────────────────────────
// The page is framed by the marketplace (and by a partner site on the
// configured origins), which cannot measure a cross-origin document: it is
// told the height instead.

/** `?embed=1` — the page drops its header and reports its height. */
export function isEmbed(search: string): boolean {
  const value = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search).get('embed')
  return value === '1' || value === 'true'
}

export const EMBED_MESSAGE = 'openova-estimate-height'

export interface EmbedHeightMessage {
  type: typeof EMBED_MESSAGE
  height: number
}

/** The message the framed page posts to its parent on every size change. */
export function heightMessage(height: number): EmbedHeightMessage {
  return { type: EMBED_MESSAGE, height: Math.max(0, Math.ceil(height)) }
}

/** What a saved estimate's link is, for the Share box. */
export function shareLink(estimate: Pick<Estimate, 'id' | 'share_url'>, origin?: string): string {
  if (estimate.share_url) return estimate.share_url
  return `${origin ?? ''}/estimate/${estimate.id}`
}
