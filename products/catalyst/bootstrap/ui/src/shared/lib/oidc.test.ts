/**
 * Tests for oidc.ts — OIDC helpers for the Sovereign Console auth flow.
 *
 * Related: GitHub issue #607
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  buildOIDCEndpoints,
  buildRedirectURI,
  buildAuthorizeURL,
  parseSilentAuthError,
  parseJWTClaims,
  getRequiredActions,
  saveTokens,
  loadTokens,
  clearTokens,
  isTokenExpired,
  SILENT_AUTH_RECOVERABLE_ERRORS,
  REQUIRED_ACTION_UPDATE_PASSWORD,
  REQUIRED_ACTION_CONFIGURE_PASSKEY,
  REQUIRED_ACTION_CONFIGURE_TOTP,
} from './oidc'
import type { TokenSet } from './oidc'

// ── buildOIDCEndpoints ────────────────────────────────────────────────────────

describe('buildOIDCEndpoints()', () => {
  it('derives the correct Keycloak issuer from the Sovereign FQDN', () => {
    const eps = buildOIDCEndpoints('otech23.omani.works')
    expect(eps.issuer).toBe('https://auth.otech23.omani.works/realms/sovereign')
  })

  it('derives the auth endpoint from the issuer', () => {
    const eps = buildOIDCEndpoints('otech23.omani.works')
    expect(eps.authEndpoint).toBe(
      'https://auth.otech23.omani.works/realms/sovereign/protocol/openid-connect/auth',
    )
  })

  it('derives the token endpoint from the issuer', () => {
    const eps = buildOIDCEndpoints('test.omani.works')
    expect(eps.tokenEndpoint).toBe(
      'https://auth.test.omani.works/realms/sovereign/protocol/openid-connect/token',
    )
  })

  it('derives the logout endpoint from the issuer', () => {
    const eps = buildOIDCEndpoints('test.omani.works')
    expect(eps.logoutEndpoint).toBe(
      'https://auth.test.omani.works/realms/sovereign/protocol/openid-connect/logout',
    )
  })
})

// ── buildRedirectURI ──────────────────────────────────────────────────────────

describe('buildRedirectURI()', () => {
  it('returns origin + /auth/callback', () => {
    expect(buildRedirectURI()).toBe(`${window.location.origin}/auth/callback`)
  })
})

// ── buildAuthorizeURL (#3374 row 29 — silent prompt=none re-auth) ─────────────

describe('buildAuthorizeURL()', () => {
  const args = {
    challenge: 'chal-abc',
    state: 'state-xyz',
    redirectURI: 'https://console.otech23.omani.works/auth/callback',
  }

  it('targets the Sovereign Keycloak authorization endpoint with a PKCE code request', () => {
    const url = new URL(buildAuthorizeURL('otech23.omani.works', args))
    expect(url.origin + url.pathname).toBe(
      'https://auth.otech23.omani.works/realms/sovereign/protocol/openid-connect/auth',
    )
    expect(url.searchParams.get('response_type')).toBe('code')
    expect(url.searchParams.get('client_id')).toBe('catalyst-ui')
    expect(url.searchParams.get('code_challenge')).toBe('chal-abc')
    expect(url.searchParams.get('code_challenge_method')).toBe('S256')
    expect(url.searchParams.get('state')).toBe('state-xyz')
    expect(url.searchParams.get('redirect_uri')).toBe(args.redirectURI)
  })

  it('OMITS prompt for an ordinary interactive request', () => {
    const url = new URL(buildAuthorizeURL('otech23.omani.works', args))
    expect(url.searchParams.has('prompt')).toBe(false)
  })

  it('adds prompt=none for a silent re-auth request', () => {
    const url = new URL(
      buildAuthorizeURL('otech23.omani.works', { ...args, prompt: 'none' }),
    )
    expect(url.searchParams.get('prompt')).toBe('none')
  })
})

// ── parseSilentAuthError (#3374 row 29) ───────────────────────────────────────

describe('parseSilentAuthError()', () => {
  it('returns the error code when a silent attempt bounces back with login_required and no code', () => {
    const params = new URLSearchParams({ error: 'login_required', state: 's' })
    expect(parseSilentAuthError(params)).toBe('login_required')
  })

  it.each(SILENT_AUTH_RECOVERABLE_ERRORS)(
    'recognises the recoverable silent-auth error %s',
    (code) => {
      const params = new URLSearchParams({ error: code })
      expect(parseSilentAuthError(params)).toBe(code)
    },
  )

  it('returns null on the happy path (authorization code present)', () => {
    const params = new URLSearchParams({ code: 'auth-code-123', state: 's' })
    expect(parseSilentAuthError(params)).toBeNull()
  })

  it('returns null when a code is present even alongside an error param', () => {
    const params = new URLSearchParams({ code: 'auth-code-123', error: 'login_required' })
    expect(parseSilentAuthError(params)).toBeNull()
  })

  it('returns null for a non-recoverable error so a genuine failure still surfaces', () => {
    const params = new URLSearchParams({ error: 'invalid_client' })
    expect(parseSilentAuthError(params)).toBeNull()
  })

  it('returns null when neither code nor error is present', () => {
    expect(parseSilentAuthError(new URLSearchParams())).toBeNull()
  })
})

// ── parseJWTClaims ────────────────────────────────────────────────────────────

/** Build a minimal, non-signed JWT with the given payload for testing. */
function buildFakeJWT(payload: Record<string, unknown>): string {
  const header = btoa(JSON.stringify({ alg: 'none', typ: 'JWT' }))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '')
  const body = btoa(JSON.stringify(payload))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '')
  return `${header}.${body}.`
}

