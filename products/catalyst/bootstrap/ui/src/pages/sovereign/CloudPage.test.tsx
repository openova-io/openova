/**
 * CloudPage.test.tsx — shell wiring lock-in for the Sovereign Cloud
 * surface (issue #309 supersedes #227).
 *
 * Coverage:
 *   1. Header renders with the canonical title ("Cloud").
 *   2. The in-page tab strip is gone (the sidebar accordion replaces
 *      it — see Sidebar.tsx and the e2e cloud-nav spec).
 *   3. The shell renders an <Outlet /> that hosts the active sub-page.
 *   4. PortalShell wires (sidebar present).
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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

import { CloudPage } from './CloudPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

const EMPTY_TREE = {
  cloud: [],
  topology: { pattern: 'solo' as const, regions: [] },
  storage: { pvcs: [], buckets: [], volumes: [] },
}

function renderShell(deploymentId: string, suffix: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => (
      <CloudPage
        disableStream
        contentOverride={<div data-testid="cloud-content-stub">{suffix}</div>}
        initialDataOverride={EMPTY_TREE}
        deploymentsOverride={[]}
      />
    ),
  })
  // Register every legal sub-route as a no-op child so TanStack
  // Router resolves /cloud/{architecture,...} to the parent shell +
  // an empty child, mirroring production routing without mounting
  // the heavyweight sub-page components in the shell test.
  const subRoute = (s: string) =>
    createRoute({
      getParentRoute: () => cloudRoute,
      path: `/${s}`,
      component: () => <div data-testid={`cloud-sub-${s}`}>{s}</div>,
    })
  const tree = rootRoute.addChildren([
    cloudRoute.addChildren([
      subRoute('architecture'),
      subRoute('compute'),
      subRoute('storage'),
      subRoute('network'),
    ]),
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/cloud/${suffix}`],
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
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CloudPage — shell', () => {
  it('renders the Cloud title', async () => {
    renderShell('d-1', 'architecture')
    const title = await screen.findByTestId('cloud-title')
    expect(title.textContent).toBe('Cloud')
  })

  it('mounts inside the PortalShell (sidebar present)', async () => {
    renderShell('d-1', 'architecture')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(screen.getByTestId('admin-sidebar')).toBeTruthy()
  })

  it('renders the Outlet content', async () => {
    renderShell('d-1', 'architecture')
    expect(await screen.findByTestId('cloud-content-stub')).toBeTruthy()
  })
})

describe('CloudPage — no in-page tab strip', () => {
  it('does NOT render an in-page tablist (the sidebar accordion replaces it)', async () => {
    renderShell('d-1', 'architecture')
    await screen.findByTestId('cloud-title')
    // The legacy tab strip lived under [data-testid=infrastructure-tabs];
    // it is intentionally absent now — sub-page nav lives in the
    // sidebar accordion (see Sidebar.tsx).
    expect(screen.queryByTestId('infrastructure-tabs')).toBeNull()
    expect(screen.queryByTestId('cloud-tabs')).toBeNull()
    expect(screen.queryByRole('tablist')).toBeNull()
  })
})
