import { API_BASE, CHECKOUT_URL, MARKETPLACE_HOME_URL, path } from './config';

// Dispatched whenever console auth state changes (login handoff, logout,
// token refresh, active-org switch). Same pattern as marketplace (#51) /
// admin (#83) — the native `storage` event only fires cross-tab, so we
// need a custom event for same-tab reactivity.
export const AUTH_CHANGED_EVENT = 'org-auth-changed';

export function notifyAuthChanged(): void {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new Event(AUTH_CHANGED_EVENT));
}

export function setAuthTokens(token: string, refreshToken: string): void {
  localStorage.setItem('org-token', token);
  if (refreshToken) localStorage.setItem('org-refresh-token', refreshToken);
  notifyAuthChanged();
}

export function setActiveOrg(orgId: string): void {
  localStorage.setItem('org-active-org', orgId);
  notifyAuthChanged();
}

let refreshing: Promise<void> | null = null;

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
    localStorage.removeItem('org-token');
    localStorage.removeItem('org-refresh-token');
    notifyAuthChanged();
  }
}

async function request<T>(path: string, opts?: RequestInit): Promise<T> {
  const token = localStorage.getItem('org-token');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
  const res = await fetch(`${API_BASE}${path}`, { ...opts, headers });
  // On 401, try one silent refresh before bouncing back to marketplace —
  // console sessions shouldn't die every 15 min when a 30-day refresh
  // token is sitting in localStorage.
  if (res.status === 401 && token && path !== '/auth/refresh') {
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
      if (retry.status === 401) {
        clearSessionState();
        window.location.href = CHECKOUT_URL;
        throw new Error('Unauthorized');
      }
      if (!retry.ok) throw new Error(`${retry.status}: ${await retry.text()}`);
      return retry.json();
    }
    // Refresh didn't yield a new token — bounce.
    clearSessionState();
    window.location.href = CHECKOUT_URL;
    throw new Error('Unauthorized');
  }
  if (res.status === 401) {
    clearSessionState();
    window.location.href = CHECKOUT_URL;
    throw new Error('Unauthorized');
  }
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
  return res.json();
}

// Auth
export const getMe = async (): Promise<User> => {
  const data = await request<{ user: User }>('/auth/me');
  return data.user;
};
function clearSessionState() {
  const rt = localStorage.getItem('org-refresh-token');
  if (rt) {
    // Best-effort server-side revocation — don't block logout on it.
    fetch(`${API_BASE}/auth/logout`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: rt }),
      keepalive: true,
    }).catch(() => {});
  }
  localStorage.removeItem('org-token');
  localStorage.removeItem('org-refresh-token');
  localStorage.removeItem('org-active-org');
  localStorage.removeItem('org-cart');
  // 'org-checkout-tenant' + 'org-tenant:*' are legacy localStorage keys
  // WRITTEN by the marketplace SPA (core/marketplace CheckoutStep) — the
  // console only clears them at logout. Key strings are a cross-app
  // contract; rename in lockstep with the marketplace or stale keys leak.
  localStorage.removeItem('org-checkout-tenant');
  for (let i = localStorage.length - 1; i >= 0; i--) {
    const k = localStorage.key(i);
    if (k && k.startsWith('org-tenant:')) localStorage.removeItem(k);
  }
  try { sessionStorage.removeItem('org-session-cache-v1'); } catch {}
  notifyAuthChanged();
}

export const logout = () => {
  clearSessionState();
  window.location.href = MARKETPLACE_HOME_URL;
};

// Revoke every refresh token for the current user — "sign out everywhere"
// (#88). Other devices bounce back to checkout on their next API call
// once their (≤15-min) access token expires.
export const logoutAll = () =>
  request<{ message: string }>('/auth/logout-all', { method: 'POST' });

// Organizations
// Org CRUD + the member / app / backing-service sub-resources all ride the
// canonical `/organizations` routes (#3383 — served by core/services/tenant
// routes.go via the gateway's /api/organizations). The tenant service keeps
// the legacy `/tenant/orgs/{id}/...` paths as one-release deprecation aliases,
// so nothing breaks during a rolling deploy.
export const getMyOrgs = () => request<Org[]>('/organizations');
export const getOrg = (id: string) => request<Org>(`/organizations/${id}`);
export const getMembers = (orgId: string) => request<Member[]>(`/organizations/${orgId}/members`);
export const inviteMember = (orgId: string, email: string, role: string) =>
  request<Member>(`/organizations/${orgId}/members`, { method: 'POST', body: JSON.stringify({ email, role }) });
export const updateOrg = (orgId: string, patch: { name?: string }) =>
  request<Org>(`/organizations/${orgId}`, { method: 'PUT', body: JSON.stringify(patch) });
export const deleteOrg = (orgId: string) =>
  request<{ status: string }>(`/organizations/${orgId}`, { method: 'DELETE' });

// Domains
export const getDomains = (orgId: string) => request<Domain[]>(`/domain/list?tenant_id=${orgId}`);

