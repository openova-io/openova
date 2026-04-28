const API_BASE = '/api';

let refreshing: Promise<void> | null = null;

// Dispatched whenever the marketplace auth state changes (login, logout,
// token refresh, active-org switch). Components in the same tab cannot rely
// on the native `storage` event — it only fires for cross-tab writes — so
// they listen for this custom event instead.
export const AUTH_CHANGED_EVENT = 'sme-auth-changed';

export function notifyAuthChanged(): void {
  if (typeof window === 'undefined') return;
  window.dispatchEvent(new Event(AUTH_CHANGED_EVENT));
}

export function setAuthTokens(token: string, refreshToken: string): void {
  localStorage.setItem('sme-token', token);
  localStorage.setItem('sme-refresh-token', refreshToken);
  notifyAuthChanged();
}

export function setActiveOrg(orgId: string): void {
  localStorage.setItem('sme-active-org', orgId);
  notifyAuthChanged();
}

async function request<T>(path: string, opts?: RequestInit): Promise<T> {
  const token = localStorage.getItem('sme-token');
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
    const newToken = localStorage.getItem('sme-token');
    if (newToken && newToken !== token) {
      const retryHeaders: Record<string, string> = {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${newToken}`,
      };
      const retry = await fetch(`${API_BASE}${path}`, { ...opts, headers: retryHeaders });
      if (!retry.ok) {
        const body = await retry.text();
        throw new Error(`${retry.status}: ${body}`);
      }
      return retry.json();
    }
  }
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status}: ${body}`);
  }
  return res.json();
}

async function tryRefresh(): Promise<void> {
  const rt = localStorage.getItem('sme-refresh-token');
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
      localStorage.removeItem('sme-token');
      localStorage.removeItem('sme-refresh-token');
      notifyAuthChanged();
    }
  } catch {
    // Refresh failed — clear tokens.
    localStorage.removeItem('sme-token');
    localStorage.removeItem('sme-refresh-token');
    notifyAuthChanged();
  }
}

// Catalog
export const getPlans = async (): Promise<Plan[]> => {
  const raw = await request<any[]>('/catalog/plans');
  return raw.map(p => ({
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
  const raw = await request<any[]>('/catalog/apps');
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
  const rt = localStorage.getItem('sme-refresh-token');
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
  localStorage.removeItem('sme-token');
  localStorage.removeItem('sme-refresh-token');
  localStorage.removeItem('sme-active-org');
  localStorage.removeItem('sme-cart');
  localStorage.removeItem('sme-checkout-tenant');
  for (let i = localStorage.length - 1; i >= 0; i--) {
    const k = localStorage.key(i);
    if (k && k.startsWith('sme-tenant:')) localStorage.removeItem(k);
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
}

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  plan_id: string;
  apps: string[];
  status: string;
}

export interface CheckoutRequest {
  plan_id: string;
  apps: string[];
  addons: string[];
  tenant_id: string;
  promo_code?: string;
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
  status: string;
  steps: ProvisionStep[];
}

export interface ProvisionStep {
  name: string;
  status: string;
  started_at?: string;
  completed_at?: string;
}
