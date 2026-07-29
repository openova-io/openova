import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  canonicalisePath,
  isPublicPath,
  hasCatalystSession,
  probeWhoamiAndCacheMarker,
  reservedProvisionIdSegment,
  sanitizeNextParam,
  sameSovereignHostFamily,
  CANONICAL_SOVEREIGN_SUBDOMAINS,
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

describe('reservedProvisionIdSegment — #4704 Task B', () => {
  it('flags a reserved route word in the $deploymentId slot', () => {
    // The founder-visible shape: an id-less `/provision/${''}/jobs` link
    // collapses through canonicalisePath into `/provision/jobs`.
    expect(reservedProvisionIdSegment(canonicalisePath('/provision//jobs'))).toBe('jobs')
    expect(reservedProvisionIdSegment('/provision/jobs')).toBe('jobs')
    expect(reservedProvisionIdSegment('/provision/dashboard')).toBe('dashboard')
    expect(reservedProvisionIdSegment('/provision/jobs/some-job-id')).toBe('jobs')
    expect(reservedProvisionIdSegment('/provision/cloud')).toBe('cloud')
  })

  it('passes real 16-hex deployment ids through untouched', () => {
    expect(reservedProvisionIdSegment('/provision/4635277cae4ffed9')).toBeNull()
    expect(reservedProvisionIdSegment('/provision/4635277cae4ffed9/jobs')).toBeNull()
  })

  it('keeps the honest malformed-id path for non-reserved garbage', () => {
    // Truncated hex / random words keep the AppsPage error banner.
    expect(reservedProvisionIdSegment('/provision/4635277c')).toBeNull()
    expect(reservedProvisionIdSegment('/provision/not-a-real-id')).toBeNull()
  })

  it('ignores non-provision paths and the legacy literal route', () => {
    expect(reservedProvisionIdSegment('/jobs')).toBeNull()
    expect(reservedProvisionIdSegment('/dashboard')).toBeNull()
    // /provision/legacy/$deploymentId is a REAL route.
    expect(reservedProvisionIdSegment('/provision/legacy/4635277cae4ffed9')).toBeNull()
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

describe('probeWhoamiAndCacheMarker', () => {
  const originalFetch = globalThis.fetch
  beforeEach(() => {
    sessionStorage.clear()
  })
  afterEach(() => {
    globalThis.fetch = originalFetch
    vi.restoreAllMocks()
  })

  it('returns true and caches the marker on 200', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      status: 200,
    } as Response) as typeof fetch
    const result = await probeWhoamiAndCacheMarker('/api')
    expect(result).toBe(true)
    expect(sessionStorage.getItem('catalyst:authed')).toBe('1')
  })

  it('returns false on 401 and does NOT cache the marker', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      status: 401,
    } as Response) as typeof fetch
    const result = await probeWhoamiAndCacheMarker('/api')
    expect(result).toBe(false)
    expect(sessionStorage.getItem('catalyst:authed')).toBeNull()
  })

  it('REVOKES a stale marker on 401 (#5460 — the flag must not outlive the session)', async () => {
    // hw290 row-29: after TTL expiry the marker survived, hasCatalystSession()
    // short-circuited the gate past its whoami probe AND the silent-SSO leg,
    // and every marker-consuming surface rendered authenticated chrome
    // against a dead session. The authority's 401 must clear the cache.
    sessionStorage.setItem('catalyst:authed', '1')
    globalThis.fetch = vi.fn().mockResolvedValue({
      status: 401,
    } as Response) as typeof fetch
    const result = await probeWhoamiAndCacheMarker('/api')
    expect(result).toBe(false)
    expect(sessionStorage.getItem('catalyst:authed')).toBeNull()
  })

  it('does NOT revoke the marker on 5xx or network error (no verdict, no revocation)', async () => {
    // A 503 or network blip is not an authority verdict — revoking on it
    // would log operators out on every transient (#5461-class flake).
    sessionStorage.setItem('catalyst:authed', '1')
    globalThis.fetch = vi.fn().mockResolvedValue({ status: 503 } as Response) as typeof fetch
    expect(await probeWhoamiAndCacheMarker('/api')).toBeNull()
    expect(sessionStorage.getItem('catalyst:authed')).toBe('1')
    globalThis.fetch = vi.fn().mockRejectedValue(new Error('network down')) as typeof fetch
    expect(await probeWhoamiAndCacheMarker('/api')).toBeNull()
    expect(sessionStorage.getItem('catalyst:authed')).toBe('1')
  })

  it('returns null on 5xx (fail-open signal to caller)', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      status: 503,
    } as Response) as typeof fetch
    const result = await probeWhoamiAndCacheMarker('/api')
    expect(result).toBeNull()
    expect(sessionStorage.getItem('catalyst:authed')).toBeNull()
  })

  it('returns null on network error (fail-open signal to caller)', async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new Error('network down')) as typeof fetch
    const result = await probeWhoamiAndCacheMarker('/api')
    expect(result).toBeNull()
    expect(sessionStorage.getItem('catalyst:authed')).toBeNull()
  })

  it('uses credentials:include so the HttpOnly cookie is sent', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200 } as Response)
    globalThis.fetch = fetchMock as typeof fetch
    await probeWhoamiAndCacheMarker('/sovereign/api')
    expect(fetchMock).toHaveBeenCalledWith(
      '/sovereign/api/v1/whoami',
      expect.objectContaining({
        method: 'GET',
        credentials: 'include',
      }),
    )
  })
})

