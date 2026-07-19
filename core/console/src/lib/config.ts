// Central URL configuration for the console app.
// Never inline URLs elsewhere — import from here.
// See ~/.claude/projects/.../memory/feedback_never_hardcode_urls.md

/** Build-time base path from Astro's `base` config, normalized to always
 *  end with '/'. Astro's BASE_URL sometimes comes without the trailing slash
 *  (e.g., '/nova') and string-concat like `${BASE}foo` would produce `/novafoo`.
 *  Normalizing here keeps callers honest. */
const _rawBase = import.meta.env.BASE_URL;
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`;

/** API root, scoped under the tier base so Nova + Sovereign don't collide on '/api'. */
export const API_BASE: string = `${BASE}api`;

/** Resolve the marketplace origin at runtime.
 *
 *  TBD-A68 (#1994, 2026-05-19): the pre-fix value was hardcoded to
 *  `https://marketplace.openova.io`, which sent every Sovereign Org
 *  (running at `console.<slug>.<sovFQDN>` — e.g. `console.acme.omani.homes`)
 *  back to the mothership marketplace instead of THEIR Sovereign's
 *  marketplace. Result: a redirect into Catalyst-Zero's storefront
 *  with no Org context, dead-ending sign-in and checkout.
 *
 *  Resolution order:
 *
 *   1. Astro public env `PUBLIC_MARKETPLACE_ORIGIN` if set at build time
 *      (per-Sovereign overlays may stamp this).
 *   2. Runtime: derive from `window.location.host` — strip the leading
 *      `console.<slug>?.` prefix and prepend `marketplace.`. Examples:
 *        console.acme.omani.homes  → marketplace.omani.homes
 *        console.omani.works       → marketplace.omani.works
 *        console.openova.io        → marketplace.openova.io
 *      The function tolerates a missing `console.` prefix by falling
 *      through to `marketplace.<host>` which keeps dev / preview hosts
 *      addressable.
 *   3. SSR/build-time fallback: `https://marketplace.openova.io` —
 *      only ever rendered when the bundle is consumed outside a
 *      browser context (Astro SSG snapshot). At hydration the runtime
 *      origin takes over.
 */
function resolveMarketplaceOrigin(): string {
  const envOrigin = (import.meta as { env?: Record<string, string | undefined> }).env?.PUBLIC_MARKETPLACE_ORIGIN;
  if (envOrigin && envOrigin.length > 0) return envOrigin.replace(/\/$/, '');
  if (typeof window !== 'undefined' && window.location && window.location.host) {
    const host = window.location.host;
    let zone = host;
    if (host.startsWith('console.')) {
      const rest = host.slice('console.'.length);
      // Drop one org-slug label if there's room (slug.parent.tld → parent.tld).
      // A bare `console.<tld>` (no slug) keeps `<tld>` so dev hosts work.
      const dot = rest.indexOf('.');
      zone = dot >= 0 ? rest.slice(dot + 1) : rest;
      if (!zone) zone = rest;
    }
    return `${window.location.protocol}//marketplace.${zone}`;
  }
  return 'https://marketplace.openova.io';
}

/** Pre-auth marketplace + checkout flow. Lazy getters so SSR build
 *  snapshots don't bake `window.location` (would crash Node) and so
 *  consumers always see the runtime-resolved origin after hydration. */
export const MARKETPLACE_URL: string = resolveMarketplaceOrigin();
export const CHECKOUT_URL: string = `${MARKETPLACE_URL}/checkout`;
export const MARKETPLACE_HOME_URL: string = `${MARKETPLACE_URL}/`;

/** Prepend base path to an in-tier route. Strips leading '/' from input. */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
