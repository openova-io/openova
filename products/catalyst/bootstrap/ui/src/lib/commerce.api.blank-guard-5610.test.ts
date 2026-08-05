/**
 * commerce.api.blank-guard-5610.test.ts — #5610 seatbelt: a per-field catalog
 * Save must not silently replace a NON-EMPTY stored value with an empty one.
 *
 * Why this guard lives at the API seam and not only in the editor
 * ──────────────────────────────────────────────────────────────
 * #5610 facet A was the summary inline editor opening BLANK over a non-empty
 * summary — one Save away from writing "" over it. The pre-fill itself is
 * fixed (CatalogDetail.tsx `summaryDraft`), but a guard built on the editor's
 * own idea of "the current value" would be reading the exact prop that was
 * wrong, so it would be blind in precisely the case it exists for. This guard
 * compares the patch against the STORED ROW that saveCatalogEdit already
 * fetches for its merge base — an independent source that holds even if the
 * pre-fill regresses.
 *
 * The wipe is REAL, not theoretical. `Store.UpdateApp` `$set`s every column
 * (core/services/catalog/store/store.go:301), so an empty tagline zeroes the
 * stored one. The catalyst-api read path happens to MASK it on the page the
 * operator wiped it from — `overlayCatalogEdits` overlays only a non-empty
 * tagline (catalog_overlay.go:112) and the git leg uses `setIfNonEmpty`
 * (catalog_edit_blueprint_yaml.go:75) — but the raw store column is what the
 * Organization console (core/console/src/lib/api.ts:239) and the customer
 * storefront (core/marketplace/src/lib/api.ts:164) render.
 *
 * The guard is a CONFIRMATION, never a ban — deliberate clearing must stay
 * possible (store.go:307 calls out clearing a theme-icon override by name).
 * Both directions are pinned below.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { saveCatalogEdit } from './commerce.api'

interface Call {
  url: string
  method: string
  body: Record<string, unknown> | null
}

const calls: Call[] = []
const storeRows: { value: Array<Record<string, unknown>> } = { value: [] }

/** A stored row whose card fields are all NON-EMPTY — every one of them is
 *  something an empty save would destroy. */
const STORED_ROW = {
  id: 'b6ac3a3b-0000-4000-8000-000000000000',
  slug: 'alloy',
  name: 'Alloy',
  tagline: 'Unified node agent for logs, metrics, and traces',
  icon_light: '/component-logos/alloy.svg',
  icon_dark: '/component-logos/alloy-dark.svg',
  supported_topologies: ['singleton'],
  published: true,
}

const IAC_SEED = {
  name: 'Alloy',
  tagline: 'Unified node agent for logs, metrics, and traces',
  supported_topologies: ['singleton'],
  icon_light: '',
  icon_dark: '',
}

