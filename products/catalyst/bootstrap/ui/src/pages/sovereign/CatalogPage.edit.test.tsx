/**
 * CatalogPage.edit.test.tsx — #3603 (EPIC #3597) lock-in for the admin
 * catalog-edit affordance on the Catalog page.
 *
 * Walk-through (the ticket's acceptance, exercised here in jsdom):
 *   • the per-card Edit button renders for an admin, NOT for a non-admin
 *   • clicking Edit opens the form (name / topologies / icons)
 *   • Save calls the #3602 API and the card reflects the new name live
 *   • the theme-correct icon renders (light icon in light theme; dark in
 *     dark theme)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, within, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { NotificationProvider } from '@/shared/ui/notifications'

// ── Mocks ──────────────────────────────────────────────────────────────
// Admin gate — flipped per-test via the mutable holder below.
const adminHolder = { value: true }
vi.mock('@/shared/lib/useCatalogAdmin', () => ({
  useCatalogAdmin: () => adminHolder.value,
}))

// Commerce API — capture saveCatalogEdit calls + drive the edits overlay.
// The impl returns the bare slug (as the real saveCatalogEdit does) and
// references both args so no-unused-vars stays happy.
const saveSpy = vi.fn((slug: string, edit: Record<string, unknown>): Promise<string> => {
  void edit
  return Promise.resolve(slug.replace(/^bp-/, ''))
})
const listAppsHolder: { value: Array<Record<string, unknown>> } = { value: [] }
vi.mock('@/lib/commerce.api', () => ({
  listApps: async () => listAppsHolder.value,
  saveCatalogEdit: (slug: string, edit: Record<string, unknown>) => saveSpy(slug, edit),
}))

// Theme — flipped per-test.
const themeHolder = { value: 'light' as 'light' | 'dark' }
vi.mock('@/shared/lib/useTheme', () => ({
  useTheme: () => ({ theme: themeHolder.value, toggle: () => {} }),
}))

// Import AFTER mocks so CatalogPage + AppCard pick up the mocked modules.
import { CatalogPage } from './CatalogPage'

function renderCatalog() {
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
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/catalog'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  adminHolder.value = true
  themeHolder.value = 'light'
  listAppsHolder.value = []
  saveSpy.mockClear()
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ apps: [] }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CatalogPage admin edit (#3603)', () => {
  it('renders the Edit button on each catalog card for an admin', async () => {
    renderCatalog()
    // cilium is a bootstrap-kit card always present in the resolved catalog.
    expect(await screen.findByTestId('sov-app-edit-bp-cilium')).toBeTruthy()
  })

  it('does NOT render the Edit button for a non-admin', async () => {
    adminHolder.value = false
    renderCatalog()
    // The card renders, but no Edit button.
    await screen.findByTestId('sov-app-card-bp-cilium')
    expect(screen.queryByTestId('sov-app-edit-bp-cilium')).toBeNull()
  })

  // #3648 (founder item #1) — the popup editor was removed; the Edit chip now
  // navigates to the catalog DETAIL page, which edits in place. #3668 §5A then
  // replaced the detail page's single global "Edit" button + monolithic form
  // with PER-FIELD inline editors; the per-field fields + save + topology
  // multiselect + visual icon picker are covered by the CatalogDetail tests.
  // Here we lock in that the chip navigates and that NO popup is mounted on the
  // grid page anymore.
  it('navigates to the catalog detail page on Edit click (no popup)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('sov-app-edit-bp-cilium'))
    expect(await screen.findByTestId('catalog-detail-target')).toBeTruthy()
    expect(screen.queryByTestId('catalog-edit-dialog')).toBeNull()
  })

  // (The save itself — the #3602 API call with the typed body — now happens in
  // the per-field CatalogInlineField editors on the detail page; it is covered
  // by the CatalogDetail tests, not on the grid page.)

  it('reflects a saved name on the card from the edits overlay', async () => {
    // The edits overlay (listApps) carries a renamed cilium row; the grid card
    // must render the edited name regardless of where the edit was made.
    listAppsHolder.value = [{ slug: 'cilium', name: 'Cilium (Renamed)' }]
    renderCatalog()
    await waitFor(() => {
      const card = screen.getByTestId('sov-app-card-bp-cilium')
      expect(within(card).getByText('Cilium (Renamed)')).toBeTruthy()
    })
  })

  it('renders the light icon in light theme and the dark icon in dark theme', async () => {
    // Seed an edited cilium row carrying both theme icons.
    listAppsHolder.value = [
      {
        slug: 'cilium',
        name: 'Cilium',
        icon_light: 'https://cdn/cilium-light.svg',
        icon_dark: 'https://cdn/cilium-dark.svg',
      },
    ]

    // Light theme → light icon.
    themeHolder.value = 'light'
    const { unmount } = renderCatalog()
    await waitFor(() => {
      const img = screen.getByTestId('sov-app-icon-bp-cilium') as HTMLImageElement
      expect(img.getAttribute('src')).toBe('https://cdn/cilium-light.svg')
    })
    unmount()
    cleanup()

    // Dark theme → dark icon.
    themeHolder.value = 'dark'
    renderCatalog()
    await waitFor(() => {
      const img = screen.getByTestId('sov-app-icon-bp-cilium') as HTMLImageElement
      expect(img.getAttribute('src')).toBe('https://cdn/cilium-dark.svg')
    })
  })
})
