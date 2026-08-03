import { parseRateLimitBody, rateLimitMessage } from './rateLimitNotice';

const API_BASE = '/api';

let refreshing: Promise<void> | null = null;

// Dispatched whenever the marketplace auth state changes (login, logout,
// token refresh, active-org switch). Components in the same tab cannot rely
// on the native `storage` event — it only fires for cross-tab writes — so
// they listen for this custom event instead.
export const AUTH_CHANGED_EVENT = 'org-auth-changed';

export function notifyAuthChanged(): void {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new Event(AUTH_CHANGED_EVENT));
}

export function setAuthTokens(token: string, refreshToken: string): void {
  localStorage.setItem('org-token', token);
  localStorage.setItem('org-refresh-token', refreshToken);
  notifyAuthChanged();
}

export function setActiveOrg(orgId: string): void {
  localStorage.setItem('org-active-org', orgId);
  notifyAuthChanged();
}

/**
 * Persist the active tenant's slug. The slug is the leftmost label of the
 * per-tenant console hostname (`console.<slug>.<sov-fqdn>` — TBD-V10
 * #2001 / TBD-A67 PR #1993). The marketplace runs ONE process for ALL
 * tenants on a Sovereign, so the slug can only be threaded into the
 * console redirect by stamping it client-side at the moment the tenant
 * becomes active (post-createTenant, post-Stripe return).
 *
 * `src/lib/config.ts::ACTIVE_ORG_SLUG_KEY` is the canonical key; we
 * duplicate the literal string here ONLY to keep this module free of a
 * circular import (config.ts already imports from elsewhere via Layout/
 * components and we want api.ts to remain dependency-free).
 */
export function setActiveOrgSlug(slug: string): void {
  if (!slug) return;
  localStorage.setItem('org-active-org-slug', slug);
  notifyAuthChanged();
}

/**
 * #4176 — persist the Org's SERVER-AUTHORITATIVE console host
 * (`console.<slug>.<parentDomain>`, e.g. console.demo.omani.works) so
 * `deriveConsoleURL` redirects to the chosen pool domain instead of
 * re-deriving `console.<slug>.<marketplace-host-domain>` (which on
 * marketplace.omantel.biz is the Sovereign domain → console.<slug>.omantel.biz,
 * an unreachable host that breaks every org creation). Canonical key:
 * `src/lib/config.ts::ACTIVE_CONSOLE_HOST_KEY`.
 */
export function setActiveOrgConsoleHost(host: string): void {
  if (!host) return;
  localStorage.setItem('org-active-console-host', host.trim().toLowerCase());
  notifyAuthChanged();
}

async function request<T>(path: string, opts?: RequestInit): Promise<T> {
  const token = localStorage.getItem('org-token');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
  const res = await fetch(`${API_BASE}${path}`, { ...opts, headers });
  if (res.status === 401 && token && path !== '/auth/refresh') {
    // Try to refresh the token once, then retry the original request.
    if (!refreshing) {
      refreshing = tryRefresh().finally(() => { refreshing = null; });
    }
    await refreshing;
    const newToken = localStorage.getItem('org-token');
    if (newToken && newToken !== token) {
      const retryHeaders: Record<string, string> = {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${newToken}`,
      };
      const retry = await fetch(`${API_BASE}${path}`, { ...opts, headers: retryHeaders });
      if (!retry.ok) {
        const body = await retry.text();
        throw new Error(requestErrorMessage(retry.status, body));
      }
      return retry.json();
    }
  }
  if (!res.ok) {
    const body = await res.text();
    throw new Error(requestErrorMessage(res.status, body));
  }
  return res.json();
}

/**
 * #5634 (UAT row 92): callers surface `e.message` straight to the customer
 * (CheckoutStep puts it in `provisionError`), so a throttled checkout used to
 * read as a raw status code joined to a raw JSON blob. A 429 becomes the plain
 * wait-and-retry notice instead, carrying the gateway's own `retry_after`.
 * Every other status keeps the existing `<status>: <body>` shape that other
 * call sites already match on.
 */
function requestErrorMessage(status: number, body: string): string {
  if (status === 429) return rateLimitMessage(parseRateLimitBody(body), 'checkout');
  return `${status}: ${body}`;
}

async function tryRefresh(): Promise<void> {
  const rt = localStorage.getItem('org-refresh-token');
  if (!rt) return;
  try {
    const res = await fetch(`${API_BASE}/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: rt }),
    });
    if (res.ok) {
      const data = await res.json();
      setAuthTokens(data.token, data.refresh_token);
    } else {
      localStorage.removeItem('org-token');
      localStorage.removeItem('org-refresh-token');
      notifyAuthChanged();
    }
  } catch {
    // Refresh failed — clear tokens.
    localStorage.removeItem('org-token');
    localStorage.removeItem('org-refresh-token');
    notifyAuthChanged();
  }
}

