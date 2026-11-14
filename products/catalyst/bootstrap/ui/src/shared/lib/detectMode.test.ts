/**
 * Tests for detectMode.ts — the Catalyst operating mode, read once at
 * start from GET /api/v1/sovereign/self.
 *
 * DETECTED_MODE is a module-level singleton resolved by a top-level await,
 * so every test loads a fresh copy of the module with vi.resetModules()
 * after it has stubbed fetch and the window location.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'

/** What the stubbed fetch does: answer with a status and a body, or fail. */
type Answer = { status: number; body: unknown } | 'network-error' | 'not-json'

async function loadDetectMode(opts: {
  answer: Answer
  hostname?: string
  pathname?: string
  viteCatalystMode?: string
  viteSovereignFqdn?: string
}) {
  vi.resetModules()
  vi.doMock('@/shared/constants/env', () => ({
    VITE_CATALYST_MODE: opts.viteCatalystMode,
    VITE_SOVEREIGN_FQDN: opts.viteSovereignFqdn,
    APP_MODE: 'saas',
    IS_SAAS: true,
    IS_SELFHOSTED: false,
    APP_VERSION: 'dev',
  }))
  const hostname = opts.hostname ?? 'localhost'
  // shared/config/urls.ts reads the host and the path at module-init (BASE).
  Object.defineProperty(window, 'location', {
    value: { hostname, host: hostname, pathname: opts.pathname ?? '/', origin: `https://${hostname}` },
    writable: true,
    configurable: true,
  })
  const fetchMock = vi.fn<(url: string, init?: RequestInit) => Promise<unknown>>(async () => {
    const a = opts.answer
    if (a === 'network-error') throw new TypeError('Failed to fetch')
    if (a === 'not-json') {
      return { ok: true, status: 200, json: async () => { throw new SyntaxError('Unexpected token <') } }
    }
    return { ok: a.status < 300, status: a.status, json: async () => a.body }
  })
  vi.stubGlobal('fetch', fetchMock)
  const mod = await import('./detectMode')
  return { mod, fetchMock }
}

afterEach(() => {
  vi.resetModules()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

const SOVEREIGN_ANSWER = {
  status: 200,
  body: {
    deploymentId: 'dep-1',
    sovereignFQDN: 'otech23.omani.works',
    mode: 'sovereign',
    phase: 'Bootstrapping',
    consume: 'origin',
    custodyHolder: 'parent',
    release: { channel: 'stable', version: '' },
    kubernetesVersion: 'v1.31.4+k3s1',
  },
}

const MOTHERSHIP_ANSWER = {
  status: 404,
  body: { error: 'not-a-sovereign', mode: 'mothership', status: 404, code: '404' },
}

describe('DETECTED_MODE from GET /api/v1/sovereign/self', () => {
  it('the mothership answer (404 not-a-sovereign, mode mothership) gives catalyst-zero', async () => {
    const { mod } = await loadDetectMode({ answer: MOTHERSHIP_ANSWER })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('a Sovereign answer gives sovereign and the FQDN the API names', async () => {
    const { mod } = await loadDetectMode({ answer: SOVEREIGN_ANSWER })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'sovereign', sovereignFQDN: 'otech23.omani.works' })
  })

  it('the host name is not read: a Sovereign answer on console.openova.io is a Sovereign', async () => {
    const { mod } = await loadDetectMode({ answer: SOVEREIGN_ANSWER, hostname: 'console.openova.io' })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'sovereign', sovereignFQDN: 'otech23.omani.works' })
  })

  it('a per-Organization console gets the Sovereign FQDN, not its own apex (#5895)', async () => {
    const { mod } = await loadDetectMode({
      answer: { status: 200, body: { sovereignFQDN: 'hw292.omani.works', mode: 'sovereign' } },
      hostname: 'console.walk-stranger-two.omani.rest',
    })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'sovereign', sovereignFQDN: 'hw292.omani.works' })
  })

  it('an empty sovereignFQDN gives sovereign with a null FQDN', async () => {
    const { mod } = await loadDetectMode({
      answer: { status: 200, body: { sovereignFQDN: '   ', mode: 'sovereign' } },
    })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'sovereign', sovereignFQDN: null })
  })

  it('an answer without a mode gives catalyst-zero', async () => {
    const { mod } = await loadDetectMode({
      answer: { status: 200, body: { deploymentId: 'dep-1', sovereignFQDN: 'otech23.omani.works' } },
      hostname: 'console.otech23.omani.works',
    })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('a network error gives catalyst-zero', async () => {
    const { mod } = await loadDetectMode({ answer: 'network-error', hostname: 'console.otech23.omani.works' })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('a body that is not JSON gives catalyst-zero', async () => {
    const { mod } = await loadDetectMode({ answer: 'not-json' })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
  })

  it('the request is made once, under the tier base, with a time limit', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const { mod, fetchMock } = await loadDetectMode({ answer: SOVEREIGN_ANSWER })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/sovereign/self')
    expect(fetchMock.mock.calls[0][1]?.signal).toBeDefined()
    expect(timeout).toHaveBeenCalledWith(mod.SELF_TIMEOUT_MS)
    expect(mod.SELF_TIMEOUT_MS).toBe(5000)
  })

  it('on Catalyst-Zero the request goes under /sovereign/api', async () => {
    const { fetchMock } = await loadDetectMode({
      answer: MOTHERSHIP_ANSWER,
      hostname: 'console.openova.io',
      pathname: '/sovereign/wizard',
    })
    expect(fetchMock.mock.calls[0][0]).toBe('/sovereign/api/v1/sovereign/self')
  })

  it('the answer is not kept in browser storage', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    await loadDetectMode({ answer: SOVEREIGN_ANSWER })
    expect(setItem).not.toHaveBeenCalled()
  })
})

