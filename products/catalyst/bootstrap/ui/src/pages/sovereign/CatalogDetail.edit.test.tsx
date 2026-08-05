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
 *     sending ONLY that field's keys so no sibling is on the wire (#5510 —
 *     the merge base is the STORED row, resolved inside saveCatalogEdit);
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

// Commerce API — capture per-field saveCatalogEdit calls. Three args since
// #5510: (slug, patch, createSeed). `patch` carries ONLY the edited field;
// `createSeed` carries the page's IaC values for the no-store-row case.
const saveSpy = vi.fn(
  (
    slug: string,
    patch: Record<string, unknown>,
    createSeed: Record<string, unknown> | undefined,
  ): Promise<string> => {
    void patch
    void createSeed
    return Promise.resolve(slug.replace(/^bp-/, ''))
  },
)
vi.mock('@/lib/commerce.api', () => ({
  saveCatalogEdit: (
    slug: string,
    patch: Record<string, unknown>,
    createSeed: Record<string, unknown> | undefined,
  ) => saveSpy(slug, patch, createSeed),
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

  it('editing the NAME field sends ONLY name — no sibling keys in the patch (#5510)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-name-edit'))
    const input = screen.getByTestId('cif-name-input') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'Grafana (Renamed)' } })
    fireEvent.click(screen.getByTestId('cif-name-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const [slug, patch, createSeed] = saveSpy.mock.calls[0]
    expect(slug).toBe('bp-grafana')
    // The edited field changed…
    expect(patch.name).toBe('Grafana (Renamed)')
    // …and NOTHING else is on the wire. #5510: this used to be the FULL
    // 5-field record (`{...currentEdit, ...patch}`), which made every save a
    // whole-record replace and silently reverted store-only siblings.
    expect(Object.keys(patch)).toEqual(['name'])
    // The IaC values ride along as the create-only seed, never as a merge base.
    expect(createSeed?.name).toBe('Grafana')
    expect(createSeed?.tagline).toBe('Visualization and dashboarding')
    // One vocabulary (#3375 DoD-1): the seed carries the CANONICAL topology
    // tokens (the fixture declares them canonical; the page folds any spelling
    // onto the canonical set).
    expect(createSeed?.supported_topologies).toEqual(['singleton', 'active-hot-standby'])
  })

  it('editing the SUMMARY field sends ONLY tagline — name is not on the wire (#5510)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-summary-edit'))
    fireEvent.change(screen.getByTestId('cif-summary-input'), {
      target: { value: 'New tagline' },
    })
    fireEvent.click(screen.getByTestId('cif-summary-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const patch = saveSpy.mock.calls[0][1]
    expect(patch.tagline).toBe('New tagline')
    // The live #5510 repro: a Summary-only save carried `name` (IaC-derived)
    // and overwrote the store's value. `name` must be ABSENT, not merely equal
    // to the IaC title — absent is what leaves the stored value alone.
    expect(patch.name).toBeUndefined()
    expect(Object.keys(patch)).toEqual(['tagline'])
  })

  it('#5610 — the summary editor opens PRE-FILLED with the visible summary, never blank (wipe hazard)', async () => {
    // A card whose visible summary comes ONLY from `description` (no tagline,
    // no summary) — the live hw292 shape. The hero renders `description`; the
    // editor must open showing that same text, not "". Opening blank is one
    // Save away from writing "" over a non-empty summary.
    const DESC_ONLY = {
      ...GRAFANA_CATALOG,
      card: { title: 'Alloy', summary: '', description: 'Node agent for logs, metrics, and traces' },
    }
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/instances')) return jsonRes({ items: [] })
      if (url.includes('/catalog/')) return jsonRes(DESC_ONLY)
      return jsonRes({})
    }) as typeof fetch

    renderCatalog()
    // Hero shows the description-sourced summary.
    expect((await screen.findByTestId('catalog-summary')).textContent).toBe(
      'Node agent for logs, metrics, and traces',
    )
    // Open the inline editor — it must be pre-filled, not empty.
    fireEvent.click(screen.getByTestId('cif-summary-edit'))
    const input = screen.getByTestId('cif-summary-input') as HTMLTextAreaElement
    expect(input.value).toBe('Node agent for logs, metrics, and traces')
  })

  it('editing SUPPORTED TOPOLOGIES toggles a mode and sends only that set', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-topologies-edit'))
    // The canonical current set pre-checks singleton + active-hot-standby;
    // toggle active-active ON.
    fireEvent.click(screen.getByTestId('catalog-edit-topo-active-active'))
    fireEvent.click(screen.getByTestId('cif-topologies-save'))
    await waitFor(() => expect(saveSpy).toHaveBeenCalledTimes(1))
    const patch = saveSpy.mock.calls[0][1] as {
      supported_topologies: string[]
      name?: string
    }
    // One vocabulary (#3375 DoD-1): the saved set is canonical.
    expect(patch.supported_topologies).toContain('active-active')
    expect(patch.supported_topologies).toContain('singleton')
    expect(patch.name).toBeUndefined()
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

  it('the edit picker exposes an Upload control for BOTH light and dark (#4280)', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('cif-icon-edit'))
    // The founder's "upload a new logo" path — not only pick-from-library.
    expect(screen.getByTestId('iconpicker-light-upload')).toBeTruthy()
    expect(screen.getByTestId('iconpicker-dark-upload')).toBeTruthy()
  })
})

describe('CatalogDetail always-visible light/dark logo pair (#4280)', () => {
  it('surfaces BOTH stored logos for an admin WITHOUT entering edit mode', async () => {
    renderCatalog()
    await screen.findByTestId('catalog-title')
    // The dedicated Logos section renders both theme tiles, always visible.
    expect(screen.getByTestId('catalog-section-logos')).toBeTruthy()
    expect(screen.getByTestId('catalog-logo-light')).toBeTruthy()
    expect(screen.getByTestId('catalog-logo-dark')).toBeTruthy()
  })

  it('a non-admin SEES both logos read-only + the sovereign-admin hint, never a blank (#4280)', async () => {
    adminHolder.value = false
    renderCatalog()
    await screen.findByTestId('catalog-title')
    // The hero shows the read-only dual-logo profile pair (not a silent blank).
    const pairs = screen.getAllByTestId('catalog-logo-pair')
    expect(pairs.length).toBeGreaterThan(0)
    // Both theme logos are present (multiple, since the always-visible section
    // also renders them) — assert at least one of each.
    expect(screen.getAllByTestId('catalog-logo-light').length).toBeGreaterThan(0)
    expect(screen.getAllByTestId('catalog-logo-dark').length).toBeGreaterThan(0)
    // The explicit "editing requires sovereign-admin" hint is shown — the
    // feature is visibly present-but-locked, not silently removed.
    const hints = screen.getAllByTestId('catalog-logo-admin-hint')
    expect(hints.length).toBeGreaterThan(0)
    expect(hints[0].textContent).toMatch(/sovereign-admin/i)
  })

  it('renders the light and dark logos with the resolved theme-appropriate src (#4280)', async () => {
    adminHolder.value = false
    renderCatalog()
    await screen.findByTestId('catalog-title')
    // Grafana fixture carries no IaC icons → both resolve to the bundled asset.
    const lightTile = screen.getAllByTestId('catalog-logo-light')[0]
    const darkTile = screen.getAllByTestId('catalog-logo-dark')[0]
    expect(lightTile.querySelector('img')?.getAttribute('src')).toBeTruthy()
    expect(darkTile.querySelector('img')?.getAttribute('src')).toBeTruthy()
  })
})
