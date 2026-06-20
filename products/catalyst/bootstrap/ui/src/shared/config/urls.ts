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
// /sovereign Traefik rule and hits the Organization console instead. Always use
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
 * '/sovereign' AND window.location.host is `console.openova.io` →
 * BASE = '/sovereign/'.
 * On Sovereign clusters: BASE = '/' regardless of pathname.
 *
 * G110 #2706 (2026-06-01): the path-only check was too permissive — an
 * operator who lands on `https://console.<sov-fqdn>/sovereign/login`
 * (stale bookmark, copy-paste, or wrong external link) would get
 * BASE = '/sovereign/' and every fetch POST would hit nginx HTTP 405
 * because the Sovereign's HTTPRoute doesn't strip a `/sovereign` prefix
 * the way contabo's Traefik does. Live evidence on hw86 2026-06-01T13:25Z:
 *
 *   POST /sovereign/api/v1/auth/pin/issue → HTTP 405 (nginx)
 *   POST /api/v1/auth/pin/issue → HTTP 200 (PIN sent)
 *
 * The host-aware check is the canonical fix: only Catalyst-Zero on
 * contabo serves the UI under `/sovereign/*` via Traefik strip-prefix,
 * and that's identified by the `console.openova.io` host. Any other
 * host (`console.<sov-fqdn>`) is a Sovereign cluster where BASE = '/'.
 *
 * Future host names that might surface for Catalyst-Zero (e.g. a
 * staging contabo `staging.openova.io`) can be added to the
 * CATALYST_ZERO_HOSTS list below — keep it explicit, never a regex
 * (per docs/INVIOLABLE-PRINCIPLES.md #4 against silent regex drift).
 */
const CATALYST_ZERO_HOSTNAMES: ReadonlyArray<string> = [
  'console.openova.io',
]

/**
 * Is the current URL a Catalyst-Zero (contabo) URL — i.e. hostname AND
 * path both match the contabo `console.openova.io/sovereign/*` pattern?
 *
 * G110-followup #2706 (2026-06-01): exported helper so router.tsx,
 * SovereignConsoleLayout.tsx, AuthCallbackPage.tsx, VerifyPinPage.tsx
 * and any future call-site keys off ONE topology test, not 5 ad-hoc
 * path-prefix checks. Each of those 5 sites previously did
 * `window.location.pathname.startsWith('/sovereign')` directly —
 * Reviewer-agent on PR #2709 flagged the inconsistency.
 *
 * Distinct from any pre-existing local `isCatalystZero` consts that
 * check the HOSTNAME ONLY (used to gate wizardAuthGuard / OIDC-callback
 * eligibility): this helper requires BOTH the contabo hostname AND the
 * `/sovereign` path prefix. Use `isCatalystZeroURL()` for routing /
 * basepath decisions (where the path matters) and the local
 * `isCatalystZero` consts for auth-mode decisions (where only the
 * hostname matters).
 *
 * Uses `window.location.hostname` (not `host`) so non-default-port
 * deployments (e.g. dev contabo on `:8443`) still match correctly.
 */
export const isCatalystZeroURL = (): boolean => {
  if (typeof window === 'undefined') return false
  const isContaboHost = CATALYST_ZERO_HOSTNAMES.includes(
    window.location.hostname,
  )
  const hasSovereignPrefix = window.location.pathname.startsWith('/sovereign')
  return isContaboHost && hasSovereignPrefix
}

export const BASE: string = (() => {
  if (typeof window !== 'undefined') {
    // G110: require BOTH the path prefix AND the contabo host before
    // claiming Catalyst-Zero. Path-only check was the root-cause for
    // the hw86 `/sovereign/login` → HTTP 405 chain.
    if (isCatalystZeroURL()) {
      return '/sovereign/'
    }
    // Sovereign cluster (or any non-contabo host) — BASE = '/'.
    return '/'
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
