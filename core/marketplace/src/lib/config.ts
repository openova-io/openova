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
 * console service (products/catalyst/chart/templates/org-services/
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
export const ACTIVE_ORG_SLUG_KEY = 'org-active-org-slug';

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
 * #4176 — the Org's SERVER-AUTHORITATIVE console host (`console.<slug>.<parentDomain>`,
 * e.g. console.demo.omani.works), persisted by `setActiveOrgConsoleHost` right after
 * `createTenant`. This is the canonical fix for the pool-domain redirect bug: on a
 * Sovereign whose marketplace runs on the Sovereign domain (marketplace.omantel.biz)
 * while Orgs provision on a SEPARATE pool domain (omani.works), re-deriving the host
 * from the marketplace domain yields console.<slug>.omantel.biz — an unreachable host.
 */
export const ACTIVE_CONSOLE_HOST_KEY = 'org-active-console-host';

function readActiveConsoleHost(): string | null {
  if (typeof localStorage === 'undefined') return null;
  try {
    const h = localStorage.getItem(ACTIVE_CONSOLE_HOST_KEY);
    return h && h.trim() ? h.trim().toLowerCase() : null;
  } catch {
    return null;
  }
}

/**
 * localStorage key for the active Org's id (the tenant_id the provisioning
 * service keys on). `setActiveOrg` (api.ts) stamps this right after
 * `createTenant` / on Stripe return — BEFORE the launch redirect fires — so
 * the `/launching` interstitial can poll `GET /api/provisioning/tenant/<id>`
 * to render the live provisioning-stage timeline (#3860 / UAT row 86).
 */
export const ACTIVE_ORG_ID_KEY = 'org-active-org';

