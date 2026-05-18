/**
 * mothershipTokenRedirect — Mothership UI bootstrap hook that intercepts
 * a handover JWT carried as `?token=<JWT>` on any Mothership URL and
 * redirects the browser to the Sovereign chroot's `/auth/handover` route.
 *
 * # Why this exists (issue #1773, DoD gate D0)
 *
 * The mothership exposes POST /api/v1/deployments/{id}/mint-handover-token
 * which returns:
 *
 *   {
 *     "token":       "<JWT>",
 *     "redirectURL": "https://console.<sovereignFqdn>/auth/handover?token=<JWT>"
 *   }
 *
 * The wizard's success page links the operator to that `redirectURL`. But
 * when the operator opens the link in a fresh tab WHILE STILL POINTING
 * AT the mothership (e.g. share-link, browser history, or any flow that
 * pastes `?token=` onto a `console.openova.io/sovereign/*` URL), the
 * mothership UI has historically just rendered its own page and stranded
 * the operator on the Mothership Jobs / Apps view. Memory file
 * `feedback_handover_redirect_is_critical_d0.md` calls this the #1 pinned
 * gate above all other DoD verifications:
 *
 *   "Without auto-redirect to Sovereign Console after handoverFiredAt,
 *    every other gate is invisible to the operator."
 *
 * # Contract
 *
 * On Mothership (console.openova.io) ONLY, examine `window.location.search`
 * for a `token` query param.
 *
 *   1. If absent → no-op.
 *   2. If present but malformed (not 3 dot-separated base64url parts, or
 *      payload fails to JSON-parse) → no-op (leave the page to render).
 *   3. If valid and the `aud` claim looks like a Sovereign chroot console
 *      (string `https://console.<host>` OR an array containing one), then
 *      construct `https://console.<host>/auth/handover?token=<JWT>` and
 *      `window.location.replace()` it. The replace (not assign) means the
 *      back button doesn't bounce the operator straight back into the
 *      same redirect loop.
 *   4. If the `aud` claim is missing OR doesn't match the chroot console
 *      pattern → no-op (the token belongs to something else; let the page
 *      render normally).
 *
 * This module is purely Mothership-side; on Sovereign hosts (basepath '/',
 * hostname starts with `console.<sov-fqdn>` ≠ `console.openova.io`) the
 * /auth/handover route in router.tsx owns the JWT — we don't want to
 * double-redirect or interfere with it. The `isMothershipHost` check in
 * `runMothershipTokenRedirect()` is the gate.
 *
 * # Why this lives separately from the existing HandoverRedirectBanner
 *
 * HandoverRedirectBanner (pages/sovereign/HandoverRedirectBanner.tsx)
 * fires AFTER the user has logged into the Mothership and the deployment
 * record's `handoverFiredAt` propagates via SSE — that's the "happy path"
 * for an operator watching their own provisioning. This module covers
 * the orthogonal case where the operator arrives with a raw `?token=`
 * URL (e.g. clicking a wizard SuccessPage's "Open your Sovereign console"
 * link in a fresh tab, or following a paste-back from email/Slack). The
 * two paths are independent; both are needed.
 */

import { parseJWTClaims } from '@/shared/lib/oidc'

/** The one hardcoded Mothership hostname. Mirrors detectMode.ts. */
const MOTHERSHIP_HOSTNAME = 'console.openova.io'

/**
 * Extract the canonical Sovereign chroot console URL from the JWT's
 * `aud` claim. The handover JWT contract (catalyst-api
 * handover_jwt.go) sets aud to either:
 *
 *   - a single string  "https://console.<sovereignFqdn>"
 *   - or an array      ["https://console.<sovereignFqdn>"]
 *
 * We accept both shapes. Anything else (missing, wrong scheme, host
 * not starting with `console.`, host equal to the Mothership itself)
 * returns null so the caller falls through to a no-op render.
 */
export function extractChrootConsoleURL(aud: unknown): string | null {
  let candidate: string | null = null
  if (typeof aud === 'string') {
    candidate = aud
  } else if (Array.isArray(aud)) {
    // Find the first string entry that smells like a console URL.
    for (const a of aud) {
      if (typeof a === 'string') {
        candidate = a
        break
      }
    }
  }
  if (!candidate) return null

  // Must be an absolute https URL whose host starts with "console." —
  // anything else is not a Sovereign chroot console.
  let parsed: URL
  try {
    parsed = new URL(candidate)
  } catch {
    return null
  }
  if (parsed.protocol !== 'https:') return null
  if (!parsed.hostname.startsWith('console.')) return null
  // Never self-redirect — if aud points to the Mothership we'd loop.
  if (parsed.hostname === MOTHERSHIP_HOSTNAME) return null
  // Strip path/query/hash — we only want the origin.
  return `${parsed.protocol}//${parsed.host}`
}

/**
 * Pure decision function — given the raw query string and a hostname,
 * return the URL to redirect to, or null to skip. Extracted from
 * `runMothershipTokenRedirect` so it can be unit-tested without
 * mutating jsdom's window.location.
 */
export function decideMothershipTokenRedirect(params: {
  hostname: string
  search: string
}): string | null {
  if (params.hostname !== MOTHERSHIP_HOSTNAME) return null
  let qs: URLSearchParams
  try {
    qs = new URLSearchParams(params.search)
  } catch {
    return null
  }
  const token = qs.get('token')
  if (!token) return null
  const claims = parseJWTClaims(token)
  // parseJWTClaims returns {} on parse failure — defensively check
  // that we got at least one well-formed key. The handover JWT
  // always carries aud + exp + sovereign_fqdn; if none are present
  // the token is malformed and we should not redirect.
  if (
    typeof claims !== 'object' ||
    claims === null ||
    Object.keys(claims).length === 0
  ) {
    return null
  }
  const chrootOrigin = extractChrootConsoleURL(claims.aud)
  if (!chrootOrigin) return null
  // Forward the raw JWT verbatim — the Sovereign-side
  // /auth/handover handler (Agent C / catalyst-api) does full RS256
  // signature verification, exp check, and aud-binding before
  // accepting the session. We are purely a transport.
  return `${chrootOrigin}/auth/handover?token=${encodeURIComponent(token)}`
}

/**
 * Bootstrap-time side-effecting hook — call ONCE from main.tsx before
 * any router state is initialised. If the current URL carries a
 * handover JWT, we hard-replace the browser to the Sovereign chroot.
 *
 * Returns true if a redirect was kicked off (caller can short-circuit
 * the rest of bootstrap if desired). Returns false on no-op.
 *
 * Safe to call in SSR / non-browser contexts — short-circuits to false.
 */
export function runMothershipTokenRedirect(): boolean {
  if (typeof window === 'undefined') return false
  const target = decideMothershipTokenRedirect({
    hostname: window.location.hostname,
    search: window.location.search,
  })
  if (!target) return false
  // `replace` (not assign) — the original ?token= URL on the
  // Mothership is not a useful history entry; bouncing the back
  // button onto it would just re-fire the redirect.
  window.location.replace(target)
  return true
}