// Catalog
// Scope to generic compute tiers (S/M/L/XL/Flexi). Product-scoped plans
// (e.g. Sandbox Free/Pro/Ent, ProductSlug="sandbox") have their own flow
// and must NOT duplicate into this Org-provisioning picker (#3156).
export const getPlans = async (): Promise<Plan[]> => {
  const raw = await request<any[]>('/catalog/plans?product=generic');
  return raw
    // Safety net: drop any product-scoped row client-side too, so the
    // picker stays generic-only even against an older catalog-service
    // that doesn't yet honour ?product= (#3156). product_slug is omitted
    // (omitempty) for generic tiers, so falsy === keep.
    .filter(p => !p.product_slug)
    .map(p => ({
    id: p.id,
    slug: p.slug || p.name?.toLowerCase() || '',
    name: p.name,
    tagline: p.description || '',
    resources: { cpu: p.cpu || '', memory: p.memory || '', storage: p.storage || '' },
    monthly_price: (p.price_omr || 0) * 1000,
    features: p.features?.length ? p.features : [p.cpu, p.memory, p.storage].filter(Boolean),
    popular: p.popular || false,
  }));
};
export const getApps = async (): Promise<App[]> => {
  // Marketplace storefront sees only Published=true (operator-curated)
  // AND System=false AND Deployable=true. The catalog service enforces
  // the three-predicate filter server-side via ?published=true (#710).
  // The Sovereign-console operator view hits /catalog/apps without the
  // query param to get every app for the per-row publish toggle.
  const raw = await request<any[]>('/catalog/apps?published=true');
  return raw.map(a => ({
    id: a.id, name: a.name, slug: a.slug, tagline: a.tagline || '',
    description: a.description || a.tagline || '',
    category: a.category || '', icon: a.icon || a.name[0], color: a.icon_bg || a.color || '#3b82f6',
    logo: appLogos[a.slug] || '',
    free: a.free ?? true, featured: a.featured, popular: a.popular,
    features: a.features || [],
    website: a.website || '',
    license: a.license || '',
    dependencies: a.dependencies || [],
    system: a.system ?? false,
    kind: (a.kind as 'business' | 'service') || (a.system ? 'service' : 'business'),
    shareable: a.shareable ?? false,
    deployable: a.deployable ?? false, // #102 — must carry through to template
    // TBD-V18 (#2026) — surface ConfigSchema so AppDetail renders
    // per-instance tunables (replicas/disk/backup for Postgres-backed
    // bundles, etc.). Go store carries this as `config_schema` (per
    // store.App.ConfigSchema bson tag); wire shape matches
    // store.ConfigField exactly. Empty list when the catalog has no
    // tunables for the app (omitempty on the Go side).
    configSchema: Array.isArray(a.config_schema)
      ? (a.config_schema as ConfigField[])
      : [],
  }));
};
export const getIndustries = async (): Promise<Industry[]> => {
  const raw = await request<any[]>('/catalog/industries');
  return raw.map(i => ({
    id: i.id, name: i.name, icon: i.emoji || i.icon || '',
    app_ids: i.suggested_apps || i.app_ids || [],
  }));
};
export const getAddons = async (): Promise<AddOn[]> => {
  const raw = await request<any[]>('/catalog/addons');
  return raw.map(a => ({
    id: a.id, name: a.name, slug: a.slug || '', tagline: a.description || '',
    icon: a.icon || '', monthly_price: (a.price_omr || 0) * 1000,
    included: a.included ?? false,
  }));
};

