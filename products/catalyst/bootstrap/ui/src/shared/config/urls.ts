// Central URL configuration for the Catalyst UI.
// Never inline URLs elsewhere — import from here.
//
// Everything is derived from Vite's `base` config (see vite.config.ts).
// When the UI is served at https://console.openova.io/sovereign/, the
// Traefik ingress strips /sovereign before reaching this container's nginx,
// so fetch calls in components still need to be prefixed with /sovereign
// so the browser sends /sovereign/api/... from the /sovereign/ page.
//
// Issue #494 (2026-05-01): the antipattern this file prevents is any
// fetch / EventSource argument that begins with `/api/`. A bare
// `/api/...` path looks absolute, but when the UI is mounted under
// `/sovereign/` the browser still sends it as `https://console/.../api/...`
// — which is NOT routed by Traefik (only `/sovereign/api/...` is). Worse,
// a non-leading-slash path like `api/v1/...` resolves RELATIVE to the
// current location → `/sovereign/provision/<id>/jobs/api/v1/...`. Both
// shapes 404. The cure is the same: route every URL through API_BASE
// (or the apiUrl helper), which derives the correct prefix from Vite's
// BASE_URL at build time. A vitest regression guardrail in
// src/test/no-hardcoded-api.test.ts fails CI if the antipattern returns.

/** Build-time base path from Vite, normalized to always end with '/'. */
const _rawBase = import.meta.env.BASE_URL
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`

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
