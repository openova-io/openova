// Central URL configuration for the Catalyst UI.
// Never inline URLs elsewhere — import from here.
//
// Everything is derived from Vite's `base` config (see vite.config.ts).
// When the UI is served at https://console.openova.io/sovereign/, the
// Traefik ingress strips /sovereign before reaching this container's nginx,
// so fetch calls in components still need to be prefixed with /sovereign
// so the browser sends /sovereign/api/... from the /sovereign/ page.

/** Build-time base path from Vite, normalized to always end with '/'. */
const _rawBase = import.meta.env.BASE_URL
export const BASE: string = _rawBase.endsWith('/') ? _rawBase : `${_rawBase}/`

/** API root, scoped under the tier base so Nova + Sovereign don't collide. */
export const API_BASE: string = `${BASE}api`

/** Prepend base path to an in-tier route. Strips leading '/' from input. */
export const path = (p: string): string => `${BASE}${p.replace(/^\//, '')}`
