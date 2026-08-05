/**
 * commerce.api.partial-patch-5510.test.ts — regression lock-in for #5510:
 * a single-field catalog Save must not destroy other fields' data.
 *
 * Live reproduction on hw291 (`bp-alloy`, store row `b6ac3a3b…`):
 *
 *   before:  name       = "Alloy-NAMEPROOF-1785352700"
 *            icon_light = "/component-logos/cilium.svg"
 *   action:  edit Summary only → Save        ("Saved to IaC ✓", PUT 200)
 *   after:   name       = "Alloy"                      ← reverted, never touched
 *            icon_light = "/component-logos/alloy.svg" ← reverted, never touched
 *
 * Both destroyed values were UAT walk evidence (a NAMEPROOF marker and a
 * deliberately-changed icon), so the mechanism erased exactly the durable state
 * an acceptance walk establishes — while reporting success.
 *
 * Root cause: the save was a whole-record replace whose base was built from the
 * IaC card alone, so every field the STORE held but the IaC lacked came back as
 * an IaC-derived default. saveCatalogEdit now takes a PATCH of only the edited
 * keys and overlays it on the stored row.
 *
 * The wire endpoint cannot express a partial patch — `PUT /catalog/admin/apps/
 * {id}` decodes into a `store.App` and `Store.UpdateApp` `$set`s every column —
 * so these tests assert the read-modify-write that implements partial-patch
 * semantics client-side: the PUT body must carry the STORED values for every
 * key the patch omits.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { saveCatalogEdit } from './commerce.api'

/** One captured outbound request. */
interface Call {
  url: string
  method: string
  body: Record<string, unknown> | null
}

const calls: Call[] = []
/** The commerce store's `apps` rows the mocked GET returns. */
const storeRows: { value: Array<Record<string, unknown>> } = { value: [] }

/**
 * The live store row from the hw291 reproduction: `name` and `icon_light` are
 * STORE-ONLY values (the IaC card carries neither) and `published` /
 * `deployable` / `helm_chart` are the untouched columns the full-$set
 * UpdateApp would zero if the body dropped them.
 */
const NAMEPROOF_ROW = {
  id: 'b6ac3a3b-0000-4000-8000-000000000000',
  slug: 'alloy',
  name: 'Alloy-NAMEPROOF-1785352700',
  tagline: 'Unified node agent',
  icon_light: '/component-logos/cilium.svg',
  icon_dark: '',
  supported_topologies: ['singleton'],
  published: true,
  deployable: true,
  helm_chart: 'alloy',
}

/** The IaC-derived values the detail page passes as the create-only seed. */
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
  storeRows.value = []
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    let body: Record<string, unknown> | null = null
    if (typeof init?.body === 'string') {
      body = JSON.parse(init.body) as Record<string, unknown>
    }
    calls.push({ url, method, body })
    if (url.includes('/org/commerce/apps') && method === 'GET') {
      return jsonRes(storeRows.value)
    }
    // Both the PUT (update) and POST (create) legs answer with the #3668
    // {stored, committed} envelope catalyst-api wraps the store row in.
    return jsonRes({ stored: true, committed: true, store: body })
  }) as typeof fetch
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** The single mutation the save issued (PUT or POST) — never the list GET. */
function mutation(): Call {
  const writes = calls.filter((c) => c.method === 'PUT' || c.method === 'POST')
  expect(writes).toHaveLength(1)
  return writes[0]
}

