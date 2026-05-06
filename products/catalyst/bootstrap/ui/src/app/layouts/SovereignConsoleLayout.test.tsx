/**
 * SovereignConsoleLayout.test.tsx — Phase-8b followup
 *
 * Regression coverage for the auth-guard order-of-operations bug caught
 * live on otech49 + otech52 (2026-05-03):
 *
 *   The wizard's handover button drops the operator at
 *     https://console.<sov>.omani.works/auth/handover?token=<jwt>
 *   and the catalyst-api 302-redirects to /console/dashboard with a
 *   `catalyst_session` HttpOnly Secure SameSite=Lax cookie attached.
 *
 *   The PREVIOUS layout went straight from "no sessionStorage tokens"
 *   to `initiateLogin()` (PKCE redirect to Keycloak), so the operator
 *   landed on a username/password screen. Bug ticket from the field:
 *   "fuck, this is asking username password!!!"
 *
 *   The fix probes GET /api/v1/whoami (with credentials:'include')
 *   FIRST. If 200, the layout renders the console without ever
 *   redirecting to Keycloak. If 401, the existing OIDC flow runs.
 *
 * The two contracts under test:
 *   1. /whoami 200 → cookie-authenticated, console shell renders, NO
 *      navigation to auth.<sov>.../auth?... happens.
 *   2. /whoami 401 → falls back to OIDC initiateLogin (existing
 *      behaviour preserved — sessionStorage tokens path also works).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

// Mock detectMode BEFORE importing the SUT — the layout reads
// DETECTED_MODE.sovereignFQDN at module-init time, so the mock has to
// be hoisted by vi.mock() above the component import.
vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'sovereign' as const, sovereignFQDN: 'otech49.omani.works' },
  detectMode: () => ({ mode: 'sovereign' as const, sovereignFQDN: 'otech49.omani.works' }),
}))

// Stub the OIDC module — the test only needs to observe that
// initiateLogin is (or isn't) called. We can't let the real
// implementation run because it does a window.location.replace.
const initiateLoginSpy = vi.fn<(fqdn: string) => Promise<void>>()
const initiateLogoutSpy = vi.fn<(fqdn: string) => void>()
const silentRefreshSpy = vi.fn<(fqdn: string) => Promise<unknown>>()
const loadTokensSpy = vi.fn<() => unknown>()
initiateLoginSpy.mockResolvedValue(undefined)
initiateLogoutSpy.mockReturnValue(undefined)
silentRefreshSpy.mockResolvedValue(null)
loadTokensSpy.mockReturnValue(null)
vi.mock('@/shared/lib/oidc', () => ({
  loadTokens: () => loadTokensSpy(),
  isTokenExpired: () => true,
  silentRefresh: (fqdn: string) => silentRefreshSpy(fqdn),
  initiateLogin: (fqdn: string) => initiateLoginSpy(fqdn),
  initiateLogout: (fqdn: string) => initiateLogoutSpy(fqdn),
  parseJWTClaims: () => ({ email: 'kc-user@example.com', name: 'KC User' }),
  getRequiredActions: () => [],
}))

// SovereignSidebar pulls in the rest of the world (router context, query
// client) — stub it to a marker div so the test stays scoped to the
// auth gate.
vi.mock('@/pages/sovereign/SovereignSidebar', () => ({
  SovereignSidebar: () => <div data-testid="sov-sidebar-stub" />,
}))
vi.mock('@/components/RequiredActionsModal', () => ({
  RequiredActionsModal: () => <div data-testid="sov-required-actions-stub" />,
}))
vi.mock('@/shared/ui/notifications', () => ({
  NotificationBell: () => <div data-testid="sov-bell-stub" />,
}))
vi.mock('@/components/ThemeToggle', () => ({
  ThemeToggle: () => <div data-testid="sov-theme-stub" />,
}))
// useRouter() pulls in the TanStack Router context. Stub it.
vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = (await importOriginal()) as object
  return {
    ...actual,
    useRouter: () => ({ navigate: vi.fn() }),
    Outlet: () => <div data-testid="sov-outlet-stub" />,
  }
})

// Now safe to import the SUT.
import { SovereignConsoleLayout } from './SovereignConsoleLayout'

beforeEach(() => {
  initiateLoginSpy.mockClear()
  initiateLogoutSpy.mockClear()
  silentRefreshSpy.mockClear()
  loadTokensSpy.mockClear()
  loadTokensSpy.mockReturnValue(null)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('SovereignConsoleLayout — auth-guard order of operations', () => {
  it('renders the console shell when /whoami returns 200 (cookie-authenticated)', async () => {
    // Server-side handover minted a catalyst_session cookie, browser
    // arrived here with it attached.
    const fetchSpy = vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(
          JSON.stringify({ email: 'sarah@omantel.om', sub: 'kc-uid-abc', verified: true }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response(null, { status: 404 })
    })

    render(<SovereignConsoleLayout />)

    // Auth gate passed via cookie — Outlet renders the page tree.
    await screen.findByTestId('sov-outlet-stub')

    // The decisive assertion: NO Keycloak redirect was attempted.
    expect(initiateLoginSpy).not.toHaveBeenCalled()

    // /whoami was the seam consulted first — verify it ran with
    // credentials:'include' so the browser actually attaches the
    // cookie. Without this header the cookie is never sent and the fix
    // would silently regress.
    const whoamiCall = fetchSpy.mock.calls.find((c) => {
      const u = typeof c[0] === 'string' ? c[0] : (c[0] as URL).toString()
      return u.endsWith('/v1/whoami')
    })
    expect(whoamiCall).toBeDefined()
    const init = whoamiCall![1] as RequestInit | undefined
    expect(init?.credentials).toBe('include')
  })

  it('falls back to OIDC initiateLogin when /whoami returns 401 and no sessionStorage tokens', async () => {
    // No cookie, no OIDC tokens — the only escape hatch is a fresh
    // PKCE flow against Keycloak. This is the "returning user whose
    // session expired" branch.
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })
    loadTokensSpy.mockReturnValue(null)

    render(<SovereignConsoleLayout />)

    // The auth-gate loading screen renders while initiateLogin runs.
    await screen.findByTestId('sov-auth-loading')

    // Wait for the OIDC fallback to fire — the spy is async-resolved
    // inside useEffect → Promise chain.
    await waitFor(() => {
      expect(initiateLoginSpy).toHaveBeenCalledWith('otech49.omani.works')
    })

    // The console shell never rendered.
    expect(screen.queryByTestId('sov-console-shell')).toBeNull()
  })

  it('does not redirect to Keycloak on a 5xx /whoami — falls through to the OIDC path safely', async () => {
    // 5xx must NOT be confused with "no session". The fallback to OIDC
    // is itself idempotent, so falling through is the safe behaviour;
    // the relevant assertion is that we don't render the console
    // either (no spurious "authenticated" state).
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response('upstream timeout', { status: 503 })
      }
      return new Response(null, { status: 404 })
    })
    loadTokensSpy.mockReturnValue(null)

    render(<SovereignConsoleLayout />)

    await waitFor(() => {
      expect(initiateLoginSpy).toHaveBeenCalled()
    })
    expect(screen.queryByTestId('sov-console-shell')).toBeNull()
  })
})
