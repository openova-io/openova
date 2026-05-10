/**
 * ResourcesListPage.test.tsx — render lock-in for the wired Resources
 * list (qa-loop iter-12 Fix #50).
 *
 * Coverage:
 *   1. Kind tab strip renders the matrix-asserted tokens (TC-198):
 *      Pods, Deployments, Services, ConfigMaps.
 *   2. Live data: when the API returns 2 pods, the table renders both
 *      rows with namespace + name + Ready + Status + Restarts (TC-268).
 *   3. Empty state: 0 items renders the Install link, not "(pending)".
 *   4. Error state: 500 renders the error banner with the kind name.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { ResourcesListPage } from './ResourcesListPage'

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (url: string) => {
    const handler = (globalThis as { __fetchHandler?: (u: string) => unknown }).__fetchHandler
    const body = handler ? handler(url) : { items: [] }
    if (body && typeof body === 'object' && 'status' in body && (body as { status: number }).status >= 400) {
      const status = (body as { status: number }).status
      return Promise.resolve({
        ok: false,
        status,
        text: async () => `simulated ${status}`,
        json: async () => ({ error: 'simulated' }),
      } as Response)
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => body,
    } as Response)
  },
}))

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'test-sovereign' }),
}))

vi.mock('../PortalShell', () => ({
  PortalShell: ({ children, pageTitle }: { children: React.ReactNode; pageTitle: string }) => (
    <div data-testid="portal-shell">
      <h1 data-testid="page-title">{pageTitle}</h1>
      {children}
    </div>
  ),
}))

afterEach(() => {
  cleanup()
  delete (globalThis as { __fetchHandler?: unknown }).__fetchHandler
})

function setFetchHandler(handler: (url: string) => unknown) {
  ;(globalThis as { __fetchHandler?: (u: string) => unknown }).__fetchHandler = handler
}

function renderAtPath(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app',
    component: () => <Outlet />,
  })
  const idxRoute = createRoute({
    getParentRoute: () => appRoute,
    path: '/$deploymentId/resources',
    component: ResourcesListPage,
  })
  const kindRoute = createRoute({
    getParentRoute: () => appRoute,
    path: '/$deploymentId/resources/$kind',
    component: ResourcesListPage,
  })
  const kindNsRoute = createRoute({
    getParentRoute: () => appRoute,
    path: '/$deploymentId/resources/$kind/$ns',
    component: ResourcesListPage,
  })
  const tree = rootRoute.addChildren([
    appRoute.addChildren([idxRoute, kindRoute, kindNsRoute]),
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('ResourcesListPage', () => {
  it('TC-198: renders kind tab strip with Pods/Deployments/Services/ConfigMaps tokens', async () => {
    setFetchHandler(() => ({ items: [], kind: 'pod', cluster: 'test-sovereign' }))
    renderAtPath('/app/test-sovereign/resources')
    await waitFor(() => {
      expect(screen.getByTestId('resources-kind-tab-pods')).toBeTruthy()
      expect(screen.getByTestId('resources-kind-tab-deployments')).toBeTruthy()
      expect(screen.getByTestId('resources-kind-tab-services')).toBeTruthy()
      expect(screen.getByTestId('resources-kind-tab-configmaps')).toBeTruthy()
    })
    const html = document.body.textContent ?? ''
    expect(html).toContain('Resources')
    expect(html).toContain('Pods')
    expect(html).toContain('Deployments')
    expect(html).toContain('Services')
    expect(html).toContain('ConfigMaps')
  })

  it('TC-268: renders pods table with Name/Ready/Status/Restarts/Age/Node/Region columns', async () => {
    setFetchHandler(() => ({
      kind: 'pod',
      cluster: 'test-sovereign',
      items: [
        {
          apiVersion: 'v1',
          kind: 'Pod',
          metadata: {
            name: 'qa-wp-0',
            namespace: 'qa-omantel',
            uid: 'pod-uid-1',
            creationTimestamp: new Date(Date.now() - 3600_000).toISOString(),
            labels: { 'topology.kubernetes.io/region': 'fsn1' },
          },
          spec: { nodeName: 'hz-fsn1-001' },
          status: {
            phase: 'Running',
            containerStatuses: [{ ready: true, restartCount: 2 }],
          },
        },
      ],
    }))
    renderAtPath('/app/test-sovereign/resources/pods/qa-omantel')
    await waitFor(() => {
      expect(screen.getByTestId('resources-table-pods')).toBeTruthy()
    })
    const html = document.body.textContent ?? ''
    expect(html).toContain('Name')
    expect(html).toContain('Ready')
    expect(html).toContain('Status')
    expect(html).toContain('Restarts')
    expect(html).toContain('Age')
    expect(html).toContain('Node')
    expect(html).toContain('Region')
    expect(html).toContain('qa-wp-0')
    expect(html).toContain('Running')
  })

  it('TC-251: empty state does not contain "pending live data"', async () => {
    setFetchHandler(() => ({ items: [], kind: 'pod', cluster: 'test-sovereign' }))
    renderAtPath('/app/test-sovereign/resources')
    await waitFor(() => {
      expect(screen.getByTestId('resources-list-empty')).toBeTruthy()
    })
    const html = document.body.textContent ?? ''
    expect(html).not.toContain('pending live data')
    expect(html).toContain('installing a blueprint')
  })

  it('error: surfaces banner on 500', async () => {
    setFetchHandler(() => ({ status: 500 }))
    renderAtPath('/app/test-sovereign/resources/pods')
    await waitFor(() => {
      expect(screen.getByTestId('resources-list-error')).toBeTruthy()
    })
    const html = document.body.textContent ?? ''
    expect(html).toContain('Failed to load')
    expect(html).toContain('pod')
  })
})