// Auth
export const sendMagicLink = (email: string) =>
  request<{ message: string }>('/auth/magic-link', {
    method: 'POST',
    body: JSON.stringify({ email }),
  });

export const verifyMagicLink = (email: string, code: string) =>
  request<AuthResponse>('/auth/verify', {
    method: 'POST',
    body: JSON.stringify({ email, code }),
  });

export const getMe = async (): Promise<User> => {
  const data = await request<{ user: User }>('/auth/me');
  return data.user;
};

export const getGoogleAuthUrl = (redirectUri: string) =>
  request<{ url: string }>(`/auth/google?redirect_uri=${encodeURIComponent(redirectUri)}`);

export const googleCallback = (code: string, redirectUri: string) =>
  request<AuthResponse>('/auth/google/callback', {
    method: 'POST',
    body: JSON.stringify({ code, redirect_uri: redirectUri }),
  });

export const refreshToken = (token: string) =>
  request<AuthResponse>('/auth/refresh', {
    method: 'POST',
    body: JSON.stringify({ refresh_token: token }),
  });

export async function logout(): Promise<void> {
  const rt = localStorage.getItem('org-refresh-token');
  try {
    if (rt) {
      await fetch(`${API_BASE}/auth/logout`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: rt }),
      });
    }
  } catch {
    // Server call is best-effort — clear local state regardless.
  }
  localStorage.removeItem('org-token');
  localStorage.removeItem('org-refresh-token');
  localStorage.removeItem('org-active-org');
  localStorage.removeItem('org-active-org-slug');
  localStorage.removeItem('org-active-console-host'); // #4176
  localStorage.removeItem('org-cart');
  localStorage.removeItem('org-checkout-tenant');
  localStorage.removeItem('org-checkout-tenant-slug');
  for (let i = localStorage.length - 1; i >= 0; i--) {
    const k = localStorage.key(i);
    if (k && k.startsWith('org-tenant:')) localStorage.removeItem(k);
  }
  notifyAuthChanged();
}

// Tenant
export const createTenant = (data: CreateTenantRequest) =>
  request<Tenant>('/tenant/orgs', {
    method: 'POST',
    body: JSON.stringify(data),
  });

export const getMyOrgs = () => request<Tenant[]>('/tenant/orgs');

export const checkSlug = (slug: string) =>
  request<{ available: boolean }>(`/tenant/check-slug/${encodeURIComponent(slug)}`);

// Billing
export const createCheckout = (data: CheckoutRequest) =>
  request<CheckoutResponse>('/billing/checkout', {
    method: 'POST',
    body: JSON.stringify(data),
  });

// #85 — canonicalise on baisa at the API boundary. The billing service now
// emits both legacy `credit_omr` (whole OMR) and `credit_baisa` (canonical
// baisa), and likewise for each ledger entry. We normalise to baisa here so
// the rest of the marketplace never has to think about the unit.
export type CreditEntry = {
  id: string;
  amount_baisa: number;
  reason: string;
  created_at: string;
};
export type CreditBalance = { credit_baisa: number; entries: CreditEntry[] };

