/**
 * Branded `TenantID` + `TenantKind` types — single source of truth for
 * the tenant identity the SPA discovers via `/api/v1/tenant/discover`.
 *
 * Why branded types: the same Sovereign Console SPA bundle serves
 * BOTH otech-admin AND Organization-admin views (issue #802, #795 [Q-mine-1]).
 * Tenant context is read from `window.location.host` and resolved
 * against the tenant registry on the back end. Threading the tenant
 * id through every API call as a free-form `string` invites
 * cross-tenant leaks at compile time — branded types refuse to land
 * a value that wasn't routed through `parseTenantID()` first.
 *
 * Mirrors the pattern established for `DeploymentID` in
 * `shared/types/deployment.ts` (issue #749).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (never compromise on quality)
 * the parsers reject the empty string and any non-string input — the
 * shapes the bug class would actually take in practice.
 */

export type TenantID = string & { readonly __brand: 'TenantID' }

/**
 * Two tenant kinds — exhaustive list matching the back-end
 * `store.TenantKind` enum.
 *
 *   • 'otech' — the Sovereign operator tier. Renders the existing
 *     post-handover Sovereign Console (Apps / Jobs / Cloud / Users
 *     [UserAccess CRD] / Settings).
 *   • 'sme'   — the Organization-admin tier (issue #795 epic). Renders the
 *     Organization user CRUD + Roles pages; Keycloak realm is the
 *     Organization-vcluster realm (different from otech). The discriminant
 *     wire VALUE stays 'sme' — it mirrors the back-end store.TenantKind enum.
 */
export type TenantKind = 'otech' | 'sme'

/**
 * Discovery response returned by `GET /api/v1/tenant/discover?host=...`.
 * Matches `tenantDiscoverResponse` in
 * api/internal/handler/tenant_discover.go verbatim. Only the public-
 * safe subset of `store.TenantRegistration` is on the wire here — no
 * admin URLs, no service-account tokens.
 */
export interface TenantDiscovery {
  host: string
  tenant_id: TenantID
  tenant_kind: TenantKind
  keycloak_realm_url: string
  keycloak_client_id: string
}

/**
 * Parse + validate a value as a `TenantID`. Throws on empty, non-
 * string, or whitespace-only inputs.
 */
export function parseTenantID(s: unknown): TenantID {
  if (typeof s !== 'string' || s.trim().length === 0) {
    const preview = typeof s === 'string' ? `"${s.slice(0, 32)}"` : `<${typeof s}>`
    throw new Error(`invalid TenantID: ${preview}`)
  }
  return s as TenantID
}

/**
 * Parse + validate a value as a `TenantKind`. Throws on anything
 * outside the closed set above.
 */
export function parseTenantKind(s: unknown): TenantKind {
  if (s === 'otech' || s === 'sme') return s
  const preview = typeof s === 'string' ? `"${s.slice(0, 32)}"` : `<${typeof s}>`
  throw new Error(`invalid TenantKind: ${preview}`)
}

/**
 * Type guard for `TenantKind`.
 */
export function isTenantKind(s: unknown): s is TenantKind {
  return s === 'otech' || s === 'sme'
}
