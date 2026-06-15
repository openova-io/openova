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

  it('opens the edit form (name / topologies / icons) on Edit click', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('sov-app-edit-bp-cilium'))
    const dialog = await screen.findByTestId('catalog-edit-dialog')
    expect(within(dialog).getByTestId('catalog-edit-name')).toBeTruthy()
    expect(within(dialog).getByTestId('catalog-edit-topologies')).toBeTruthy()
    expect(within(dialog).getByTestId('catalog-edit-icon-light')).toBeTruthy()
    expect(within(dialog).getByTestId('catalog-edit-icon-dark')).toBeTruthy()
    // Topology multiselect exposes the canonical ALL_MODES options.
    expect(within(dialog).getByTestId('catalog-edit-topo-single-region')).toBeTruthy()
    expect(within(dialog).getByTestId('catalog-edit-topo-active-active')).toBeTruthy()
    expect(within(dialog).getByTestId('catalog-edit-topo-active-hotstandby')).toBeTruthy()
  })

  it('saves the edit via the #3602 API with the typed body', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('sov-app-edit-bp-cilium'))
    const dialog = await screen.findByTestId('catalog-edit-dialog')
    fireEvent.change(within(dialog).getByTestId('catalog-edit-name'), {
      target: { value: 'Cilium (Edited)' },
    })
    fireEvent.click(within(dialog).getByTestId('catalog-edit-topo-active-active'))
    fireEvent.change(within(dialog).getByTestId('catalog-edit-icon-dark'), {
      target: { value: 'https://cdn/cilium-dark.svg' },
    })
    fireEvent.click(within(dialog).getByTestId('catalog-edit-save'))

    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const [slugArg, editArg] = saveSpy.mock.calls[0]
    expect(slugArg).toBe('bp-cilium')
    expect(editArg).toMatchObject({
      name: 'Cilium (Edited)',
      icon_dark: 'https://cdn/cilium-dark.svg',
    })
    expect(editArg.supported_topologies).toContain('active-active')
  })

  it('reflects a saved name on the card after the edits refetch', async () => {
    renderCatalog()
    await screen.findByTestId('sov-app-card-bp-cilium')
    // After a save the edits query refetches; simulate the store now
    // carrying the renamed cilium row, then trigger the dialog save path.
    listAppsHolder.value = [{ slug: 'cilium', name: 'Cilium (Renamed)' }]
    fireEvent.click(screen.getByTestId('sov-app-edit-bp-cilium'))
    fireEvent.click(await screen.findByTestId('catalog-edit-save'))
    // editsQuery.refetch() re-reads listApps → the card title updates.
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
