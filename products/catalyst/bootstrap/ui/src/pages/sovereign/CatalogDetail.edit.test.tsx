/**
 * CatalogDetail.edit.test.tsx — #3668 (founder item §5A + §5B) lock-in for the
 * PER-FIELD inline editing + visual icon picker on the catalog detail page.
 *
 * Founder, verbatim: "why there is a global edit button instead of setting each
 * catalog items fields to be edited independently and save globally, why this
 * ugly no standard ux approach… why I am not able to see the already uploaded
 * logos? i need to have profile icon picker like approach."
 *
 * Acceptance pinned here (exercised in jsdom):
 *   • NO single global "Edit" button — each card field is independently editable
 *     in place (name / summary / supported-topologies / icon);
 *   • editing one field and saving commits THAT field via saveCatalogEdit,
 *     merging onto the current values so siblings are preserved;
 *   • the icon field opens the VISUAL icon picker (a grid of the vendored
 *     logos), not a bare filename input;
 *   • a non-admin sees none of the edit affordances.
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
const adminHolder = { value: true }
vi.mock('@/shared/lib/useCatalogAdmin', () => ({
  useCatalogAdmin: () => adminHolder.value,
}))

const themeHolder = { value: 'light' as 'light' | 'dark' }
vi.mock('@/shared/lib/useTheme', () => ({
  useTheme: () => ({ theme: themeHolder.value, toggle: () => {} }),
}))

// No self-discovery deploymentId — keeps the bootstrap-singleton branch off so
// the multi-instance table + the editable topologies section render.
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: undefined, loading: false }),
}))

// Commerce API — capture per-field saveCatalogEdit calls.
const saveSpy = vi.fn((slug: string, edit: Record<string, unknown>): Promise<string> => {
  void edit
  return Promise.resolve(slug.replace(/^bp-/, ''))
})
vi.mock('@/lib/commerce.api', () => ({
  saveCatalogEdit: (slug: string, edit: Record<string, unknown>) => saveSpy(slug, edit),
}))

// Import AFTER mocks.
import { CatalogDetail } from './CatalogDetail'
import { AVAILABLE_ICONS } from '@/shared/lib/catalogIconGallery'

const GRAFANA_CATALOG = {
  name: 'grafana',
  version: '1.0.5',
  card: {
    title: 'Grafana',
    summary: 'Visualization and dashboarding',
    description: 'Dashboards',
    family: 'observability',
    category: 'insights',
  },
  origin: 'upstream',
  source: 'gitea',
  raw: {
    spec: {
      multiInstance: { enabled: true },
      topology: {
        supported: ['singleton', 'active-hot-standby'],
        default: 'singleton',
      },
    },
  },
}

function jsonRes(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

function renderCatalog(blueprintName = 'grafana') {
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
    history: createMemoryHistory({ initialEntries: [`/catalog/${blueprintName}`] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  adminHolder.value = true
  themeHolder.value = 'light'
  saveSpy.mockClear()
  globalThis.fetch = ((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/instances')) return jsonRes({ items: [] })
    if (url.includes('/catalog/')) return jsonRes(GRAFANA_CATALOG)
    return jsonRes({})
  }) as typeof fetch
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('CatalogDetail per-field inline editing (#3668 §5A)', () => {
  it('does NOT render a single global "Edit" button (the rejected UX)', async () => {
    renderCatalog()
    await screen.findByTestId('catalog-title')
    expect(screen.queryByTestId('catalog-detail-edit')).toBeNull()
  })

  it('renders an independent edit affordance per card field for an admin', async () => {
    renderCatalog()
    await screen.findByTestId('catalog-title')
    expect(screen.getByTestId('cif-name-edit')).toBeTruthy()
    expect(screen.getByTestId('cif-summary-edit')).toBeTruthy()
    expect(screen.getByTestId('cif-icon-edit')).toBeTruthy()
    expect(screen.getByTestId('cif-topologies-edit')).toBeTruthy()
    // The full-CR IaC editor is still reachable.
    expect(screen.getByTestId('catalog-detail-edit-iac')).toBeTruthy()
  })

  it('a non-admin sees NONE of the per-field edit affordances', async () => {
    adminHolder.value = false
    renderCatalog()
    await screen.findByTestId('catalog-title')
    expect(screen.queryByTestId('cif-name-edit')).toBeNull()
    expect(screen.queryByTestId('cif-summary-edit')).toBeNull()
    expect(screen.queryByTestId('cif-icon-edit')).toBeNull()
    expect(screen.queryByTestId('catalog-detail-edit-iac')).toBeNull()
  })

  it('editing the NAME field saves only that field, merged onto current values', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-name-edit'))
    const input = screen.getByTestId('cif-name-input') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'Grafana (Renamed)' } })
    fireEvent.click(screen.getByTestId('cif-name-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const [slug, edit] = saveSpy.mock.calls[0]
    expect(slug).toBe('bp-grafana')
    // The edited field changed…
    expect(edit.name).toBe('Grafana (Renamed)')
    // …and the sibling fields are preserved (merge base), not wiped.
    expect(edit.tagline).toBe('Visualization and dashboarding')
    // One vocabulary (#3375 DoD-1): the edit form carries the CANONICAL
    // topology tokens (the fixture declares them canonical; the form folds
    // any spelling onto the canonical set).
    expect(edit.supported_topologies).toEqual(['singleton', 'active-hot-standby'])
  })

  it('editing the SUMMARY field saves tagline only, preserving the name', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), {
      target: { value: 'New tagline' },
    })
    fireEvent.click(screen.getByTestId('cif-summary-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const edit = saveSpy.mock.calls[0][1]
    expect(edit.tagline).toBe('New tagline')
    expect(edit.name).toBe('Grafana')
  })

  it('editing SUPPORTED TOPOLOGIES toggles a mode and saves the set', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-topologies-edit'))
    // The canonical current set pre-checks singleton + active-hot-standby;
    // toggle active-active ON.
    fireEvent.click(screen.getByTestId('catalog-edit-topo-active-active'))
    fireEvent.click(screen.getByTestId('cif-topologies-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const edit = saveSpy.mock.calls[0][1] as { supported_topologies: string[] }
    // One vocabulary (#3375 DoD-1): the saved set is canonical.
    expect(edit.supported_topologies).toContain('active-active')
    expect(edit.supported_topologies).toContain('singleton')
  })

  it('Cancel closes a field editor without saving', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-name-edit'))
    expect(screen.getByTestId('cif-name-input')).toBeTruthy()
    fireEvent.click(screen.getByTestId('cif-name-cancel'))
    await waitFor(() => expect(screen.queryByTestId('cif-name-input')).toBeNull())
    expect(saveSpy).not.toHaveBeenCalled()
  })
})

describe('CatalogDetail visual icon picker (#3668 §5B)', () => {
  it('the icon field opens the VISUAL gallery (not a bare filename input)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-icon-edit'))
    // Both theme pickers render their gallery grids.
    expect(screen.getByTestId('iconpicker-light-grid')).toBeTruthy()
    expect(screen.getByTestId('iconpicker-dark-grid')).toBeTruthy()
    // A known vendored logo shows as a clickable tile.
    expect(screen.getByTestId('iconpicker-light-tile-grafana')).toBeTruthy()
  })

  it('picking a gallery tile + Save writes the chosen icon url to icon_light', async () => {
    const grafana = AVAILABLE_ICONS.find((g) => g.id === 'grafana')!
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-icon-edit'))
    fireEvent.click(screen.getByTestId('iconpicker-light-tile-grafana'))
    fireEvent.click(screen.getByTestId('cif-icon-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const edit = saveSpy.mock.calls[0][1] as { icon_light: string }
    expect(edit.icon_light).toBe(grafana.url)
  })
})
