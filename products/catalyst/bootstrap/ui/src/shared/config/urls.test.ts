/**
 * Tests for the central URL helpers (issue #494).
 *
 * The fix for #494 introduces `apiUrl()` to normalise paths that may
 * arrive from the server as a tier-naive `/api/v1/...` literal (e.g.
 * the `streamURL` returned by POST /api/v1/deployments). The helper
 * has to be idempotent — `apiUrl(apiUrl(x)) === apiUrl(x)` — and pass
 * absolute URLs through untouched so callers can opt in to cross-origin.
 */

import { describe, it, expect } from 'vitest'
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
})
