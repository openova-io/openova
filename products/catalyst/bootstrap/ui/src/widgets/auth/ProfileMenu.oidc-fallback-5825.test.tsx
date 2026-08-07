/**
 * ProfileMenu.oidc-fallback-5825.test.tsx — UAT rows 1 / 26 / 72.
 *
 * ProfileMenu decided "signed in" from `useSession` alone — a cookie session
 * read off GET /whoami. The sovereign console can also be authenticated by the
 * legacy OIDC PKCE token set, where /whoami returns 401 and `session.signedIn`
 * is false. SovereignSidebar already falls back to the id_token claims for
 * exactly this reason (#4187), so the two identity readers in the SAME header
 * disagreed: the sidebar rendered `EB emrah.baysal@openova.io` while the header
 * rendered a [Sign in] button three inches away.
 *
 * That is what rows 1 and 26 recorded — "a Sign in button still renders in the
 * banner DOM alongside the authenticated chip" — and row 72 saw it on the BSS
 * billing page. It is not cosmetic: the rows' clause is "lands signed-in as the
 * owner, NO PIN/login form", and a visible sign-in affordance is a login form by
 * that clause's own definition.
 *
 * WHY EXPIRY IS TESTED AS HARD AS PRESENCE. A stale token set survives in
 * sessionStorage after the session dies. Accepting it would trade a false
 * "signed out" for a false "signed in" — the worse direction, because it paints
 * an avatar for a session that can no longer call the API. The presence case
 * gets one test; the reject cases get three.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

const useSession = vi.fn()

vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => useSession(),
}))

// The PIN modal pulls in the whole auth stack; it is not under test here.
vi.mock('./PinSignInModal', () => ({
  PinSignInModal: () => null,
}))

const { ProfileMenu } = await import('./ProfileMenu')

/** A JWT whose payload decodes to `claims`. Signature is never verified. */
function jwt(claims: Record<string, unknown>): string {
  const b64 = (o: unknown) =>
    btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64({ alg: 'none' })}.${b64(claims)}.sig`
}

function seedTokens(claims: Record<string, unknown>, expiresAt: number) {
  // Keys must match src/shared/lib/oidc.ts verbatim — a typo here would make
  // loadTokens() return null and every "rejects" assertion below pass for the
  // wrong reason. The positive test is what pins them honest.
  sessionStorage.setItem('oidc:id_token', jwt(claims))
  sessionStorage.setItem('oidc:access_token', 'at')
  sessionStorage.setItem('oidc:expires_at', String(expiresAt))
}

const anonymousSession = {
  signedIn: false,
  email: null,
  sub: null,
  tier: '',
  roles: [],
  loading: false,
  refetch: vi.fn(),
  signOut: vi.fn(),
}

beforeEach(() => {
  sessionStorage.clear()
  useSession.mockReturnValue(anonymousSession)
})

afterEach(() => {
  cleanup()
  sessionStorage.clear()
  vi.clearAllMocks()
})

describe('#5825 — ProfileMenu honours an OIDC session when /whoami has none', () => {
  it('renders the identity, not a [Sign in] button, on a live id_token', () => {
    seedTokens({ email: 'emrah.baysal@openova.io' }, Date.now() + 3_600_000)

    render(<ProfileMenu />)

    expect(
      screen.queryByTestId('profile-menu-signin'),
      'a [Sign in] button rendered for a signed-in operator — the exact contradiction ' +
        'rows 1/26/72 recorded next to the sidebar’s authenticated chip',
    ).toBeNull()
    // The avatar carries the initial of the resolved identity.
    expect(screen.getByTitle('emrah.baysal@openova.io')).toBeTruthy()
  })

  it('still shows [Sign in] when there is no token at all', () => {
    render(<ProfileMenu />)
    expect(screen.getByTestId('profile-menu-signin')).toBeTruthy()
  })

  it('rejects an EXPIRED token rather than painting a dead session as live', () => {
    // 30s of validity left — inside isTokenExpired's 60s margin, so already
    // treated as expired by the same helper the refresh path uses.
    seedTokens({ email: 'emrah.baysal@openova.io' }, Date.now() + 30_000)

    render(<ProfileMenu />)
    expect(
      screen.getByTestId('profile-menu-signin'),
      'a stale token set was accepted as proof of identity — the avatar would front ' +
        'a session that can no longer call the API',
    ).toBeTruthy()
  })

  it('rejects a token set with no email-bearing claim', () => {
    seedTokens({ sub: 'abc-123' }, Date.now() + 3_600_000)

    render(<ProfileMenu />)
    expect(screen.getByTestId('profile-menu-signin')).toBeTruthy()
  })

  it('control: the cookie session still wins when both are present', () => {
    // Precedence must match SovereignSidebar (#4187) — cookie authoritative,
    // id_token fallback — or the two readers can name different people again.
    useSession.mockReturnValue({ ...anonymousSession, signedIn: true, email: 'cookie@openova.io' })
    seedTokens({ email: 'stale-token@openova.io' }, Date.now() + 3_600_000)

    render(<ProfileMenu />)
    expect(screen.queryByTestId('profile-menu-signin')).toBeNull()
    expect(screen.getByTitle('cookie@openova.io')).toBeTruthy()
  })
})
