/**
 * StorageClassesPage.test.tsx
 *
 * #5611: this file previously LOCKED IN the placeholder — it asserted
 * the "storage class data is not in the current informer set" empty
 * state and its #321 docs link, i.e. it pinned the defect in place and
 * would have gone red on the fix. The `storageclass` GVR is now in the
 * catalyst-api k8scache registry, so the route renders the live list
 * instead, and this asserts THAT.
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
import { StorageClassesPage } from './StorageClassesPage'
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
    path: '/storage-classes',
    component: StorageClassesPage,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([stRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-1/cloud/storage/storage-classes'],
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

describe('StorageClassesPage', () => {
  it('#5611: renders the live storageclass list, not the #321 placeholder', async () => {
    renderPage()
    // The generic K8sListPage's container for kind=storageclass. Its
    // presence is what proves the route is wired to the live stream.
    expect(await screen.findByTestId('cloud-storageclass-list')).toBeTruthy()
    // And the placeholder is genuinely gone — without this the assertion
    // above could pass alongside a leftover stub.
    expect(screen.queryByTestId('cloud-storage-classes-empty')).toBeNull()
    expect(screen.queryByTestId('cloud-storage-classes-docs-link')).toBeNull()
  })
})
