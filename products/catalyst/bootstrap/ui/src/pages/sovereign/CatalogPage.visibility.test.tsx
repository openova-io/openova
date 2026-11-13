/**
 * CatalogPage.visibility.test.tsx — SVC-030: the Catalog page honours
 * `visibility`.
 *
 *   • By default the grid shows only listed Blueprints.
 *   • A card's visibility is the live catalog item's; a card the live
 *     catalog does not serve falls back to the build-time catalog.
 *   • "Show unlisted" (default off) shows every card; it lives in the URL
 *     as `?unlisted=1`.
 *
 * Fixtures: LISTED_STATIC and UNLISTED_STATIC are the first cards of the
 * static graph that the build-time catalog marks listed and unlisted.
 * `bp-alpha` and `bp-beta` exist only in the (mocked) live catalog.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor, within } from '@testing-library/react'
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
import { BLUEPRINT_BY_ID } from '@/shared/constants/catalog.generated'
import { resolveApplications } from './applicationCatalog'

const STATIC_IDS = resolveApplications(INITIAL_WIZARD_STATE.selectedComponents).map((a) => a.id)
const LISTED_STATIC = STATIC_IDS.find((id) => BLUEPRINT_BY_ID[id]?.visibility === 'listed') ?? ''
const UNLISTED_STATIC =
  STATIC_IDS.find((id) => BLUEPRINT_BY_ID[id]?.visibility === 'unlisted') ?? ''

const liveHolder: { value: Array<Record<string, unknown>> } = { value: [] }

vi.mock('@/lib/useCatalog', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/useCatalog')>()
  return {
    ...actual,
    useCatalog: () => ({
      data: liveHolder.value,
      isSuccess: true,
      isError: false,
      isLoading: false,
    }),
  }
})

const LIVE_ITEMS = [
  {
    name: 'bp-alpha',
    version: '1.0.0',
    visibility: 'listed',
    card: { title: 'Alpha', summary: 'A listed Blueprint' },
    origin: 2,
    source: 'sovereign',
  },
  {
    name: 'bp-beta',
    version: '1.0.0',
    visibility: 'unlisted',
    card: { title: 'Beta', summary: 'An unlisted Blueprint' },
    origin: 2,
    source: 'sovereign',
  },
]

function renderCatalog(path: string) {
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
  const tree = rootRoute.addChildren([catalogRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  liveHolder.value = LIVE_ITEMS
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ apps: [] }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CatalogPage — visibility (SVC-030)', () => {
  it('the fixtures hold: the static graph has a listed and an unlisted card', () => {
    expect(LISTED_STATIC).toMatch(/^bp-/)
    expect(UNLISTED_STATIC).toMatch(/^bp-/)
  })

  it('shows only listed Blueprints by default', async () => {
    renderCatalog('/provision/d-1/catalog')
    const grid = await screen.findByTestId('sov-catalog-grid')
    expect(within(grid).getByTestId('sov-app-card-bp-alpha')).toBeTruthy()
    expect(within(grid).getByTestId(`sov-app-card-${LISTED_STATIC}`)).toBeTruthy()
    expect(within(grid).queryByTestId('sov-app-card-bp-beta')).toBeNull()
    expect(within(grid).queryByTestId(`sov-app-card-${UNLISTED_STATIC}`)).toBeNull()
    const toggle = screen.getByTestId('sov-catalog-show-unlisted') as HTMLInputElement
    expect(toggle.checked).toBe(false)
  })

  it('the live visibility wins over the build-time catalog', async () => {
    liveHolder.value = [
      ...LIVE_ITEMS,
      {
        name: UNLISTED_STATIC,
        version: '1.0.0',
        visibility: 'listed',
        card: { title: 'Made listed' },
        origin: 2,
        source: 'sovereign',
      },
      {
        name: LISTED_STATIC,
        version: '1.0.0',
        visibility: 'unlisted',
        card: { title: 'Made unlisted' },
        origin: 2,
        source: 'sovereign',
      },
    ]
    renderCatalog('/provision/d-1/catalog')
    const grid = await screen.findByTestId('sov-catalog-grid')
    expect(within(grid).getByTestId(`sov-app-card-${UNLISTED_STATIC}`)).toBeTruthy()
    expect(within(grid).queryByTestId(`sov-app-card-${LISTED_STATIC}`)).toBeNull()
  })

  it('a bp- qualified live item wins over a bare-name item of the same Blueprint', async () => {
    liveHolder.value = [
      ...LIVE_ITEMS,
      {
        name: 'bp-gamma',
        version: '1.0.0',
        visibility: 'unlisted',
        card: { title: 'Gamma' },
        origin: 2,
        source: 'sovereign',
      },
      {
        name: 'gamma',
        version: '1.0.0',
        visibility: 'listed',
        card: { title: 'Gamma (seed)' },
        origin: 1,
        source: 'public',
      },
    ]
    renderCatalog('/provision/d-1/catalog')
    const grid = await screen.findByTestId('sov-catalog-grid')
    expect(within(grid).getByTestId('sov-app-card-bp-alpha')).toBeTruthy()
    expect(within(grid).queryByTestId('sov-app-card-bp-gamma')).toBeNull()
  })

  it('with ?unlisted=1 every card shows and the toggle is on', async () => {
    renderCatalog('/provision/d-1/catalog?unlisted=1')
    const grid = await screen.findByTestId('sov-catalog-grid')
    expect(within(grid).getByTestId('sov-app-card-bp-alpha')).toBeTruthy()
    expect(within(grid).getByTestId('sov-app-card-bp-beta')).toBeTruthy()
    expect(within(grid).getByTestId(`sov-app-card-${UNLISTED_STATIC}`)).toBeTruthy()
    expect(within(grid).getByTestId(`sov-app-card-${LISTED_STATIC}`)).toBeTruthy()
    const toggle = screen.getByTestId('sov-catalog-show-unlisted') as HTMLInputElement
    expect(toggle.checked).toBe(true)
  })

  it('the toggle writes ?unlisted=1 to the URL and removes it again', async () => {
    const router = renderCatalog('/provision/d-1/catalog')
    await screen.findByTestId('sov-catalog-grid')
    fireEvent.click(screen.getByTestId('sov-catalog-show-unlisted'))
    await waitFor(() => {
      expect(screen.getByTestId('sov-app-card-bp-beta')).toBeTruthy()
    })
    expect(String((router.state.location.search as Record<string, unknown>).unlisted)).toBe('1')
    fireEvent.click(screen.getByTestId('sov-catalog-show-unlisted'))
    await waitFor(() => {
      expect(screen.queryByTestId('sov-app-card-bp-beta')).toBeNull()
    })
    expect((router.state.location.search as Record<string, unknown>).unlisted).toBeUndefined()
  })
})
