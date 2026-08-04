/**
 * SovereignConsoleLayout.test.tsx
 *
 * Regression coverage for two stacked auth-guard bugs:
 *
 * 1. Phase-8b followup (otech49 + otech52, 2026-05-03)
 *    The wizard's handover button drops the operator at
 *      https://console.<sov>.omani.works/auth/handover?token=<jwt>
 *    and the catalyst-api 302-redirects to /dashboard with a
 *    `catalyst_session` HttpOnly Secure SameSite=Lax cookie attached.
 *    PREVIOUS layout went straight from "no sessionStorage tokens" to
 *    Keycloak's hosted login. The fix probes /api/v1/whoami first.
 *
 * 2. Issue #1089 (chroot anon → KC, 2026-05-08)
 *    On a chroot Sovereign with no cookie + no OIDC tokens, the
 *    layout USED TO call initiateLogin() which redirected to the
 *    Keycloak hosted login UI (`auth.<sov>/realms/sovereign/...`).
 *    The matrix forbids that surface. Operators must land on the
 *    OpenOva PIN-login page (`/login`) with the original deep-link
 *    preserved as `?next=<original-path>`. After PIN verify, the
 *    VerifyPinPage routes the operator back to `next`.
 *
 * Contracts under test:
 *   A. /whoami 200 → cookie-authenticated, console shell renders, NO
 *      navigation away.
 *   B. /whoami 401 + no sessionStorage tokens → window.location.replace
 *      to '/login?next=<encoded-pathname>', NEVER initiateLogin().
 *   C. /whoami 5xx + no tokens → falls through safely to the PIN-login
 *      bounce (NOT to KC).
 *   D. Deep-link preservation → window.location.pathname is encoded
 *      verbatim into the `next` query param.
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

// Stub the OIDC module — initiateLogin must NEVER be called from this
// layout post-#1089. We export it as a spy so we can assert that.
// The options argument is forwarded, not dropped: `{ prompt: 'none' }` is
// the difference between an invisible re-auth and Keycloak rendering an
// interactive login wall, so a spy that only records the FQDN cannot tell
// the two apart.
const initiateLoginSpy = vi.fn<(fqdn: string, opts?: { prompt?: string }) => Promise<void>>()
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
  initiateLogin: (fqdn: string, opts?: { prompt?: string }) => initiateLoginSpy(fqdn, opts),
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
// The silent-SSO contract constants come from the router module the SUT
// itself lazy-imports — read them rather than restating the values, so a
// change to the grace period or the guard key can never silently
// desynchronise this suite from the code under test.
import { SILENT_SSO_GUARD_KEY, SILENT_SSO_NAV_GRACE_MS } from '../router'

/**
 * Stub `window.location` so we can observe redirect targets without
 * jsdom navigating away. We replace `replace` with a spy and read the
 * arg back; pathname is set per-test.
 */
function stubLocation(pathname: string, search = '') {
  const replaceSpy = vi.fn<(url: string) => void>()
  Object.defineProperty(window, 'location', {
    value: {
      ...window.location,
      pathname,
      search,
      replace: replaceSpy,
      assign: vi.fn(),
      href: `https://console.otech49.omani.works${pathname}${search}`,
    },
    writable: true,
  })
  return replaceSpy
}

