export type AppMode = 'saas' | 'selfhosted'

export const APP_MODE: AppMode =
  (import.meta.env['VITE_APP_MODE'] as AppMode) ?? 'saas'

export const IS_SAAS = APP_MODE === 'saas'
export const IS_SELFHOSTED = APP_MODE === 'selfhosted'
export const APP_VERSION = import.meta.env['VITE_APP_VERSION'] ?? 'dev'

/**
 * Catalyst operating mode.
 *
 * - 'catalyst-zero': Running on Catalyst-Zero, the mothership, where new
 *   Sovereigns are provisioned via the Wizard. Without
 *   VITE_CATALYST_MODE this is the mode when GET /api/v1/sovereign/self
 *   answers mode "mothership", or cannot be read (see detectMode.ts).
 * - 'sovereign': Running on console.<sov-fqdn> — Sovereign Console; the
 *   operator manages their own Sovereign. Keycloak auth gates all routes.
 *
 * Override via VITE_CATALYST_MODE in .env.local for local dev:
 *   VITE_CATALYST_MODE=sovereign VITE_SOVEREIGN_FQDN=otech23.omani.works npm run dev
 */
export type CatalystMode = 'catalyst-zero' | 'sovereign'

/** Build-time override (dev / CI). Without it the mode is read from the catalyst-api at start. */
export const VITE_CATALYST_MODE = import.meta.env['VITE_CATALYST_MODE'] as
  | CatalystMode
  | undefined

/**
 * Build-time Sovereign FQDN override — used in Sovereign mode dev/CI
 * when the hostname can't be read from window.location (SSR, jsdom).
 * Example: VITE_SOVEREIGN_FQDN=otech23.omani.works
 */
export const VITE_SOVEREIGN_FQDN = import.meta.env['VITE_SOVEREIGN_FQDN'] as
  | string
  | undefined
