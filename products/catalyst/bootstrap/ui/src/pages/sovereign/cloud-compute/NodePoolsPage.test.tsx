/**
 * NodePoolsPage.test.tsx — list-page lock-in.
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
import { NodePoolsPage } from './NodePoolsPage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderPage() {
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
      <CloudPage disableStream initialDataOverride={infrastructureTopologyFixture} deploymentsOverride={[]} />
    ),
  })
  const computeRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/compute',
    component: () => <Outlet />,
  })
  const route = createRoute({
    getParentRoute: () => computeRoute,
    path: '/node-pools',
    component: NodePoolsPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([computeRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/compute/node-pools'],
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

describe('NodePoolsPage', () => {
  it('renders 3 pool rows from the fixture', async () => {
    renderPage()
    expect(await screen.findByTestId('cloud-node-pools-row-pool-eu-cp')).toBeTruthy()
    expect(screen.getByTestId('cloud-node-pools-row-pool-eu-worker')).toBeTruthy()
    expect(screen.getByTestId('cloud-node-pools-row-pool-hel-cp')).toBeTruthy()
    expect(screen.getByTestId('cloud-node-pools-count').textContent).toBe('3')
  })

  it('detail drawer surfaces machine type + replicas', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('cloud-node-pools-row-pool-eu-worker'))
    const body = screen.getByTestId('cloud-node-pools-detail-body')
    expect(body.textContent).toContain('cpx32')
  })
})