describe('sanitizeNextParam — open-redirect defense (CWE-601)', () => {
  // qa-loop iter-4 cluster `users-page-null-map-and-open-redirect` —
  // TC-009 surfaced /login?next=//dashboard with a leading `//` which
  // would let an attacker craft /login?next=//evil.com to bounce the
  // operator off-origin after sign-in.

  it('returns undefined for undefined / empty / non-string input', () => {
    expect(sanitizeNextParam(undefined)).toBeUndefined()
    expect(sanitizeNextParam('')).toBeUndefined()
    expect(sanitizeNextParam(null)).toBeUndefined()
    expect(sanitizeNextParam(123)).toBeUndefined()
    expect(sanitizeNextParam({})).toBeUndefined()
  })

  it('passes safe single-leading-slash paths through unchanged', () => {
    expect(sanitizeNextParam('/dashboard')).toBe('/dashboard')
    expect(sanitizeNextParam('/provision/d-1/users')).toBe('/provision/d-1/users')
    expect(sanitizeNextParam('/users/some-user@example.org?tab=grants')).toBe(
      '/users/some-user@example.org?tab=grants',
    )
    expect(sanitizeNextParam('/dashboard#anchor')).toBe('/dashboard#anchor')
  })

  it('collapses leading multi-slashes to a single slash (TC-009 attack vector)', () => {
    // Protocol-relative URLs treated as host references by the browser
    expect(sanitizeNextParam('//dashboard')).toBeUndefined()
    expect(sanitizeNextParam('//evil.com/path')).toBeUndefined()
    expect(sanitizeNextParam('///foo')).toBeUndefined()
    expect(sanitizeNextParam('////a/b')).toBeUndefined()
  })

  it('rejects absolute URLs with explicit schemes', () => {
    expect(sanitizeNextParam('http://evil.com/path')).toBeUndefined()
    expect(sanitizeNextParam('https://evil.com/path')).toBeUndefined()
    expect(sanitizeNextParam('javascript:alert(1)')).toBeUndefined()
    expect(sanitizeNextParam('data:text/html,<script>alert(1)</script>')).toBeUndefined()
    expect(sanitizeNextParam('file:///etc/passwd')).toBeUndefined()
    expect(sanitizeNextParam('vbscript:msgbox(1)')).toBeUndefined()
  })

  it('rejects backslash-prefixed paths (browser quirk)', () => {
    // Some browsers normalize \ to / in URLs — catching `\\evil.com`
    // and `/\evil.com` as cousins of `//evil.com`.
    expect(sanitizeNextParam('\\\\evil.com/path')).toBeUndefined()
    expect(sanitizeNextParam('/\\evil.com')).toBeUndefined()
    expect(sanitizeNextParam('\\evil.com')).toBeUndefined()
  })

  it('rejects strings that do not start with /', () => {
    expect(sanitizeNextParam('dashboard')).toBeUndefined()
    expect(sanitizeNextParam('./dashboard')).toBeUndefined()
    expect(sanitizeNextParam('../etc/passwd')).toBeUndefined()
    expect(sanitizeNextParam('mailto:attacker@evil.com')).toBeUndefined()
  })

  it('rejects strings containing whitespace or control characters', () => {
    expect(sanitizeNextParam('/dashboard\n')).toBeUndefined()
    expect(sanitizeNextParam('/dashboard ')).toBeUndefined()
    expect(sanitizeNextParam(' /dashboard')).toBeUndefined()
    expect(sanitizeNextParam('/dash\x00board')).toBeUndefined()
  })
})

describe('sameSovereignHostFamily', () => {
  it('derives {console,auth,api}.<fqdn> by stripping the first label', () => {
    expect(sameSovereignHostFamily('console.t42.omani.works')).toEqual([
      'console.t42.omani.works',
      'auth.t42.omani.works',
      'api.t42.omani.works',
    ])
    expect(sameSovereignHostFamily('api.t42.omani.works')).toEqual([
      'console.t42.omani.works',
      'auth.t42.omani.works',
      'api.t42.omani.works',
    ])
  })

  it('preserves a :port suffix on every family member', () => {
    expect(sameSovereignHostFamily('console.t42.omani.works:8443')).toEqual([
      'console.t42.omani.works:8443',
      'auth.t42.omani.works:8443',
      'api.t42.omani.works:8443',
    ])
  })

  it('trusts nothing for bare hosts / single-label parents / localhost', () => {
    expect(sameSovereignHostFamily('')).toEqual([])
    expect(sameSovereignHostFamily('localhost')).toEqual([])
    expect(sameSovereignHostFamily('localhost:5173')).toEqual([])
    // A single-label parent like `console.local` → parent `local` is refused.
    expect(sameSovereignHostFamily('console.local')).toEqual([])
    expect(sameSovereignHostFamily('console.')).toEqual([])
  })

  it('CANONICAL_SOVEREIGN_SUBDOMAINS is the expected control-plane set', () => {
    expect(CANONICAL_SOVEREIGN_SUBDOMAINS).toEqual(['console', 'auth', 'api'])
  })
})

