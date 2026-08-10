/**
 * UAT row 29 / #3374, root cause #5895 — the silent re-auth host.
 *
 * `detectMode()` derives the Sovereign FQDN by stripping ONE leading `console.`
 * label, which is right for the Sovereign console and wrong for a per-Org
 * console — both match `console.*`:
 *
 *   console.hw293.omantel.biz       -> hw293.omantel.biz       (Sovereign, correct)
 *   console.walkone.omani.homes     -> walkone.omani.homes     (an ORG, wrong)
 *
 * That value feeds buildOIDCEndpoints() -> `https://auth.${fqdn}/realms/sovereign`,
 * so on a per-Org console the silent `prompt=none` re-auth navigates to
 * `auth.<orgslug>.<parent>`. A walker measured 404 on 10/10 probes there with a
 * VALID wildcard cert and realm cookies intact: the per-Org Gateway listener is
 * `*.<slug>.<parent>` (org_console_tls.go:257) so TLS terminates, but no
 * HTTPRoute carries that hostname — only `console.<slug>.<parent>`
 * (tenant_route.go:147). There is no per-Org IdP by design; gitops.go:307 builds
 * the single shared issuer at `auth.<SovereignFQDN>/realms/<realm>`.
 *
 * The host cannot be told apart by SHAPE, so it has to be ASKED for.
 * GET /api/v1/sovereign/self is unauthenticated by design (sovereign_self.go:24
 * "carries no secrets, only public identifiers ... usable on the very first
 * browser hit before login") and is Org-scope-allowlisted (org_scope.go), which
 * is what makes it usable on exactly the expired-session path row 29 walks.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'

async function loadResolver(
  hostname: string,
  viteCatalystMode?: string,
  viteSovereignFqdn?: string,
) {
  vi.resetModules()
  vi.doMock('@/shared/constants/env', () => ({
    VITE_CATALYST_MODE: viteCatalystMode,
    VITE_SOVEREIGN_FQDN: viteSovereignFqdn,
    APP_MODE: 'saas',
    IS_SAAS: true,
    IS_SELFHOSTED: false,
    APP_VERSION: 'dev',
  }))
  // pathname/host are read at module-init by shared/config/urls.ts (BASE), so
  // a hostname-only stub would throw before the resolver ever runs.
  Object.defineProperty(window, 'location', {
    value: { hostname, host: hostname, pathname: '/', origin: `https://${hostname}` },
    writable: true,
    configurable: true,
  })
  return await import('./resolveSovereignFQDN')
}

afterEach(() => {
  vi.resetModules()
  vi.restoreAllMocks()
})

/** A /sovereign/self responder that records how many times it was called. */
function mockSelf(sovereignFQDN: string) {
  const fetchMock = vi.fn(async () => ({
    ok: true,
    status: 200,
    json: async () => ({ deploymentId: 'dep-1', sovereignFQDN }),
  }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('#5895 / UAT row 29 — resolveSovereignFQDN', () => {
  // THE SUBJECT. A per-Org console must resolve the SOVEREIGN, not its own
  // apex, or the silent re-auth navigates to a host the gateway does not route.
  it('on a per-Org console, resolves the Sovereign FQDN and NOT the Org apex', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.walkone.omani.homes')
    mockSelf('hw293.omantel.biz')

    const got = await resolveSovereignFQDN()

    expect(got).toBe('hw293.omantel.biz')
    // Named explicitly: this is the value that produced auth.<org> 404s.
    expect(got).not.toBe('walkone.omani.homes')
  })

  // The CONTROL that stops the fix from being "always ask the API": the
  // Sovereign console must keep resolving its own FQDN. A change that broke
  // this would have "fixed" row 29 by breaking every operator login.
  it('on the Sovereign console, still resolves the Sovereign FQDN', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.hw293.omantel.biz')
    mockSelf('hw293.omantel.biz')

    expect(await resolveSovereignFQDN()).toBe('hw293.omantel.biz')
  })

  // The CONTROL that keeps the API from being a hard dependency of login. If
  // /sovereign/self is unreachable the resolver must degrade to the hostname
  // derivation — the Sovereign console then behaves exactly as it does today
  // rather than losing its auth host entirely.
  it('falls back to the hostname derivation when /sovereign/self fails', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.hw293.omantel.biz')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('network down')
      }),
    )

    expect(await resolveSovereignFQDN()).toBe('hw293.omantel.biz')
  })

  // A non-2xx is not a source of truth either — same fallback, asserted
  // separately because `ok:false` and a thrown error are different code paths.
  it('falls back when /sovereign/self answers non-2xx', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.hw293.omantel.biz')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 503, json: async () => ({}) })),
    )

    expect(await resolveSovereignFQDN()).toBe('hw293.omantel.biz')
  })

  // An EMPTY sovereignFQDN must not be accepted as an answer — it would build
  // `https://auth./realms/sovereign`. Asserting on the VALUE rather than on the
  // response being present is the whole point.
  it('falls back when /sovereign/self answers with an empty sovereignFQDN', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.hw293.omantel.biz')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        status: 200,
        json: async () => ({ deploymentId: 'dep-1', sovereignFQDN: '   ' }),
      })),
    )

    expect(await resolveSovereignFQDN()).toBe('hw293.omantel.biz')
  })

  // The build-time override must still win, and must NOT cost a request —
  // dev/CI have no catalyst-api to answer.
  it('honours the build-time VITE_SOVEREIGN_FQDN override without calling the API', async () => {
    const { resolveSovereignFQDN } = await loadResolver(
      'console.hw293.omantel.biz',
      'sovereign',
      'forced.example',
    )
    const fetchMock = mockSelf('hw293.omantel.biz')

    expect(await resolveSovereignFQDN()).toBe('forced.example')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  // catalyst-zero has no Sovereign at all; asking for one would be a wrong
  // question, not a slow one.
  it('returns null in catalyst-zero mode without calling the API', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.openova.io')
    const fetchMock = mockSelf('hw293.omantel.biz')

    expect(await resolveSovereignFQDN()).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  // Row 29 is a re-auth path that can fire from several places in one page
  // load (rootBeforeLoad, the layout's expiry path, the callback). Resolving
  // once keeps the authorize leg and the token-exchange leg on the SAME host —
  // two different answers would break the exchange rather than fix the login.
  it('resolves once and caches, so every leg uses the same host', async () => {
    const { resolveSovereignFQDN } = await loadResolver('console.walkone.omani.homes')
    const fetchMock = mockSelf('hw293.omantel.biz')

    const [a, b, c] = await Promise.all([
      resolveSovereignFQDN(),
      resolveSovereignFQDN(),
      resolveSovereignFQDN(),
    ])

    expect([a, b, c]).toEqual(['hw293.omantel.biz', 'hw293.omantel.biz', 'hw293.omantel.biz'])
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
