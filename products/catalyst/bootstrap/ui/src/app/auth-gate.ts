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
 * Reserved route words that can NEVER be a deployment id (#4704 Task B).
 *
 * These are the child-page segments of the `/provision/$deploymentId/*`
 * tree (plus sibling top-level route words). When a per-deployment link
 * is built with an EMPTY deployment id (`/provision/${''}/jobs`),
 * `canonicalisePath` collapses the double slash and the literal child
 * word lands in the `$deploymentId` slot (`/provision/jobs`): AppsPage
 * then banners `The path segment "jobs" is not a valid deployment id
 * (expected 16 lowercase hex characters; got 4)` and useDeploymentEvents
 * fires a doomed `/api/v1/deployments/jobs` poll (HTTP 404).
 *
 * A real deployment id is 16 lowercase hex chars (catalyst-api `newID()`),
 * so none of these words can collide with a genuine id. `legacy` is NOT
 * in the set — `/provision/legacy/$deploymentId` is a real route.
 */
export const RESERVED_DEPLOYMENT_ID_SEGMENTS: ReadonlySet<string> = new Set([
  'jobs',
  'dashboard',
  'cloud',
  'catalog',
  'app',
  'apps',
  'users',
  'settings',
  'sessions',
  'infrastructure',
  'rbac',
  'blueprints',
  'organizations',
  'notifications',
  'install',
  'timeline',
  'deployments',
  'wizard',
])

/**
 * Return the reserved route word occupying the `$deploymentId` slot of a
 * canonicalised `/provision/<segment>[/...]` path, or null when the slot
 * holds anything else (a real id, a malformed id — which keeps the honest
 * AppsPage error banner — or a non-provision path).
 */
export function reservedProvisionIdSegment(canonical: string): string | null {
  const seg = canonical.match(/^\/provision\/([^/]+)/)?.[1]
  return seg && RESERVED_DEPLOYMENT_ID_SEGMENTS.has(seg) ? seg : null
}

/**
 * Derive the set of trusted same-Sovereign hostnames from a host that
 * the UI is currently served on (e.g. `window.location.host`).
 *
 * A Sovereign exposes three first-party control-plane hosts that all
 * share the same parent FQDN: `console.<fqdn>` (this UI), `auth.<fqdn>`
 * (Keycloak), and `api.<fqdn>` (catalyst-api). They are derived by
 * stripping the first DNS label off the current host and re-prefixing
 * each canonical label. So from `console.t42.omani.works` we derive
 * `{console,auth,api}.t42.omani.works`.
 *
 * The host may carry a `:port` suffix (dev / non-default-port
 * deployments). We split it off, derive the family from the bare
 * hostname, and re-append the same port to every family member so a
 * `https://api.<fqdn>:8443/...` next survives on a non-default port.
 *
 * Returns an empty array when the current host has no parent label to
 * strip (bare hostname / IP / `localhost`) — in that degenerate case
 * NO absolute host is trusted and `sanitizeNextParam` falls back to
 * paths-only, which is the safe default.
 */
export const CANONICAL_SOVEREIGN_SUBDOMAINS = ['console', 'auth', 'api'] as const

export function sameSovereignHostFamily(currentHost: string): string[] {
  if (!currentHost) return []
  const colon = currentHost.indexOf(':')
  const hostname = colon === -1 ? currentHost : currentHost.slice(0, colon)
  const port = colon === -1 ? '' : currentHost.slice(colon)
  const firstDot = hostname.indexOf('.')
  // No parent label to strip (bare host / IP / localhost) → trust nothing.
  if (firstDot <= 0 || firstDot === hostname.length - 1) return []
  const parentFqdn = hostname.slice(firstDot + 1)
  // Guard against a parent that is itself a bare label with no dot
  // (e.g. host `console.local` → parent `local`). A real Sovereign FQDN
  // is multi-label; refuse to trust a single-label parent.
  if (!parentFqdn.includes('.')) return []
  return CANONICAL_SOVEREIGN_SUBDOMAINS.map((label) => `${label}.${parentFqdn}${port}`)
}

/**
 * Sanitize a `next=` redirect target so it can NEVER point to an
 * UNTRUSTED external host (open-redirect class CWE-601).
 *
 * Rules:
 *   - If the input is empty / non-string → return undefined.
 *   - Reject embedded NUL / control / whitespace characters.
 *   - Reject backslashes anywhere (some browsers normalize `\` to
 *     `/`, which would smuggle `\\evil.com` past a leading-slash
 *     check).
 *   - Same-origin relative paths (`/dashboard`, `/provision/...`) pass
 *     through unchanged, exactly as before. Leading `//` (protocol-
 *     relative authority) is still rejected.
 *   - Absolute `http(s)://` URLs are accepted ONLY when their host is
 *     in the same-Sovereign trusted family (`console.<fqdn>` /
 *     `auth.<fqdn>` / `api.<fqdn>`) derived from the current origin.
 *     This is required for the cross-host OAuth `next` continuation
 *     after PIN-verify: KC's identity broker bounces the operator to
 *     `/login?next=https://api.<fqdn>/oidc/auth?...` (#3271). Without
 *     this allowlist, that legitimate same-Sovereign continuation was
 *     dropped and every app SSO landed the user on the console
 *     `/dashboard` instead of the target app.
 *   - Every OTHER absolute host is rejected — `https://evil.com`,
 *     scheme-relative `//evil.com`, and crucially look-alike suffix
 *     attacks like `https://api.<fqdn>.evil.com/` (the registrable
 *     host is `evil.com`, NOT the Sovereign FQDN).
 *   - Non-`http(s)` schemes (`javascript:`, `data:`, `file:`,
 *     `vbscript:`, `mailto:`) are rejected outright.
 *
 * Returns the sanitized target (relative path or trusted absolute
 * URL), or `undefined` if the input is unsafe — callers fall back to
 * the default landing page (e.g. `/dashboard` or `/wizard`).
 *
 * The current host is read from `window.location.host` by default but
 * may be passed explicitly so the function stays pure + unit-testable
 * without a DOM.
 *
 * qa-loop iter-4 cluster `users-page-null-map-and-open-redirect`
 * (TC-009 / 2026-05-09 routing matrix) + #3271 cross-host SSO
 * continuation. The contract: post-login navigation MUST land on the
 * same Sovereign under all circumstances — never off it.
 */
export function sanitizeNextParam(
  raw: unknown,
  currentHost: string | undefined = typeof window !== 'undefined'
    ? window.location.host
    : undefined,
): string | undefined {
  if (typeof raw !== 'string' || raw.length === 0) return undefined
  // Reject embedded NULs / control chars / any whitespace. The control
  // range is intentional (we are screening for exactly those bytes).
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\s]/.test(raw)) return undefined
  // Reject backslashes anywhere — some browsers normalize `\` to `/`
  // before parsing, so `/\evil.com` and `\\evil.com` end up host
  // references at runtime.
  if (raw.indexOf('\\') !== -1) return undefined

  // Absolute http(s) URL → allow ONLY if the host is in the trusted
  // same-Sovereign family. Any other absolute host is an open-redirect.
  if (/^https?:\/\//i.test(raw)) {
    let parsed: URL
    try {
      parsed = new URL(raw)
    } catch {
      return undefined
    }
    const family = sameSovereignHostFamily(currentHost ?? '')
    // `parsed.host` includes the port if present — `family` members
    // carry the same port suffix, so this comparison is port-exact and
    // rejects look-alike suffix hosts (api.<fqdn>.evil.com → host is
    // api.<fqdn>.evil.com which is not a family member).
    if (family.includes(parsed.host)) return raw
    return undefined
  }
  // Reject any other scheme (javascript:, data:, file:, mailto:,
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
    if (res.status === 401) {
      // #5460 — the authority's NEGATIVE verdict must revoke the cached
      // marker, symmetrically with the positive one writing it. A stale
      // `catalyst:authed=1` surviving session expiry short-circuits the
      // rootBeforeLoad gate (`hasCatalystSession()`), which skips the
      // whoami probe AND the #3374 row-29 silent-SSO leg — the operator
      // lands on the PIN wall even with a live Keycloak session, and
      // every surface consulting the marker renders authenticated chrome
      // against a dead session (hw290 row-29 walk, 2026-07-29).
      try {
        sessionStorage.removeItem('catalyst:authed')
      } catch {
        /* private browsing may throw */
      }
      return false
    }
    return null
  } catch {
    return null
  }
}