function jsonRes(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

beforeEach(() => {
  calls.length = 0
  storeRows.value = [{ ...STORED_ROW }]
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    let body: Record<string, unknown> | null = null
    if (typeof init?.body === 'string') body = JSON.parse(init.body) as Record<string, unknown>
    calls.push({ url, method, body })
    if (url.includes('/org/commerce/apps') && method === 'GET') return jsonRes(storeRows.value)
    return jsonRes({ stored: true, committed: true, store: body })
  }) as typeof fetch
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** Every write (PUT/POST) the save issued — the list GET is not a write. */
function writes(): Call[] {
  return calls.filter((c) => c.method === 'PUT' || c.method === 'POST')
}

describe('saveCatalogEdit — blank-write seatbelt (#5610)', () => {
  it('REFUSES a summary save that would blank a non-empty stored tagline — nothing reaches the wire', async () => {
    const verdict = await saveCatalogEdit('bp-alloy', { tagline: '' }, IAC_SEED)

    // The refusal is reported…
    expect(verdict.blanked).toEqual([
      { key: 'tagline', current: 'Unified node agent for logs, metrics, and traces' },
    ])
    expect(verdict.stored).toBe(false)
    // …and — the whole point — NO write was issued. A refusal that still PUT
    // would be theater.
    expect(writes()).toHaveLength(0)
    // Non-vacuity: the guard ran against a row it actually fetched.
    expect(calls.filter((c) => c.method === 'GET')).toHaveLength(1)
  })

  it('whitespace-only is blank too — "   " does not sneak the wipe past the guard', async () => {
    const verdict = await saveCatalogEdit('bp-alloy', { tagline: '   ' }, IAC_SEED)
    expect(verdict.blanked?.[0].key).toBe('tagline')
    expect(writes()).toHaveLength(0)
  })

  it('holds even when the caller believes the current value is empty (the pre-fill-regression case)', async () => {
    // Simulates exactly the #5610 facet-A shape: the page thinks the summary
    // is "" (a blank editor), so its create-seed carries "" too, and the patch
    // is an innocent-looking empty tagline. The stored row still has the text.
    // A guard keyed on the caller's own view would wave this through.
    const verdict = await saveCatalogEdit('bp-alloy', { tagline: '' }, { ...IAC_SEED, tagline: '' })
    expect(verdict.blanked?.[0].current).toBe('Unified node agent for logs, metrics, and traces')
    expect(writes()).toHaveLength(0)
  })

  it('reports EVERY column a multi-key patch would blank (the icon pair)', async () => {
    const verdict = await saveCatalogEdit('bp-alloy', { icon_light: '', icon_dark: '' }, IAC_SEED)
    expect(verdict.blanked?.map((f) => f.key)).toEqual(['icon_light', 'icon_dark'])
    expect(writes()).toHaveLength(0)
  })

  it('clearing the whole topology set is guarded as well', async () => {
    const verdict = await saveCatalogEdit('bp-alloy', { supported_topologies: [] }, IAC_SEED)
    expect(verdict.blanked).toEqual([{ key: 'supported_topologies', current: 'singleton' }])
    expect(writes()).toHaveLength(0)
  })

  /* ── The other direction: the guard must not break real editing ──────── */

  it('NEGATIVE CASE — a confirmed clear still writes the empty value through', async () => {
    // store.go:307 documents clearing a theme-icon override as a supported
    // operation. The seatbelt costs a confirmation, it does not remove the
    // capability — if this fails, the "fix" is a blanket block.
    const verdict = await saveCatalogEdit(
      'bp-alloy',
      { icon_light: '' },
      IAC_SEED,
      { allowBlank: ['icon_light'] },
    )
    expect(verdict.blanked).toBeUndefined()
    expect(verdict.stored).toBe(true)
    const put = writes()
    expect(put).toHaveLength(1)
    expect(put[0].method).toBe('PUT')
    // The empty value genuinely reached the store…
    expect(put[0].body!.icon_light).toBe('')
    // …and #5510 still holds: the untouched siblings kept their stored values.
    expect(put[0].body!.tagline).toBe('Unified node agent for logs, metrics, and traces')
    expect(put[0].body!.name).toBe('Alloy')
  })

  it('NEGATIVE CASE — confirming ONE column does not license blanking another', async () => {
    const verdict = await saveCatalogEdit(
      'bp-alloy',
      { icon_light: '', icon_dark: '' },
      IAC_SEED,
      { allowBlank: ['icon_light'] },
    )
    expect(verdict.blanked?.map((f) => f.key)).toEqual(['icon_dark'])
    expect(writes()).toHaveLength(0)
  })

  it('an ordinary non-empty edit is untouched by the guard — single call, still writes', async () => {
    const verdict = await saveCatalogEdit('bp-alloy', { tagline: 'Edited by the walk' }, IAC_SEED)
    expect(verdict.blanked).toBeUndefined()
    expect(writes()).toHaveLength(1)
    expect(writes()[0].body!.tagline).toBe('Edited by the walk')
  })

  it('clearing a column that is ALREADY empty is not a wipe — no confirmation demanded', async () => {
    storeRows.value = [{ ...STORED_ROW, icon_dark: '' }]
    const verdict = await saveCatalogEdit('bp-alloy', { icon_dark: '' }, IAC_SEED)
    expect(verdict.blanked).toBeUndefined()
    expect(writes()).toHaveLength(1)
  })

  it('the CREATE path (no stored row) is never guarded — there is nothing to lose', async () => {
    storeRows.value = []
    const verdict = await saveCatalogEdit('bp-alloy', { tagline: '' }, IAC_SEED)
    expect(verdict.blanked).toBeUndefined()
    const post = writes()
    expect(post).toHaveLength(1)
    expect(post[0].method).toBe('POST')
  })
})