describe('sanitizeNextParam — same-Sovereign cross-host allowlist (#3271)', () => {
  // After PIN-verify the OAuth `next` continuation is a legitimate
  // cross-host URL on the SAME Sovereign:
  //   https://api.<fqdn>/oidc/auth?...&redirect_uri=https://auth.<fqdn>/...
  // sanitizeNextParam must ACCEPT it (so the app SSO lands the user in
  // the target app, not the console /dashboard) while still rejecting
  // every host outside the trusted {console,auth,api}.<fqdn> family.
  const HOST = 'console.t42.omani.works'

  it('accepts an absolute api.<fqdn> OAuth continuation (the #3271 walk)', () => {
    const next =
      'https://api.t42.omani.works/oidc/auth?client_id=catalyst-pin' +
      '&redirect_uri=https://auth.t42.omani.works/realms/sovereign/broker/catalyst-pin/endpoint' +
      '&state=abc&response_type=code'
    expect(sanitizeNextParam(next, HOST)).toBe(next)
  })

  it('accepts absolute auth.<fqdn> and console.<fqdn> same-Sovereign URLs', () => {
    expect(sanitizeNextParam('https://auth.t42.omani.works/realms/sovereign', HOST)).toBe(
      'https://auth.t42.omani.works/realms/sovereign',
    )
    expect(sanitizeNextParam('https://console.t42.omani.works/dashboard', HOST)).toBe(
      'https://console.t42.omani.works/dashboard',
    )
  })

  it('still accepts same-origin relative paths', () => {
    expect(sanitizeNextParam('/dashboard', HOST)).toBe('/dashboard')
    expect(sanitizeNextParam('/provision/d-1/users', HOST)).toBe('/provision/d-1/users')
  })

  it('REJECTS an unrelated absolute host (open-redirect)', () => {
    expect(sanitizeNextParam('https://evil.com/path', HOST)).toBeUndefined()
    expect(sanitizeNextParam('http://evil.com', HOST)).toBeUndefined()
  })

  it('REJECTS protocol-relative //evil.com', () => {
    expect(sanitizeNextParam('//evil.com', HOST)).toBeUndefined()
    expect(sanitizeNextParam('//api.t42.omani.works/oidc/auth', HOST)).toBeUndefined()
  })

  it('REJECTS the look-alike suffix attack api.<fqdn>.evil.com (CWE-601)', () => {
    // The registrable host is evil.com — NOT the Sovereign FQDN. A naive
    // `next.includes("api.<fqdn>")` check would be fooled; the URL-parse +
    // exact host membership test must reject this.
    expect(
      sanitizeNextParam('https://api.t42.omani.works.evil.com/oidc/auth', HOST),
    ).toBeUndefined()
    expect(
      sanitizeNextParam('https://api.t42.omani.works@evil.com/oidc/auth', HOST),
    ).toBeUndefined()
    expect(
      sanitizeNextParam('https://evil.com/api.t42.omani.works/oidc/auth', HOST),
    ).toBeUndefined()
  })

  it('REJECTS a different Sovereign FQDN (cross-Sovereign redirect)', () => {
    expect(
      sanitizeNextParam('https://api.t99.omantel.biz/oidc/auth', HOST),
    ).toBeUndefined()
  })

  it('REJECTS a same-host but wrong-subdomain (e.g. grafana.<fqdn>) absolute URL', () => {
    // Only the control-plane family is trusted for a bare redirect target.
    expect(
      sanitizeNextParam('https://grafana.t42.omani.works/login', HOST),
    ).toBeUndefined()
  })

  it('REJECTS javascript:/data: schemes regardless of host', () => {
    expect(sanitizeNextParam('javascript:alert(1)', HOST)).toBeUndefined()
    expect(sanitizeNextParam('data:text/html,<script>1</script>', HOST)).toBeUndefined()
  })

  it('falls back to paths-only when no trusted family can be derived', () => {
    // On localhost (dev) there is no multi-label parent → absolute URLs
    // are all rejected, relative paths still work.
    expect(sanitizeNextParam('https://api.t42.omani.works/oidc/auth', 'localhost')).toBeUndefined()
    expect(sanitizeNextParam('/dashboard', 'localhost')).toBe('/dashboard')
  })

  it('is port-exact: a mismatched port is rejected', () => {
    // Current host has no port; an absolute next carrying :8443 is a
    // different origin and must not be trusted.
    expect(
      sanitizeNextParam('https://api.t42.omani.works:8443/oidc/auth', HOST),
    ).toBeUndefined()
    // …but matches when the current host carries the same port.
    expect(
      sanitizeNextParam(
        'https://api.t42.omani.works:8443/oidc/auth',
        'console.t42.omani.works:8443',
      ),
    ).toBe('https://api.t42.omani.works:8443/oidc/auth')
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
