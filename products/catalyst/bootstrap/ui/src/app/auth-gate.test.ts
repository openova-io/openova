import { describe, it, expect, beforeEach } from 'vitest'
import {
  canonicalisePath,
  isPublicPath,
  hasCatalystSession,
  PUBLIC_PATH_PREFIXES,
} from './auth-gate'

describe('canonicalisePath', () => {
  it('lowercases paths', () => {
    expect(canonicalisePath('/Dashboard')).toBe('/dashboard')
    expect(canonicalisePath('/APP/MyComponent')).toBe('/app/mycomponent')
  })

  it('collapses duplicate slashes', () => {
    expect(canonicalisePath('//dashboard')).toBe('/dashboard')
    expect(canonicalisePath('///app//foo')).toBe('/app/foo')
  })

  it('strips trailing slash except root', () => {
    expect(canonicalisePath('/dashboard/')).toBe('/dashboard')
    expect(canonicalisePath('/')).toBe('/')
    expect(canonicalisePath('/apps/')).toBe('/apps')
  })

  it('passes already-canonical paths through unchanged', () => {
    expect(canonicalisePath('/dashboard')).toBe('/dashboard')
    expect(canonicalisePath('/users/some-user@example.org')).toBe('/users/some-user@example.org')
  })
})

describe('isPublicPath', () => {
  it('treats / as gated (not public — index redirects to /login or /dashboard)', () => {
    expect(isPublicPath('/')).toBe(false)
  })

  it('matches /login + nested', () => {
    expect(isPublicPath('/login')).toBe(true)
    expect(isPublicPath('/login/verify')).toBe(true)
  })

  it('matches /auth/handover and /auth/callback', () => {
    expect(isPublicPath('/auth/handover')).toBe(true)
    expect(isPublicPath('/auth/handover-error')).toBe(true)
    expect(isPublicPath('/auth/callback')).toBe(true)
  })

  it('matches /forgot, /signup', () => {
    expect(isPublicPath('/forgot')).toBe(true)
    expect(isPublicPath('/signup')).toBe(true)
  })

  it('matches /readyz, /healthz, /sovereignty/preview, /designs', () => {
    expect(isPublicPath('/readyz')).toBe(true)
    expect(isPublicPath('/healthz')).toBe(true)
    expect(isPublicPath('/sovereignty/preview')).toBe(true)
    expect(isPublicPath('/designs')).toBe(true)
    expect(isPublicPath('/designs/showcase')).toBe(true)
  })

  it('rejects gated paths (the bug rows from #1090 cluster A2)', () => {
    expect(isPublicPath('/dashboard')).toBe(false)
    expect(isPublicPath('/apps')).toBe(false)
    expect(isPublicPath('/app/some-component-id')).toBe(false)
    expect(isPublicPath('/users/some-user@example.org')).toBe(false)
    expect(isPublicPath('/jobs/timeline')).toBe(false)
    expect(isPublicPath('/cloud')).toBe(false)
    expect(isPublicPath('/settings')).toBe(false)
    expect(isPublicPath('/notifications')).toBe(false)
  })

  it('rejects prefix-collisions where the gated path starts with a public token', () => {
    // /loginz is gated even though /login is public — we use exact-or-slash match
    expect(isPublicPath('/loginz')).toBe(false)
    expect(isPublicPath('/auth/handovers')).toBe(false)
  })

  it('PUBLIC_PATH_PREFIXES contains the expected set', () => {
    expect(PUBLIC_PATH_PREFIXES).toEqual([
      '/login',
      '/signup',
      '/forgot',
      '/auth/handover',
      '/auth/handover-error',
      '/auth/callback',
      '/readyz',
      '/healthz',
      '/sovereignty/preview',
      '/designs',
      '/api/',
    ])
  })
})

describe('hasCatalystSession', () => {
  beforeEach(() => {
    sessionStorage.clear()
  })

  it('returns false when sessionStorage is empty', () => {
    expect(hasCatalystSession()).toBe(false)
  })

  it('returns true when an oidc:* key is present (legacy PKCE flow)', () => {
    sessionStorage.setItem('oidc:access_token', 'eyJ...')
    expect(hasCatalystSession()).toBe(true)
  })

  it('returns true when catalyst:authed marker is set (new cookie flow)', () => {
    sessionStorage.setItem('catalyst:authed', '1')
    expect(hasCatalystSession()).toBe(true)
  })

  it('returns false when catalyst:authed has a different value', () => {
    sessionStorage.setItem('catalyst:authed', '0')
    expect(hasCatalystSession()).toBe(false)
  })

  it('returns false on unrelated keys', () => {
    sessionStorage.setItem('something:else', 'foo')
    expect(hasCatalystSession()).toBe(false)
  })
})

describe('integration — anti-regression for #1090 cluster A2 bypass routes', () => {
  // For each of the 7 routes that bypassed the layout-only gate, the
  // rootBeforeLoad gate should now redirect to /login. We verify the
  // pure helpers say "not public" and "not authed" — the actual
  // redirect happens in router.tsx and is validated end-to-end via
  // Playwright (matrix file /tmp/test-matrix-routing-bypass.json).
  beforeEach(() => sessionStorage.clear())

  const bypassRoutes = [
    '/app/some-component-id',
    '/users/some-user@example.org',
    '/dashboard/',          // canonicalisePath → /dashboard
    '/Dashboard',           // canonicalisePath → /dashboard
    '//dashboard',          // canonicalisePath → /dashboard
    '/apps',
    '/dashboard',           // regression check (already PASSED in iter-2)
  ]

  it.each(bypassRoutes)('%s → canonicalises + isPublicPath false + would gate', (raw) => {
    const canonical = canonicalisePath(raw)
    expect(isPublicPath(canonical)).toBe(false)
    expect(hasCatalystSession()).toBe(false)
    // Together → rootBeforeLoad would throw redirect to /login?next=...
  })
})
