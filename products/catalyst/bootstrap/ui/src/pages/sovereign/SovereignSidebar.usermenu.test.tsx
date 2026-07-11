/**
 * SovereignSidebar.usermenu.test.tsx — UAT row 27 / #5000 account menu.
 *
 * The footer identity card is the ONLY Sign-out affordance in the Sovereign
 * Console. Before #5000 it was a static <div> with no menu and no way to sign
 * out. This suite pins the interactive contract:
 *
 *   1. The card renders a menu TRIGGER (aria-haspopup=menu) and the menu is
 *      CLOSED by default — no "Sign out" item in the DOM until opened.
 *   2. Clicking the trigger opens a "Signed in as <owner>" menu exposing a
 *      Sign out item; a second click / Escape closes it.
 *   3. Clicking Sign out invokes the shared two-hop `session.signOut()`.
 *
 * The identity assertions (avatar initials / owner email) live in the sibling
 * SovereignSidebar.identity.test.tsx; here we only exercise the new menu.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
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

const signOutMock = vi.fn(async () => {})
const sessionMock = vi.fn()
vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => sessionMock(),
}))
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => ({ orgScoped: false, org: null, loading: false }),
}))
vi.mock('@/lib/console-ui.api', () => ({
  getSidebarEntries: async () => [],
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: '' }),
}))
vi.mock('@/shared/lib/oidc', () => ({
  loadTokens: () => null,
  parseJWTClaims: () => ({}),
}))

import { SovereignSidebar } from './SovereignSidebar'

function renderSidebar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const host = createRoute({
    getParentRoute: () => rootRoute,
    path: '/apps',
    component: () => <SovereignSidebar sovereignFQDN="console.omantel.biz" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([host]),
    history: createMemoryHistory({ initialEntries: ['/apps'] }),
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('SovereignSidebar — UAT row 27 / #5000 account menu', () => {
  beforeEach(() => {
    sessionMock.mockReset()
    signOutMock.mockClear()
    sessionMock.mockReturnValue({
      signedIn: true,
      email: 'uat215@omani.works',
      sub: 'kc-sub-1',
      tier: 'owner',
      roles: ['catalyst-owner'],
      loading: false,
      refetch: () => {},
      signOut: signOutMock,
    })
  })
  afterEach(() => cleanup())

  it('renders a menu trigger and keeps the menu (and Sign out) closed by default', async () => {
    renderSidebar()
    const trigger = await screen.findByTestId('sov-console-user-trigger')
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu')
    expect(trigger.getAttribute('aria-expanded')).toBe('false')
    // No Sign-out affordance in the DOM until the menu is opened.
    expect(screen.queryByTestId('sov-console-user-signout')).toBeNull()
    expect(screen.queryByTestId('sov-console-user-menu')).toBeNull()
  })

  it('opens a "Signed in as <owner>" menu with a Sign out item on click', async () => {
    renderSidebar()
    const trigger = await screen.findByTestId('sov-console-user-trigger')
    fireEvent.click(trigger)

    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    const menu = screen.getByTestId('sov-console-user-menu')
    expect(menu.getAttribute('role')).toBe('menu')
    expect(screen.getByText('Signed in as')).toBeTruthy()
    expect(screen.getByTestId('sov-console-user-menu-owner').textContent).toBe(
      'uat215@omani.works',
    )
    const signout = screen.getByTestId('sov-console-user-signout')
    expect(signout.getAttribute('role')).toBe('menuitem')
    expect(signout.textContent).toContain('Sign out')
  })

  it('invokes session.signOut() when Sign out is clicked', async () => {
    renderSidebar()
    fireEvent.click(await screen.findByTestId('sov-console-user-trigger'))
    fireEvent.click(screen.getByTestId('sov-console-user-signout'))
    expect(signOutMock).toHaveBeenCalledTimes(1)
    // Clicking Sign out also closes the menu.
    expect(screen.queryByTestId('sov-console-user-menu')).toBeNull()
  })

  it('closes the menu on Escape', async () => {
    renderSidebar()
    fireEvent.click(await screen.findByTestId('sov-console-user-trigger'))
    expect(screen.getByTestId('sov-console-user-menu')).toBeTruthy()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByTestId('sov-console-user-menu')).toBeNull()
  })
})