// Billing
export const getSubscription = () => request<Subscription>('/billing/subscription');
// #85 — normalise money fields to baisa at the API boundary. The backend
// emits both `amount_omr` (legacy int) and `amount_baisa` (canonical). The
// UI should only ever see `amount_baisa`.
export const getInvoices = async (): Promise<Invoice[]> => {
  const raw = await request<any[]>('/billing/invoices');
  return (raw ?? []).map(inv => ({
    id: inv.id,
    amount_baisa: typeof inv.amount_baisa === 'number'
      ? inv.amount_baisa
      : (typeof inv.amount_omr === 'number' ? inv.amount_omr * 1000 : 0),
    status: inv.status || 'draft',
    created_at: inv.created_at,
  }));
};
// #85 — prefer `credit_baisa` from the new backend; fall back to the legacy
// `credit_omr * 1000` for clients that still see the older payload shape.
// Ledger entries get the same treatment per-row.
export const getCreditBalance = async (): Promise<CreditBalance> => {
  const raw = await request<any>('/billing/balance');
  const credit_baisa = typeof raw.credit_baisa === 'number'
    ? raw.credit_baisa
    : Math.round((raw.credit_omr ?? 0) * 1000);
  const entries: CreditEntry[] = (raw.entries ?? []).map((e: any) => ({
    id: e.id,
    amount_baisa: typeof e.amount_baisa === 'number'
      ? e.amount_baisa
      : Math.round((e.amount_omr ?? 0) * 1000),
    reason: e.reason,
    created_at: e.created_at,
  }));
  return { credit_baisa, entries };
};

// Catalog
export const getPlans = () => request<Plan[]>('/catalog/plans');

// Keep in sync with apps/marketplace/src/lib/api.ts so console renders the
// same GitHub-avatar logos as the marketplace and admin.
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
};

export const getApps = async (): Promise<CatalogApp[]> => {
  const raw = await request<any[]>('/catalog/apps');
  return raw.map(a => ({
    id: a.id,
    slug: a.slug,
    name: a.name,
    icon: a.icon || a.name?.[0] || '?',
    icon_bg: a.icon_bg || a.color || '#3b82f6',
    color: a.icon_bg || a.color || '#3b82f6',
    logo: appLogos[a.slug] || '',
    category: a.category || '',
    tagline: a.tagline || '',
    description: a.description || a.tagline || '',
    free: a.free ?? true,
    system: a.system ?? false,
    kind: (a.kind as 'business' | 'service') || (a.system ? 'service' : 'business'),
    shareable: a.shareable ?? false,
    dependencies: a.dependencies || [],
    config_schema: a.config_schema || [],
  }));
};

// Provisioning
// The `/provisioning/tenant/…` path + `tenant_id` query param are the
// provisioning service's wire contract (core/services/provisioning
// routes.go / jobs.go) — URL strings stay until a BE lockstep rename.
export const getProvisionStatus = (orgId: string) => request<Provision>(`/provisioning/tenant/${orgId}`);
// Day-2 Jobs: install/uninstall records the Jobs page renders alongside the
// initial Org provision. Returns newest-first.
export const getJobs = (orgId: string) =>
  request<Job[]>(`/provisioning/jobs?tenant_id=${encodeURIComponent(orgId)}`);

// Preview of what an uninstall will do to backing services. Used by the
// confirm modal so the user sees purge vs retain before proceeding.
export const getUninstallPreview = (orgId: string, slug: string) =>
  request<UninstallPreview>(`/organizations/${orgId}/apps/${slug}/uninstall-preview`);

// Day-2 app management (backend: task #134 — returns 501 until shipped)
// dep_choices maps dependency slug -> 'dedicated' | existing instance slug/id.
export const installApp = (
  orgId: string,
  slug: string,
  depChoices?: Record<string, string>,
) =>
  request<{ status: string; message?: string; upgrade_suggestion?: string }>(
    `/organizations/${orgId}/apps`,
    {
      method: 'POST',
      body: JSON.stringify({
        slug,
        ...(depChoices && Object.keys(depChoices).length ? { dep_choices: depChoices } : {}),
      }),
    },
  );

export const uninstallApp = (orgId: string, slug: string) =>
  request<{ status: string }>(
    `/organizations/${orgId}/apps/${slug}`,
    { method: 'DELETE' },
  );

// G117.4 #2743 AC3 — Launch button calls catalyst-api which mints the
// signed silent-SSO URL (carries `prompt=none` + `kc_idp_hint=catalyst-pin`).
// The handler lives on the catalyst-api at `/catalyst/v1/apps/{id}/launch-url`
// (NOT under the Organization `/api` namespace), so we bypass `request()` and hit
// the route directly from the same origin.
//
// Error surfaces (per `endpoint_handler.go:HandleGetLaunchURL`):
//   - 404 `endpoint-not-found`     — Blueprint has no such endpoint
//   - 409 `sso-not-enabled`        — endpoint exists but ssoEnabled=false
//   - 503 `cluster-unavailable`    — k8s client / blueprint fetch failed
//
// Callers should catch + fall back to a direct-URL open or toast the error.
export interface LaunchURLResponse {
  url: string;
  expiresAt: string;
  endpoint?: string;
}

