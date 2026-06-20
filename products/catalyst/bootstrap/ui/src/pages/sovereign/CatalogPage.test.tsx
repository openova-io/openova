/**
 * CatalogPage.test.tsx — lock-in for the standalone Catalog page (#3601,
 * EPIC #3597). The catalog grid that used to be the "Catalog" tab on
 * /apps now lives at /catalog as its own surface.
 *
 *   • Renders the catalog grid (one .app-card per catalog descriptor)
 *   • Cards link to the CLASS page /catalog/$blueprint (provision tree:
 *     /provision/$id/catalog/$blueprint)
 *   • Search narrows the visible cards
 *   • Mounts inside PortalShell (sidebar present)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CatalogPage } from './CatalogPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { NotificationProvider } from '@/shared/ui/notifications'

function renderCatalog(deploymentId: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog',
    component: () => <CatalogPage />,
  })
  const catalogDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog/$blueprintName',
    component: () => <div data-testid="catalog-detail-target" />,
  })
  const tree = rootRoute.addChildren([catalogRoute, catalogDetailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}/catalog`] }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ apps: [] }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CatalogPage (#3601)', () => {
  it('mounts inside the PortalShell (sidebar present)', async () => {
    renderCatalog('d-1')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(screen.getByTestId('admin-sidebar')).toBeTruthy()
  })

  it('renders the catalog grid with one card per descriptor', async () => {
    renderCatalog('d-1')
    const grid = await screen.findByTestId('sov-catalog-grid')
    const cards = within(grid).getAllByTestId(/^sov-app-card-bp-/)
    // BOOTSTRAP_KIT (11+) is always present in the resolved catalog.
    expect(cards.length).toBeGreaterThan(0)
  })

  it('catalog card links to the CLASS page /catalog/$blueprint', async () => {
    renderCatalog('d-1')
    const card = await screen.findByTestId('sov-app-card-bp-cilium')
    expect(card.getAttribute('href')).toBe('/provision/d-1/catalog/bp-cilium')
  })

  it('search narrows the visible cards', async () => {
    renderCatalog('d-1')
    const before = within(await screen.findByTestId('sov-catalog-grid')).getAllByTestId(/^sov-app-card-bp-/)
    fireEvent.change(screen.getByTestId('sov-catalog-search'), { target: { value: 'cilium' } })
    const after = within(await screen.findByTestId('sov-catalog-grid')).getAllByTestId(/^sov-app-card-bp-/)
    expect(after.length).toBeLessThan(before.length)
    expect(after.some((c) => c.getAttribute('data-testid') === 'sov-app-card-bp-cilium')).toBe(true)
  })
})

/**
 * #3668 (hw171 catalog walk) — the grid must render every Blueprint the
 * LIVE catalog serves, not only the build-time component graph. A
 * seeded-but-uncatalogued Blueprint such as `bp-wordpress` (the
 * marketplace-funnel CMS — present in catalog-seed/blueprints.yaml but NOT
 * in componentGroups.ts) was invisible in the grid even though its detail
 * page `/catalog/bp-wordpress` rendered fully. This test locks in that the
 * grid now unions the `/api/v1/catalog` items (via `useCatalog`) as extra
 * cards. `useCatalog` is mocked so the merge runs deterministically
 * regardless of the test harness's detected mode.
 */
vi.mock('@/lib/useCatalog', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/useCatalog')>()
  return {
    ...actual,
    useCatalog: () => ({
      data: [
        {
          name: 'bp-wordpress',
          version: '0.4.1',
          card: { title: 'WordPress', summary: 'Turnkey SSO CMS' },
          origin: 2,
          source: 'sovereign',
        },
      ],
      isSuccess: true,
      isError: false,
      isLoading: false,
    }),
  }
})

describe('CatalogPage — live catalog union (#3668)', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
    globalThis.fetch = (() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ apps: [] }),
      } as unknown as Response)) as typeof fetch
  })

  afterEach(() => cleanup())

  it('renders a grid card for a catalog blueprint absent from the static graph (bp-wordpress)', async () => {
    renderCatalog('d-1')
    const grid = await screen.findByTestId('sov-catalog-grid')
    // The wordpress card appears (it is NOT a bootstrap-kit or wizard
    // component, so it can only come from the live-catalog union).
    const wp = await within(grid).findByTestId('sov-app-card-bp-wordpress')
    expect(wp).toBeTruthy()
    expect(wp.getAttribute('href')).toBe('/provision/d-1/catalog/bp-wordpress')
  })

  it('still renders the static bootstrap-kit cards alongside the catalog union', async () => {
    renderCatalog('d-1')
    const grid = await screen.findByTestId('sov-catalog-grid')
    expect(within(grid).getByTestId('sov-app-card-bp-cilium')).toBeTruthy()
    expect(within(grid).getByTestId('sov-app-card-bp-wordpress')).toBeTruthy()
  })
})