export const getCreditBalance = async (): Promise<CreditBalance> => {
  const raw = await request<{
    credit_baisa?: number;
    credit_omr?: number;
    entries?: Array<{ id: string; amount_baisa?: number; amount_omr?: number; reason: string; created_at: string }>;
  }>('/billing/balance');
  const credit_baisa = typeof raw.credit_baisa === 'number'
    ? raw.credit_baisa
    : Math.round((raw.credit_omr ?? 0) * 1000);
  const entries: CreditEntry[] = (raw.entries ?? []).map(e => ({
    id: e.id,
    reason: e.reason,
    created_at: e.created_at,
    amount_baisa: typeof e.amount_baisa === 'number' ? e.amount_baisa : Math.round((e.amount_omr ?? 0) * 1000),
  }));
  return { credit_baisa, entries };
};

export type VoucherPreview = {
  code: string;
  credit_baisa: number;
  description: string;
  active: boolean;
  accepting_redemptions: boolean;
};

// Preview the credit a voucher would grant WITHOUT redeeming it, so checkout can
// reflect a pending voucher's credit in the cart BEFORE the /billing/checkout
// commit (the redeem itself commits at checkout). Returns null on unknown/invalid
// (404) or non-redeemable/capped (410) codes — only a live, redeemable voucher's
// credit should pre-apply. credit_omr is whole OMR; normalised to baisa per #85.
export const redeemVoucherPreview = async (code: string): Promise<VoucherPreview | null> => {
  try {
    const raw = await request<{
      code: string;
      credit_omr?: number;
      credit_baisa?: number;
      description?: string;
      active?: boolean;
      accepting_redemptions?: boolean;
    }>('/billing/vouchers/redeem-preview', {
      method: 'POST',
      body: JSON.stringify({ code }),
    });
    return {
      code: raw.code,
      credit_baisa: typeof raw.credit_baisa === 'number' ? raw.credit_baisa : Math.round((raw.credit_omr ?? 0) * 1000),
      description: raw.description ?? '',
      active: raw.active ?? false,
      accepting_redemptions: raw.accepting_redemptions ?? false,
    };
  } catch {
    return null;
  }
};

// Provisioning
export const getProvisionStatus = (id: string) =>
  request<Provision>(`/provisioning/status/${id}`);

export const getProvisionByTenant = (tenantId: string) =>
  request<Provision>(`/provisioning/tenant/${tenantId}`);

export const startProvisioning = (data: { tenant_id: string; order_id: string; plan_id: string; apps: string[]; subdomain: string }) =>
  request<Provision>('/provisioning/start', {
    method: 'POST',
    body: JSON.stringify(data),
  });

// Types
export interface Plan {
  id: string;
  slug: string;
  name: string;
  tagline: string;
  resources: { cpu: string; memory: string; storage: string };
  monthly_price: number;
  features: string[];
  popular?: boolean;
}

// ConfigField mirrors the Go `core/services/catalog/store/store.go`
// `ConfigField` struct (line 91) one-for-one. The wire JSON tag for
// each Go field is the lowercase form used here, e.g. Go's
// `Default any` ⇄ TS `default?: number | string | boolean`. The
// console renders one input widget per `type` —
//   - "int"    → <input type="number">  (min/max bound)
//   - "string" → <input type="text">
//   - "bool"   → <input type="checkbox">
//   - "enum"   → <select> populated from `options`
//   - "size"   → <input type="text">    (e.g. "10Gi", parsed downstream)
//
// `advanced` fields collapse behind an "Advanced" toggle (UI iteration
// follow-up; for now they render inline with an `advanced` badge so
// nothing is hidden from the operator). See TBD-V18 (#2026).
export interface ConfigField {
  key: string;
  label: string;
  type: 'int' | 'string' | 'bool' | 'enum' | 'size';
  default?: number | string | boolean;
  min?: number;
  max?: number;
  options?: string[];
  description?: string;
  advanced?: boolean;
}