describe('parseJWTClaims()', () => {
  it('decodes a valid JWT payload', () => {
    const claims = parseJWTClaims(
      buildFakeJWT({ sub: 'user-123', email: 'test@example.com' }),
    )
    expect(claims.sub).toBe('user-123')
    expect(claims.email).toBe('test@example.com')
  })

  it('returns {} for a malformed JWT', () => {
    expect(parseJWTClaims('not-a-jwt')).toEqual({})
    expect(parseJWTClaims('')).toEqual({})
  })

  it('decodes requiredActions claim', () => {
    const jwt = buildFakeJWT({
      sub: 'u1',
      requiredActions: [REQUIRED_ACTION_UPDATE_PASSWORD, REQUIRED_ACTION_CONFIGURE_PASSKEY],
    })
    const claims = parseJWTClaims(jwt)
    expect(claims.requiredActions).toEqual([
      REQUIRED_ACTION_UPDATE_PASSWORD,
      REQUIRED_ACTION_CONFIGURE_PASSKEY,
    ])
  })
})

// ── getRequiredActions ────────────────────────────────────────────────────────

describe('getRequiredActions()', () => {
  it('returns the requiredActions array from a token', () => {
    const jwt = buildFakeJWT({
      requiredActions: [REQUIRED_ACTION_CONFIGURE_TOTP],
    })
    expect(getRequiredActions(jwt)).toEqual([REQUIRED_ACTION_CONFIGURE_TOTP])
  })

  it('returns [] when no requiredActions claim is present', () => {
    const jwt = buildFakeJWT({ sub: 'u1' })
    expect(getRequiredActions(jwt)).toEqual([])
  })

  it('returns [] for a malformed token', () => {
    expect(getRequiredActions('garbage')).toEqual([])
  })
})

// ── Token storage ─────────────────────────────────────────────────────────────

describe('token storage (sessionStorage)', () => {
  const sample: TokenSet = {
    idToken: buildFakeJWT({ sub: 'u1', name: 'Alice' }),
    accessToken: buildFakeJWT({ sub: 'u1', scope: 'openid' }),
    refreshToken: 'refresh-abc',
    expiresAt: Date.now() + 3600_000,
  }

  beforeEach(() => clearTokens())
  afterEach(() => clearTokens())

  it('round-trips tokens through sessionStorage', () => {
    saveTokens(sample)
    const loaded = loadTokens()
    expect(loaded).not.toBeNull()
    expect(loaded!.idToken).toBe(sample.idToken)
    expect(loaded!.accessToken).toBe(sample.accessToken)
    expect(loaded!.refreshToken).toBe(sample.refreshToken)
    expect(loaded!.expiresAt).toBe(sample.expiresAt)
  })

  it('loadTokens returns null when nothing is stored', () => {
    expect(loadTokens()).toBeNull()
  })

  it('clearTokens removes all stored items', () => {
    saveTokens(sample)
    clearTokens()
    expect(loadTokens()).toBeNull()
  })

  it('handles a null refreshToken', () => {
    const noRefresh: TokenSet = { ...sample, refreshToken: null }
    saveTokens(noRefresh)
    const loaded = loadTokens()
    expect(loaded!.refreshToken).toBeNull()
  })
})

// ── isTokenExpired ────────────────────────────────────────────────────────────

describe('isTokenExpired()', () => {
  it('returns false for a token with > 60s validity', () => {
    const tokens: TokenSet = {
      idToken: '',
      accessToken: '',
      refreshToken: null,
      expiresAt: Date.now() + 120_000,
    }
    expect(isTokenExpired(tokens)).toBe(false)
  })

  it('returns true for a token that has expired', () => {
    const tokens: TokenSet = {
      idToken: '',
      accessToken: '',
      refreshToken: null,
      expiresAt: Date.now() - 1,
    }
    expect(isTokenExpired(tokens)).toBe(true)
  })

  it('returns true for a token expiring within 60s (buffer)', () => {
    const tokens: TokenSet = {
      idToken: '',
      accessToken: '',
      refreshToken: null,
      expiresAt: Date.now() + 30_000, // 30s — within the 60s buffer
    }
    expect(isTokenExpired(tokens)).toBe(true)
  })
})
