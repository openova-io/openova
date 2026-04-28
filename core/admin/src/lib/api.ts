import { API_BASE, path as routePath } from './config';

// Dispatched whenever admin auth state changes (login, logout, token
// refresh). Components in the same tab cannot rely on the native `storage`
// event — it only fires for cross-tab writes — so they listen for this
// custom event instead. Mirrors the marketplace pattern from #51 (#83).
export const AUTH_CHANGED_EVENT = 'sme-admin-auth-changed';

export function notifyAuthChanged(): void {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new Event(AUTH_CHANGED_EVENT));
}

export function setAuthTokens(token: string, refreshToken: string): void {
  localStorage.setItem('sme-admin-token', token);
  if (refreshToken) localStorage.setItem('sme-admin-refresh-token', refreshToken);
  notifyAuthChanged();
}

let refreshing: Promise<void> | null = null;

async function tryRefresh(): Promise<void> {
  const rt = localStorage.getItem('sme-admin-refresh-token');
  if (!rt) return;
  try {
    const res = await fetch(`${API_BASE}/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: rt }),
    });
    if (res.ok) {
      const data = await res.json();
      // /auth/refresh returns { token, refresh_token, user } — admin uses
      // the same shape as the other apps so the response is interchangeable.
      setAuthTokens(data.token, data.refresh_token);
    } else {
      localStorage.removeItem('sme-admin-token');
      localStorage.removeItem('sme-admin-refresh-token');
      notifyAuthChanged();
    }
  } catch {
    localStorage.removeItem('sme-admin-token');
    localStorage.removeItem('sme-admin-refresh-token');
    notifyAuthChanged();
  }
}

async function request<T>(path: string, opts?: RequestInit): Promise<T> {
  const token = localStorage.getItem('sme-admin-token');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
  const res = await fetch(`${API_BASE}${path}`, { ...opts, headers });
  // On 401, try one silent refresh before bouncing to /login (#84). Admin
  // JWTs expire after 15 min — without this the superadmin is forced to
  // re-enter the password every time they step away from their desk.
  if (res.status === 401 && token && path !== '/auth/refresh') {
    if (!refreshing) {
      refreshing = tryRefresh().finally(() => { refreshing = null; });
    }
    await refreshing;
    const newToken = localStorage.getItem('sme-admin-token');
    if (newToken && newToken !== token) {
      const retryHeaders: Record<string, string> = {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${newToken}`,
      };
      const retry = await fetch(`${API_BASE}${path}`, { ...opts, headers: retryHeaders });
      if (retry.status === 401) {
        localStorage.removeItem('sme-admin-token');
        localStorage.removeItem('sme-admin-refresh-token');
        notifyAuthChanged();
        if (typeof window !== 'undefined' && !window.location.pathname.startsWith(routePath('login'))) {
          window.location.href = routePath('login');
        }
        throw new Error('Unauthorized');
      }
      if (!retry.ok) throw new Error(`${retry.status}: ${await retry.text()}`);
      return retry.json();
    }
    // Refresh failed — fall through to the normal 401 handling below.
    if (typeof window !== 'undefined' && !window.location.pathname.startsWith(routePath('login'))) {
      window.location.href = routePath('login');
    }
    throw new Error('Unauthorized');
  }
  if (res.status === 401) {
    localStorage.removeItem('sme-admin-token');
    localStorage.removeItem('sme-admin-refresh-token');
    notifyAuthChanged();
    if (typeof window !== 'undefined' && !window.location.pathname.startsWith(routePath('login'))) {
      window.location.href = routePath('login');
    }
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

export const logout = () => {
  const rt = localStorage.getItem('sme-admin-refresh-token');
  if (rt) {
    fetch(`${API_BASE}/auth/logout`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: rt }),
      keepalive: true,
    }).catch(() => {});
  }
  localStorage.removeItem('sme-admin-token');
  localStorage.removeItem('sme-admin-refresh-token');
  notifyAuthChanged();
  window.location.href = routePath('login');
};

// Revoke every refresh token for the current user — "sign out everywhere"
// (#88). Other tabs/devices will bounce to /login on their next API call.
export const logoutAll = () =>
  request<{ message: string }>('/auth/logout-all', { method: 'POST' });

// Admin Catalog CRUD
// Kept in sync with apps/marketplace/src/lib/api.ts so admin renders the
// same GitHub-avatar logos as the public marketplace.
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

