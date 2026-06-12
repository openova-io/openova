/**
 * ContextsTab tests — #3370. Locks the entity-first table contract:
 *
 *   | Context  | Occupied by | Credential            | Status |
 *   | db/gitea | gitea →     | gitea-database-secret | ready  |
 *
 * The SAME component renders every shareable blueprint's Contexts —
 * the kind prefix comes from the wire row, never from per-service code
 * (a keyspace/ row asserts the generality).
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  RouterProvider,
  Outlet,
} from '@tanstack/react-router'
import { ContextsTab } from './ContextsTab'
import type { InstanceContext } from '@/lib/catalog.api'

afterEach(cleanup)

function renderWithRouter(contexts: InstanceContext[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <ContextsTab contexts={contexts} />,
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => null,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, appRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}

describe('ContextsTab — #3370 entity-first table', () => {
  it('renders one row per Context with kind prefix, consumer link, credential, status', async () => {
    renderWithRouter([
      {
        name: 'gitea',
        kind: 'db',
        occupiedBy: 'gitea',
        credential: 'gitea-database-secret',
        status: 'ready',
      },
      {
        name: 'registry',
        kind: 'db',
        occupiedBy: 'harbor',
        credential: 'harbor-database-secret',
        status: 'pending',
      },
    ])
    expect(await screen.findByTestId('sov-contexts-table')).toBeTruthy()
    // Entity-first: db/gitea.
    expect(screen.getByTestId('sov-context-entity-gitea').textContent).toBe('db/gitea')
    // Occupied by: consumer link with the → affordance.
    const consumer = screen.getByTestId('sov-context-consumer-gitea')
    expect(consumer.textContent).toContain('gitea →')
    expect(consumer.getAttribute('href')).toBe('/app/bp-gitea')
    // Credential + status verbatim.
    expect(screen.getByText('gitea-database-secret')).toBeTruthy()
    expect(screen.getByTestId('sov-context-status-gitea').textContent).toBe('ready')
    expect(screen.getByTestId('sov-context-status-registry').textContent).toBe('pending')
  })

  it('is blueprint-agnostic: a keyspace/ Context renders through the SAME table', async () => {
    renderWithRouter([
      {
        name: 'sessions',
        kind: 'keyspace',
        occupiedBy: 'newapi',
        credential: 'newapi-keyspace-credential',
        status: 'ready',
      },
    ])
    expect(await screen.findByTestId('sov-contexts-table')).toBeTruthy()
    expect(screen.getByTestId('sov-context-entity-sessions').textContent).toBe(
      'keyspace/sessions',
    )
    expect(
      screen.getByTestId('sov-context-consumer-sessions').getAttribute('href'),
    ).toBe('/app/bp-newapi')
  })

  it('renders the honest empty state when no Contexts are declared', async () => {
    renderWithRouter([])
    expect(await screen.findByTestId('sov-app-contexts-empty')).toBeTruthy()
  })
})
