// Tests for #5496 — a React Query cache key mismatch that caused SILENT DATA LOSS.
//
// CatalogDetail registered its query under `qualifiedName` ("bp-alloy") and
// invalidated under the bare `name` ("alloy"). The keys never matched, so the
// invalidation no-oped and `staleTime: 30_000` held the pre-edit document.
// Two consequences from one cause:
//   (a) the hero kept stale text until a full reload — UAT 127/132/133/142/153;
//   (b) the next per-field save computed its merge base from the stale cache and
//       REVERTED the previous save, with no error surfaced. Reproduced live on
//       hw291: a WordPress summary was wiped by the following icon save.
//
// This is a SOURCE INVARIANT test, and it is worth being honest about that: it
// asserts every `['catalog-item', …]` key in the component uses the same
// identifier. It does NOT execute a save. A behavioural two-save test is the
// stronger guard and needs a QueryClient plus API mocks that this suite does
// not currently stand up — that is tracked on #5496 rather than faked here.
//
// The invariant is nonetheless the exact shape of the defect: the bug was a
// literal identifier mismatch between two string-array keys in one file, and
// nothing in TypeScript or the existing tests could see it, because both
// identifiers are valid `string`s in scope.

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const SRC = join(__dirname, 'CatalogDetail.tsx')

describe('#5496 catalog-item query keys must all use the same identifier', () => {
  const source = readFileSync(SRC, 'utf8')

  // Matches ['catalog-item', <identifier>] and captures the identifier.
  const KEY_RE = /\[\s*'catalog-item'\s*,\s*([A-Za-z_$][\w$]*)\s*\]/g

  it('finds the query keys at all (vacuity control)', () => {
    const found = [...source.matchAll(KEY_RE)]
    // If this ever hits zero the rest of the file proves nothing — the keys
    // were renamed or restructured and this guard must be revisited, not
    // silently passed.
    expect(found.length).toBeGreaterThanOrEqual(3)
  })

  it('registers and invalidates under one identifier, never a mix', () => {
    const idents = [...source.matchAll(KEY_RE)].map((m) => m[1])
    const unique = [...new Set(idents)]
    expect(unique).toHaveLength(1)
  })

  it('uses the qualified name, not the bare route param', () => {
    const idents = [...source.matchAll(KEY_RE)].map((m) => m[1])
    // `name` is the bare route param ("alloy"); the query is keyed on the
    // qualified form ("bp-alloy"). Invalidating the bare form is the #5496 bug.
    expect(idents).not.toContain('name')
    expect([...new Set(idents)]).toEqual(['qualifiedName'])
  })
})
