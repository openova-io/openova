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
 * localStorage key for the active tenant's slug — persisted by CheckoutStep
 * after `createTenant` succeeds (and again on Stripe return). The Sovereign
 * marketplace at `marketplace.<sov-fqdn>` runs ONE process for ALL tenants,
 * so the per-tenant console host `console.<slug>.<sov-fqdn>` can only be
 * composed at redirect time once we know which workspace the user just
 * created (or last activated). When this key is absent we fall back to the
 * operator console at `console.<sov-fqdn>` — same shape as the legacy
 * (pre-V10) behaviour, only used for users who never had a workspace.
 *
 * Cleared by `logout()` and on `clearActiveOrgSlug()` (see api.ts). The
 * Stripe-return path persists this BEFORE the cross-origin hop so the
 * value survives the round-trip.
 */
export const ACTIVE_ORG_SLUG_KEY = 'sme-active-org-slug';

/**
 * Read the persisted tenant slug from localStorage. Returns null in SSR
 * (no `window`) or when no slug has been stamped yet (visitor still in
 * the storefront, never completed checkout).
 */
function readActiveOrgSlug(): string | null {
  if (typeof localStorage === 'undefined') return null;
  try {
    const s = localStorage.getItem(ACTIVE_ORG_SLUG_KEY);
    return s && s.trim() ? s.trim().toLowerCase() : null;
  } catch {
    return null;
  }
}

/**
 * Derive the customer console URL from the current marketplace host AND the
 * active tenant slug (if known).
 *
 * Bug fix (2026-05-20, TBD-V10 #2001): the previous shape on Sovereign was
 * `console.<sov-fqdn>` which is the OPERATOR console, not the per-tenant
 * customer console. The canonical per-tenant console hostname is
 * `console.<tenant-slug>.<sov-fqdn>` — emitted by the chart-side
 * tenant-public-routes.yaml HTTPRoute (PR #1993 TBD-A67) AND by the
 * runtime organization-controller. PowerDNS resolves
 * `console.<slug>.<parentDomain>` for every Org on the role=sme-pool
 * parent zone; without prepending the slug the marketplace was bouncing
 * customers into the operator console.
 *
 * The marketplace runs at `marketplace.<sov-fqdn>` where `<sov-fqdn>` IS
 * the sme-pool parent domain for sme-pool Sovereigns (e.g.
 * `marketplace.omani.homes`), so we just splice the slug as a new
 * left-most label.
 *
 * Earlier fix (2026-05-18, PR #1627): map `marketplace.<sov> → console.<sov>`
 * instead of always going to mothership. This patch refines that one
 * step further — when we ALSO know the tenant slug (post-checkout, post-
 * Stripe, returning visitor), we go all the way to
 * `console.<slug>.<sov>`. Without a slug (new visitor with no workspace)
 * we keep the legacy slug-less host so the operator-console fallback
 * still works.
 *
 * Rules (in evaluation order):
 *   - SSR / no `window`              → mothership URL (safe fallback for
 *                                       static page render)
 *   - host === 'marketplace.openova.io' → mothership URL (preserves
 *                                       existing behaviour, /nova prefix)
 *   - host starts with `marketplace.`   → if slug known: `https://console.<slug>.<rest-of-host>`
 *                                       else:            `https://console.<rest-of-host>`
 *                                       (Sovereign — NO /nova)
 *   - anything else (partner-branded
 *     vanity host e.g. `omantel.openova.io`,
 *     dev `localhost:4321`)             → mothership URL fallback
 */
function deriveConsoleURL(slug?: string | null): string {
  if (typeof window === 'undefined') return MOTHERSHIP_CONSOLE_URL;
  const host = (window.location.hostname || '').toLowerCase();
  if (!host) return MOTHERSHIP_CONSOLE_URL;
  // Mothership marketplace keeps the canonical /nova prefix.
  if (host === 'marketplace.openova.io') return MOTHERSHIP_CONSOLE_URL;
  // Sovereign pattern: marketplace.<sov-fqdn>
  //   - with slug:    marketplace.<sov-fqdn> → console.<slug>.<sov-fqdn>
  //   - without slug: marketplace.<sov-fqdn> → console.<sov-fqdn>      (op-console fallback)
  if (host.startsWith('marketplace.')) {
    const sovFqdn = host.slice('marketplace.'.length);
    if (sovFqdn) {
      const s = (slug ?? readActiveOrgSlug());
      if (s) return `https://console.${s}.${sovFqdn}`;
      return `https://console.${sovFqdn}`;
    }
  }
  // Partner-branded vanity hosts (omantel.openova.io) and dev/preview hosts
  // fall back to mothership. Demo tenants set skipConsoleRedirect anyway, so
  // this only matters when an authenticated user clicks an explicit
  // "Go to Console" link there — sending them to mothership is the
  // least-bad option (better than constructing a bogus `console.omantel.openova.io`).
  return MOTHERSHIP_CONSOLE_URL;
}

/**
 * Compose the per-tenant console hostname for a `marketplace.<sov-fqdn>`
 * host + tenant slug. Exported (and SSR-safe — pure function) so the
 * playwright fixture and any future unit test can assert the exact wire
 * shape WITHOUT mounting `window`.
 *
 * Returns null when the input is not a Sovereign marketplace host (mothership
 * or partner vanity); callers fall back to MOTHERSHIP_CONSOLE_URL in that
 * case.
 *
 * Examples:
 *   composeTenantConsoleURL('marketplace.omani.homes', 'demo')
 *     → 'https://console.demo.omani.homes'
 *   composeTenantConsoleURL('marketplace.t38.omani.works', 'acme')
 *     → 'https://console.acme.t38.omani.works'
 *   composeTenantConsoleURL('marketplace.openova.io', 'demo')
 *     → null   (mothership stays on /nova)
 */
export function composeTenantConsoleURL(host: string, slug: string): string | null {
  const h = (host || '').toLowerCase().trim();
  const s = (slug || '').toLowerCase().trim();
  if (!h || !s) return null;
  if (h === 'marketplace.openova.io') return null;
  if (!h.startsWith('marketplace.')) return null;
  const sovFqdn = h.slice('marketplace.'.length);
  if (!sovFqdn) return null;
  return `https://console.${s}.${sovFqdn}`;
}

/** Post-auth Nova customer console. All references to the customer dashboard
 *  go through here so the marketplace never hardcodes a cross-host URL.
 *
 *  Computed at module-load with the slug from localStorage. For paths where
 *  the slug is known at call time (post-createTenant, post-Stripe return),
 *  prefer `consoleHref(..., { slug })` which re-derives. */
export const CONSOLE_URL: string = deriveConsoleURL();

/** Build a URL into the Nova console with optional token/refresh handoff
 *  query params — used when marketplace hands a signed-in session to the
 *  console (post-checkout and from Header "Portal" link).
 *
 *  Pass `opts.slug` to override the active-org-slug read from localStorage
 *  (e.g. immediately after `createTenant` returns, before the value has
 *  necessarily been written back). */
export const consoleHref = (
  path: string = '',
  params?: Record<string, string>,
  opts?: { slug?: string | null },
): string => {
  const base = opts && opts.slug !== undefined
    ? deriveConsoleURL(opts.slug)
    : CONSOLE_URL;
  const suffix = path ? (path.startsWith('/') ? path : `/${path}`) : '';
  const qs = params && Object.keys(params).length
    ? '?' + new URLSearchParams(params).toString()
    : '';
  return `${base}${suffix}${qs}`;
};

/** Prepend base to an internal marketplace route (strip leading '/'). */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
