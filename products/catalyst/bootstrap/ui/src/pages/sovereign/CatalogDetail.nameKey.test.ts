import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

/**
 * #5449 — the hero version chip read a DIFFERENT object than the rest of the
 * page.
 *
 * `/api/v1/catalog/alloy` and `/api/v1/catalog/bp-alloy` are not two spellings
 * of one thing: the bare form resolves the stale Gitea catalog seed, the
 * `bp-`-qualified form resolves the live Blueprint CR. CatalogDetail stripped
 * the prefix once and then re-added it at every call site EXCEPT the hero
 * fetch, so the chip rendered v1.0.2 from the seed beside a title and summary
 * the page itself had just saved as v1.0.3.
 *
 * This is asserted against the source text rather than by rendering, because
 * the defect is not a rendering bug — it is a call-site drifting off the shared
 * key. A render test would pass with the drift restored so long as a fixture
 * happened to answer both spellings identically, which is exactly how this
 * survived in the first place.
 */
const SRC = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'CatalogDetail.tsx'),
  'utf8',
)

describe('CatalogDetail blueprint name key (#5449)', () => {
  it('derives one qualified key from the route param', () => {
    expect(SRC).toMatch(/const\s+qualifiedName\s*=/)
  })

  it('fetches the catalog item with the bp- qualified key, never the bare name', () => {
    const call = SRC.match(/getCatalogItem\(([^)]*)\)/)
    expect(call, 'getCatalogItem call not found').toBeTruthy()
    const arg = (call as RegExpMatchArray)[1].trim()
    // The bare `name` here is the whole defect: it resolves the stale seed.
    expect(arg).not.toBe('name')
    expect(arg).toBe('qualifiedName')
  })

  it('has exactly one place that builds the bp- prefix', () => {
    // Every call site sharing one derived key is what keeps them from drifting
    // apart again. More than one construction site means the invariant is back
    // to being maintained by hand.
    const inline = SRC.match(/`bp-\$\{name\}`/g) ?? []
    expect(inline.length).toBe(1)
    expect(SRC).toContain("const qualifiedName = name ? `bp-${name}` : ''")
  })

  it('keeps the bare name available for display', () => {
    // The fix must not swing the other way and start rendering "bp-alloy" as
    // the page heading.
    expect(SRC).toMatch(/const\s+name\s*=\s*\(params\.blueprintName\s*\?\?\s*''\)\.replace\(\/\^bp-\/,\s*''\)/)
  })
})

/**
 * #5496 — the SECOND half of the same drift, and the one with teeth.
 *
 * #5449 corrected the REGISTRATION site (`queryKey: ['catalog-item',
 * qualifiedName]`) and left the INVALIDATION site on the bare `name`. A key
 * that never matches makes `invalidateQueries` a silent no-op, so the 30s
 * `staleTime` window holds the pre-save document. The next per-field save then
 * computes its merge base from that stale cache and writes the *pre-edit*
 * sibling values back — the first save is silently reverted, with no error and
 * nothing in the UI to show a write was lost.
 *
 * The fix is live, but nothing locked it: with the invalidation key put back to
 * the bare `name`, all 43 tests across the six CatalogDetail suites still
 * passed. That is precisely the state #5449 shipped in — a half-applied fix
 * under a green suite — so the guard below asserts the property for EVERY
 * `catalog-item` key rather than for the one call site that happened to be
 * wrong at the time.
 */
describe('CatalogDetail catalog-item cache key is uniform (#5496)', () => {
  const keys = [...SRC.matchAll(/\['catalog-item',\s*([A-Za-z0-9_]+)\s*\]/g)].map((m) => m[1])

  it('finds every catalog-item key site', () => {
    // Vacuity guard: if the key shape is ever refactored, this test must fail
    // loudly rather than silently assert over an empty list.
    expect(keys.length).toBeGreaterThanOrEqual(3)
  })

  it('registers AND invalidates on the same qualified key', () => {
    // A single bare `name` anywhere in this set re-opens the lost-update bug:
    // the registered key and the invalidated key stop matching and the refetch
    // becomes a no-op.
    expect(keys).not.toContain('name')
    expect(new Set(keys)).toEqual(new Set(['qualifiedName']))
  })

  it('keeps the documented refetch contract honest', () => {
    // The comment above refetchCatalog states the refetch exists so "the next
    // field's merge base picks up the saved sibling". If the key drifts, that
    // sentence becomes false while still reading as reassuring in review.
    const fn = SRC.match(/const refetchCatalog = \(\) => \{[\s\S]*?\n  \}/)
    expect(fn, 'refetchCatalog not found').toBeTruthy()
    expect((fn as RegExpMatchArray)[0]).toContain('qualifiedName')
    expect((fn as RegExpMatchArray)[0]).not.toMatch(/\['catalog-item',\s*name\s*\]/)
  })
})