beforeEach(() => {
  initiateLoginSpy.mockClear()
  initiateLogoutSpy.mockClear()
  silentRefreshSpy.mockClear()
  loadTokensSpy.mockClear()
  loadTokensSpy.mockReturnValue(null)
  // jsdom hands every test in this file the SAME window, so sessionStorage
  // is shared state. Two keys the SUT writes are scenario-defining:
  // `catalyst:authed` (contract A's cookie path sets it) and the silent-SSO
  // one-shot guard. Leaking either makes a later test model a DIFFERENT
  // scenario than its title claims — contract B ("no sessionStorage
  // tokens", i.e. an anonymous visitor) would inherit contract A's marker
  // and be read as a session EXPIRY instead. Real browsers scope
  // sessionStorage per tab, so a fresh anonymous visit genuinely has
  // neither key; clear both between tests.
  sessionStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('SovereignConsoleLayout — auth-guard order of operations', () => {
  it('renders the console shell when /whoami returns 200 (cookie-authenticated)', async () => {
    stubLocation('/dashboard')
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
})

describe('SovereignConsoleLayout — #1089 anon redirect to OpenOva /login', () => {
  it('on /whoami 401 + no sessionStorage tokens, redirects to /login?next=<path>, NOT to Keycloak', async () => {
    const replaceSpy = stubLocation('/dashboard')
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })
    loadTokensSpy.mockReturnValue(null)

    render(<SovereignConsoleLayout />)

    await screen.findByTestId('sov-auth-loading')

    // Wait for the redirect to fire (chained off the /whoami promise).
    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalled()
    })

    const target = replaceSpy.mock.calls[0]![0]
    expect(target).toContain('/login?next=')
    expect(target).toContain(encodeURIComponent('/dashboard'))

    // The decisive assertion: Keycloak's hosted login was NEVER touched.
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })

  it('preserves a deep-linked path (/jobs/timeline) verbatim in the next param', async () => {
    const replaceSpy = stubLocation('/jobs/timeline')
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })
    loadTokensSpy.mockReturnValue(null)

    render(<SovereignConsoleLayout />)

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalled()
    })
    const target = replaceSpy.mock.calls[0]![0]
    expect(target).toBe('/login?next=' + encodeURIComponent('/jobs/timeline'))
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })

  it('preserves a deep-linked path including search (/apps?filter=running) verbatim', async () => {
    const replaceSpy = stubLocation('/apps', '?filter=running')
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })
    loadTokensSpy.mockReturnValue(null)

    render(<SovereignConsoleLayout />)

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalled()
    })
    const target = replaceSpy.mock.calls[0]![0]
    expect(target).toBe('/login?next=' + encodeURIComponent('/apps?filter=running'))
  })

  it('on /whoami 401 + expired tokens + silentRefresh failure, also redirects to /login (NOT KC)', async () => {
    const replaceSpy = stubLocation('/cloud')
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })
    // Have tokens, but they're expired AND silentRefresh returns null.
    loadTokensSpy.mockReturnValue({ idToken: 'expired', accessToken: 'expired', expiresAt: 0 })
    silentRefreshSpy.mockResolvedValue(null)

    render(<SovereignConsoleLayout />)

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalled()
    })
    const target = replaceSpy.mock.calls[0]![0]
    expect(target).toBe('/login?next=' + encodeURIComponent('/cloud'))
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })

  it('on /whoami 5xx, falls through to the PIN-login bounce (not KC)', async () => {
    const replaceSpy = stubLocation('/dashboard')
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
      expect(replaceSpy).toHaveBeenCalled()
    })
    const target = replaceSpy.mock.calls[0]![0]
    expect(target).toContain('/login?next=')
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })
})

/* ── Contract E: cookie EXPIRY still runs the silent leg (#5460) ───── */

/**
 * The counterpart to contract B. B asserts the silent Keycloak leg does
 * NOT fire for an anonymous visitor; on its own that is a negative
 * assertion, and a negative assertion passes just as happily if the leg
 * were deleted outright. This block pins the other direction so the
 * discriminator is verified both ways: a stale `catalyst:authed` marker
 * means this tab HELD a live console session, so a 401 is a cookie
 * EXPIRY and the silent prompt=none re-mint is exactly right there.
 *
 * It also pins the safety net. `attemptSilentSovereignSSO` returning true
 * means "the browser is being navigated to Keycloak", which normally ends
 * this document. When that navigation never commits (auth host
 * unreachable) the layout must not leave the operator on the
 * "Authenticating…" spinner forever — after the grace period it re-arms
 * the one-shot guard and falls back to the PIN page, mirroring
 * rootBeforeLoad.
 */
describe('SovereignConsoleLayout — #5460 cookie-expiry silent SSO', () => {
  it('with a stale catalyst:authed marker, runs the silent prompt=none leg, then falls back to /login when the KC navigation never commits', async () => {
    const replaceSpy = stubLocation('/dashboard')
    sessionStorage.setItem('catalyst:authed', '1')
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })
    loadTokensSpy.mockReturnValue(null)

    render(<SovereignConsoleLayout />)

    // The silent leg fired — and truly silently: `prompt: 'none'` is what
    // stops Keycloak rendering the interactive PIN wall during what is
    // supposed to be an invisible re-auth.
    await waitFor(() => {
      expect(initiateLoginSpy).toHaveBeenCalled()
    })
    expect(initiateLoginSpy.mock.calls[0]![1]).toEqual({ prompt: 'none' })

    // The stale marker is revoked either way — it must never outlive the
    // session it claims.
    expect(sessionStorage.getItem('catalyst:authed')).toBeNull()

    // initiateLogin is stubbed here, so the document never actually
    // unloads — the same shape as an auth host that fails to answer.
    // The grace-period safety net must still land the operator on /login
    // and re-arm the one-shot guard for a later genuine expiry.
    await waitFor(
      () => {
        expect(replaceSpy).toHaveBeenCalled()
      },
      { timeout: SILENT_SSO_NAV_GRACE_MS + 4000 },
    )
    expect(replaceSpy.mock.calls[0]![0]).toContain('/login?next=')
    expect(sessionStorage.getItem(SILENT_SSO_GUARD_KEY)).toBeNull()
  }, SILENT_SSO_NAV_GRACE_MS + 12000)
})
