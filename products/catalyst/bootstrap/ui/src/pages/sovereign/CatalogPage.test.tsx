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

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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
