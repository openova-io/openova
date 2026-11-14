/**
 * detectMode — resolves the Catalyst operating mode from the runtime
 * hostname, with a build-time VITE_CATALYST_MODE override for dev/CI.
 *
 * Mode contract:
 *
 *   console.openova.io          → 'catalyst-zero'  (Wizard UI, provisioning)
 *   console.<sov-fqdn>          → 'sovereign'       (Sovereign Console, Keycloak-gated)
 *
 * The VITE_CATALYST_MODE env var takes precedence over hostname so that
 * local dev and CI can force a specific mode without needing a real DNS.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), ONLY the
 * Catalyst-Zero apex hostname is hardcoded here — it is the one
 * invariant in the system.  Everything else derives from runtime data.
 *
 * Related: GitHub issue #607
 */

import type { CatalystMode } from '@/shared/constants/env'
import { VITE_CATALYST_MODE, VITE_SOVEREIGN_FQDN } from '@/shared/constants/env'

/** The one hardcoded hostname that is always Catalyst-Zero. */
const CATALYST_ZERO_HOSTNAME = 'console.openova.io'

export interface ModeResult {
  mode: CatalystMode
  /**
   * In Sovereign mode: the Sovereign's base FQDN derived from the hostname.
   * e.g. hostname `console.otech23.omani.works` → `otech23.omani.works`
   *
   * In catalyst-zero mode: null.
   */
  sovereignFQDN: string | null
}

/**
 * Derive the FQDN of the Sovereign from the given hostname.
 *
 * Convention:
 *   hostname = `console.<sovereignFQDN>`
 *   → sovereignFQDN = hostname.slice('console.'.length)
 *
 * We only strip the single leading `console.` segment — deeper nesting
 * is left intact so multi-label FQDNs work correctly:
 *   `console.otech23.omani.works` → `otech23.omani.works`
 *   `console.eu-west.acmecorp.com` → `eu-west.acmecorp.com`
 */
function sovereignFQDNFromHostname(hostname: string): string | null {
  // Strip port if present (e.g. localhost:5173 in dev)
  const host = hostname.split(':')[0]
  if (!host) return null
  const prefix = 'console.'
  if (host.startsWith(prefix)) {
    const fqdn = host.slice(prefix.length)
    return fqdn.length > 0 ? fqdn : null
  }
  return null
}

/**
 * Detect the current Catalyst operating mode.
 *
 * Call once at app startup — the result is stable for the lifetime of
 * the page load.  Prefer reading from the module-level export
 * `DETECTED_MODE` for components that need a single static value.
 */
export function detectMode(): ModeResult {
  // 1. Build-time override wins (dev / CI / test environments).
  if (VITE_CATALYST_MODE) {
    const sovereignFQDN =
      VITE_CATALYST_MODE === 'sovereign'
        ? (VITE_SOVEREIGN_FQDN ?? null)
        : null
    return { mode: VITE_CATALYST_MODE, sovereignFQDN }
  }

  // 2. Runtime hostname detection.
  const hostname =
    typeof window !== 'undefined' ? window.location.hostname : ''

  if (!hostname || hostname === CATALYST_ZERO_HOSTNAME) {
    return { mode: 'catalyst-zero', sovereignFQDN: null }
  }

  // localhost / 127.0.0.1 / bare IPs → treat as catalyst-zero (dev default)
  if (
    hostname === 'localhost' ||
    hostname === '127.0.0.1' ||
    /^\d+\.\d+\.\d+\.\d+$/.test(hostname)
  ) {
    return { mode: 'catalyst-zero', sovereignFQDN: null }
  }

  // Any other hostname → check for the `console.` prefix that marks a
  // Sovereign console deployment.
  const fqdn = sovereignFQDNFromHostname(hostname)
  if (fqdn) {
    return { mode: 'sovereign', sovereignFQDN: fqdn }
  }

  // Fallback: unknown hostname — default to catalyst-zero to avoid an
  // infinite redirect loop.
  return { mode: 'catalyst-zero', sovereignFQDN: null }
}

/** Module-level singleton resolved on first import. */
export const DETECTED_MODE: ModeResult = detectMode()
