/**
 * CatalogDetail.merge-base-5510.test.tsx — the END-TO-END lock-in for #5510:
 * editing ONE field in the catalog card editor must not silently revert other,
 * untouched fields.
 *
 * Unlike CatalogDetail.edit.test.tsx (which mocks saveCatalogEdit and inspects
 * the patch) this suite mocks ONLY `fetch`, so the whole chain runs for real —
 * CatalogDetail's merge-base construction → CatalogInlineField.save →
 * saveCatalogEdit's read-modify-write → the outbound PUT body. That is the
 * layer the live defect lived at: every individual piece looked correct, and
 * the destruction only appeared in the bytes on the wire.
 *
 * The live hw291 reproduction, replayed here verbatim (`bp-alloy`, store row
 * `b6ac3a3b…`):
 *
 *   IaC card:   title "Alloy", no iconLight  (the incomplete base)
 *   store row:  name "Alloy-NAMEPROOF-1785352700", icon_light ".../cilium.svg"
 *   action:     edit Summary only → Save     ("Saved to IaC ✓", PUT 200)
 *   defect:     name → "Alloy", icon_light → ".../alloy.svg" (the bundled asset)
 *
 * Both destroyed values were UAT walk evidence, so this mechanism erased exactly
 * the durable state an acceptance walk establishes while reporting success.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// ── Mocks ──────────────────────────────────────────────────────────────
// Admin (the edit affordances render) + light theme + no self-discovered
// deploymentId (keeps the bootstrap-singleton branch off).
vi.mock('@/shared/lib/useCatalogAdmin', () => ({ useCatalogAdmin: () => true }))
vi.mock('@/shared/lib/useTheme', () => ({
  useTheme: () => ({ theme: 'light' as const, toggle: () => {} }),
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: undefined, loading: false }),
}))

// NOTE: @/lib/commerce.api is deliberately NOT mocked — saveCatalogEdit is the
// unit under test here, exercised through the page.
import { CatalogDetail } from './CatalogDetail'

/**
 * The IaC card as the catalog read returns it: title "Alloy", NO iconLight.
 * This is the incomplete base the old code built its whole-record replace from.
 */
const ALLOY_CATALOG = {
  name: 'bp-alloy',
  version: '1.0.2',
  card: {
    title: 'Alloy',
    summary: 'Unified node agent for logs, metrics, and traces',
    category: 'observability',
  },
  origin: 'upstream',
  source: 'gitea',
  raw: { spec: { multiInstance: { enabled: true }, topology: { supported: ['singleton'] } } },
}

/** The live store row — `name` + `icon_light` exist ONLY in the store. */
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
}

interface Call {
  url: string
  method: string
  body: Record<string, unknown> | null
}

const calls: Call[] = []
const storeRows: { value: Array<Record<string, unknown>> } = { value: [] }

