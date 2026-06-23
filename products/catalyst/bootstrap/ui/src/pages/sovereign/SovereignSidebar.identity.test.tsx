/**
 * SovereignSidebar.identity.test.tsx — #4187 sovereign-owner avatar.
 *
 * The Sovereign Console authenticates via the server-minted
 * `catalyst_session` cookie (the 6-digit PIN / handover flow), so there is
 * NO OIDC id_token in sessionStorage. The footer user card must therefore
 * read the signed-in principal from GET /api/v1/whoami (surfaced via the
 * shared `useSession` hook), NOT from `loadTokens()` / the id_token — which
 * previously left the card showing the generic `User` placeholder + the
 * bare Sovereign FQDN even though the owner's email was available.
 *
 * Asserts:
 *   1. When /whoami resolves an email, the footer renders that email and
 *      derives the avatar initials from it (never the `User` placeholder).
 *   2. When there is no session AND no OIDC token, it still falls back to
 *      `User` (no crash / no blank).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
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

// Drive the whoami-backed session directly.
const sessionMock = vi.fn()
vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => sessionMock(),
}))
// Org-scope is irrelevant here — render the full sovereign-admin nav.
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => ({ orgScoped: false, org: null, loading: false }),
}))
vi.mock('@/lib/console-ui.api', () => ({
  getSidebarEntries: async () => [],
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: '' }),
}))
// No OIDC token on a cookie/PIN session — the whole point of #4187.
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

describe('SovereignSidebar — #4187 owner identity from /whoami', () => {
  beforeEach(() => sessionMock.mockReset())
  afterEach(() => cleanup())

  it('renders the whoami email (not the "User" placeholder) in the footer', async () => {
    sessionMock.mockReturnValue({
      signedIn: true,
      email: 'uat215@omani.works',
      sub: 'kc-sub-1',
      tier: 'owner',
      roles: ['catalyst-owner'],
      loading: false,
      refetch: () => {},
      signOut: async () => {},
    })
    renderSidebar()
    const name = await screen.findByTestId('sov-console-user-name')
    expect(name.textContent).toBe('uat215@omani.works')
    expect(name.textContent).not.toBe('User')
    // Initials derived from the email local-part, split on @ / dot.
    expect(screen.getByTestId('sov-console-user-avatar').textContent).toBe('UO')
  })

  it('falls back to "User" when no session and no OIDC token resolve', async () => {
    sessionMock.mockReturnValue({
      signedIn: false,
      email: null,
      sub: null,
      tier: '',
      roles: [],
      loading: false,
      refetch: () => {},
      signOut: async () => {},
    })
    renderSidebar()
    const name = await screen.findByTestId('sov-console-user-name')
    expect(name.textContent).toBe('User')
  })
})
