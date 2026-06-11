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
  card: {
    title: 'Grafana',
    summary: 'Visualization and dashboarding',
    description: 'Dashboards',
    family: 'observability',
    category: 'insights',
    docs: 'https://grafana.com/docs/',
    license: 'AGPL-3.0',
    tags: ['observability', 'dashboards'],
  },
  origin: 'upstream',
  source: 'gitea',
  raw: {
    spec: {
      multiInstance: { enabled: true },
      dependencies: ['cnpg', 'loki'],
      topology: {
        supported: ['singleton', 'active-hot-standby'],
        default: 'singleton',
        defaults: { 'single-region': 'singleton' },
        perTopology: {
          'active-hot-standby': {
            placement: {
              tier: 'mgmt',
              clusters: ['mgmt-A', 'mgmt-B'],
              roles: { 'mgmt-A': 'active', 'mgmt-B': 'passive' },
            },
          },
        },
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

  // ── #3165 rebuild — instances-centered, AppDetail-matching surface ──

  it('renders the breadcrumb ‹ Catalog › <Title>', async () => {
    renderCatalog('grafana')
    const crumb = await screen.findByTestId('catalog-breadcrumb')
    expect(crumb.textContent).toContain('Catalog')
    expect(crumb.textContent).toContain('Grafana')
  })

  it('renders the hero with version, category/family, and tag chips', async () => {
    renderCatalog('grafana')
    await screen.findByTestId('catalog-hero')
    expect(screen.getByTestId('catalog-category').textContent).toContain('insights')
    expect(screen.getByTestId('catalog-family').textContent).toContain('observability')
    expect(screen.getByTestId('catalog-license').textContent).toContain('AGPL-3.0')
    expect(screen.getByTestId('catalog-tag-dashboards')).toBeTruthy()
    expect(screen.getByTestId('catalog-docs-link').getAttribute('href')).toBe(
      'https://grafana.com/docs/',
    )
  })

  it('renders the About section from card.description', async () => {
    renderCatalog('grafana')
    expect((await screen.findByTestId('catalog-description')).textContent).toContain(
      'Dashboards',
    )
  })

  it('renders the per-topology placement summary', async () => {
    renderCatalog('grafana')
    await screen.findByTestId('catalog-section-topologies')
    expect(
      screen.getByTestId('catalog-topology-placement-active-hot-standby').textContent,
    ).toContain('mgmt-A (active)')
    // The default topology is flagged.
    expect(screen.getByTestId('catalog-topology-default-singleton')).toBeTruthy()
  })

  it('renders the bundled dependencies section', async () => {
    renderCatalog('grafana')
    await screen.findByTestId('catalog-section-deps')
    expect(screen.getByTestId('catalog-dep-cnpg')).toBeTruthy()
    expect(screen.getByTestId('catalog-dep-loki')).toBeTruthy()
  })

  it('instances table exposes Version + Actions columns and an Open link', async () => {
    renderCatalog('grafana')
    await screen.findByTestId('sov-instances-table')
    // Action "Open →" links drill into the INSTANCE page.
    const open = screen.getByTestId('sov-instance-open-obs-1')
    expect(open.getAttribute('href')).toBe('/app/bp-grafana')
  })

  it('bootstrap/singleton apps surface the running install, not an empty table', async () => {
    // Wire a self-discovery deploymentId + a getApplication response
    // flagged bootstrap:true so CatalogDetail takes the platform-singleton
    // branch instead of the multi-instance table.
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/sovereign/self')) {
        return jsonRes({ deploymentId: 'dep-123', sovereignFQDN: 't1.omani.works' })
      }
      if (url.includes('/applications/bp-grafana')) {
        return jsonRes({
          name: 'bp-grafana',
          namespace: 'grafana',
          blueprint: 'bp-grafana',
          bootstrap: true,
          phase: 'Ready',
          conditions: [],
        })
      }
      if (url.includes('/instances')) {
        return jsonRes({ items: [] })
      }
      if (url.includes('/catalog/')) {
        return jsonRes(GRAFANA_CATALOG)
      }
      return jsonRes({})
    }) as typeof fetch

    renderCatalog('grafana')
    // Platform-singleton card — link to the running install, labelled
    // "platform component", NOT an empty instances table.
    expect(await screen.findByTestId('catalog-section-platform-singleton')).toBeTruthy()
    expect(screen.getByTestId('badge-platform-component')).toBeTruthy()
    expect(screen.getByTestId('catalog-singleton-link').getAttribute('href')).toBe(
      '/app/bp-grafana',
    )
    expect(screen.queryByTestId('sov-instances-table')).toBeNull()
    expect(screen.queryByTestId('btn-new-instance')).toBeNull()
  })

  // ── #3188 / ADR-0010 — the "Data instances" panel on data-engine pages ──

  it('renders the Data instances panel on the bp-postgres class page', async () => {
    const POSTGRES_CATALOG = {
      name: 'postgres',
      version: '0.1.1',
      card: { title: 'PostgreSQL', category: 'data' },
      origin: 'upstream',
      source: 'gitea',
      raw: { spec: { multiInstance: { enabled: true } } },
    }
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      // bp-postgres instances feed the data-instances panel (engine count).
      if (url.includes('/catalog/postgres/instances')) {
        return jsonRes({
          items: [
            {
              id: 'pg-1',
              name: 'shared-pg',
              blueprint: 'postgres',
              org: 'sovereign',
              topology: 'singleton',
              status: 'Ready',
            },
          ],
        })
      }
      if (url.includes('/instances')) return jsonRes({ items: [] })
      if (url.includes('/catalog/')) return jsonRes(POSTGRES_CATALOG)
      return jsonRes({})
    }) as typeof fetch

    renderCatalog('postgres')
    // The ADR-0010 panel + the PostgreSQL engine-class card render here.
    expect(await screen.findByTestId('data-instances-panel')).toBeTruthy()
    expect(await screen.findByTestId('engine-class-card')).toBeTruthy()
    // The one bp-postgres install surfaces as a data-instance card.
    expect(await screen.findByTestId('data-instance-card')).toBeTruthy()
  })

  it('does NOT render the Data instances panel on a non-data Blueprint (grafana)', async () => {
    renderCatalog('grafana')
    await screen.findByTestId('catalog-title')
    expect(screen.queryByTestId('data-instances-panel')).toBeNull()
  })
})
