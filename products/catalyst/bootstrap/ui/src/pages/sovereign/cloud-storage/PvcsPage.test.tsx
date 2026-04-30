/**
 * PvcsPage.test.tsx — list-page lock-in.
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
import { PvcsPage } from './PvcsPage'
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
  const stRoute = createRoute({
    getParentRoute: () => cloudRoute,
    path: '/storage',
    component: () => <Outlet />,
  })
  const route = createRoute({
    getParentRoute: () => stRoute,
    path: '/pvcs',
    component: PvcsPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([stRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/storage/pvcs'],
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

describe('PvcsPage', () => {
  it('renders 2 PVC rows from the fixture', async () => {
    renderPage()
    expect(await screen.findByTestId('cloud-pvcs-row-pvc-postgres-data')).toBeTruthy()
    expect(screen.getByTestId('cloud-pvcs-row-pvc-redis-data')).toBeTruthy()
    expect(screen.getByTestId('cloud-pvcs-count').textContent).toBe('2')
  })

  it('detail drawer surfaces capacity + storage class', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('cloud-pvcs-row-pvc-postgres-data'))
    const body = screen.getByTestId('cloud-pvcs-detail-body')
    expect(body.textContent).toContain('20Gi')
    expect(body.textContent).toContain('local-path')
  })
})
