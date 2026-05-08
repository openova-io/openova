/**
 * SovereignConsoleLayout.catalyst-zero.test.tsx — issue #1090 cluster B
 *
 * Regression coverage for the mothership-anon-hung bug:
 *
 *   The same SovereignConsoleLayout mounts at console.openova.io/sovereign/*
 *   on Catalyst-Zero (mothership) under the basepath strip. In that mode
 *   `DETECTED_MODE.mode === 'catalyst-zero'` and `sovereignFQDN` is null.
 *
 *   The PREVIOUS layout's `if (!sovereignFQDN)` early-return set
 *   `unauthenticated` state and rendered the loading spinner forever —
 *   never navigated. Visitors to `/sovereign/dashboard`, `/jobs/timeline`,
 *   `/cloud`, `/users`, `/settings`, `/notifications`, `/apps` saw an
 *   indefinite "Authenticating…" spinner.
 *
 *   The fix: in the no-sovereignFQDN branch, probe /whoami first (cookie
 *   auth works on the mothership too — same backend, same cookie). On
 *   200, render the console; on 401, hard-redirect to /sovereign/login
 *   with `?next=<post-basepath-path>` so VerifyPinPage routes back to
 *   the deep link after PIN verify.
 *
 * The two contracts under test:
 *   1. catalyst-zero + /whoami 200 → console shell renders, NO navigate.
 *   2. catalyst-zero + /whoami 401 → window.location.replace fires with
 *      target = /sovereign/login?next=<post-basepath-path>. The OIDC
 *      `initiateLogin` MUST NOT fire (catalyst-zero has no Keycloak).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

// Mock detectMode for catalyst-zero mode (sovereignFQDN: null).
vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'catalyst-zero' as const, sovereignFQDN: null },
  detectMode: () => ({ mode: 'catalyst-zero' as const, sovereignFQDN: null }),
}))

// Mock urls so BASE matches the contabo-mkt /sovereign/ basepath.
vi.mock('@/shared/config/urls', () => ({
  BASE: '/sovereign/',
  API_BASE: '/sovereign/api',
  apiUrl: (p: string) => `/sovereign/api${p.startsWith('/') ? p : '/' + p}`,
  path: (p: string) => `/sovereign/${p.replace(/^\//, '')}`,
}))

const initiateLoginSpy = vi.fn<(fqdn: string) => Promise<void>>()
const silentRefreshSpy = vi.fn<(fqdn: string) => Promise<unknown>>()
const loadTokensSpy = vi.fn<() => unknown>()
initiateLoginSpy.mockResolvedValue(undefined)
silentRefreshSpy.mockResolvedValue(null)
loadTokensSpy.mockReturnValue(null)
vi.mock('@/shared/lib/oidc', () => ({
  loadTokens: () => loadTokensSpy(),
  isTokenExpired: () => true,
  silentRefresh: (fqdn: string) => silentRefreshSpy(fqdn),
  initiateLogin: (fqdn: string) => initiateLoginSpy(fqdn),
  initiateLogout: () => undefined,
  parseJWTClaims: () => ({}),
  getRequiredActions: () => [],
}))

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
vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = (await importOriginal()) as object
  return {
    ...actual,
    useRouter: () => ({ navigate: vi.fn() }),
    Outlet: () => <div data-testid="sov-outlet-stub" />,
  }
})

import { SovereignConsoleLayout } from './SovereignConsoleLayout'

let originalLocation: Location

beforeEach(() => {
  initiateLoginSpy.mockClear()
  silentRefreshSpy.mockClear()
  loadTokensSpy.mockClear()
  loadTokensSpy.mockReturnValue(null)

  originalLocation = window.location
  // jsdom's window.location is non-configurable; replace with a stub
  // that exposes a `replace` spy plus the URL surface we read.
  delete (window as unknown as { location?: Location }).location
  ;(window as unknown as { location: Partial<Location> }).location = {
    pathname: '/sovereign/dashboard',
    search: '',
    hostname: 'console.openova.io',
    href: 'https://console.openova.io/sovereign/dashboard',
    origin: 'https://console.openova.io',
    replace: vi.fn(),
    assign: vi.fn(),
  } as unknown as Location
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  ;(window as unknown as { location: Location }).location = originalLocation
})

describe('SovereignConsoleLayout — catalyst-zero (mothership) auth path', () => {
  it('renders the console shell when /whoami returns 200 (mothership cookie-authenticated)', async () => {
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(
          JSON.stringify({ email: 'op@openova.io', sub: 'kc-uid', verified: true }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        )
      }
      return new Response(null, { status: 404 })
    })

    render(<SovereignConsoleLayout />)

    // Cookie probe succeeded → outlet renders, NO redirect.
    await screen.findByTestId('sov-outlet-stub')
    expect(window.location.replace).not.toHaveBeenCalled()
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })

  it('redirects to /sovereign/login with next= when /whoami returns 401 (no Keycloak fallback)', async () => {
    // Anonymous mothership visit — must hard-navigate to the PIN flow,
    // NEVER to Keycloak (catalyst-zero has no KC issuer wired).
    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })

    render(<SovereignConsoleLayout />)

    await waitFor(() => {
      expect(window.location.replace).toHaveBeenCalledTimes(1)
    })

    const target = (window.location.replace as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    // Goes to mothership PIN page under the /sovereign basepath.
    expect(target).toMatch(/^\/sovereign\/login\?next=/)
    // next= holds the POST-basepath path (so VerifyPinPage's
    // navigate({ to: next }) round-trips cleanly through the router).
    const url = new URL(target, 'https://console.openova.io')
    expect(url.searchParams.get('next')).toBe('/dashboard')

    // Decisive: NO Keycloak redirect on the mothership.
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })

  it('preserves the full deep-link path including search params when redirecting', async () => {
    // A deep link like /sovereign/cloud?view=graph must round-trip the
    // ?view=graph query through the next= param so the operator lands
    // back on the same view after PIN verify.
    ;(window.location as unknown as { pathname: string }).pathname = '/sovereign/cloud'
    ;(window.location as unknown as { search: string }).search = '?view=graph'

    vi.spyOn(global, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as URL).toString()
      if (url.endsWith('/v1/whoami')) {
        return new Response(JSON.stringify({ error: 'unauthenticated' }), { status: 401 })
      }
      return new Response(null, { status: 404 })
    })

    render(<SovereignConsoleLayout />)

    await waitFor(() => {
      expect(window.location.replace).toHaveBeenCalledTimes(1)
    })

    const target = (window.location.replace as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    const url = new URL(target, 'https://console.openova.io')
    expect(url.searchParams.get('next')).toBe('/cloud?view=graph')
    expect(initiateLoginSpy).not.toHaveBeenCalled()
  })
})
