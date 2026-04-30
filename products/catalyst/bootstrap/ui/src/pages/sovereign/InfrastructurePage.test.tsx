/**
 * InfrastructurePage.test.tsx — shell + tab wiring lock-in for the
 * Sovereign Infrastructure surface (issue #227).
 *
 * Coverage:
 *   1. Header renders with the canonical title.
 *   2. Tabs render in the canonical Topology / Compute / Storage /
 *      Network order.
 *   3. The active tab follows the URL path suffix.
 *   4. PortalShell wires (sidebar present).
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { InfrastructurePage, resolveActiveTab, INFRA_TABS } from './InfrastructurePage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

const EMPTY_TREE = {
  cloud: [],
  topology: { pattern: 'solo' as const, regions: [] },
  storage: { pvcs: [], buckets: [], volumes: [] },
}

function renderShell(deploymentId: string, suffix: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const infraRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/infrastructure',
    component: () => (
      <InfrastructurePage
        disableStream
        contentOverride={<div data-testid="infra-content-stub">{suffix}</div>}
        initialDataOverride={EMPTY_TREE}
        deploymentsOverride={[]}
      />
    ),
  })
  // Register every legal sub-route as a no-op child so TanStack
  // Router resolves /infrastructure/{topology,...} to the parent
  // shell + an empty child, mirroring production routing without
  // mounting the heavyweight tab components in the shell test.
  const subRoute = (s: string) =>
    createRoute({
      getParentRoute: () => infraRoute,
      path: `/${s}`,
      component: () => <div data-testid={`infra-sub-${s}`}>{s}</div>,
    })
  const tree = rootRoute.addChildren([
    infraRoute.addChildren([
      subRoute('topology'),
      subRoute('compute'),
      subRoute('storage'),
      subRoute('network'),
    ]),
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/infrastructure/${suffix}`],
    }),
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
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('InfrastructurePage — shell', () => {
  it('renders the Infrastructure title', async () => {
    renderShell('d-1', 'topology')
    expect(await screen.findByTestId('infrastructure-title')).toBeTruthy()
  })

  it('mounts inside the PortalShell (sidebar present)', async () => {
    renderShell('d-1', 'topology')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(screen.getByTestId('admin-sidebar')).toBeTruthy()
  })

  it('renders the Outlet content', async () => {
    renderShell('d-1', 'topology')
    expect(await screen.findByTestId('infra-content-stub')).toBeTruthy()
  })
})

describe('InfrastructurePage — tabs', () => {
  it('renders Topology / Compute / Storage / Network in canonical order', { timeout: 30_000 }, async () => {
    renderShell('d-1', 'topology')
    const tablist = await screen.findByTestId('infrastructure-tabs', undefined, { timeout: 15_000 })
    const tabs = within(tablist).getAllByRole('tab')
    expect(tabs).toHaveLength(4)
    expect(tabs.map((t) => t.textContent?.trim())).toEqual([
      'Topology',
      'Compute',
      'Storage',
      'Network',
    ])
  })

  it('marks the topology tab active when URL ends in /topology', async () => {
    renderShell('d-1', 'topology')
    const topologyTab = await screen.findByTestId('infra-tab-topology')
    expect(topologyTab.getAttribute('aria-selected')).toBe('true')
    expect(screen.getByTestId('infra-tab-compute').getAttribute('aria-selected')).toBe('false')
  })

  it('marks the compute tab active when URL ends in /compute', async () => {
    renderShell('d-1', 'compute')
    const computeTab = await screen.findByTestId('infra-tab-compute')
    expect(computeTab.getAttribute('aria-selected')).toBe('true')
  })

  it('marks the storage tab active when URL ends in /storage', async () => {
    renderShell('d-1', 'storage')
    const tab = await screen.findByTestId('infra-tab-storage')
    expect(tab.getAttribute('aria-selected')).toBe('true')
  })

  it('marks the network tab active when URL ends in /network', async () => {
    renderShell('d-1', 'network')
    const tab = await screen.findByTestId('infra-tab-network')
    expect(tab.getAttribute('aria-selected')).toBe('true')
  })
})

describe('resolveActiveTab — helper', () => {
  it('returns topology by default for unknown paths', () => {
    expect(resolveActiveTab('/sovereign/provision/x/infrastructure')).toBe('topology')
    expect(resolveActiveTab('/anything-else')).toBe('topology')
  })

  it('returns the matching tab for each suffix', () => {
    for (const t of INFRA_TABS) {
      expect(
        resolveActiveTab(`/sovereign/provision/x/infrastructure/${t.suffix}`),
      ).toBe(t.key)
    }
  })
})
