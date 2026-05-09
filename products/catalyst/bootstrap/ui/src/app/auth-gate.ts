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
 * Sanitize a `next=` redirect target so it can NEVER point to an
 * external host (open-redirect class CWE-601).
 *
 * Rules (strictly reject anything ambiguous — paths only):
 *   - If the input is empty / non-string → return undefined.
 *   - Reject embedded NUL / control / whitespace characters.
 *   - Reject explicit URL schemes (`http:`, `https:`, `javascript:`,
 *     `data:`, `file:`, `vbscript:`, `mailto:`, etc).
 *   - Reject backslashes anywhere (some browsers normalize `\` to
 *     `/`, which would smuggle `\\evil.com` past a leading-slash
 *     check).
 *   - Reject anything that doesn't start with a single forward
 *     slash. In particular `//evil.com/foo` is rejected — the
 *     browser parses leading `//` as a protocol-relative authority
 *     reference (host = evil.com).
 *
 * Returns the sanitized path, or `undefined` if the input is unsafe
 * — callers should fall back to the default landing page (e.g.
 * `/dashboard` or `/wizard`).
 *
 * qa-loop iter-4 cluster `users-page-null-map-and-open-redirect`
 * (TC-009 / 2026-05-09 routing matrix). The contract: post-login
 * navigation MUST land on the same origin under all circumstances.
 */
export function sanitizeNextParam(raw: unknown): string | undefined {
  if (typeof raw !== 'string' || raw.length === 0) return undefined
  // Reject embedded NULs / control chars / any whitespace.
  if (/[\x00-\x1f\s]/.test(raw)) return undefined
  // Reject backslashes anywhere — some browsers normalize `\` to `/`
  // before parsing, so `/\evil.com` and `\\evil.com` end up host
  // references at runtime.
  if (raw.indexOf('\\') !== -1) return undefined
  // Reject schemes (http:, https:, javascript:, data:, file:, mailto:,
  // vbscript:, etc.). A scheme is `<alpha>[<alpha>|<digit>|+|.|-]*:`.
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(raw)) return undefined
  // Require single-leading-slash absolute path. Reject leading `//`
  // (protocol-relative authority) and anything that doesn't start
  // with `/` (relative paths or bare strings).
  if (!raw.startsWith('/') || raw.startsWith('//')) return undefined
  return raw
}

/**
 * Synchronous best-effort check that the operator has a session marker
 * cached in JS-readable storage.
 *
 * The catalyst_session cookie is HttpOnly so we cannot read it from JS;
 * instead we look at sessionStorage:
 *   - any oidc:* key (legacy PKCE flow tokens)
 *   - 'catalyst:authed' marker set by VerifyPinPage on successful PIN
 *     verify, /auth/handover route's beforeLoad, and SovereignConsoleLayout
 *     after a successful /whoami probe
 *
 * Returning `false` ONLY means "we don't have a fast cached marker" — it
 * does NOT mean the operator is unauthenticated. Iter-2 fix
 * (qa-loop iter-2 cluster `spa-route-guard-rejects-pin-session`):
 * callers that act on `false` MUST follow up with an async /whoami probe
 * via `probeWhoamiAndCacheMarker()` before redirecting to /login. The
 * catalyst_session cookie is HttpOnly + arrives via Set-Cookie on
 * /auth/pin/verify and /auth/handover — opening a new tab, refreshing
 * after sessionStorage cleared, or pasting a deep-link URL into a fresh
 * window all leave the cookie intact while losing the JS-side marker.
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

/**
 * Async authoritative check via GET /api/v1/whoami.
 *
 * Probes the catalyst-api with the HttpOnly `catalyst_session` cookie.
 * On 200 the cookie is valid — cache the `catalyst:authed=1` marker so
 * subsequent navigations short-circuit through `hasCatalystSession()`
 * without re-fetching, and return true. On 401 the cookie is missing
 * or expired — return false so the caller can redirect to /login. On
 * any other status (5xx, network error) return null so the caller can
 * decide whether to fail open (let the route render and let downstream
 * handlers surface the error) or redirect.
 *
 * Why this exists (qa-loop iter-2 cluster
 * `spa-route-guard-rejects-pin-session`): the synchronous gate that
 * read sessionStorage alone bounced operators with valid HttpOnly
 * cookies but no JS-side marker — a regression caught when the founder
 * could not deep-link into /dashboard from a fresh tab on omantel.biz
 * after a PIN-verify in a sibling tab. The async fallback keeps the
 * sync fast-path for the common case (just-verified) and adds an
 * authoritative check for the cookie-but-no-marker case.
 *
 * This is a pure helper: no React, no router. Callers (router.tsx
 * rootBeforeLoad) own the redirect decision.
 */
export async function probeWhoamiAndCacheMarker(
  apiBase: string,
): Promise<true | false | null> {
  if (typeof window === 'undefined' || typeof fetch === 'undefined') return null
  try {
    const res = await fetch(`${apiBase}/v1/whoami`, {
      method: 'GET',
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
    if (res.status === 200) {
      try {
        sessionStorage.setItem('catalyst:authed', '1')
      } catch {
        /* private browsing may throw */
      }
      return true
    }
    if (res.status === 401) return false
    return null
  } catch {
    return null
  }
}
