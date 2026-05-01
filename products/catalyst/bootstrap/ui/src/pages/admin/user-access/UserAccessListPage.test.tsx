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

function renderList(initialItems: UserAccessItem[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/users',
    component: () => <UserAccessListPage initialItems={initialItems} disableFetch />,
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
  return render(<RouterProvider router={router} />)
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
})
