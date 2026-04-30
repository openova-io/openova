/**
 * VClustersPage.test.tsx — list-page lock-in.
 */

import { describe, it, expect, afterEach, beforeEach } from 'vitest'
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

import { CloudPage } from '../CloudPage'
import { VClustersPage } from './VClustersPage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderPage(data: HierarchicalInfrastructure) {
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
    component: () => <Outlet />,
  })
  const route = createRoute({
    getParentRoute: () => computeRoute,
    path: '/vclusters',
    component: VClustersPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([computeRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/compute/vclusters'],
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

describe('VClustersPage', () => {
  it('renders 4 vCluster rows from the fixture', async () => {
    renderPage(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-vclusters-row-vc-eu-central-dmz')).toBeTruthy()
    expect(screen.getByTestId('cloud-vclusters-row-vc-eu-central-rtz')).toBeTruthy()
    expect(screen.getByTestId('cloud-vclusters-row-vc-eu-central-mgmt')).toBeTruthy()
    expect(screen.getByTestId('cloud-vclusters-row-vc-hel-rtz')).toBeTruthy()
    expect(screen.getByTestId('cloud-vclusters-count').textContent).toBe('4')
  })

  it('clicking a row opens the detail drawer with parent cluster info', async () => {
    renderPage(infrastructureTopologyFixture)
    fireEvent.click(await screen.findByTestId('cloud-vclusters-row-vc-eu-central-dmz'))
    const body = screen.getByTestId('cloud-vclusters-detail-body')
    expect(body.textContent).toContain('omantel-primary')
    expect(body.textContent).toContain('dmz')
  })
})