export interface App {
  id: string;
  name: string;
  slug: string;
  tagline: string;
  description: string;
  category: string;
  icon: string;
  color: string;
  logo: string;
  free: boolean;
  featured?: boolean;
  popular?: boolean;
  features: string[];
  website: string;
  license: string;
  dependencies?: string[];
  system?: boolean;
  kind?: 'business' | 'service';
  shareable?: boolean;
  // deployable=false means the catalog lists the app but provisioning isn't
  // wired yet. Cards show a 'Coming soon' overlay, toggle is disabled.
  // See issue #102.
  deployable?: boolean;
  // TBD-V18 (#2026) — per-instance tunables (replicas / disk / backup
  // for Postgres-backed bundles, replicas / persistence for Redis,
  // etc.). Empty array when the catalog has no tunables for this app.
  // The customer's chosen values are persisted to
  // `CartState.appConfigs[slug]` (see cart.ts::setAppConfig) and
  // threaded into the install POST as `CreateTenantRequest.app_configs`
  // (TBD-V18-D follow-up to PR #2038).
  configSchema?: ConfigField[];
}

// GitHub org/user avatar URLs — reliable, CDN-backed, consistent sizing
const appLogos: Record<string, string> = {
  wordpress: 'https://github.com/WordPress.png?size=64',
  ghost: 'https://github.com/TryGhost.png?size=64',
  'stalwart-mail': 'https://github.com/stalwartlabs.png?size=64',
  'rocket-chat': 'https://github.com/RocketChat.png?size=64',
  nextcloud: 'https://github.com/nextcloud.png?size=64',
  twenty: 'https://github.com/twentyhq.png?size=64',
  umami: 'https://github.com/umami-software.png?size=64',
  medusa: 'https://github.com/medusajs.png?size=64',
  plane: 'https://github.com/makeplane.png?size=64',
  erpnext: 'https://github.com/frappe.png?size=64',
  invoiceshelf: 'https://github.com/InvoiceShelf.png?size=64',
  listmonk: 'https://github.com/knadh.png?size=64',
  'cal-com': 'https://github.com/calcom.png?size=64',
  gitea: 'https://github.com/go-gitea.png?size=64',
  'uptime-kuma': 'https://github.com/louislam.png?size=64',
  librechat: 'https://github.com/danny-avila.png?size=64',
  documenso: 'https://github.com/documenso.png?size=64',
  vaultwarden: 'https://github.com/dani-garcia.png?size=64',
  bookstack: 'https://github.com/BookStackApp.png?size=64',
  formbricks: 'https://github.com/formbricks.png?size=64',
  dify: 'https://github.com/langgenius.png?size=64',
  openclaw: 'https://github.com/openclaw.png?size=64',
  chatwoot: 'https://github.com/chatwoot.png?size=64',
  postiz: 'https://github.com/gitroomhq.png?size=64',
  nocodb: 'https://github.com/nocodb.png?size=64',
  'jitsi-meet': 'https://github.com/jitsi.png?size=64',
  immich: 'https://github.com/immich-app.png?size=64',
  // Wave 4 — Sandbox marketplace catalog entry. Logo is the OpenOva
  // org avatar to match how WordPress/Ghost/Nextcloud cards lean on
  // upstream-org GitHub avatars; Sandbox is our own product so the
  // OpenOva mark is the canonical brand.
  sandbox: 'https://github.com/openova-io.png?size=64',
};

export interface Industry {
  id: string;
  name: string;
  icon: string;
  app_ids: string[];
}

export interface AddOn {
  id: string;
  name: string;
  slug: string;
  tagline: string;
  icon: string;
  monthly_price: number;
  included: boolean;
}

export interface User {
  id: string;
  email: string;
  name: string;
}

export interface AuthResponse {
  token: string;
  refresh_token: string;
  user: User;
}

