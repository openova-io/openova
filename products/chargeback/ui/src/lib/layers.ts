import type { CostSource, Customer, Layer, PriceBook } from '../api/types'

/**
 * The two-layer ownership model in the UI (DESIGN.md §2, founder direction
 * 2026-09-08). A source belongs to one layer — cloud (a cloud project) or
 * platform (an Organization on this Sovereign) — and the price book that
 * rates it is assigned PER SOURCE, so only a book whose scope equals the
 * source's layer may be offered. Everything here is pure, so the rules the
 * Sources tab and the Price books page apply are unit-tested rather than
 * eyeballed.
 */

export const LAYERS: ReadonlyArray<{ value: Layer; label: string; help: string }> = [
  { value: 'cloud', label: 'Cloud', help: 'A cloud project: its resource kinds are cloud SKUs, priced by a cloud price book.' },
  { value: 'platform', label: 'Platform', help: 'An Organization on this Sovereign: its resource kinds are platform SKUs, priced by a platform price book.' },
]

/** The kinds an operator may create by hand; platform sources are automatic. */
export const CLOUD_SOURCE_KINDS: ReadonlyArray<{ value: string; label: string; help: string }> = [
  { value: 'huawei-project', label: 'Huawei project', help: 'Metered through the Huawei APIs with an AK/SK of the project.' },
  { value: 'file', label: 'File import', help: 'Usage uploaded as CSV rather than collected.' },
]

/** Every source kind, for displaying a source the sync created. */
export const SOURCE_KIND_LABELS: Readonly<Record<string, string>> = {
  'huawei-project': 'Huawei project',
  file: 'File import',
  'openova-org': 'OpenOva Organization',
  'openova-platform': 'Platform overhead (this Sovereign)',
  'k8s-namespace': 'Kubernetes namespace',
}

export function sourceKindLabel(kind: string | null | undefined): string {
  if (!kind) return '—'
  return SOURCE_KIND_LABELS[kind] ?? kind
}

/** cloud (huawei-project, file) or platform — the same derivation the server stores. */
export function layerOfKind(kind: string | null | undefined): Layer {
  return kind === 'huawei-project' || kind === 'file' ? 'cloud' : 'platform'
}

/** A source's layer, falling back to its kind for a document that predates the field. */
export function layerOf(source: Pick<CostSource, 'layer' | 'kind'>): Layer {
  const l = source.layer
  return l === 'cloud' || l === 'platform' ? l : layerOfKind(source.kind)
}

export function layerLabel(layer: Layer | string): string {
  return LAYERS.find((l) => l.value === layer)?.label ?? String(layer)
}

/** A book's scope, defaulting to cloud for a document that predates the field. */
export function scopeOf(book: Pick<PriceBook, 'scope'>): Layer {
  return book.scope === 'platform' ? 'platform' : 'cloud'
}

/**
 * The books that may be assigned to a source: those whose scope equals the
 * source's layer. Offering any other book would produce a 400 the operator
 * cannot act on — the select never shows a choice the server refuses.
 */
export function booksForSource(books: PriceBook[], source: Pick<CostSource, 'layer' | 'kind'>): PriceBook[] {
  const layer = layerOf(source)
  return books.filter((b) => scopeOf(b) === layer)
}

/** The books of one scope — the Price books page filter. */
export function booksInScope(books: PriceBook[], scope: Layer | 'all'): PriceBook[] {
  if (scope === 'all') return books
  return books.filter((b) => scopeOf(b) === scope)
}

/** How many books carry each scope, for the filter's counts. */
export function scopeCounts(books: PriceBook[]): { cloud: number; platform: number; total: number } {
  const out = { cloud: 0, platform: 0, total: books.length }
  for (const b of books) out[scopeOf(b)]++
  return out
}

/**
 * The message a source's price-book cell shows: which book rates it, or why
 * nothing does. An internal source is never billed at all.
 */
export function bookCellText(source: CostSource): string {
  if (source.internal) return 'not billed'
  if (source.price_book_name) return source.price_book_name
  if (source.price_book_id) return source.price_book_id
  return 'no price book'
}

/**
 * "1 cloud · 1 platform" for the customers list. Falls back to the plain
 * total when a document predates the per-layer counts, and says "none" when
 * the customer has no source at all — nothing is collected for it.
 */
export function sourcesByLayerText(c: Pick<Customer, 'cloud_source_count' | 'platform_source_count' | 'source_count'>): string {
  const cloud = c.cloud_source_count
  const platform = c.platform_source_count
  if (typeof cloud !== 'number' || typeof platform !== 'number') {
    return typeof c.source_count === 'number' ? String(c.source_count) : '—'
  }
  if (cloud + platform === 0) return 'none'
  const parts: string[] = []
  if (cloud > 0) parts.push(`${cloud} cloud`)
  if (platform > 0) parts.push(`${platform} platform`)
  return parts.join(' · ')
}

/**
 * The one-line explanation for platform meters a platform book does not
 * price. They are the allocation basis, not a hole in the rate card, so the
 * banner states it instead of nagging "Add rate" (DESIGN.md §2.5).
 */
export function notSoldPerUseNote(rows: ReadonlyArray<{ sku: string }>): string {
  if (rows.length === 0) return ''
  const skus = rows.map((r) => r.sku).join(', ')
  return `${skus} ${rows.length === 1 ? 'is' : 'are'} not sold per use: the plan covers them, and they are the allocation basis. No rate is missing.`
}
