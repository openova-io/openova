/**
 * UserAccessListPage.test.tsx — list view coverage (issue #323).
 *
 *   • Page heading + tagline render
 *   • Empty-state renders when no items
 *   • Populated table renders one row per item
 *   • Mounts inside the PortalShell
 */

import { describe, it, expect, afterEach } from 'vitest'
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
import { UserAccessListPage } from './UserAccessListPage'
import type { UserAccessItem } from './userAccess.api'

function renderList(initialItems: UserAccessItem[] | null | undefined) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users',
    component: () => (
      <UserAccessListPage
        initialItems={initialItems as UserAccessItem[] | undefined}
        disableFetch
      />
    ),
  })
  const newRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users/new',
    component: () => <div data-testid="users-new-target" />,
  })
  const editRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users/$name',
    component: () => <div data-testid="users-edit-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([listRoute, newRoute, editRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/users'] }),
  })
  // PortalShell + child sidebar consume useQuery hooks, so the tree
  // must be wrapped in a QueryClientProvider. Without it every test
  // bombs with "No QueryClient set, use QueryClientProvider to set one".
  // Fixed alongside qa-loop iter-4 cluster
  // `users-page-null-map-and-open-redirect` so the new null-safety
  // test rows can render at all.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('UserAccessListPage', () => {
  it('renders page heading + tagline', async () => {
    renderList([])
    // Both the PortalShell page-title slot AND the in-content <h1>
    // render "User Access" — assert there are 1+.
    expect((await screen.findAllByText('User Access')).length).toBeGreaterThanOrEqual(1)
    expect(
      screen.getByText(/Per-user access to Sovereigns/),
    ).toBeTruthy()
  })

  it('renders empty-state when no items', async () => {
    renderList([])
    expect(await screen.findByTestId('user-access-empty-state')).toBeTruthy()
  })

  it('renders one row per UserAccess item', async () => {
    const items: UserAccessItem[] = [
      {
        name: 'alice-helmwatch',
        spec: {
          user: { keycloakSubject: 'alice' },
          sovereignRef: 'omantel',
          applications: [
            {
              app: 'helmwatch',
              role: 'editor',
              namespaces: ['helmwatch-prod'],
            },
          ],
        },
        creationTimestamp: '2026-05-01T12:00:00Z',
      },
      {
        name: 'ops-team',
        spec: {
          user: { keycloakGroups: ['sovereign-ops'] },
          sovereignRef: 'omantel',
          applications: [{ app: 'catalyst', role: 'admin' }],
        },
      },
    ]
    renderList(items)
    expect(await screen.findByTestId('user-access-row-alice-helmwatch')).toBeTruthy()
    expect(screen.getByTestId('user-access-row-ops-team')).toBeTruthy()
    // grants summary renders the "<app> (<role>)" form
    expect(screen.getByText(/helmwatch \(editor\)/)).toBeTruthy()
    expect(screen.getByText(/catalyst \(admin\)/)).toBeTruthy()
    // user label renders the subject when present, the groups otherwise
    expect(screen.getByText('alice')).toBeTruthy()
    expect(screen.getByText('sovereign-ops')).toBeTruthy()
  })

  it('mounts inside the PortalShell (sidebar present)', async () => {
    renderList([])
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(screen.getByTestId('admin-sidebar')).toBeTruthy()
  })

  it('exposes the + New CTA linking to the create route', async () => {
    renderList([])
    const cta = await screen.findByTestId('user-access-new-cta')
    expect(cta).toBeTruthy()
    expect(cta.getAttribute('href') || '').toContain('/provision/d-1/users/new')
  })

  // qa-loop iter-4 cluster `users-page-null-map-and-open-redirect` —
  // TC-028/169/222 surfaced the page crashing with
  // `TypeError: Cannot read properties of null (reading 'map')` when
  // the BE serialized Go zero-value `[]Item` slices as `null` over
  // the wire. The page renders defensively now even if the API or a
  // future refactor leaks nulls back into the props.
  it('renders without crashing when item.spec.applications is null', async () => {
    const items = [
      {
        name: 'broken-grant',
        spec: {
          user: { keycloakSubject: 'eve' },
          sovereignRef: 'omantel',
          // Simulate a wire null leaking past the api-client normalizer.
          applications: null as unknown as never,
        },
      },
    ] as unknown as UserAccessItem[]
    renderList(items)
    // The row renders — no React error boundary engaged.
    expect(await screen.findByTestId('user-access-row-broken-grant')).toBeTruthy()
    // The grants summary cell is empty, but the page is alive.
    expect(screen.getByText('eve')).toBeTruthy()
  })

  it('renders without crashing when initialItems itself is null-ish', async () => {
    // Belt-and-suspenders: even if a parent component passes null/
    // undefined the page should render the empty-state without crashing.
    renderList(null as unknown as UserAccessItem[])
    expect(await screen.findByTestId('user-access-empty-state')).toBeTruthy()
  })
})
