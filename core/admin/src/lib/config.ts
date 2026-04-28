// Central URL configuration for the admin app.
// Never inline URLs elsewhere — import from here.
// See ~/.claude/projects/.../memory/feedback_never_hardcode_urls.md

/** Build-time base path from Astro's `base` config, normalized to always
 *  end with '/'. Astro's BASE_URL sometimes comes without the trailing slash
 *  (e.g., '/nova') and string-concat would produce `/novafoo`. */
const _rawBase = import.meta.env.BASE_URL;
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`;

/** API root, scoped under the tier base. */
export const API_BASE: string = `${BASE}api`;

/** Prepend base path to an in-tier route. Strips leading '/' from input. */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