describe('saveCatalogEdit — partial patch, stored row is the merge base (#5510)', () => {
  it('a Summary-only save leaves the store-only name AND icon_light intact', async () => {
    storeRows.value = [{ ...NAMEPROOF_ROW }]

    const verdict = await saveCatalogEdit(
      'bp-alloy',
      { tagline: 'Summary edited by the walk' },
      IAC_SEED,
    )

    // Non-vacuity: the write actually happened, against the row's _id, and the
    // server verdict came back — none of the assertions below can pass on a
    // no-op that never reached the wire.
    const put = mutation()
    expect(put.method).toBe('PUT')
    expect(put.url).toContain('/org/commerce/apps/b6ac3a3b-0000-4000-8000-000000000000')
    expect(verdict).toEqual({ slug: 'alloy', stored: true, committed: true })

    const sent = put.body!
    // The edited field landed…
    expect(sent.tagline).toBe('Summary edited by the walk')
    // …and the two fields the live repro destroyed SURVIVED with their STORED
    // values — not the IaC-derived defaults in IAC_SEED.
    expect(sent.name).toBe('Alloy-NAMEPROOF-1785352700')
    expect(sent.icon_light).toBe('/component-logos/cilium.svg')
    expect(sent.name).not.toBe(IAC_SEED.name)
    // The untouched non-card columns still round-trip (full-$set UpdateApp).
    expect(sent.published).toBe(true)
    expect(sent.deployable).toBe(true)
    expect(sent.helm_chart).toBe('alloy')
  })

  it('an icon-only save leaves the store-only name intact', async () => {
    storeRows.value = [{ ...NAMEPROOF_ROW }]

    await saveCatalogEdit(
      'bp-alloy',
      { icon_light: '/component-logos/loki.svg', icon_dark: '' },
      IAC_SEED,
    )

    const sent = mutation().body!
    expect(sent.icon_light).toBe('/component-logos/loki.svg')
    expect(sent.name).toBe('Alloy-NAMEPROOF-1785352700')
    expect(sent.tagline).toBe('Unified node agent')
  })

  it('an explicit edit to a field STILL persists (requirement 3, direction A)', async () => {
    storeRows.value = [{ ...NAMEPROOF_ROW }]

    await saveCatalogEdit('bp-alloy', { name: 'Alloy-NAMEPROOF-2' }, IAC_SEED)

    const sent = mutation().body!
    expect(sent.name).toBe('Alloy-NAMEPROOF-2')
    // …and the sibling the operator did not touch is untouched.
    expect(sent.icon_light).toBe('/component-logos/cilium.svg')
  })

  it('an explicitly CLEARED field is written as empty (undefined ≠ empty string)', async () => {
    storeRows.value = [{ ...NAMEPROOF_ROW }]

    // #5610 — clearing a NON-EMPTY stored value now needs the operator's
    // explicit confirmation, carried as `allowBlank`. The #5510 contract this
    // test exists for is unchanged and still asserted below: present-but-empty
    // is a real edit that reaches the store, and it must not drag siblings
    // with it. What changed is that the intent must be stated, so a blank
    // editor can no longer wipe a value by accident (see
    // `commerce.api.blank-guard-5610.test.ts` for the refusal side).
    await saveCatalogEdit('bp-alloy', { icon_light: '' }, IAC_SEED, {
      allowBlank: ['icon_light'],
    })

    const sent = mutation().body!
    // Present-but-empty means "the operator cleared it" — it must reach the
    // store, otherwise clearing an override would be impossible.
    expect(sent.icon_light).toBe('')
    expect(sent.name).toBe('Alloy-NAMEPROOF-1785352700')
  })

  it('with NO store row yet, the create seeds from the IaC values + the edit', async () => {
    storeRows.value = []

    await saveCatalogEdit('bp-alloy', { tagline: 'First edit' }, IAC_SEED)

    const post = mutation()
    expect(post.method).toBe('POST')
    const sent = post.body!
    expect(sent.slug).toBe('alloy')
    expect(sent.tagline).toBe('First edit')
    // There is nothing stored to preserve on this path, so seeding the row with
    // the values the page already renders keeps the create non-destructive.
    expect(sent.name).toBe('Alloy')
    // The seed carries no icon: the IaC declares none, and the bundled console
    // asset (`/component-logos/alloy.svg`) is a build-time bundle path, not an
    // IaC declaration — materializing it as a stored value is the same class of
    // defect. An absent key leaves the catalog read falling back to the seed.
    expect(sent.icon_light).toBe('')
  })

  it('with no store row and no seed, name falls back to the slug', async () => {
    storeRows.value = []

    await saveCatalogEdit('bp-alloy', { tagline: 'First edit' })

    const sent = mutation().body!
    expect(sent.slug).toBe('alloy')
    expect(sent.name).toBe('alloy')
    expect(sent.tagline).toBe('First edit')
  })
})