export const getApps = async (): Promise<App[]> => {
  const raw = await request<any[]>('/catalog/apps');
  return raw.map(a => ({
    id: a.id, name: a.name, slug: a.slug,
    tagline: a.tagline || '',
    description: a.description || a.tagline || '',
    category: a.category || '', icon: a.icon || a.name[0], color: a.icon_bg || a.color || '#3b82f6',
    logo: appLogos[a.slug] || '',
    free: a.free ?? true,
    dependencies: a.dependencies || [],
    system: a.system ?? false,
    kind: a.kind || (a.system ? 'service' : 'business'),
    shareable: a.shareable ?? false,
    config_schema: a.config_schema || [],
  }));
};
export const createApp = (app: Partial<App>) =>
  request<App>('/catalog/admin/apps', { method: 'POST', body: JSON.stringify(app) });
export const updateApp = (id: string, app: Partial<App>) =>
  request<App>(`/catalog/admin/apps/${id}`, { method: 'PUT', body: JSON.stringify(app) });
export const deleteApp = (id: string) =>
  request<void>(`/catalog/admin/apps/${id}`, { method: 'DELETE' });

export const getPlans = async (): Promise<Plan[]> => {
  const raw = await request<any[]>('/catalog/plans');
  return raw.map(p => ({
    id: p.id, slug: p.slug || '', name: p.name, description: p.description || '',
    monthly_price: p.price_omr || 0,
    resources: { cpu: p.cpu || '', memory: p.memory || '', storage: p.storage || '' },
    popular: p.popular || false, sort_order: p.sort_order || 0,
    features: p.features || [], stripe_price_id: p.stripe_price_id || '',
  }));
};
export const createPlan = (plan: Partial<Plan>) =>
  request<Plan>('/catalog/admin/plans', { method: 'POST', body: JSON.stringify(plan) });
export const updatePlan = (id: string, plan: Partial<Plan>) =>
  request<Plan>(`/catalog/admin/plans/${id}`, { method: 'PUT', body: JSON.stringify(plan) });
export const deletePlan = (id: string) =>
  request<void>(`/catalog/admin/plans/${id}`, { method: 'DELETE' });

export const getIndustries = async (): Promise<Industry[]> => {
  const raw = await request<any[]>('/catalog/industries');
  return raw.map(i => ({
    id: i.id, slug: i.slug || '', name: i.name, icon: i.emoji || i.icon || '',
    description: i.description || '',
    app_ids: i.suggested_apps || i.app_ids || [],
  }));
};
export const createIndustry = (ind: Partial<Industry>) =>
  request<Industry>('/catalog/admin/industries', { method: 'POST', body: JSON.stringify(ind) });
export const updateIndustry = (id: string, ind: Partial<Industry>) =>
  request<Industry>(`/catalog/admin/industries/${id}`, { method: 'PUT', body: JSON.stringify(ind) });
export const deleteIndustry = (id: string) =>
  request<void>(`/catalog/admin/industries/${id}`, { method: 'DELETE' });

export const getAddons = async (): Promise<AddOn[]> => {
  const raw = await request<any[]>('/catalog/addons');
  return raw.map(a => ({
    id: a.id, slug: a.slug || '', name: a.name, description: a.description || '',
    monthly_price: a.price_omr || 0, included: a.included ?? false,
    category: a.category || '',
  }));
};
export const createAddon = (addon: Partial<AddOn>) =>
  request<AddOn>('/catalog/admin/addons', { method: 'POST', body: JSON.stringify(addon) });
export const updateAddon = (id: string, addon: Partial<AddOn>) =>
  request<AddOn>(`/catalog/admin/addons/${id}`, { method: 'PUT', body: JSON.stringify(addon) });
export const deleteAddon = (id: string) =>
  request<void>(`/catalog/admin/addons/${id}`, { method: 'DELETE' });

// Admin Tenants
export const getAdminTenants = (page?: number, pageSize = 20) => {
  const offset = ((page || 1) - 1) * pageSize;
  return request<{ tenants: Tenant[]; total: number; offset: number; limit: number }>(
    `/tenant/admin/tenants?offset=${offset}&limit=${pageSize}`,
  );
};
export const updateTenantStatus = (id: string, status: string) =>
  request<Tenant>(`/tenant/admin/tenants/${id}/status`, {
    method: 'PUT',
    body: JSON.stringify({ status }),
  });
export const adminDeleteTenant = (id: string) =>
  request<{ status: string }>(`/tenant/admin/tenants/${id}`, { method: 'DELETE' });