function jsonRes(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

function renderCatalog() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/$blueprintName',
    component: CatalogDetail,
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const tree = rootRoute.addChildren([catalogRoute, appRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/catalog/alloy'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

/** The single card-save mutation (PUT or POST) — never the list GETs. */
function mutation(): Call {
  const writes = calls.filter(
    (c) => c.url.includes('/org/commerce/apps') && c.method !== 'GET',
  )
  expect(writes).toHaveLength(1)
  return writes[0]
}

beforeEach(() => {
  calls.length = 0
  storeRows.value = [{ ...NAMEPROOF_ROW }]
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    let body: Record<string, unknown> | null = null
    if (typeof init?.body === 'string') body = JSON.parse(init.body) as Record<string, unknown>
    calls.push({ url, method, body })
    // Order matters: the commerce path must be matched BEFORE the generic
    // /catalog/ check, and /instances before both.
    if (url.includes('/instances')) return jsonRes({ items: [] })
    if (url.includes('/org/commerce/apps')) {
      if (method === 'GET') return jsonRes(storeRows.value)
      return jsonRes({ stored: true, committed: true, store: body })
    }
    if (url.includes('/v1/catalog/')) return jsonRes(ALLOY_CATALOG)
    return jsonRes({})
  }) as typeof fetch
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('CatalogDetail — a single-field Save must not destroy siblings (#5510)', () => {
  it('a Summary-only Save preserves the store-only name AND icon_light on the wire', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), {
      target: { value: 'Summary edited by the walk' },
    })
    fireEvent.click(screen.getByTestId('cif-summary-save'))

    // Non-vacuity: wait for the save to land and assert the PUT reached the
    // store row's _id. Nothing below can pass on a save that never happened.
    await waitFor(() => expect(mutation().method).toBe('PUT'))
    const put = mutation()
    expect(put.url).toContain('/org/commerce/apps/b6ac3a3b-0000-4000-8000-000000000000')
    // And the UI reported the durable verdict, i.e. the round-trip completed.
    const toast = await screen.findByTestId('cif-summary-save-verdict')
    expect(toast.textContent).toBe('Saved to IaC ✓')

    const sent = put.body!
    expect(sent.tagline).toBe('Summary edited by the walk')
    // The exact two reverts observed live — now preserved.
    expect(sent.name).toBe('Alloy-NAMEPROOF-1785352700')
    expect(sent.icon_light).toBe('/component-logos/cilium.svg')
    // Restated as the defect: the IaC title and the bundled console asset must
    // NOT be what goes on the wire for a field the operator never touched.
    expect(sent.name).not.toBe('Alloy')
    expect(sent.icon_light).not.toBe('/component-logos/alloy.svg')
  })

  it('an icon-only Save preserves the store-only name (the reverse pairing)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-icon-edit'))
    fireEvent.click(screen.getByTestId('iconpicker-light-tile-loki'))
    fireEvent.click(screen.getByTestId('cif-icon-save'))

    await waitFor(() => expect(mutation().method).toBe('PUT'))
    const sent = mutation().body!
    expect(String(sent.icon_light)).toContain('loki')
    expect(sent.name).toBe('Alloy-NAMEPROOF-1785352700')
    expect(sent.tagline).toBe('Unified node agent')
  })

  it('an explicit NAME edit still persists (requirement 3, direction A)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-name-edit'))
    fireEvent.change(screen.getByTestId('cif-name-input'), {
      target: { value: 'Alloy-NAMEPROOF-2' },
    })
    fireEvent.click(screen.getByTestId('cif-name-save'))

    await waitFor(() => expect(mutation().method).toBe('PUT'))
    const sent = mutation().body!
    expect(sent.name).toBe('Alloy-NAMEPROOF-2')
    // …and the untouched icon still survives.
    expect(sent.icon_light).toBe('/component-logos/cilium.svg')
  })

  it('with an EMPTY store the IaC value still renders (requirement 3, direction B)', async () => {
    storeRows.value = []
    renderCatalog()
    // The IaC card title renders — the store holds nothing to overlay.
    const titleEl = await screen.findByTestId('catalog-title')
    expect(titleEl.textContent).toBe('Alloy')
    // The hero icon falls back to the bundled console asset (resolveCatalogIcon
    // step 3) since the IaC declares no iconLight. Rendering it is correct;
    // PERSISTING it on an unrelated save is the defect.
    const heroLogo = document.querySelector('img.hero-logo') as HTMLImageElement | null
    expect(heroLogo?.getAttribute('src')).toContain('/component-logos/alloy.svg')

    // A first Summary edit then CREATES the row, seeded from the IaC values —
    // and must not bake the bundled console asset into the store.
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), {
      target: { value: 'First ever summary' },
    })
    fireEvent.click(screen.getByTestId('cif-summary-save'))

    await waitFor(() => expect(mutation().method).toBe('POST'))
    const sent = mutation().body!
    expect(sent.slug).toBe('alloy')
    expect(sent.tagline).toBe('First ever summary')
    expect(sent.name).toBe('Alloy')
    expect(sent.icon_light).toBe('')
  })
})