export const getLaunchURL = async (
  appId: string,
  endpoint?: string,
): Promise<LaunchURLResponse> => {
  const qs = endpoint ? `?endpoint=${encodeURIComponent(endpoint)}` : '';
  const path = `/catalyst/v1/apps/${encodeURIComponent(appId)}/launch-url${qs}`;
  const token = localStorage.getItem('org-token');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
  const res = await fetch(path, { headers });
  if (!res.ok) {
    let code = `${res.status}`;
    let message = res.statusText;
    try {
      const body = await res.json();
      if (body && typeof body === 'object') {
        code = body.code || code;
        message = body.message || message;
      }
    } catch { /* non-JSON body, keep status */ }
    throw new Error(`${res.status}:${code}:${message}`);
  }
  return res.json();
};

// Backing services (databases, caches, queues) inventory per Org. Merges
// catalog metadata with live pod status from provisioning so the console can
// render name/version/endpoint + a Running/Pending/Failed pill in one call.
export const getBackingServices = async (orgId: string): Promise<BackingService[]> => {
  const raw = await request<{ services: BackingService[] }>(
    `/organizations/${orgId}/backing-services`,
  );
  return raw.services || [];
};
export interface BackingService {
  id: string;
  slug: string;
  name: string;
  category: string;
  version: string;
  endpoint_host: string;
  endpoint_port: number;
  pod_status: string; // "Running" | "Pending" | "Failed" | "unknown" | "not_found"
  ready_replicas: number;
  total_replicas: number;
  image?: string;
}

// Types
export interface User { id: string; email: string; name: string; }
export interface Org {
  id: string; name: string; slug: string; plan_id: string;
  apps: string[]; status: string; created_at: string;
  app_states?: Record<string, string>;
}
export interface Member { id: string; user_id: string; email: string; role: string; joined_at: string; }
export interface Domain { id: string; domain: string; tld: string; subdomain: string; dns_status: string; }
export interface Subscription { id: string; plan_id: string; status: string; current_period_end: string; }
/** #85 — canonical money unit is baisa (1/1000 OMR). */
export interface Invoice { id: string; amount_baisa: number; status: string; created_at: string; }
export interface CreditEntry {
  id: string;
  amount_baisa: number;
  reason: string;
  order_id?: string;
  created_at: string;
}
export interface CreditBalance { credit_baisa: number; entries: CreditEntry[]; }
export interface ProvisionStep {
  name: string;
  status: string; // pending, running, completed, failed
  message?: string;
  started_at?: string;
  done_at?: string;
}
export interface Provision {
  id: string;
  /** Legacy BE wire key (provisioning service JSON) — type-erased, never
   *  accessed by console code; changes only in a BE+FE lockstep. */
  tenant_id: string;
  order_id?: string;
  plan_id?: string;
  apps?: string[];
  subdomain?: string;
  status: string; // pending, provisioning, completed, failed
  steps: ProvisionStep[];
  progress?: number;
  created_at?: string;
  updated_at?: string;
}
export interface JobStep {
  name: string;
  status: string; // pending, running, completed, failed
  message?: string;
  started_at?: string;
  done_at?: string;
}
export interface Job {
  id: string;
  /** Legacy BE wire keys (provisioning service JSON) — type-erased, never
   *  accessed by console code; change only in a BE+FE lockstep. */
  tenant_id: string;
  tenant_slug?: string;
  kind: 'install' | 'uninstall';
  app_slug: string;
  app_id?: string;
  app_name?: string;
  status: string; // pending, running, succeeded, failed
  steps: JobStep[];
  progress?: number;
  purged_services?: string[];
  retained_services?: string[];
  created_at?: string;
  updated_at?: string;
}
export interface UninstallPreviewService {
  slug: string;
  name: string;
}
export interface UninstallPreview {
  app_slug: string;
  app_name: string;
  installed: boolean;
  purged_services: UninstallPreviewService[];
  retained_services: UninstallPreviewService[];
  dependents: string[];
}
export interface Plan { id: string; slug: string; name: string; price_omr: number; }
export interface ConfigField {
  key: string;
  label: string;
  type: 'int' | 'string' | 'bool' | 'enum' | 'size';
  default?: any;
  min?: number;
  max?: number;
  options?: string[];
  description?: string;
  advanced?: boolean;
}
export interface CatalogApp {
  id: string;
  slug: string;
  name: string;
  icon: string;
  icon_bg: string;
  color: string;
  logo: string;
  category: string;
  tagline: string;
  description: string;
  free: boolean;
  system: boolean;
  kind: 'business' | 'service';
  shareable: boolean;
  dependencies: string[];
  config_schema: ConfigField[];
}
