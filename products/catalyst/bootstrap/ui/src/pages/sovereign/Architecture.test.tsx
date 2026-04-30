/**
 * Architecture.test.tsx — render lock-in for the Sovereign Cloud /
 * Architecture sub-page hierarchical canvas (issue #309 supersedes
 * #228).
 *
 * Coverage:
 *   1. Empty state shows when the tree has no nodes.
 *   2. With the synthetic fixture, the SVG canvas mounts with all 4
 *      depths (Cloud → Region → Cluster → vCluster) rendered.
 *   3. Clicking a node opens the right-side InfrastructureDetailPanel.
 *   4. Closing the panel removes it from the DOM.
 *   5. Clicking a cluster node sets zoom state and brightens its
 *      vClusters (data-dim="false").
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'
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

function renderTopologyPage(data: HierarchicalInfrastructure) {
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
    component: () => <CloudPage disableStream initialDataOverride={data} deploymentsOverride={[]} />,
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
    renderTopologyPage(empty)
    expect(await screen.findByTestId('cloud-architecture-empty')).toBeTruthy()
  })
})

describe('Architecture — hierarchical render', () => {
  it('renders the canvas with all 4 depths', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-architecture-svg')).toBeTruthy()

    // Depth 0 — cloud
    expect(screen.getByTestId('cloud-node-cloud-hetzner')).toBeTruthy()
    // Depth 1 — region
    expect(screen.getByTestId('cloud-node-region-eu-central')).toBeTruthy()
    // Depth 2 — cluster
    expect(screen.getByTestId('cloud-node-cluster-eu-central-primary')).toBeTruthy()
    // Depth 3 — vcluster
    expect(screen.getByTestId('cloud-node-vc-eu-central-dmz')).toBeTruthy()
  })

  it('renders edges between parent and child', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    await screen.findByTestId('cloud-architecture-svg')
    expect(screen.getByTestId('cloud-edge-cloud-hetzner-region-eu-central')).toBeTruthy()
    expect(screen.getByTestId('cloud-edge-region-eu-central-cluster-eu-central-primary')).toBeTruthy()
  })

  it('opens the right-side detail panel on node click and closes it on dismiss', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    const node = await screen.findByTestId('cloud-node-cluster-eu-central-primary')
    expect(screen.queryByTestId('infrastructure-detail-panel')).toBeNull()
    fireEvent.click(node)
    expect(screen.getByTestId('infrastructure-detail-panel')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-detail-panel-name').textContent).toBe('omantel-primary')
    fireEvent.click(screen.getByTestId('infrastructure-detail-panel-close'))
    expect(screen.queryByTestId('infrastructure-detail-panel')).toBeNull()
  })

  it('zooms in on a cluster click — vClusters lose data-dim=true', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    const cluster = await screen.findByTestId('cloud-node-cluster-eu-central-primary')

    // Before zoom — vClusters are dim by default.
    const vcBefore = screen.getByTestId('cloud-node-vc-eu-central-dmz')
    expect(vcBefore.getAttribute('data-dim')).toBe('true')

    fireEvent.click(cluster)

    // After zoom — vClusters of THIS cluster are bright.
    const vcAfter = screen.getByTestId('cloud-node-vc-eu-central-dmz')
    expect(vcAfter.getAttribute('data-dim')).toBe('false')
    // Zoom-status banner is visible.
    expect(screen.getByTestId('cloud-architecture-zoom-status')).toBeTruthy()
  })
})

describe('Architecture — CRUD modal triggers', () => {
  it('opens the Add Region modal when the top-level button is clicked', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    const btn = await screen.findByTestId('cloud-architecture-add-region')
    fireEvent.click(btn)
    const modal = screen.getByTestId('infrastructure-modal-add-region')
    expect(modal).toBeTruthy()
    expect(within(modal).getByTestId('infrastructure-modal-add-region-title').textContent).toContain('Add region')
  })

  it('opens the Add Cluster modal from a region detail panel', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-node-region-eu-central'))
    fireEvent.click(screen.getByTestId('infrastructure-detail-panel-action-add-cluster'))
    expect(screen.getByTestId('infrastructure-modal-add-cluster')).toBeTruthy()
  })

  it('opens the Add vCluster modal from a cluster detail panel', async () => {
    renderTopologyPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-node-cluster-eu-central-primary'))
    fireEvent.click(screen.getByTestId('infrastructure-detail-panel-action-add-vcluster'))
    expect(screen.getByTestId('infrastructure-modal-add-vcluster')).toBeTruthy()
  })
})
