/**
 * Tests for detectMode.ts — Catalyst operating mode detection.
 *
 * Related: GitHub issue #607
 */

import { describe, it, expect, vi, afterEach } from 'vitest'

// We need to reset module state between tests because DETECTED_MODE is a
// module-level singleton derived at import time. We use vi.resetModules()
// and dynamic imports to control the environment in each test.

// Helper to load detectMode in a controlled environment.
async function loadDetectMode(
  hostname: string,
  viteCatalystMode?: string,
  viteSovereignFqdn?: string,
) {
  vi.resetModules()

  // Mock the env constants module so VITE_CATALYST_MODE can be varied.
  vi.doMock('@/shared/constants/env', () => ({
    VITE_CATALYST_MODE: viteCatalystMode,
    VITE_SOVEREIGN_FQDN: viteSovereignFqdn,
    APP_MODE: 'saas',
    IS_SAAS: true,
    IS_SELFHOSTED: false,
    APP_VERSION: 'dev',
  }))

  // Mock window.location.hostname
  Object.defineProperty(window, 'location', {
    value: { hostname },
    writable: true,
    configurable: true,
  })

  const mod = await import('./detectMode')
  return mod
}

afterEach(() => {
  vi.resetModules()
  vi.restoreAllMocks()
})

describe('detectMode()', () => {
  it('returns catalyst-zero for console.openova.io', async () => {
    const { detectMode } = await loadDetectMode('console.openova.io')
    expect(detectMode()).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('returns catalyst-zero for localhost', async () => {
    const { detectMode } = await loadDetectMode('localhost')
    expect(detectMode()).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('returns catalyst-zero for 127.0.0.1', async () => {
    const { detectMode } = await loadDetectMode('127.0.0.1')
    expect(detectMode()).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('returns sovereign for console.otech23.omani.works', async () => {
    const { detectMode } = await loadDetectMode('console.otech23.omani.works')
    expect(detectMode()).toEqual({
      mode: 'sovereign',
      sovereignFQDN: 'otech23.omani.works',
    })
  })

  it('returns sovereign for console.eu-west.acmecorp.com', async () => {
    const { detectMode } = await loadDetectMode('console.eu-west.acmecorp.com')
    expect(detectMode()).toEqual({
      mode: 'sovereign',
      sovereignFQDN: 'eu-west.acmecorp.com',
    })
  })

  it('strips port from hostname before processing', async () => {
    const { detectMode } = await loadDetectMode('console.otech99.omani.works:5173')
    expect(detectMode()).toEqual({
      mode: 'sovereign',
      sovereignFQDN: 'otech99.omani.works',
    })
  })

  it('returns catalyst-zero for an unrecognised hostname (fallback)', async () => {
    const { detectMode } = await loadDetectMode('unknown-host.internal')
    expect(detectMode()).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })
})

describe('VITE_CATALYST_MODE override', () => {
  it('forced sovereign mode returns the VITE_SOVEREIGN_FQDN', async () => {
    const { detectMode } = await loadDetectMode(
      'localhost',
      'sovereign',
      'otech23.omani.works',
    )
    expect(detectMode()).toEqual({
      mode: 'sovereign',
      sovereignFQDN: 'otech23.omani.works',
    })
  })

  it('forced sovereign mode without VITE_SOVEREIGN_FQDN returns null FQDN', async () => {
    const { detectMode } = await loadDetectMode('localhost', 'sovereign', undefined)
    expect(detectMode()).toEqual({ mode: 'sovereign', sovereignFQDN: null })
  })

  it('forced catalyst-zero mode overrides a sovereign hostname', async () => {
    const { detectMode } = await loadDetectMode(
      'console.otech23.omani.works',
      'catalyst-zero',
    )
    expect(detectMode()).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })
})

/**
 * #5895 — the per-Org console derives the WRONG Sovereign FQDN.
 *
 * `sovereignFQDNFromHostname` strips one leading `console.` segment and treats
 * whatever remains as the Sovereign. That is correct for the Sovereign console
 * and WRONG for a per-Organization console, because a per-Org console satisfies
 * the `console.*` pattern without being a Sovereign:
 *
 *   console.hw292.omani.works            -> hw292.omani.works            (Sovereign, correct)
 *   console.walk-stranger-two.omani.rest -> walk-stranger-two.omani.rest (an ORG, wrong)
 *
 * The derived value feeds `buildOIDCEndpoints()` (oidc.ts:56), which builds
 * `https://auth.${sovereignFQDN}/realms/sovereign`. Measured live on hw292,
 * same path and query on each host:
 *
 *   auth.hw292.omani.works             -> 400  (Keycloak present, rejecting a bogus redirect_uri)
 *   auth.walk-stranger-two.omani.rest  -> 404  (nothing there; host root and
 *                                               /.well-known/openid-configuration also 404)
 *
 * So the browser is navigated to a host that serves nothing and renders a blank
 * page. There is no per-Org IdP by design — gitops.go:307 builds the single
 * shared issuer at `auth.<SovereignFQDN>/realms/<realm>` (#4272, #4399).
 *
 * WHY `it.fails` RATHER THAN ASSERTING THE BUG: a test that asserts the wrong
 * value would lock the defect in and go green forever. `it.fails` passes only
 * while the expectation below is UNMET — so the day someone makes the per-Org
 * console resolve its real Sovereign, this test starts failing and tells them to
 * delete the wrapper. It is a tripwire, not a spec for the broken behaviour.
 */
describe('#5895 — per-Org console FQDN derivation', () => {
  it.fails(
    'SHOULD NOT treat a per-Org console hostname as a Sovereign FQDN',
    async () => {
      const { detectMode } = await loadDetectMode(
        'console.walk-stranger-two.omani.rest',
      )
      // The Org's own domain is NOT a Sovereign, and no IdP is served at
      // auth.<that>. Anything that makes this pass is a valid fix: injecting
      // the real FQDN, resolving it from the per-Org API, or declining to
      // claim sovereign mode for a host that cannot be verified.
      expect(detectMode().sovereignFQDN).not.toBe('walk-stranger-two.omani.rest')
    },
  )

  it('documents the Sovereign console, which is correct and must stay correct', async () => {
    const { detectMode } = await loadDetectMode('console.hw292.omani.works')
    expect(detectMode()).toEqual({
      mode: 'sovereign',
      sovereignFQDN: 'hw292.omani.works',
    })
  })
})
