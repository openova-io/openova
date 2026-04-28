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

/** Pre-auth marketplace + checkout flow lives on its own subdomain. */
export const MARKETPLACE_URL = 'https://marketplace.openova.io';
export const CHECKOUT_URL = `${MARKETPLACE_URL}/checkout`;
export const MARKETPLACE_HOME_URL = `${MARKETPLACE_URL}/`;

/** Prepend base path to an in-tier route. Strips leading '/' from input. */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
