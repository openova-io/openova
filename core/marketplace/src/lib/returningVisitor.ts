// returningVisitor — where a cookie-borne returning customer is sent
// from ANY marketplace page (#5940, UAT rows 3 + 91).
//
// ── What was measured, and what it means ─────────────────────────────
//
// UAT row 91 on hw292 2026-08-10: `redirectCount 0` from the marketplace
// root, `document.cookie` empty, localStorage holding only `org-cart` /
// `org-pending-voucher` — cart state, not a session. The returning-user
// redirect in `Layout.astro` bails on its very first line for this
// visitor: `var token = localStorage.getItem('org-token'); if (!token)
// return;`. A customer who authenticated on the CONSOLE origin has no
// marketplace-origin token, so the redirect could never fire for them.
//
// This module computes the destination for exactly that visitor, from
// the readable hint cookie (`./sessionHint`). It is the SPEC for the
// inline `hintedConsoleTarget()` in `../layouts/Layout.astro`; that
// script has to be `is:inline` (it runs before the bundle so a returning
// customer never sees the storefront flash), so it cannot import this.
// `layoutReturningVisitor.test.ts` executes the REAL inline script and
// asserts it agrees with this module on every case, which is what keeps
// the two from drifting.
//
// ── The destination, and the thing it must never be ──────────────────
//
// Row 91 has two halves. The positive half — "sends the customer to
// their own Org console" — is what this adds. The forbidden half —
// "never to `console.openova.io` / the mothership" — already passes and
// must KEEP passing, so this module has no mothership branch at all: an
// unrecognised host yields NO redirect rather than a fallback. That is
// asserted directly in the tests, not left to inspection.

import type { SessionHint } from './sessionHint';

/** Where the console lands a signed-in visitor. */
const CONSOLE_LANDING_PATH = '/dashboard';

export type ReturningVisitorReason =
  /** Demo/partner Organization that opts out of the console hand-off. */
  | 'opt-out'
  /** No readable hint — anonymous, or signed out. Render the storefront. */
  | 'no-session-hint'
  /**
   * A hint exists, but this host yields no console host we trust (dev
   * `localhost`, a preview host, an unrecognised shape). Deliberately NOT
   * a mothership fallback — see the module doc.
   */
  | 'unknown-console-host'
  /** The hint identified a console; `url` carries where to send them. */
  | 'session-hint';

export type ReturningVisitorDecision =
  | { redirect: false; reason: Exclude<ReturningVisitorReason, 'session-hint'> }
  | { redirect: true; reason: 'session-hint'; url: string };

export interface ReturningVisitorInput {
  /** `window.location.hostname`, e.g. `marketplace.t92.omani.works`. */
  host: string;
  /** `__ORG_TENANT__.skipConsoleRedirect` — the demo/partner opt-out. */
  skipConsoleRedirect: boolean;
  /** Parsed `catalyst_session_hint`, or null when the visitor has none. */
  hint: SessionHint | null;
  /**
   * `localStorage['org-active-console-host']` — the SERVER-authoritative
   * console host stamped at signup (#4176). It is the only source that
   * knows an Org's chosen POOL parent domain, which is frequently NOT the
   * marketplace's own domain, so it wins over any derivation.
   */
  stampedConsoleHost?: string;
}

/**
 * Resolve where a hint-bearing visitor should be sent.
 *
 * Precedence mirrors `deriveConsoleURL` in `./config.ts`:
 *   1. opt-out tenants never bounce, whatever the session says;
 *   2. no hint → no redirect (the pre-#5940 behaviour, unchanged);
 *   3. the stamped server-authoritative console host, when it names this
 *      Org (a stamp for a DIFFERENT Org must never misroute this one);
 *   4. `marketplace.<sov>` → `console.<slug>.<sov>`, or `console.<sov>`
 *      for a Sovereign-admin session that carries no slug;
 *   5. anything else → no redirect. Never a mothership fallback.
 */
export function resolveReturningVisitor(
  input: ReturningVisitorInput,
): ReturningVisitorDecision {
  if (input.skipConsoleRedirect) return { redirect: false, reason: 'opt-out' };
  if (!input.hint) return { redirect: false, reason: 'no-session-hint' };

  const host = (input.host || '').toLowerCase().trim();
  const org = (input.hint.org || '').toLowerCase().trim();
  const stamped = (input.stampedConsoleHost || '').toLowerCase().trim();

  if (stamped.startsWith('console.')) {
    // Trust the stamp only when it names THIS Org, or when the session
    // carries no Org at all (a Sovereign-admin session has no slug to
    // contradict it).
    if (!org || stamped.startsWith(`console.${org}.`)) {
      return { redirect: true, reason: 'session-hint', url: consoleURL(stamped) };
    }
  }

  if (host.startsWith('marketplace.')) {
    const sovFqdn = host.slice('marketplace.'.length);
    if (sovFqdn) {
      return {
        redirect: true,
        reason: 'session-hint',
        url: consoleURL(org ? `console.${org}.${sovFqdn}` : `console.${sovFqdn}`),
      };
    }
  }

  return { redirect: false, reason: 'unknown-console-host' };
}

/**
 * The console URL for a console HOST. Exported so the redeem page and
 * the Layout redirect compose the identical address — UAT row 3 asserts
 * the owner lands on `/dashboard`, and two call sites picking two
 * landing paths would make that row pass on one page and fail on the
 * other.
 */
export function consoleURL(consoleHost: string): string {
  return `https://${consoleHost}${CONSOLE_LANDING_PATH}`;
}
