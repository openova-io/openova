// Central URL configuration for the marketplace app.
// Never inline URLs elsewhere — import from here.
// See ~/.claude/projects/.../memory/feedback_never_hardcode_urls.md

/** Build-time base path, normalized to always end with '/'. */
const _rawBase = import.meta.env.BASE_URL;
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`;

/** API root (served at marketplace.openova.io/api/). */
export const API_BASE: string = `${BASE}api`;

/** Post-auth Nova customer console. All references to the customer dashboard
 *  go through here so the marketplace never hardcodes a cross-host URL. */
export const CONSOLE_URL = 'https://console.openova.io/nova';

/** Build a URL into the Nova console with optional token/refresh handoff
 *  query params — used when marketplace hands a signed-in session to the
 *  console (post-checkout and from Header "Portal" link). */
export const consoleHref = (
  path: string = '',
  params?: Record<string, string>,
): string => {
  const suffix = path ? (path.startsWith('/') ? path : `/${path}`) : '';
  const qs = params && Object.keys(params).length
    ? '?' + new URLSearchParams(params).toString()
    : '';
  return `${CONSOLE_URL}${suffix}${qs}`;
};

/** Prepend base to an internal marketplace route (strip leading '/'). */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`;
