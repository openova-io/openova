/**
 * detectMode — resolves the Catalyst operating mode once, at start, from
 * the catalyst-api, with a build-time VITE_CATALYST_MODE override for dev
 * and CI.
 *
 * Source: GET /api/v1/sovereign/self. Its `mode` field is the value of
 * SOVEREIGN_MODE in the Sovereign's variables document, with the
 * contract default when the key is absent. The route answers 200 on a
 * Sovereign, with the Sovereign's name in `sovereignFQDN`, and 404
 * not-a-sovereign on the mothership; both bodies carry `mode`.
 *
 *   mode "mothership"  → 'catalyst-zero'  (Wizard UI, provisioning)
 *   mode "sovereign"   → 'sovereign'      (Sovereign Console)
 *
 * The host name is never read: the same image serves the mothership,
 * every Sovereign console and every Organization console.
 *
 * When the answer cannot be read (network error, time-out, a body
 * without a known mode) the result is 'catalyst-zero' with no FQDN, the
 * default this module has always used when it cannot tell.
 *
 * The request is made once, by the top-level await at the end of this
 * file, before any module that imports DETECTED_MODE runs. The answer is
 * not kept in browser storage.
 */

import type { CatalystMode } from '@/shared/constants/env'
import { VITE_CATALYST_MODE, VITE_SOVEREIGN_FQDN } from '@/shared/constants/env'
import { API_BASE } from '@/shared/config/urls'

export interface ModeResult {
  mode: CatalystMode
  /**
   * In Sovereign mode: the Sovereign's base FQDN, as the API names it
   * (`sovereignFQDN` of GET /api/v1/sovereign/self), e.g.
   * `otech23.omani.works`. Null when the API names none.
   *
   * In catalyst-zero mode: null.
   */
  sovereignFQDN: string | null
}

/** The path of the route that names the mode, under API_BASE. */
export const SELF_PATH = '/v1/sovereign/self'

/** How long the start-up request may take before the default applies. */
export const SELF_TIMEOUT_MS = 5000

function cannotTell(): ModeResult {
  return { mode: 'catalyst-zero', sovereignFQDN: null }
}

/**
 * Map the JSON body of GET /api/v1/sovereign/self to a ModeResult.
 * The HTTP status is not used: the 200 and the 404 answer both carry
 * `mode`.
 */
export function modeFromSelf(body: unknown): ModeResult {
  if (typeof body !== 'object' || body === null) return cannotTell()
  const { mode, sovereignFQDN } = body as {
    mode?: unknown
    sovereignFQDN?: unknown
  }
  if (mode === 'mothership') {
    return { mode: 'catalyst-zero', sovereignFQDN: null }
  }
  if (mode === 'sovereign') {
    const fqdn =
      typeof sovereignFQDN === 'string' && sovereignFQDN.trim() !== ''
        ? sovereignFQDN.trim()
        : null
    return { mode: 'sovereign', sovereignFQDN: fqdn }
  }
  return cannotTell()
}

/**
 * Detect the current Catalyst operating mode.
 *
 * Called once, at the end of this file. Components read the module-level
 * export `DETECTED_MODE`.
 */
export async function detectMode(): Promise<ModeResult> {
  // 1. Build-time override wins (dev / CI / test environments).
  if (VITE_CATALYST_MODE) {
    const sovereignFQDN =
      VITE_CATALYST_MODE === 'sovereign'
        ? (VITE_SOVEREIGN_FQDN ?? null)
        : null
    return { mode: VITE_CATALYST_MODE, sovereignFQDN }
  }

  // 2. The catalyst-api names the mode.
  try {
    const res = await fetch(`${API_BASE}${SELF_PATH}`, {
      method: 'GET',
      headers: { Accept: 'application/json' },
      signal: AbortSignal.timeout(SELF_TIMEOUT_MS),
    })
    return modeFromSelf(await res.json())
  } catch {
    return cannotTell()
  }
}

/** Module-level singleton, resolved once before any importer runs. */
export const DETECTED_MODE: ModeResult = await detectMode()
