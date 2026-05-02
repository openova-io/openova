// Central URL configuration for the Catalyst UI.
// Never inline URLs elsewhere — import from here.
//
// BASE is determined at module-init time from window.location (runtime),
// NOT from Vite's BASE_URL (build-time). The reason: the same catalyst-ui
// image is deployed in two topologies that share Vite base '/':
//
//   1. Sovereign clusters — served at console.<sov-fqdn>/ (BASE = '/')
//   2. Catalyst-Zero on contabo — served at console.openova.io/sovereign/*
//      with a Traefik strip-prefix middleware. The browser URL keeps the
//      /sovereign prefix. Traefik routes /sovereign/* to catalyst-ui, which
//      in turn proxies /api/* to catalyst-api. So browser fetch calls must
//      go to /sovereign/api/... (BASE = '/sovereign/').
//
// Issue #494 (2026-05-01): the antipattern this file prevents is any
// fetch / EventSource argument that begins with a bare `/api/`. A bare
// `/api/...` path looks absolute, but on contabo-mkt it bypasses the
// /sovereign Traefik rule and hits the SME console instead. Always use
// `API_BASE` (or `apiUrl()`) so the prefix is resolved once, here, at
// module-init. A vitest regression guardrail in
// src/test/no-hardcoded-api.test.ts fails CI if the antipattern returns.
//
// Issue #618 (2026-05-02): switched from build-time BASE_URL to runtime
// window.location detection. Sovereign clusters retain basepath '/' so
// their behavior is unchanged; contabo-mkt now correctly uses /sovereign/.

/**
 * Runtime base path, normalized to always end with '/'.
 *
 * On contabo-mkt (Catalyst-Zero): window.location.pathname starts with
 * '/sovereign' → BASE = '/sovereign/'.
 * On Sovereign clusters: BASE = '/'.
 */
export const BASE: string = (() => {
  if (typeof window !== 'undefined' && window.location.pathname.startsWith('/sovereign')) {
    return '/sovereign/'
  }
  // Build-time fallback (SSR / jsdom / unit tests without window).
  const _rawBase = import.meta.env.BASE_URL
  return _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`
})()

/** API root, scoped under the tier base so Nova + Sovereign don't collide. */
export const API_BASE: string = `${BASE}api`

/** Prepend base path to an in-tier route. Strips leading '/' from input. */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`

/**
 * Build a tier-correct API URL from a path that may or may not begin
 * with `/api`. Use this for any URL that might come back from the
 * server (e.g. `streamURL` in a deployment-create response, where the
 * backend emits `/api/v1/...`) so the same code works regardless of
 * whether the consumer mounted under `/sovereign/` or root.
 *
 *   apiUrl('/api/v1/foo')      → `${BASE}api/v1/foo`
 *   apiUrl('/v1/foo')          → `${BASE}api/v1/foo`   (treat as already-API-rooted)
 *   apiUrl('v1/foo')           → `${BASE}api/v1/foo`
 *   apiUrl('https://x/api/y')  → `https://x/api/y`     (absolute → pass through)
 *
 * Inputs that are already absolute (http/https) pass through unchanged
 * so callers can opt in to cross-origin behaviour explicitly.
 */
export function apiUrl(input: string): string {
  if (/^https?:\/\//i.test(input)) return input
  // Trim a leading `/api` (with or without trailing slash) so the
  // result is always exactly `${API_BASE}/<rest>`. This makes the
  // helper idempotent: apiUrl(apiUrl(x)) === apiUrl(x).
  let rest = input
  if (rest.startsWith('/api/')) rest = rest.slice('/api'.length) // keep leading '/'
  else if (rest === '/api') rest = ''
  else if (rest.startsWith('api/')) rest = '/' + rest.slice('api/'.length)
  // Make sure rest starts with '/' for clean concatenation. An empty
  // rest collapses to API_BASE root.
  if (rest && !rest.startsWith('/')) rest = '/' + rest
  return `${API_BASE}${rest}`
}