describe('VITE_CATALYST_MODE override', () => {
  it('forced sovereign mode returns the VITE_SOVEREIGN_FQDN and asks no API', async () => {
    const { mod, fetchMock } = await loadDetectMode({
      answer: MOTHERSHIP_ANSWER,
      viteCatalystMode: 'sovereign',
      viteSovereignFqdn: 'otech23.omani.works',
    })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'sovereign', sovereignFQDN: 'otech23.omani.works' })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('forced sovereign mode without VITE_SOVEREIGN_FQDN returns null FQDN', async () => {
    const { mod } = await loadDetectMode({ answer: MOTHERSHIP_ANSWER, viteCatalystMode: 'sovereign' })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'sovereign', sovereignFQDN: null })
  })

  it('forced catalyst-zero mode overrides a Sovereign answer and asks no API', async () => {
    const { mod, fetchMock } = await loadDetectMode({ answer: SOVEREIGN_ANSWER, viteCatalystMode: 'catalyst-zero' })
    expect(mod.DETECTED_MODE).toEqual({ mode: 'catalyst-zero', sovereignFQDN: null })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('modeFromSelf()', () => {
  it.each([
    ['null', null, { mode: 'catalyst-zero', sovereignFQDN: null }],
    ['a string', 'sovereign', { mode: 'catalyst-zero', sovereignFQDN: null }],
    ['an unknown mode', { mode: 'parent', sovereignFQDN: 'a.example' }, { mode: 'catalyst-zero', sovereignFQDN: null }],
    ['mothership with a name', { mode: 'mothership', sovereignFQDN: 'a.example' }, { mode: 'catalyst-zero', sovereignFQDN: null }],
    ['sovereign with a padded name', { mode: 'sovereign', sovereignFQDN: ' a.example ' }, { mode: 'sovereign', sovereignFQDN: 'a.example' }],
    ['sovereign without a name', { mode: 'sovereign' }, { mode: 'sovereign', sovereignFQDN: null }],
  ])('%s', async (_name, body, want) => {
    const { mod } = await loadDetectMode({ answer: MOTHERSHIP_ANSWER })
    expect(mod.modeFromSelf(body)).toEqual(want)
  })
})
