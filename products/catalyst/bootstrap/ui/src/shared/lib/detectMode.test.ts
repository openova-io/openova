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
