/**
 * InfrastructureCompute.test.tsx — render lock-in for the Compute tab.
 *
 * Coverage:
 *   1. Empty state shows when the tree has no clusters / nodes.
 *   2. Pool + Node tables render with counts and rows.
 *   3. Bulk-action strip is present.
 *   4. Row-level Scale opens the ScalePoolModal.
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
import { InfrastructureCompute } from './InfrastructureCompute'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderComputePage(data: HierarchicalInfrastructure) {
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
  const computeRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/compute',
    component: InfrastructureCompute,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([computeRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/compute'],
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

describe('InfrastructureCompute — empty', () => {
  it('renders the empty state when there are no clusters or nodes', async () => {
    const empty: HierarchicalInfrastructure = {
      cloud: [],
      topology: { pattern: 'solo', regions: [] },
      storage: { pvcs: [], buckets: [], volumes: [] },
    }
    renderComputePage(empty)
    expect(await screen.findByTestId('infrastructure-compute-empty')).toBeTruthy()
  })
})

describe('InfrastructureCompute — populated', () => {
  it('renders the Pools and Nodes tables', async () => {
    renderComputePage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('infrastructure-pools-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-nodes-table')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-pools-count').textContent).toBe('3')
    // 4 nodes in cluster-eu-central + 2 in helsinki = 6 total
    expect(screen.getByTestId('infrastructure-nodes-count').textContent).toBe('6')
  })

  it('renders the bulk-actions strip', async () => {
    renderComputePage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('infrastructure-compute-bulk')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-compute-bulk-scale')).toBeTruthy()
    expect(screen.getByTestId('infrastructure-compute-bulk-drain')).toBeTruthy()
  })

  it('opens ScalePoolModal when row-level Scale is clicked', async () => {
    renderComputePage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('infrastructure-pool-row-pool-eu-cp-scale'))
    expect(screen.getByTestId('infrastructure-modal-scale-pool')).toBeTruthy()
  })

  it('opens NodeActionConfirm (drain) when row-level Drain is clicked', async () => {
    renderComputePage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('infrastructure-node-row-node-eu-w-0-drain'))
    expect(screen.getByTestId('infrastructure-modal-node-drain')).toBeTruthy()
  })
})
