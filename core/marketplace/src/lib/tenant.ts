// Tenant-aware branding for the marketplace.
// One pod, multiple ingress hostnames — branding switches client-side
// based on window.location.hostname so we don't need extra deployments.
//
// Default tenant ('openova') matches the historical OpenOva brand. Adding
// a new tenant = one entry in TENANTS keyed by hostname. Logo SVG is loaded
// from /logos/<tenant>.svg in the public/ folder; place the file there.

export type TenantId = 'openova' | 'omantel';

export interface TenantConfig {
  id: TenantId;
  /** Display name shown next to the logo and in <title>. */
  brand: string;
  /** Path under the marketplace's BASE_URL. Empty string for the inline default OpenOva logo. */
  logoSrc: string;
  /** Force a specific theme on first load when no user preference is stored. */
  forceTheme?: 'light' | 'dark';
  /** Skip the "returning user → console" auto-redirect. Demo tenants want
   *  visitors to land on the marketplace, not bounce to the Nova console. */
  skipConsoleRedirect?: boolean;
}

const TENANTS: Record<string, TenantConfig> = {
  'omantel.openova.io': {
    id: 'omantel',
    brand: 'Omantel Cloud',
    logoSrc: '/logos/omantel.svg',
    forceTheme: 'light',
    skipConsoleRedirect: true,
  },
};

const DEFAULT_TENANT: TenantConfig = {
  id: 'openova',
  brand: 'OpenOva',
  logoSrc: '',
};

/** Resolve tenant from a hostname (lowercased). Falls back to OpenOva. */
export function tenantForHost(host: string | undefined | null): TenantConfig {
  if (!host) return DEFAULT_TENANT;
  return TENANTS[host.toLowerCase()] ?? DEFAULT_TENANT;
}

/** Browser-side helper. Safe to call from Svelte $effect / Astro is:inline. */
export function currentTenant(): TenantConfig {
  if (typeof window === 'undefined') return DEFAULT_TENANT;
  return tenantForHost(window.location.hostname);
}
