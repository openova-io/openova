/**
 * ParentDomainsPage.test.tsx — admin parent-domains surface coverage
 * (issue #829, parent epic #825).
 *
 *   • Empty state renders when no items
 *   • Populated table renders one row per item with role + status
 *   • Add CTA opens the modal with all four fields
 *   • Drawer toggles open/close on the row caret
 */

import { describe, it, expect, afterEach } from 'vitest'
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
import { ParentDomainsPage } from './ParentDomainsPage'
import type { ParentDomain } from './parentDomains.api'

afterEach(cleanup)

/**
 * Harness — since #4093 (commit e8dd34764) ParentDomainsPage is a thin
 * PortalShell wrapper around ParentDomainsSection. That pulls in two
 * context dependencies the original standalone-render harness never had:
 *
 *   • TanStack Router — PortalShell renders Sidebar, which calls
 *     useRouterState + <Link> (Sidebar.tsx:154, :210).
 *   • TanStack Query  — ParentDomainsPage calls useResolvedDeploymentId
 *     (useQuery), and PortalShell renders ReadinessChip (#3935,
 *     ReadinessChip.tsx:149, also useQuery).
 *
 * src/main.tsx:69 wraps the real app in a QueryClientProvider; this
 * mirrors it. Every assertion below is unchanged — the harness moved,
 * not the contract.
 */
function renderPage(props: { initialItems: ParentDomain[] }) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const domainsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/organizations/domains',
    component: () => <ParentDomainsPage initialItems={props.initialItems} disableFetch />,
  })
  // Sidebar <Link> targets — registered so href resolution works.
  const stub = (path: string) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path,
      component: () => <div />,
    })
  const tree = rootRoute.addChildren([
    domainsRoute,
    stub('/provision/$deploymentId'),
    stub('/provision/$deploymentId/dashboard'),
    stub('/provision/$deploymentId/jobs'),
    stub('/provision/$deploymentId/cloud'),
    stub('/provision/$deploymentId/settings'),
    stub('/provision/$deploymentId/users'),
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: ['/provision/d-test-1234/organizations/domains'],
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

const sampleItems: ParentDomain[] = [
  {
    name: 'omani.works',
    role: 'primary',
    flipStatus: 'ready',
    addedAt: '2026-05-04T08:00:00Z',
    flippedAt: '2026-05-04T08:30:00Z',
  },
  {
    name: 'omani.trade',
    role: 'org-pool',
    flipStatus: 'flipping',
    registrarKind: 'dynadot',
    addedAt: '2026-05-04T09:00:00Z',
  },
]

describe('ParentDomainsPage', () => {
  it('renders the empty state when items list is empty', async () => {
    renderPage({ initialItems: [] })
    expect(await screen.findByTestId('parent-domains-page')).toBeTruthy()
    expect(screen.getByTestId('parent-domains-empty')).toBeTruthy()
    expect(screen.getByTestId('parent-domains-add-cta')).toBeTruthy()
  })

  it('renders one row per item with role + status badges', async () => {
    renderPage({ initialItems: sampleItems })
    expect(await screen.findByTestId('parent-domain-row-omani.works')).toBeTruthy()
    expect(screen.getByTestId('parent-domain-row-omani.trade')).toBeTruthy()
    expect(screen.getByTestId('parent-domain-role-omani.works').textContent).toContain('primary')
    expect(screen.getByTestId('parent-domain-role-omani.trade').textContent).toContain('org-pool')
    expect(screen.getByTestId('parent-domain-status-omani.works').textContent).toContain('Ready')
    expect(screen.getByTestId('parent-domain-status-omani.trade').textContent).toContain('Flipping')
  })

  it('locks delete on the primary row', async () => {
    renderPage({ initialItems: sampleItems })
    expect(await screen.findByTestId('parent-domain-delete-omani.trade')).toBeTruthy()
    expect(screen.queryByTestId('parent-domain-delete-omani.works')).toBeNull()
  })

  it('opens the add-domain modal on CTA click', async () => {
    renderPage({ initialItems: [] })
    fireEvent.click(await screen.findByTestId('parent-domains-add-cta'))
    expect(screen.getByTestId('add-domain-modal')).toBeTruthy()
    expect(screen.getByTestId('add-domain-name')).toBeTruthy()
    expect(screen.getByTestId('add-domain-role')).toBeTruthy()
    expect(screen.getByTestId('add-domain-registrar')).toBeTruthy()
    expect(screen.getByTestId('add-domain-token')).toBeTruthy()
    expect(screen.getByTestId('add-domain-submit')).toBeTruthy()
  })

  it('expands the propagation drawer on row toggle', async () => {
    renderPage({ initialItems: sampleItems })
    const toggle = await screen.findByTestId('parent-domain-toggle-omani.trade')
    expect(screen.queryByTestId('parent-domain-drawer-omani.trade')).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByTestId('parent-domain-drawer-omani.trade')).toBeTruthy()
  })

  it('renders the propagation panel with disabled polling for tests', async () => {
    renderPage({ initialItems: sampleItems })
    fireEvent.click(await screen.findByTestId('parent-domain-toggle-omani.trade'))
    // disablePolling propagated to PropagationPanel — null result means
    // the panel rendered but didn't fire the network call.
    expect(screen.getByTestId('parent-domain-drawer-omani.trade')).toBeTruthy()
  })
})
