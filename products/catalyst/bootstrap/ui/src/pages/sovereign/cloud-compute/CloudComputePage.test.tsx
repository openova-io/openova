/**
 * CloudComputePage.test.tsx — landing page for /cloud/compute (P3 of
 * #309). Asserts that the four tiles render with counts derived from
 * the fixture topology.
 */

import { describe, it, expect, afterEach, beforeEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { CloudPage } from '../CloudPage'
import { CloudComputePage } from './CloudComputePage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderLanding(data: HierarchicalInfrastructure) {
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
    component: CloudComputePage,
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

beforeEach(() => {
  if (typeof window !== 'undefined') window.localStorage.clear()
})
afterEach(() => cleanup())

describe('CloudComputePage', () => {
  it('renders 4 tiles (clusters / vclusters / node-pools / worker-nodes)', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-compute-page-tile-clusters')).toBeTruthy()
    expect(screen.getByTestId('cloud-compute-page-tile-vclusters')).toBeTruthy()
    expect(screen.getByTestId('cloud-compute-page-tile-node-pools')).toBeTruthy()
    expect(screen.getByTestId('cloud-compute-page-tile-worker-nodes')).toBeTruthy()
  })

  it('counts derive from the fixture (2 clusters, 4 vclusters, 3 node-pools, 6 worker nodes)', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect((await screen.findByTestId('cloud-compute-page-tile-clusters-count')).textContent).toBe('2')
    expect(screen.getByTestId('cloud-compute-page-tile-vclusters-count').textContent).toBe('4')
    expect(screen.getByTestId('cloud-compute-page-tile-node-pools-count').textContent).toBe('3')
    expect(screen.getByTestId('cloud-compute-page-tile-worker-nodes-count').textContent).toBe('6')
  })

  it('each tile is a Link to the per-resource list page', async () => {
    renderLanding(infrastructureTopologyFixture)
    const clustersLink = (await screen.findByTestId('cloud-compute-page-tile-clusters')) as HTMLAnchorElement
    expect(clustersLink.tagName).toBe('A')
    expect(clustersLink.getAttribute('href') ?? '').toMatch(/\/cloud\/compute\/clusters$/)
  })
})
