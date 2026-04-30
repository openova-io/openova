/**
 * WorkerNodesPage.test.tsx — list-page lock-in.
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
import { WorkerNodesPage } from './WorkerNodesPage'
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
    path: '/worker-nodes',
    component: WorkerNodesPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([computeRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/compute/worker-nodes'],
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

describe('WorkerNodesPage', () => {
  it('renders 6 node rows from the fixture', async () => {
    renderPage()
    expect(await screen.findByTestId('cloud-worker-nodes-row-node-eu-cp-0')).toBeTruthy()
    expect(screen.getByTestId('cloud-worker-nodes-count').textContent).toBe('6')
  })

  it('role filter narrows to control-plane', async () => {
    renderPage()
    const roleSelect = (await screen.findByTestId('cloud-worker-nodes-filter-role')) as HTMLSelectElement
    fireEvent.change(roleSelect, { target: { value: 'control-plane' } })
    expect(screen.getByTestId('cloud-worker-nodes-row-node-eu-cp-0')).toBeTruthy()
    expect(screen.queryByTestId('cloud-worker-nodes-row-node-eu-w-0')).toBeNull()
  })

  it('detail drawer surfaces hostname + IP', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('cloud-worker-nodes-row-node-eu-w-0'))
    const body = screen.getByTestId('cloud-worker-nodes-detail-body')
    expect(body.textContent).toContain('eu-w-0')
    expect(body.textContent).toContain('10.0.1.10')
  })
})
