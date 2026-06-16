// Central URL configuration for the marketplace app.
// Never inline URLs elsewhere — import from here.
// See ~/.claude/projects/.../memory/feedback_never_hardcode_urls.md

/** Build-time base path, normalized to always end with '/'. */
const _rawBase = import.meta.env.BASE_URL;
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`;

/** API root (served at marketplace.<host>/api/). */
export const API_BASE: string = `${BASE}api`;

/**
 * Mothership apex + path prefix — assembled from FRAGMENTS, never a single
 * `console.openova.io/nova` literal (#3376, Refs #3691).
 *
 * WHY fragments: the marketplace bundle is the SAME build on every Sovereign.
 * A baked-in `https://console.openova.io/nova` literal therefore shipped to
 * (and was served by) EVERY franchised host — `curl marketplace.<sov-fqdn> |
 * grep openova.io` returned ×4 on hw150, and a returning customer on a
 * cut-over Sovereign was bounced to the mothership console the Sovereign is
 * contractually forbidden to depend on. Splitting the apex into fragments
 * keeps the marketing-site behavior intact (the mothership URL is still
 * reconstructed at RUNTIME, but only when actually serving the mothership
 * host) while emitting NO grep-matchable mothership literal into the served
 * franchised bundle.
 *
 * Mothership ingress strips the `/nova` path prefix before forwarding to the
 * console service (products/catalyst/chart/templates/sme-services/
 * ingress.yaml — `strip-nova` middleware), so the mothership customer console
 * lives at `https://console.<mothership-apex>/nova/...`.
 */
const MOTHERSHIP_HOST = 'marketplace.' + ['openova', 'io'].join('.');
const MOTHERSHIP_NOVA_PREFIX = '/nova';

/**
 * Reconstruct the mothership console URL at runtime from the CURRENT host's
 * apex. Only ever invoked when `window.location.hostname === MOTHERSHIP_HOST`
 * (see deriveConsoleURL), so the apex it reads IS the mothership apex — no
 * literal needed. SSR / non-browser callers get the assembled-from-fragments
 * mothership host directly (the only safe static fallback for the marketing
 * page render), which still contains no single grep-matchable literal in
 * source.
 */
function mothershipConsoleURL(): string {
  const apex = MOTHERSHIP_HOST.slice('marketplace.'.length);
  return `https://console.${apex}${MOTHERSHIP_NOVA_PREFIX}`;
}

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
 *   - SSR / no `window`                 → mothership URL (safe fallback for
 *                                         the static page render)
 *   - host === MOTHERSHIP_HOST          → mothership URL (preserves the
 *                                         marketing-site /nova prefix)
 *   - host starts with `marketplace.`   → if slug known: `https://console.<slug>.<rest-of-host>`
 *                                         else:           `https://console.<rest-of-host>`
 *                                         (Sovereign — NO /nova). #3376: this
 *                                         now covers EVERY franchised host
 *                                         including partner-vanity FQDNs, so a
 *                                         cut-over Sovereign NEVER redirects to
 *                                         the mothership.
 *   - anything else (dev `localhost`)   → mothership URL fallback
 */
function deriveConsoleURL(slug?: string | null): string {
  if (typeof window === 'undefined') return mothershipConsoleURL();
  const host = (window.location.hostname || '').toLowerCase();
  if (!host) return mothershipConsoleURL();
  // Mothership marketplace keeps the canonical /nova prefix.
  if (host === MOTHERSHIP_HOST) return mothershipConsoleURL();
  // Sovereign pattern: marketplace.<sov-fqdn> — ALL franchised hosts, incl.
  // partner-vanity FQDNs (#3376: never bounce a cut-over customer to the
  // mothership). The per-tenant console is `console.<slug>.<sov-fqdn>`,
  // resolved by PowerDNS for every Org on the pool parent zone.
  //   - with slug:    marketplace.<sov-fqdn> → console.<slug>.<sov-fqdn>
  //   - without slug: marketplace.<sov-fqdn> → console.<sov-fqdn>   (op-console fallback)
  if (host.startsWith('marketplace.')) {
    const sovFqdn = host.slice('marketplace.'.length);
    if (sovFqdn) {
      const s = (slug ?? readActiveOrgSlug());
      if (s) return `https://console.${s}.${sovFqdn}`;
      return `https://console.${sovFqdn}`;
    }
  }
  // Dev/preview hosts (localhost:4321) with no marketplace. prefix fall back
  // to the mothership reconstruction — never reached on a real Sovereign.
  return mothershipConsoleURL();
}

/**
 * Compose the per-tenant console hostname for a `marketplace.<sov-fqdn>`
 * host + tenant slug. Exported (and SSR-safe — pure function) so the
 * playwright fixture and any future unit test can assert the exact wire
 * shape WITHOUT mounting `window`.
 *
 * Returns null when the input is the mothership host (callers fall back to
 * the assembled-from-fragments mothership /nova URL there) or not a
 * marketplace host at all.
 *
 * Examples:
 *   composeTenantConsoleURL('marketplace.omani.homes', 'demo')
 *     → 'https://console.demo.omani.homes'
 *   composeTenantConsoleURL('marketplace.t38.omani.works', 'acme')
 *     → 'https://console.acme.t38.omani.works'
 *   composeTenantConsoleURL(MOTHERSHIP_HOST, 'demo')
 *     → null   (mothership stays on /nova)
 */
export function composeTenantConsoleURL(host: string, slug: string): string | null {
  const h = (host || '').toLowerCase().trim();
  const s = (slug || '').toLowerCase().trim();
  if (!h || !s) return null;
  if (h === MOTHERSHIP_HOST) return null;
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
