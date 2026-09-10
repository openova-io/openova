import { describe, expect, it } from 'vitest'
import type { CostSource, PriceBook } from '../api/types'
import { BOOK_ROLES, PAYG_BOOK_NAME, PLAN_BOOK_NAME, bookCellText, bookRole, bookRoleLabel, booksForSource, booksInScope, layerLabel, layerOf, layerOfKind, notSoldPerUseNote, scopeCounts, scopeOf, sourceKindLabel, sourcesByLayerText } from './layers'

const book = (id: string, scope: string, name = id): PriceBook =>
  ({ id, name, scope, currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute' }) as PriceBook

const source = (over: Partial<CostSource>): CostSource =>
  ({ id: 's1', kind: 'huawei-project', layer: 'cloud', region: '', project_id: '', status: 'verified', ...over }) as CostSource

describe('layerOfKind', () => {
  it('maps the cloud kinds to cloud and everything else to platform', () => {
    expect(layerOfKind('huawei-project')).toBe('cloud')
    expect(layerOfKind('file')).toBe('cloud')
    expect(layerOfKind('openova-org')).toBe('platform')
    expect(layerOfKind('openova-platform')).toBe('platform')
    expect(layerOfKind('k8s-namespace')).toBe('platform')
  })
  it('an unknown or absent kind is platform, never cloud', () => {
    // Erring towards platform keeps an unknown source away from a cloud
    // book, which is the assignment the server refuses.
    expect(layerOfKind('something-new')).toBe('platform')
    expect(layerOfKind(null)).toBe('platform')
  })
})

describe('layerOf', () => {
  it('reads the server field', () => {
    expect(layerOf(source({ layer: 'platform', kind: 'openova-org' }))).toBe('platform')
    expect(layerOf(source({ layer: 'cloud', kind: 'huawei-project' }))).toBe('cloud')
  })
  it('falls back to the kind for a document that predates the field', () => {
    expect(layerOf({ layer: undefined as unknown as string, kind: 'openova-org' })).toBe('platform')
    expect(layerOf({ layer: '', kind: 'file' })).toBe('cloud')
  })
  it('labels both layers for people', () => {
    expect(layerLabel('cloud')).toBe('Cloud')
    expect(layerLabel('platform')).toBe('Platform')
  })
})

describe('scopeOf', () => {
  it('defaults a book with no scope to cloud', () => {
    expect(scopeOf(book('b', 'platform'))).toBe('platform')
    expect(scopeOf(book('b', 'cloud'))).toBe('cloud')
    expect(scopeOf({ scope: undefined as unknown as string })).toBe('cloud')
  })
})

describe('booksForSource', () => {
  const books = [book('cloud-a', 'cloud', 'NC list'), book('cloud-b', 'cloud', 'NC negotiated'), book('plans', 'platform', 'OpenOva plans')]

  it('offers only the books whose scope matches the layer', () => {
    expect(booksForSource(books, source({ layer: 'cloud' })).map((b) => b.id)).toEqual(['cloud-a', 'cloud-b'])
    expect(booksForSource(books, source({ layer: 'platform', kind: 'openova-org' })).map((b) => b.id)).toEqual(['plans'])
  })

  // The select must never show a choice the server answers 400 for
  // ("price book scope cloud does not match source layer platform").
  it('never offers a cloud book on a platform source', () => {
    const offered = booksForSource(books, source({ layer: 'platform', kind: 'openova-org' }))
    expect(offered.some((b) => scopeOf(b) === 'cloud')).toBe(false)
  })
  it('never offers a platform book on a cloud source', () => {
    const offered = booksForSource(books, source({ layer: 'cloud' }))
    expect(offered.some((b) => scopeOf(b) === 'platform')).toBe(false)
  })
  it('an empty catalogue offers nothing rather than everything', () => {
    expect(booksForSource([], source({ layer: 'cloud' }))).toEqual([])
  })
})

describe('booksInScope + scopeCounts', () => {
  const books = [book('c1', 'cloud'), book('c2', 'cloud'), book('p1', 'platform')]
  it('filters the price-books list by scope', () => {
    expect(booksInScope(books, 'all')).toHaveLength(3)
    expect(booksInScope(books, 'cloud').map((b) => b.id)).toEqual(['c1', 'c2'])
    expect(booksInScope(books, 'platform').map((b) => b.id)).toEqual(['p1'])
  })
  it('counts each scope for the filter', () => {
    expect(scopeCounts(books)).toEqual({ cloud: 2, platform: 1, total: 3 })
    expect(scopeCounts([])).toEqual({ cloud: 0, platform: 0, total: 0 })
  })
})

describe('bookCellText', () => {
  it('names the book, the gap, or that the source is never billed', () => {
    expect(bookCellText(source({ price_book_id: 'b1', price_book_name: 'NC list' }))).toBe('NC list')
    expect(bookCellText(source({ price_book_id: null }))).toBe('no price book')
    expect(bookCellText(source({ internal: true, kind: 'openova-platform', layer: 'platform' }))).toBe('not billed')
  })
})

describe('sourcesByLayerText', () => {
  it('reads "1 cloud · 1 platform"', () => {
    expect(sourcesByLayerText({ cloud_source_count: 1, platform_source_count: 1 })).toBe('1 cloud · 1 platform')
    expect(sourcesByLayerText({ cloud_source_count: 2, platform_source_count: 0 })).toBe('2 cloud')
    expect(sourcesByLayerText({ cloud_source_count: 0, platform_source_count: 3 })).toBe('3 platform')
  })
  it('says none for a customer with no source, and falls back to the total', () => {
    expect(sourcesByLayerText({ cloud_source_count: 0, platform_source_count: 0 })).toBe('none')
    expect(sourcesByLayerText({ source_count: 4 })).toBe('4')
    expect(sourcesByLayerText({})).toBe('—')
  })
})

describe('notSoldPerUseNote', () => {
  it('explains the basis meters instead of asking for a rate', () => {
    const note = notSoldPerUseNote([{ sku: 'k8s.vcpu' }, { sku: 'k8s.mem_gb' }])
    expect(note).toContain('k8s.vcpu, k8s.mem_gb')
    expect(note).toContain('not sold per use')
    expect(note).toContain('No rate is missing')
    expect(note).not.toContain('Add rate')
  })
  it('is empty when there is nothing to explain', () => {
    expect(notSoldPerUseNote([])).toBe('')
  })
})

describe('sourceKindLabel', () => {
  it('names every kind, including the internal one', () => {
    expect(sourceKindLabel('huawei-project')).toBe('Huawei project')
    expect(sourceKindLabel('openova-org')).toBe('OpenOva Organization')
    expect(sourceKindLabel('openova-platform')).toBe('Platform overhead (this Sovereign)')
    expect(sourceKindLabel('unknown-kind')).toBe('unknown-kind')
    expect(sourceKindLabel(null)).toBe('—')
  })
})

// The two platform books are the two ways an Organization is billed, and a
// list of platform books is unreadable without saying which is which.
describe('bookRole', () => {
  it('names the committed-plans card and the pay-per-use card', () => {
    expect(bookRole(book('b1', 'platform', PLAN_BOOK_NAME))).toBe('plans')
    expect(bookRole(book('b2', 'platform', PAYG_BOOK_NAME))).toBe('payg')
    expect(bookRoleLabel(book('b1', 'platform', PLAN_BOOK_NAME))).toBe('committed plans')
    expect(bookRoleLabel(book('b2', 'platform', PAYG_BOOK_NAME))).toBe('pay per use')
  })
  it('matches the name case-insensitively, the way the server looks it up', () => {
    expect(bookRole(book('b1', 'platform', '  organization payg  '))).toBe('payg')
    expect(bookRole(book('b2', 'platform', 'OPENOVA PLANS'))).toBe('plans')
  })
  it('labels no other book, so a clone or a cloud book is never mislabelled', () => {
    expect(bookRole(book('b3', 'platform', 'Acme negotiated'))).toBeNull()
    expect(bookRole(book('b4', 'cloud', PLAN_BOOK_NAME))).toBeNull()
    expect(bookRoleLabel(book('b5', 'cloud', 'NC list'))).toBe('')
  })
  it('explains what each card prices and what it deliberately does not', () => {
    expect(BOOK_ROLES.plans.help).toContain('plan.<slug>')
    expect(BOOK_ROLES.plans.help).toContain('unpriced')
    expect(BOOK_ROLES.payg.help).toContain('flexi')
    expect(BOOK_ROLES.payg.help).toContain('no plan line')
  })
})
