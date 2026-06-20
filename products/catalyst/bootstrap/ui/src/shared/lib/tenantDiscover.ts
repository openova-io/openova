/**
 * tenantDiscover — host-driven tenant discovery for the unified
 * Sovereign Console SPA (issue #802, #795 [Q-mine-1]).
 *
 * The same SPA bundle serves both otech-admin (`console.<otech-fqdn>`)
 * and Organization-admin (`console.<org-domain>` — free-subdomain
 * `console.acme.<otech-fqdn>` OR BYO domain `console.acme.com`).
 * Tenant context is discovered from `window.location.host` against
 * the back-end's tenant registry — NOT from path/subdomain string
 * parsing — so a BYO CNAME-fronted domain resolves the same way as a
 * platform-hosted subdomain.
 *
 * Bootstrap order (called from main.tsx before `router.start()`):
 *
 *   1. Read `window.location.host`.
 *   2. Call `GET /api/v1/tenant/discover?host=<host>`.
 *   3. On 200 — store the discovery payload in module state and let
 *      the OIDC client read `keycloak_realm_url` + `keycloak_client_id`
 *      to start the redirect against the right realm.
 *   4. On 404 — host is not registered. Show the SPA's generic
 *      "unknown tenant" landing.
 *   5. On network failure — render the catalyst-zero default surface.
 *      The SPA must NEVER assume otech vs Organization without an authoritative
 *      registry response.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the
 * registry endpoint is composed via `apiUrl()` from
 * `shared/config/urls`, never inlined here.
 */

import { apiUrl } from '@/shared/config/urls'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import {
  parseTenantID,
  parseTenantKind,
  type TenantDiscovery,
} from '@/shared/types/tenant'

/**
 * Possible terminal states of bootstrap discovery.
 *
 *   • 'discovered' — host is registered; payload available.
 *   • 'unknown'    — host is not registered (404). SPA renders the
 *                    generic landing.
 *   • 'unwired'    — the back-end's tenant registry is not wired
 *                    (503). Treat as 'unknown' for UX purposes; log
 *                    once for ops.
 *   • 'error'      — network or parse failure. Surfaced for
 *                    diagnostics; SPA continues on the catalyst-zero
 *                    default path.
 */
export type DiscoveryStatus = 'discovered' | 'unknown' | 'unwired' | 'error'

export interface DiscoveryResult {
  status: DiscoveryStatus
  /** Discovery payload — present iff `status === 'discovered'`. */
  tenant?: TenantDiscovery
  /** Failure detail — present on 'error' for diagnostics. */
  error?: string
}

/**
 * Resolve a host against the back-end tenant registry. Pure function:
 * no side effects, no module state — safe to call from tests.
 *
 * `fetchImpl` is an injectable seam so unit tests can stub the
 * network without monkey-patching global fetch.
 */
export async function discoverTenant(
  host: string,
  fetchImpl: typeof fetch = fetch,
): Promise<DiscoveryResult> {
  if (!host || host.trim().length === 0) {
    return { status: 'error', error: 'host argument is empty' }
  }

  const url = apiUrl(`/v1/tenant/discover?host=${encodeURIComponent(host)}`)
  let res: Response
  try {
    res = await fetchImpl(url, {
      method: 'GET',
      headers: { Accept: 'application/json' },
    })
  } catch (err) {
    return {
      status: 'error',
      error: err instanceof Error ? err.message : String(err),
    }
  }

  if (res.status === 404) return { status: 'unknown' }
  if (res.status === 503) return { status: 'unwired' }
  if (!res.ok) {
    return { status: 'error', error: `discover HTTP ${res.status}` }
  }

  try {
    const raw = (await res.json()) as Record<string, unknown>
    const tenant: TenantDiscovery = {
      host: typeof raw.host === 'string' ? raw.host : host,
      tenant_id: parseTenantID(raw.tenant_id),
      tenant_kind: parseTenantKind(raw.tenant_kind),
      keycloak_realm_url:
        typeof raw.keycloak_realm_url === 'string' ? raw.keycloak_realm_url : '',
      keycloak_client_id:
        typeof raw.keycloak_client_id === 'string' ? raw.keycloak_client_id : '',
    }
    return { status: 'discovered', tenant }
  } catch (err) {
    return {
      status: 'error',
      error: err instanceof Error ? err.message : String(err),
    }
  }
}

/**
 * Module-singleton state. The bootstrap path (called once from
 * main.tsx) writes here; route components read via `getTenantContext()`
 * to pick which route tree to mount.
 */
let cachedResult: DiscoveryResult | null = null

/**
 * Run discovery once at app boot. Idempotent — repeated calls with
 * the same host return the cached value. Components that need
 * synchronous access after the bootstrap can read
 * `getTenantContext()`.
 *
 * Skipped on Sovereign chroot mode (TC-229, 2026-05-07): the
 * `/api/v1/tenant/discover` endpoint is mother-only — only the
 * Catalyst-Zero apex (`console.openova.io`) registers it. A chroot
 * Sovereign Console (e.g. `console.<sov-fqdn>`) IS its own tenant by
 * definition, so calling tenant-discover there is both meaningless
 * and noisy: the chroot's catalyst-api returns 404 on every request
 * and the SPA's React-Query layer (DashboardLayout etc.) keeps
 * re-issuing the call as the Dashboard mounts/unmounts — 50+ HTTP
 * 404 spam per navigation was observed live on console.omantel.biz.
 *
 * Short-circuit returns `status: 'unwired'` (the contract for "no
 * registry available; proceed on the host's own identity") and
 * caches it so a second call is a no-op. Downstream code that
 * inspects `getTenantContext()` (sidebar nav, OIDC bootstrap) treats
 * unwired the same as a successful no-op on the chroot path —
 * tenant identity is already encoded in the session JWT
 * (sovereign_fqdn / deployment_id claims) so no registry payload is
 * needed for any chroot UI.
 */
export async function bootstrapTenant(
  host: string = typeof window !== 'undefined' ? window.location.host : '',
  fetchImpl: typeof fetch = fetch,
): Promise<DiscoveryResult> {
  if (cachedResult) return cachedResult
  if (DETECTED_MODE.mode === 'sovereign') {
    cachedResult = { status: 'unwired' }
    return cachedResult
  }
  cachedResult = await discoverTenant(host, fetchImpl)
  return cachedResult
}

/** Synchronous accessor for code paths that run after bootstrap. */
export function getTenantContext(): DiscoveryResult | null {
  return cachedResult
}

/** Test-only reset hook — lets a test reset module state between cases. */
export function _resetTenantContextForTesting(): void {
  cachedResult = null
}
