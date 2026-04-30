/**
 * ServicesPage.test.tsx — placeholder lock-in. Asserts the empty state
 * + the documentation link land in the expected shape.
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
import { ServicesPage } from './ServicesPage'
import { IngressesPage } from './IngressesPage'
import { DnsZonesPage } from './DnsZonesPage'
import { infrastructureTopologyFixture } from '@/test/fixtures/infrastructure-topology.fixture'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderPlaceholder(path: string, Page: () => React.ReactElement, suffix: string) {
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
    path: `/${suffix}`,
    component: Page,
  })
  const tree = rootRoute.addChildren([cloudRoute.addChildren([networkRoute.addChildren([route])])])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [path] }),
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

describe('Network placeholder pages', () => {
  it('ServicesPage renders header + empty state + docs link', async () => {
    renderPlaceholder('/provision/d-1/cloud/network/services', ServicesPage, 'services')
    expect(await screen.findByTestId('cloud-services-page')).toBeTruthy()
    expect(screen.getByTestId('cloud-services-empty')).toBeTruthy()
    expect(screen.getByTestId('cloud-services-docs-link')).toBeTruthy()
  })

  it('IngressesPage renders empty state', async () => {
    renderPlaceholder('/provision/d-1/cloud/network/ingresses', IngressesPage, 'ingresses')
    expect(await screen.findByTestId('cloud-ingresses-page')).toBeTruthy()
    expect(screen.getByTestId('cloud-ingresses-empty')).toBeTruthy()
  })

  it('DnsZonesPage renders empty state', async () => {
    renderPlaceholder('/provision/d-1/cloud/network/dns-zones', DnsZonesPage, 'dns-zones')
    expect(await screen.findByTestId('cloud-dns-zones-page')).toBeTruthy()
    expect(screen.getByTestId('cloud-dns-zones-empty')).toBeTruthy()
  })
})