// Backing services (databases/caches) per tenant — superadmin view.
export const getAdminBackingServices = async (id: string): Promise<BackingService[]> => {
  const raw = await request<{ services: BackingService[] }>(
    `/tenant/admin/tenants/${id}/backing-services`,
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
  pod_status: string;
  ready_replicas: number;
  total_replicas: number;
  image?: string;
}

// Admin Revenue
//
// #85 — the admin surface now normalises to baisa at the API boundary. The
// revenue summary emits `total_mrr` as whole OMR (legacy), we upconvert to
// `total_mrr_baisa` here so `formatOMR` in components receives the canonical
// unit. Order amounts prefer `amount_baisa` but fall back to `amount_omr *
// 1000` for databases predating the column.
export const getRevenue = async (): Promise<Revenue> => {
  const raw = await request<any>('/billing/admin/revenue');
  const mrr = typeof raw.total_mrr === 'number' ? raw.total_mrr : 0;
  return {
    total_mrr_baisa: typeof raw.total_mrr_baisa === 'number'
      ? raw.total_mrr_baisa
      : mrr * 1000,
    total_customers: raw.total_customers ?? 0,
    active_subscriptions: raw.active_subscriptions ?? 0,
  };
};
export const getAdminOrders = async (): Promise<Order[]> => {
  const raw = await request<any[]>('/billing/admin/orders');
  return (raw ?? []).map(o => ({
    id: o.id,
    tenant_id: o.tenant_id,
    plan_id: o.plan_id,
    amount_baisa: typeof o.amount_baisa === 'number'
      ? o.amount_baisa
      : (typeof o.amount_omr === 'number' ? o.amount_omr * 1000 : 0),
    status: o.status || 'unknown',
    created_at: o.created_at,
    // #91 — the backend LEFT JOINs promo_codes and sets promo_deleted when the
    // associated promo has been soft-deleted. The UI renders a "deleted" pill
    // so historical orders stay visible without looking "active".
    promo_code: typeof o.promo_code === 'string' && o.promo_code ? o.promo_code : undefined,
    promo_deleted: Boolean(o.promo_deleted),
  }));
};

// Admin Billing Settings (Stripe keys)
export const getBillingSettings = () =>
  request<BillingSettings>('/billing/admin/settings');
export const updateBillingSettings = (settings: Partial<BillingSettingsInput>) =>
  request<{ ok: boolean }>('/billing/admin/settings', {
    method: 'PUT',
    body: JSON.stringify(settings),
  });

// Admin Promo Codes
export const listPromoCodes = () => request<PromoCode[]>('/billing/admin/promos');
export const upsertPromoCode = (p: Partial<PromoCode>) =>
  request<PromoCode>('/billing/admin/promos', { method: 'POST', body: JSON.stringify(p) });
export const deletePromoCode = (code: string) =>
  request<{ ok: boolean }>(`/billing/admin/promos/${encodeURIComponent(code)}`, {
    method: 'DELETE',
  });

// Types
export interface User { id: string; email: string; name: string; role: string; }
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
  dependencies?: string[];
  system?: boolean;
  kind?: 'business' | 'service';
  shareable?: boolean;
  config_schema?: ConfigField[];
}
export interface Plan { id: string; slug: string; name: string; description: string; monthly_price: number; resources: { cpu: string; memory: string; storage: string }; popular: boolean; sort_order: number; features: string[]; stripe_price_id: string; }
export interface Industry { id: string; slug: string; name: string; icon: string; description: string; app_ids: string[]; }
export interface AddOn { id: string; slug: string; name: string; description: string; monthly_price: number; included: boolean; category: string; }
export interface Tenant { id: string; name: string; slug: string; plan_id: string; status: string; created_at: string; member_count: number; }
export interface Revenue { total_mrr_baisa: number; total_customers: number; active_subscriptions: number; }
export interface Order {
  id: string;
  tenant_id: string;
  plan_id: string;
  amount_baisa: number;
  status: string;
  created_at: string;
  /** Promo code applied at checkout, if any (#91). */
  promo_code?: string;
  /** True when the associated promo has been soft-deleted (#91). */
  promo_deleted?: boolean;
}
export interface BillingSettings {
  stripe_secret_key_configured: boolean;
  stripe_webhook_secret_configured: boolean;
  stripe_secret_key_last4: string;
  stripe_webhook_secret_last4: string;
  stripe_public_key: string;
  updated_at: string;
}
export interface BillingSettingsInput {
  stripe_secret_key: string;
  stripe_webhook_secret: string;
  stripe_public_key: string;
}
export interface PromoCode {
  code: string;
  credit_omr: number;
  description: string;
  active: boolean;
  max_redemptions: number;
  times_redeemed: number;
  created_at: string;
}
