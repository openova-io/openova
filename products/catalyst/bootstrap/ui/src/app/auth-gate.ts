/**
 * auth-gate.ts — pure helpers for the rootRoute auth gate (#1090
 * cluster A2). Extracted from router.tsx so the canonicalisation +
 * public-path matching logic can be unit-tested without booting the
 * router or React.
 */

/** Public paths that bypass the rootRoute auth gate. Prefix-matched. */
export const PUBLIC_PATH_PREFIXES = [
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
] as const

export function isPublicPath(canonical: string): boolean {
  if (canonical === '/') return false
  return PUBLIC_PATH_PREFIXES.some(
    (prefix) => canonical === prefix || canonical.startsWith(prefix + '/'),
  )
}

/**
 * Canonicalise a request pathname:
 *   - collapse duplicate slashes (//foo → /foo)
 *   - strip trailing slash (/dashboard/ → /dashboard, except '/')
 *   - lowercase the path (/Dashboard → /dashboard)
 */
export function canonicalisePath(pathname: string): string {
  let p = pathname.replace(/\/+/g, '/')
  if (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1)
  return p.toLowerCase()
}

/**
 * Synchronous best-effort check that the operator has a session.
 * The catalyst_session cookie is HttpOnly so we cannot read it from JS;
 * instead we look at sessionStorage:
 *   - any oidc:* key (legacy PKCE flow tokens)
 *   - 'catalyst:authed' marker set by SovereignConsoleLayout after a
 *     successful /whoami probe
 *
 * Conservative: false positives (claiming authed when no session) are
 * caught downstream by the layout's /whoami probe, which redirects to
 * /login with proper error context. False negatives would break
 * navigation, so we err on the side of "let it through."
 */
export function hasCatalystSession(): boolean {
  if (typeof window === 'undefined' || typeof sessionStorage === 'undefined') return true
  try {
    const ssKeys = Object.keys(sessionStorage)
    if (ssKeys.some((k) => k.startsWith('oidc:'))) return true
    if (sessionStorage.getItem('catalyst:authed') === '1') return true
  } catch {
    /* private browsing may throw */
  }
  return false
}
