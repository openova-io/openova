/**
 * Tests for the central URL helpers (issue #494).
 *
 * The fix for #494 introduces `apiUrl()` to normalise paths that may
 * arrive from the server as a tier-naive `/api/v1/...` literal (e.g.
 * the `streamURL` returned by POST /api/v1/deployments). The helper
 * has to be idempotent — `apiUrl(apiUrl(x)) === apiUrl(x)` — and pass
 * absolute URLs through untouched so callers can opt in to cross-origin.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { API_BASE, apiUrl, BASE, path } from './urls'

describe('shared/config/urls', () => {
  describe('BASE / API_BASE', () => {
    it('BASE always ends with /', () => {
      expect(BASE.endsWith('/')).toBe(true)
    })

    it('API_BASE is `${BASE}api`', () => {
      expect(API_BASE).toBe(`${BASE}api`)
    })
  })

  describe('path()', () => {
    it('strips a leading / from the input', () => {
      expect(path('/dashboard')).toBe(`${BASE}dashboard`)
    })

    it('accepts inputs without a leading /', () => {
      expect(path('dashboard')).toBe(`${BASE}dashboard`)
    })
  })

  describe('apiUrl()', () => {
    it('strips a /api/ prefix from a tier-naive server response', () => {
      // Mirror catalyst-api/internal/handler/deployments.go which emits
      // `/api/v1/deployments/<id>/logs`. The helper must re-root it
      // under the active tier so the browser sends the strip-sovereign
      // path when mounted at /sovereign/.
      expect(apiUrl('/api/v1/deployments/abc/logs')).toBe(
        `${API_BASE}/v1/deployments/abc/logs`,
      )
    })

    it('treats /v1/... as already-API-rooted', () => {
      expect(apiUrl('/v1/deployments')).toBe(`${API_BASE}/v1/deployments`)
    })

    it('handles input without a leading /', () => {
      expect(apiUrl('v1/deployments')).toBe(`${API_BASE}/v1/deployments`)
    })

    it('handles api/ without leading slash', () => {
      expect(apiUrl('api/v1/foo')).toBe(`${API_BASE}/v1/foo`)
    })

    it('passes absolute http(s) URLs through unchanged', () => {
      expect(apiUrl('https://example.com/api/v1/foo')).toBe(
        'https://example.com/api/v1/foo',
      )
      expect(apiUrl('http://localhost:8080/api/v1/foo')).toBe(
        'http://localhost:8080/api/v1/foo',
      )
    })

    it('is idempotent — apiUrl(apiUrl(x)) === apiUrl(x)', () => {
      const inputs = [
        '/api/v1/deployments/abc/logs',
        '/v1/deployments',
        'v1/deployments',
        'https://example.com/api/v1/foo',
      ]
      for (const x of inputs) {
        expect(apiUrl(apiUrl(x))).toBe(apiUrl(x))
      }
    })
  })

  /**
   * G110 #2706 (2026-06-01) — host-aware topology detection regression
   * guards. The path-only check in the prior implementation caused an
   * operator on `console.<sov-fqdn>/sovereign/login` to get BASE =
   * '/sovereign/' and every fetch POST returned nginx HTTP 405. The new
   * implementation requires BOTH the path prefix AND the contabo host.
   * These tests pin the per-input → BASE mapping by re-importing the
   * module under different window.location stubs.
   */
  describe('BASE — G110 host-aware topology detection (#2706)', () => {
    // BASE is evaluated at module-load time; reset module cache BEFORE
    // each test so the dynamic import re-evaluates the IIFE with the
    // current window stub. Without resetModules in beforeEach, the
    // first test sees the module-load BASE (captured by the static
    // `import` at the top of this file) and silently fails.
    beforeEach(() => {
      vi.resetModules()
    })
    afterEach(() => {
      vi.unstubAllGlobals()
      vi.resetModules()
    })

    const cases: Array<{ host: string; pathname: string; expected: string }> = [
      // Catalyst-Zero on contabo — both signals match → /sovereign/
      { host: 'console.openova.io', pathname: '/sovereign/login', expected: '/sovereign/' },
      { host: 'console.openova.io', pathname: '/sovereign/deployments', expected: '/sovereign/' },

      // Catalyst-Zero on contabo — operator lands on root path → '/'
      { host: 'console.openova.io', pathname: '/', expected: '/' },

      // Sovereign cluster — operator on canonical path → '/'
      { host: 'console.hw86.omani.works', pathname: '/login', expected: '/' },
      { host: 'console.t38.omani.works', pathname: '/', expected: '/' },

      // Sovereign cluster — operator on stale-bookmark /sovereign path.
      // Pre-G110 this returned '/sovereign/' and broke all API POSTs.
      // Post-G110: '/'.
      { host: 'console.hw86.omani.works', pathname: '/sovereign/login', expected: '/' },
      { host: 'console.t38.omani.works', pathname: '/sovereign/deployments', expected: '/' },
    ]

    for (const { host, pathname, expected } of cases) {
      it(`host=${host} pathname=${pathname} → BASE=${expected}`, async () => {
        vi.stubGlobal('window', {
          // G110-followup #2706: urls.ts now uses hostname, not host —
          // expose both so existing tests still match (host carries the
          // port; hostname is portless).
          location: { host, hostname: host, pathname },
        })
        const mod = await import('./urls')
        expect(mod.BASE).toBe(expected)
      })
    }
  })

  /**
   * G110-followup #2706 — direct isCatalystZero() helper tests.
   *
   * Reviewer-agent flagged 5 other call-sites doing pathname.startsWith('/sovereign')
   * directly. PR #2709-followup extracts isCatalystZero() and routes them
   * through it. These tests pin the helper's contract so future use-sites
   * cannot regress.
   *
   * Also covers the non-default-port edge case the reviewer flagged
   * (host includes `:port` when non-default, hostname is portless).
   */
  describe('isCatalystZero() — G110-followup helper (#2706)', () => {
    beforeEach(() => {
      vi.resetModules()
    })
    afterEach(() => {
      vi.unstubAllGlobals()
      vi.resetModules()
    })

    const cases: Array<{
      hostname: string
      pathname: string
      expected: boolean
      note: string
    }> = [
      // Catalyst-Zero on contabo
      { hostname: 'console.openova.io', pathname: '/sovereign/login', expected: true, note: 'contabo + /sovereign' },
      { hostname: 'console.openova.io', pathname: '/sovereign', expected: true, note: 'contabo + /sovereign root' },
      { hostname: 'console.openova.io', pathname: '/', expected: false, note: 'contabo + root' },
      { hostname: 'console.openova.io', pathname: '/login', expected: false, note: 'contabo + /login (no /sovereign)' },
      // Sovereign clusters (any non-contabo hostname)
      { hostname: 'console.hw86.omani.works', pathname: '/sovereign/login', expected: false, note: 'sovereign + stale-bookmark /sovereign' },
      { hostname: 'console.hw86.omani.works', pathname: '/login', expected: false, note: 'sovereign + canonical /login' },
      { hostname: 'console.t38.omani.works', pathname: '/sovereign', expected: false, note: 'another Sovereign + /sovereign' },
      // Non-default-port contabo (reviewer-flagged edge case)
      // urls.ts now uses hostname so the port is NOT in the comparison —
      // this case correctly identifies as Catalyst-Zero even if served
      // on `:8443` (dev / staging contabo).
      { hostname: 'console.openova.io', pathname: '/sovereign/login', expected: true, note: 'contabo non-default-port (hostname portless)' },
    ]

    for (const { hostname, pathname, expected, note } of cases) {
      it(`${note}: hostname=${hostname} pathname=${pathname} → ${expected}`, async () => {
        vi.stubGlobal('window', {
          location: { hostname, pathname },
        })
        const mod = await import('./urls')
        expect(mod.isCatalystZeroURL()).toBe(expected)
      })
    }

    it('returns false when window is undefined (SSR / build-time)', async () => {
      vi.stubGlobal('window', undefined)
      const mod = await import('./urls')
      expect(mod.isCatalystZeroURL()).toBe(false)
    })
  })
})
