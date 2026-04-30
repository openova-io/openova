/**
 * CloudNetworkPage.test.tsx — landing page for /cloud/network (P3).
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
import { CloudNetworkPage } from './CloudNetworkPage'
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
  const netRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/network',
    component: CloudNetworkPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([netRoute])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/network'],
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

describe('CloudNetworkPage', () => {
  it('renders 4 tiles (services / ingresses / load-balancers / dns-zones)', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect(await screen.findByTestId('cloud-network-page-tile-services')).toBeTruthy()
    expect(screen.getByTestId('cloud-network-page-tile-ingresses')).toBeTruthy()
    expect(screen.getByTestId('cloud-network-page-tile-load-balancers')).toBeTruthy()
    expect(screen.getByTestId('cloud-network-page-tile-dns-zones')).toBeTruthy()
  })

  it('Load Balancers tile shows the fixture count (1)', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect((await screen.findByTestId('cloud-network-page-tile-load-balancers-count')).textContent).toBe('1')
  })

  it('placeholder tiles show — for the count', async () => {
    renderLanding(infrastructureTopologyFixture)
    expect((await screen.findByTestId('cloud-network-page-tile-services-count')).textContent).toBe('—')
    expect(screen.getByTestId('cloud-network-page-tile-ingresses-count').textContent).toBe('—')
    expect(screen.getByTestId('cloud-network-page-tile-dns-zones-count').textContent).toBe('—')
  })
})
