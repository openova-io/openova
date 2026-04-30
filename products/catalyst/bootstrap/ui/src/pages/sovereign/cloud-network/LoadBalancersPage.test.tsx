/**
 * LoadBalancersPage.test.tsx — list-page lock-in.
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
import { LoadBalancersPage } from './LoadBalancersPage'
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
  const networkRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/network',
    component: () => <Outlet />,
  })
  const route = createRoute({
    getParentRoute: () => networkRoute,
    path: '/load-balancers',
    component: LoadBalancersPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([networkRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/network/load-balancers'],
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

describe('LoadBalancersPage', () => {
  it('renders 1 LB row from the fixture', async () => {
    renderPage()
    expect(await screen.findByTestId('cloud-load-balancers-row-lb-eu-central-edge')).toBeTruthy()
    expect(screen.getByTestId('cloud-load-balancers-count').textContent).toBe('1')
  })

  it('detail drawer surfaces public IP + listeners', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('cloud-load-balancers-row-lb-eu-central-edge'))
    const body = screen.getByTestId('cloud-load-balancers-detail-body')
    expect(body.textContent).toContain('116.203.42.1')
    expect(body.textContent).toContain('tcp:80')
    expect(body.textContent).toContain('tcp:443')
  })
})