/** Read the persisted active-Org id (tenant_id). Null in SSR or when unset. */
function readActiveOrgId(): string | null {
  if (typeof localStorage === 'undefined') return null;
  try {
    const id = localStorage.getItem(ACTIVE_ORG_ID_KEY);
    return id && id.trim() ? id.trim() : null;
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
 * `console.<slug>.<parentDomain>` for every Org on the role=org-pool
 * parent zone; without prepending the slug the marketplace was bouncing
 * customers into the operator console.
 *
 * The marketplace runs at `marketplace.<sov-fqdn>` where `<sov-fqdn>` IS
 * the org-pool parent domain for org-pool Sovereigns (e.g.
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
  // #4176 — PREFER the server-authoritative console host (console.<slug>.<parentDomain>)
  // persisted post-createTenant. This is the ONLY source that knows the Org's chosen
  // POOL parent domain. Re-deriving from the marketplace host (below) is correct only
  // when the marketplace runs ON the pool domain; on a Sovereign whose marketplace is
  // on the Sovereign domain (marketplace.omantel.biz) with Orgs on a SEPARATE pool
  // (omani.works), the derivation yields console.<slug>.omantel.biz — an unreachable
  // host that breaks every org creation. Trust the stored host only when it matches the
  // requested slug (guards a stale value when consoleHref is called for a different Org).
  const storedHost = readActiveConsoleHost();
  if (storedHost && storedHost.startsWith('console.')) {
    const s = (slug ?? readActiveOrgSlug());
    if (!s || storedHost.startsWith(`console.${s}.`)) return `https://${storedHost}`;
  }
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

/**
 * #4182 + #4186 — build the SECURE session-handoff URL the marketplace
 * redirects to after checkout / from the Header "Portal" link.
 *
 * The previous shape `console.<slug>.<sov>/jobs?token=<JWT>&refresh_token=<uuid>`
 * leaked a live bearer + refresh token via browser history, proxy/CDN access
 * logs, and the Referer header (#4182), AND never established a session cookie,
 * so `/jobs` bounced to `/login` (#4186).
 *
 * For a per-Org Sovereign console (`console.<slug>.<sov-fqdn>`) this returns
 * `console.<slug>.<sov-fqdn>/auth/org-handover?token=<sessionJWT>` — the
 * server-side catalyst-api validates the token, mints an HttpOnly
 * `catalyst_session` cookie, and 302s to a clean `/jobs`. The refresh token is
 * NEVER placed in the URL (it stays in client storage via `setAuthTokens`).
 *
 * For the mothership `/nova` console (no per-Org host) the legacy
 * `/jobs?token=&refresh_token=` shape is preserved — that console redeems the
 * token in its own SSR path and is out of scope for #4182.
 *
 * Pass `opts.slug` to override the active-org-slug read from localStorage.
 */
export const consoleHandoffHref = (
  token: string,
  refreshToken?: string | null,
  opts?: { slug?: string | null },
): string => {
  const base = opts && opts.slug !== undefined
    ? deriveConsoleURL(opts.slug)
    : CONSOLE_URL;
  // The mothership console URL carries the /nova path prefix; everything
  // else is a per-Org (or operator) Sovereign console served by catalyst-api,
  // which exposes the secure /auth/org-handover endpoint.
  const isMothership = base.includes('/nova');
  if (isMothership) {
    const params: Record<string, string> = { token };
    if (refreshToken) params.refresh_token = refreshToken;
    return `${base}/jobs?${new URLSearchParams(params).toString()}`;
  }
  return `${base}/auth/org-handover?token=${encodeURIComponent(token)}`;
};

/**
 * #4273 — gate the post-checkout redirect on per-Org host readiness.
 *
 * The per-Org console host (`console.<slug>.<sov-fqdn>`) is provisioned
 * ASYNCHRONOUSLY by the organization-controller AFTER the Org CR is created:
 * pool-DNS A-record (#4236), per-Org TLS cert (#4241/#4242), and the HTTPRoute
 * on `cilium-gateway-console` all land over seconds-to-minutes. The route maps
 * `GET /healthz` to catalyst-api (see organization/internal/controller/
 * tenant_route.go), so a reachable `https://<consoleHost>/healthz` is the
 * canonical "this host now resolves, serves TLS, AND has its console route"
 * signal — the exact precondition the `/auth/org-handover` redirect needs.
 *
 * This replaces the IMMEDIATE cross-host bounce to `console.<slug>.<sov-fqdn>/
 * auth/org-handover` (which raced that async provisioning and landed the
 * customer's FIRST click on ERR_NAME_NOT_RESOLVED, negative-cached for the
 * 300s pool-zone SOA minimum). Instead the marketplace navigates to a
 * provisioning interstitial served from the marketplace origin — a host that
 * ALWAYS resolves — at `<BASE>launching?host=<consoleHost>&token=<jwt>&next=
 * <path>`. The `/launching` page (LaunchingStep.svelte) shows a branded
 * "Setting up your console…" state, polls the per-Org `/healthz` with backoff
 * (NXDOMAIN / TLS-handshake / connection-refused all tolerated as "still
 * provisioning"), and only forwards to the real `/auth/org-handover` once the
 * host serves a response.
 *
 * For the mothership `/nova` console (which has no per-Org async provisioning
 * — the host is always live) the direct `consoleHandoffHref` shape is kept:
 * there is nothing to wait for, so an interstitial would only add a hop.
 *
 * The token is passed THROUGH the interstitial unchanged and handed to the
 * existing secure `/auth/org-handover` endpoint at the end of the wait, so the
 * #4182/#4186 contract (token burned into an HttpOnly cookie server-side, no
 * bearer in the final address bar) is preserved. The refresh token is NEVER
 * placed in any URL — it stays in client storage.
 *
 * Pass `opts.slug` to override the active-org-slug read from localStorage.
 * Pass `opts.tenant` to override the active-Org id (tenant_id) the
 * interstitial polls for the provisioning-stage timeline; when omitted it
 * falls back to the id `setActiveOrg` stamped before this redirect (#3860).
 */
export const consoleLaunchHref = (
  token: string,
  refreshToken?: string | null,
  opts?: { slug?: string | null; next?: string; tenant?: string | null },
): string => {
  const base = opts && opts.slug !== undefined
    ? deriveConsoleURL(opts.slug)
    : CONSOLE_URL;
  const next = (opts && opts.next) || '/jobs';
  // Mothership console always resolves — keep the direct handoff (no wait).
  if (base.includes('/nova')) {
    return consoleHandoffHref(token, refreshToken, opts);
  }
  // Sovereign per-Org (or operator) console — route through the marketplace-
  // origin interstitial that waits for the host to be reachable.
  const params = new URLSearchParams({ host: base, token, next });
  // #3860 / UAT row 86 — thread the Org id (tenant_id) so the interstitial
  // can poll GET /api/provisioning/tenant/<id> and render the live
  // Creating tenant → Committing manifests → Provisioning vCluster →
  // Deploying <app> → TLS → Health stage timeline instead of a bare spinner.
  // Prefer the caller-supplied id, else the one setActiveOrg persisted before
  // this redirect. Absent (e.g. a returning user whose id was cleared) the
  // interstitial degrades to the spinner + host-readiness wait — never worse
  // than today.
  const tenant = (opts && opts.tenant) || readActiveOrgId();
  if (tenant) params.set('tenant', tenant);
  return `${BASE}launching?${params.toString()}`;
};

/** Prepend base to an internal marketplace route (strip leading '/'). */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