export interface CreateTenantRequest {
  slug: string;
  name: string;
  plan_id: string;
  apps: string[];
  addons: string[];
  // #4176/#4179 — the org-pool parent apex the customer chose at the
  // /addons step (e.g. "omani.works"). The tenant-service composes the
  // per-Org console host `console.<slug>.<parent_domain>` and returns it as
  // the response `console_host`, which CheckoutStep persists + uses for the
  // post-checkout redirect. WITHOUT sending this, the server has no idea
  // which pool domain the Org is on, returns no console_host, and the client
  // falls back to splicing the slug onto the marketplace host —
  // console.<slug>.omantel.biz on the production Sovereign, an unreachable
  // host that breaks EVERY org-create. Optional so single-domain Sovereigns
  // (marketplace ON the pool domain) keep working via the host-splice path.
  parent_domain?: string;
  // Wave 4 Sandbox — forwarded only when `apps` contains 'sandbox'.
  // The tenant-service uses this to publish a `tenant.sandbox_requested`
  // event the sandbox-controller consumes to mint a Sandbox CR with
  // matching spec.agentCatalogue. Optional so legacy clients keep
  // working unchanged.
  agents?: string[];
  // TBD-V18-D follow-up to PR #2038 — per-instance configSchema
  // values, keyed by app slug. Optional so legacy clients (older cart
  // shape, machine-to-machine callers) keep working unchanged. Wire
  // mirror of `store.Tenant.AppConfigs` (bson:"app_configs"). The
  // backend tenant-service decodes via the same JSON tag and
  // round-trips on the `tenant.created` event payload — see
  // `tenant_created_wire_test.go`.
  app_configs?: Record<string, Record<string, number | string | boolean>>;
  // #4956 — when true, the tenant-service persists the Org shell but does NOT
  // fire the provisioning triggers (Organization CR, cart-install, sandbox).
  // The Org stays `pending_payment` until billing settlement launches it, so a
  // checkout that 400s (bad/unseeded voucher, declined card) can never leave a
  // provisioned Org with no purchased apps behind. The funnel checkout sets
  // this; omitting it preserves the immediate-launch behaviour for every other
  // caller.
  defer_launch?: boolean;
}

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  plan_id: string;
  apps: string[];
  status: string;
  // #4176 — server-authoritative console host: `console.<slug>.<parentDomain>`
  // (e.g. console.demo.omani.works). Derived server-side from the Org's chosen
  // pool parent domain; the client MUST use this for the post-checkout redirect
  // rather than re-deriving from the marketplace host (which on a Sovereign whose
  // marketplace runs on the Sovereign domain yields the wrong, unreachable host).
  console_host?: string;
  parent_domain?: string;
}

export interface CheckoutRequest {
  plan_id: string;
  apps: string[];
  addons: string[];
  tenant_id: string;
  promo_code?: string;
  // #5104 facet B — BCP topology chosen on /bcp, canonical vocabulary
  // ('single-region' | 'active-hot-standby'). Billing prices the
  // hot-standby surcharge server-side; omitting it bills single-region.
  topology?: string;
}

export interface CheckoutResponse {
  session_url?: string;
  order_id?: string;
  paid_by_credit?: boolean;
  credit_balance?: number;
}

export interface Provision {
  id: string;
  tenant_id: string;
  // pending | provisioning | completed | failed (store.Provision.Status).
  status: string;
  steps: ProvisionStep[];
  // 0-100 rollup the provisioning service stamps as steps complete
  // (store.Provision.Progress). Optional so older payloads still decode.
  progress?: number;
}

export interface ProvisionStep {
  name: string;
  // pending | running | completed | failed (store.ProvisionStep.Status).
  status: string;
  // Human-readable detail the workflow attaches on completion/failure
  // (store.ProvisionStep.Message) — e.g. the reason a stage failed.
  message?: string;
  started_at?: string;
  // Wire field is `done_at` (store.ProvisionStep.DoneAt) — NOT completed_at.
  // The operator console (core/console) already decodes done_at; the
  // marketplace type was drifted and is corrected here so the launch
  // interstitial reads the real field.
  done_at?: string;
}
