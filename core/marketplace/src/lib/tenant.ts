// Tenant-aware branding for the marketplace.
// One pod, multiple ingress hostnames — branding switches client-side
// based on window.location.hostname so we don't need extra deployments.
//
// Default tenant ('openova') matches the historical OpenOva brand.
//
// #3376 (Refs #3691): the per-partner brand table is DATA, injected at
// runtime via `window.__SME_BRANDS__` (seeded from Sovereign config by the
// page bootstrap), never a source literal. The previous build hardcoded a
// `'omantel.openova.io'` entry — a banned mothership/partner literal that
// shipped into (and was served by) EVERY franchised bundle. The franchised
// bundle now carries NO partner literal; a Sovereign that wants partner
// chrome supplies it through config.

export type TenantId = string;

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

const DEFAULT_TENANT: TenantConfig = {
  id: 'openova',
  brand: 'OpenOva',
  logoSrc: '',
};

/**
 * Runtime brand table injected by the page bootstrap (Layout.astro) from
 * Sovereign config. Keyed by marketplace hostname. Empty in the shipped
 * bundle — no partner literals baked into source.
 */
function runtimeBrands(): Record<string, TenantConfig> {
  if (typeof window === 'undefined') return {};
  const w = window as unknown as { __SME_BRANDS__?: Record<string, TenantConfig> };
  return (w.__SME_BRANDS__ && typeof w.__SME_BRANDS__ === 'object') ? w.__SME_BRANDS__ : {};
}

/** Resolve tenant from a hostname (lowercased). Falls back to OpenOva. */
export function tenantForHost(host: string | undefined | null): TenantConfig {
  if (!host) return DEFAULT_TENANT;
  return runtimeBrands()[host.toLowerCase()] ?? DEFAULT_TENANT;
}

/** Browser-side helper. Safe to call from Svelte $effect / Astro is:inline. */
export function currentTenant(): TenantConfig {
  if (typeof window === 'undefined') return DEFAULT_TENANT;
  return tenantForHost(window.location.hostname);
}
