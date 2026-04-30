/**
 * Architecture.test.tsx — render lock-in for the Sovereign Cloud /
 * Architecture sub-page force-directed canvas (P2 of #309).
 *
 * The legacy SVG-layered tests (depth labels, zoom-on-click,
 * data-dim toggles) have been retired with the layout itself. The
 * new coverage:
 *
 *   1. Empty state shows when the tree has no nodes.
 *   2. With the synthetic fixture, the force-graph mounts and renders
 *      a node per type with composite ids.
 *   3. Edge legend lists relations.
 *   4. Search isolates matches + neighbors and shows the counter.
 *   5. Clicking a node opens the right-side detail panel + neighbor list.
 *   6. Right-clicking a node opens the context menu with kind-aware items.
 *   7. Right-clicking the canvas surface offers "Add region".
 *   8. Density slider for a tunable type renders.
 *   9. CRUD modals (Add cluster / Add vCluster) still mount via the
 *      detail panel — same testids the legacy tests asserted.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { CloudPage } from './CloudPage'
import { Architecture } from './Architecture'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderArchitecturePage(data: HierarchicalInfrastructure) {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch

  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => (
      <CloudPage disableStream initialDataOverride={data} deploymentsOverride={[]} />
    ),
  })
  const architectureRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/architecture',
    component: Architecture,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([architectureRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/architecture'],
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

afterEach(() => cleanup())

describe('Architecture — empty', () => {
  it('renders the Provisioning… overlay when the tree is empty', async () => {
    const empty: HierarchicalInfrastructure = {
      cloud: [],
      topology: { pattern: 'solo', regions: [] },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    renderArchitecturePage(empty)
    expect(await screen.findByTestId('cloud-architecture-empty')).toBeTruthy()
  })
})

describe('Architecture — force graph render', () => {
  it('renders the canvas + node groups for every type in the fixture', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('arch-graph-canvas')).toBeTruthy()
    expect(screen.getByTestId('arch-graph-svg')).toBeTruthy()

    // Composite ids: ${type}:${elementId}
    expect(screen.getByTestId('arch-graph-node-Cloud-Cloud:cloud-hetzner')).toBeTruthy()
    expect(screen.getByTestId('arch-graph-node-Region-Region:region-eu-central')).toBeTruthy()
    expect(
      screen.getByTestId('arch-graph-node-Cluster-Cluster:cluster-eu-central-primary'),
    ).toBeTruthy()
    expect(screen.getByTestId('arch-graph-node-vCluster-vCluster:vc-eu-central-dmz')).toBeTruthy()
    expect(screen.getByTestId('arch-graph-node-WorkerNode-WorkerNode:node-eu-cp-0')).toBeTruthy()
    expect(
      screen.getByTestId('arch-graph-node-LoadBalancer-LoadBalancer:lb-eu-central-edge'),
    ).toBeTruthy()
    expect(screen.getByTestId('arch-graph-node-Network-Network:net-eu-central')).toBeTruthy()
  })

  it('renders the edge legend with every relation type', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    await screen.findByTestId('arch-graph-svg')
    expect(screen.getByTestId('cloud-architecture-edge-legend')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-edge-legend-contains')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-edge-legend-runs-on')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-edge-legend-routes-to')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-edge-legend-attached-to')).toBeTruthy()
  })

  it('shows the live nodes/edges stats overlay', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    await screen.findByTestId('arch-graph-svg')
    expect(screen.getByTestId('arch-graph-stats-nodes')).toBeTruthy()
    expect(screen.getByTestId('arch-graph-stats-edges')).toBeTruthy()
  })
})

describe('Architecture — search isolation', () => {
  it('isolates matches + neighbors when typing in the search box', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    await screen.findByTestId('arch-graph-svg')
    const search = screen.getByTestId('cloud-architecture-search') as HTMLInputElement

    fireEvent.change(search, { target: { value: 'omantel-primary' } })

    // Counter shows up after the 250ms debounce; we advance via Vitest
    // fake timers OR simply test the rendered counter element exists
    // once the value has been applied. React Testing Library defers
    // by-state updates to next tick, so we wait for the counter to
    // appear.
    const counter = await screen.findByTestId('cloud-architecture-search-counter')
    expect(counter.textContent).toMatch(/matches/)
  })
})

describe('Architecture — detail panel', () => {
  it('opens the panel on node click and shows the type label', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    const node = await screen.findByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    expect(screen.queryByTestId('infrastructure-detail-panel')).toBeNull()
    fireEvent.click(node)
    expect(screen.getByTestId('infrastructure-detail-panel')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-detail-panel-name').textContent).toBe(
      'omantel-primary',
    )
    expect(screen.getByTestId('infrastructure-detail-panel-type').textContent).toBe('Cluster')
  })

  it('lists neighbors and lets the operator drill into one', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    fireEvent.click(
      await screen.findByTestId('arch-graph-node-Cluster-Cluster:cluster-eu-central-primary'),
    )
    const neighbors = screen.getByTestId('infrastructure-detail-panel-neighbors')
    expect(neighbors).toBeTruthy()
    // At least the parent region and a vcluster should be neighbors.
    expect(
      screen.getByTestId('infrastructure-detail-panel-neighbor-Region:region-eu-central'),
    ).toBeTruthy()
  })

  it('closes the panel on dismiss', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    fireEvent.click(
      await screen.findByTestId('arch-graph-node-Region-Region:region-eu-central'),
    )
    expect(screen.getByTestId('infrastructure-detail-panel')).toBeTruthy()
    fireEvent.click(screen.getByTestId('infrastructure-detail-panel-close'))
    expect(screen.queryByTestId('infrastructure-detail-panel')).toBeNull()
  })
})

describe('Architecture — context menu', () => {
  it('opens the node context menu on right-click with kind-aware items', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    const node = await screen.findByTestId(
      'arch-graph-node-Cluster-Cluster:cluster-eu-central-primary',
    )
    fireEvent.contextMenu(node)
    const menu = screen.getByTestId('cloud-architecture-context-menu')
    expect(menu.getAttribute('data-context-target')).toBe('Cluster')
    expect(screen.getByTestId('cloud-architecture-context-add-vcluster')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-context-add-nodepool')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-context-delete')).toBeTruthy()
  })

  it('opens the canvas context menu with Add region on empty-canvas right-click', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    const svg = await screen.findByTestId('arch-graph-svg')
    fireEvent.contextMenu(svg)
    const menu = screen.getByTestId('cloud-architecture-context-menu')
    expect(menu.getAttribute('data-context-target')).toBe('canvas')
    expect(screen.getByTestId('cloud-architecture-context-add-region')).toBeTruthy()
  })
})

describe('Architecture — density slider', () => {
  it('exposes the global density slider with the default 50%', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    await screen.findByTestId('arch-graph-svg')
    const slider = screen.getByTestId(
      'cloud-architecture-global-density',
    ) as HTMLInputElement
    expect(slider).toBeTruthy()
    expect(slider.value).toBe('50')
  })

  it('exposes per-type badges for every type', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    await screen.findByTestId('arch-graph-svg')
    expect(screen.getByTestId('cloud-architecture-type-badge-Cloud')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-Region')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-Cluster')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-vCluster')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-NodePool')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-WorkerNode')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-LoadBalancer')).toBeTruthy()
    expect(screen.getByTestId('cloud-architecture-type-badge-Network')).toBeTruthy()
  })
})

describe('Architecture — CRUD modal triggers', () => {
  it('opens the Add Cluster modal from a region detail panel', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    fireEvent.click(
      await screen.findByTestId('arch-graph-node-Region-Region:region-eu-central'),
    )
    fireEvent.click(screen.getByTestId('infrastructure-detail-panel-action-add-cluster'))
    expect(screen.getByTestId('infrastructure-modal-add-cluster')).toBeTruthy()
  })

  it('opens the Add vCluster modal from a cluster detail panel', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    fireEvent.click(
      await screen.findByTestId('arch-graph-node-Cluster-Cluster:cluster-eu-central-primary'),
    )
    fireEvent.click(screen.getByTestId('infrastructure-detail-panel-action-add-vcluster'))
    expect(screen.getByTestId('infrastructure-modal-add-vcluster')).toBeTruthy()
  })

  it('opens the Add Region modal from the empty-canvas context menu', async () => {
    renderArchitecturePage(infrastructureTopologyFixture)
    const svg = await screen.findByTestId('arch-graph-svg')
    fireEvent.contextMenu(svg)
    fireEvent.click(screen.getByTestId('cloud-architecture-context-add-region'))
    expect(screen.getByTestId('infrastructure-modal-add-region')).toBeTruthy()
  })
})
