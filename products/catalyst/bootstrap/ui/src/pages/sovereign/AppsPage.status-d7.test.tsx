/**
 * AppsPage.status-d7.test.tsx — ADR-0015 D7 (Refs #7067): the apps grid card
 * renders the object's own statusOf verbatim, and an `installed` catalog
 * membership never repaints a NotReady object green.
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'

import { AppCard } from './AppsPage'
import type { ApplicationDescriptor } from './applicationCatalog'

afterEach(() => cleanup())

const app: ApplicationDescriptor = {
  id: 'wordpress',
  bareId: 'wordpress',
  title: 'wordpress',
  description: 'WordPress instance',
  familyId: 'platform',
  familyName: 'WordPress',
  tier: 'optional',
  logoUrl: null,
  dependencies: [],
  bootstrapKit: false,
}

function renderCard(props: Partial<Parameters<typeof AppCard>[0]>) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => (
      <AppCard
        app={app}
        status="installed"
        isCatalog={false}
        isService={false}
        environment="prod"
        marketplacePublished={null}
        slug="wordpress"
        {...props}
      />
    ),
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => <div />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute, appRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}

describe('ADR-0015 D7 — apps grid status', () => {
  it('blocked-sync WordPress: NotReady + reason/message verbatim, no INSTALLED pill', async () => {
    renderCard({
      objectStatus: {
        state: 'NotReady',
        reason: 'SynchronousStandbyNotStreaming',
        message: 'primary wordpress-db requires 1 synchronous standby; wordpress-db-replica not streaming',
      },
    })
    const badge = await screen.findByTestId('app-card-status-wordpress')
    expect(badge.textContent).toBe('NotReady')
    expect(screen.getByTestId('app-card-status-reason-wordpress').textContent).toBe(
      'SynchronousStandbyNotStreaming — primary wordpress-db requires 1 synchronous standby; wordpress-db-replica not streaming',
    )
    expect(document.body.textContent).not.toContain('INSTALLED')
  })

  it('a Ready object shows Ready and no reason line', async () => {
    renderCard({ objectStatus: { state: 'Ready', reason: 'Reconciled', message: 'ok' } })
    expect((await screen.findByTestId('app-card-status-wordpress')).textContent).toBe('Ready')
    expect(screen.queryByTestId('app-card-status-reason-wordpress')).toBeNull()
  })

  it('a catalog-only row (no objectStatus) keeps the membership pill', async () => {
    renderCard({ status: 'available' })
    await screen.findByText('AVAILABLE')
    expect(screen.queryByTestId('app-card-status-wordpress')).toBeNull()
  })
})
