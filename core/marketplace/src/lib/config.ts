// Central URL configuration for the marketplace app.
// Never inline URLs elsewhere — import from here.
// See ~/.claude/projects/.../memory/feedback_never_hardcode_urls.md

/** Build-time base path, normalized to always end with '/'. */
const _rawBase = import.meta.env.BASE_URL;
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`;

/** API root (served at marketplace.<host>/api/). */
export const API_BASE: string = `${BASE}api`;

/**
 * Mothership console URL — used when the marketplace runs on
 * `marketplace.openova.io` (or in SSR / non-browser contexts).
 *
 * Mothership ingress strips a `/nova` path prefix before forwarding to the
 * console service (see products/catalyst/chart/templates/sme-services/
 * ingress.yaml — `strip-nova` middleware), so the customer console lives at
 * `https://console.openova.io/nova/...`.
 */
const MOTHERSHIP_CONSOLE_URL = 'https://console.openova.io/nova';

/**
 * Derive the customer console URL from the current marketplace host.
 *
 * Bug fix (2026-05-18): post-purchase redirect was always sending the user
 * to `console.openova.io/nova` even when they signed up on a Sovereign's
 * `marketplace.<sov-fqdn>` host. That bounced them back to the mothership
 * and re-prompted sign-in. The Sovereign console is at
 * `console.<sov-fqdn>` (Cilium Gateway `*.<sov-fqdn>` wildcard route in
 * `marketplace-routes.yaml`) — NO `/nova` prefix because the Sovereign
 * ingress doesn't have the `strip-nova` middleware.
 *
 * Rules:
 *   - SSR / no `window`              → mothership URL (safe fallback for
 *                                       static page render)
 *   - host === 'marketplace.openova.io' → mothership URL (preserves
 *                                       existing behaviour, /nova prefix)
 *   - host starts with `marketplace.`   → `https://console.<rest-of-host>`
 *                                       (Sovereign — strip `marketplace.`,
 *                                       prepend `console.`, NO /nova)
 *   - anything else (partner-branded
 *     vanity host e.g. `omantel.openova.io`,
 *     dev `localhost:4321`)             → mothership URL fallback
 */
function deriveConsoleURL(): string {
  if (typeof window === 'undefined') return MOTHERSHIP_CONSOLE_URL;
  const host = (window.location.hostname || '').toLowerCase();
  if (!host) return MOTHERSHIP_CONSOLE_URL;
  // Mothership marketplace keeps the canonical /nova prefix.
  if (host === 'marketplace.openova.io') return MOTHERSHIP_CONSOLE_URL;
  // Sovereign pattern: marketplace.<sov-fqdn> → console.<sov-fqdn>
  if (host.startsWith('marketplace.')) {
    const sovFqdn = host.slice('marketplace.'.length);
    if (sovFqdn) return `https://console.${sovFqdn}`;
  }
  // Partner-branded vanity hosts (omantel.openova.io) and dev/preview hosts
  // fall back to mothership. Demo tenants set skipConsoleRedirect anyway, so
  // this only matters when an authenticated user clicks an explicit
  // "Go to Console" link there — sending them to mothership is the
  // least-bad option (better than constructing a bogus `console.omantel.openova.io`).
  return MOTHERSHIP_CONSOLE_URL;
}

/** Post-auth Nova customer console. All references to the customer dashboard
 *  go through here so the marketplace never hardcodes a cross-host URL. */
export const CONSOLE_URL: string = deriveConsoleURL();

/** Build a URL into the Nova console with optional token/refresh handoff
 *  query params — used when marketplace hands a signed-in session to the
 *  console (post-checkout and from Header "Portal" link). */
export const consoleHref = (
  path: string = '',
  params?: Record<string, string>,
): string => {
  const suffix = path ? (path.startsWith('/') ? path : `/${path}`) : '';
  const qs = params && Object.keys(params).length
    ? '?' + new URLSearchParams(params).toString()
    : '';
  return `${CONSOLE_URL}${suffix}${qs}`;
};

/** Prepend base to an internal marketplace route (strip leading '/'). */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
