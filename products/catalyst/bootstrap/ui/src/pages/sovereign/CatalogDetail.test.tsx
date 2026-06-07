/**
 * CatalogDetail.test.tsx — #3090 lock-in for the CLASS page.
 *
 * The Catalog / class page (`/catalog/$blueprintName`) renders:
 *   • Blueprint header (title + version + multi-instance badge)
 *   • Supported topologies
 *   • The installed-instances list (one row per concrete install)
 *   • A "+ New instance" button that opens the topology-picker dialog
 *     INLINE (no navigation to a /new route — that route 404'd before).
 *
 * Instance rows link to the INSTANCE page `/app/$componentId`.
 *
 * This is the production React tree (products/catalyst/bootstrap/ui) —
 * it replaces the abandoned Astro+Svelte console scaffold whose specs
 * lived under products/catalyst/console (never shipped).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CatalogDetail } from './CatalogDetail'

const GRAFANA_CATALOG = {
  name: 'grafana',
  version: '1.0.5',
  card: { title: 'Grafana', description: 'Dashboards', family: 'observability' },
  origin: 'upstream',
  source: 'gitea',
  raw: {
    spec: {
      multiInstance: { enabled: true },
      topology: {
        supported: ['singleton', 'active-hot-standby'],
        default: 'singleton',
        defaults: { 'single-region': 'singleton' },
      },
    },
  },
}

const GRAFANA_INSTANCES = {
  items: [
    {
      id: 'aaaaaaaa-1111',
      name: 'obs-1',
      blueprint: 'grafana',
      org: 'acme',
      topology: 'singleton',
      status: 'Ready',
    },
    {
      id: 'bbbbbbbb-2222',
      name: 'obs-2',
      blueprint: 'grafana',
      org: 'acme',
      topology: 'singleton',
      status: 'Ready',
    },
  ],
}

function installCatalogFetch() {
  globalThis.fetch = ((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/instances')) {
      return jsonRes(GRAFANA_INSTANCES)
    }
    // /catalog/grafana and /catalog/grafana/versions/<v> both return the
    // catalog item (the dialog fetches the version variant for topology).
    if (url.includes('/catalog/')) {
      return jsonRes(GRAFANA_CATALOG)
    }
    return jsonRes({})
  }) as typeof fetch
}

function jsonRes(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

function renderCatalog(blueprintName: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/$blueprintName',
    component: CatalogDetail,
  })
  // Instance rows link here.
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
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  installCatalogFetch()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('CatalogDetail — #3090 class page', () => {
  it('renders the Blueprint header + multi-instance badge', async () => {
    renderCatalog('grafana')
    expect((await screen.findByTestId('catalog-title')).textContent).toContain('Grafana')
    expect(screen.getByTestId('catalog-version').textContent).toContain('v1.0.5')
    expect(screen.getByTestId('badge-multi-instance')).toBeTruthy()
  })

  it('renders the supported-topologies list', async () => {
    renderCatalog('grafana')
    await screen.findByTestId('catalog-title')
    expect(screen.getByText('singleton')).toBeTruthy()
    expect(screen.getByText('active-hot-standby')).toBeTruthy()
  })

  it('renders the instances list (one row per install) + "+ New instance" button', async () => {
    renderCatalog('grafana')
    // The shared InstancesSection drives the list.
    expect(await screen.findByTestId('sov-section-instances')).toBeTruthy()
    expect(await screen.findByTestId('sov-instances-table')).toBeTruthy()
    expect(screen.getByTestId('sov-instance-row-obs-1')).toBeTruthy()
    expect(screen.getByTestId('sov-instance-row-obs-2')).toBeTruthy()
    expect(screen.getByTestId('btn-new-instance')).toBeTruthy()
  })

  it('instance rows link to the INSTANCE page /app/$componentId', async () => {
    renderCatalog('grafana')
    const link = await screen.findByTestId('sov-instance-link-obs-1')
    expect(link.getAttribute('href')).toBe('/app/bp-grafana')
  })

  it('"+ New instance" opens the topology dialog INLINE (no navigation, no 404)', async () => {
    renderCatalog('grafana')
    const btn = await screen.findByTestId('btn-new-instance')
    // No href — it is a button, not a link to a /new route (the old
    // `/catalog/$blueprintName/new` link 404'd).
    expect(btn.getAttribute('href')).toBeNull()
    fireEvent.click(btn)
    // The dialog mounts in-place; we did NOT navigate away to the
    // instance page (no /new route, no /app route reached).
    expect(await screen.findByTestId('dialog-new-instance')).toBeTruthy()
    expect(screen.queryByTestId('app-detail-target')).toBeNull()
    // The class page itself is still mounted under the dialog.
    expect(screen.getByTestId('catalog-drilldown')).toBeTruthy()
  })
})
