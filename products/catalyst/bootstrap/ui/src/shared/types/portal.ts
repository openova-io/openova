/**
 * Branded `PortalID` + `PortalKind` types — single source of truth for
 * the console-portal identity the SPA discovers via
 * `/api/v1/tenant/discover` (legacy back-end wire path — the route name
 * predates the org-rename; the route + its JSON keys are the
 * catalyst-api wire contract and change only in a BE+FE lockstep).
 *
 * Why branded types: the same Sovereign Console SPA bundle serves
 * BOTH otech-admin AND Organization-admin views (issue #802, #795 [Q-mine-1]).
 * Portal context is read from `window.location.host` and resolved
 * against the host registry on the back end. Threading the portal
 * id through every API call as a free-form `string` invites
 * cross-Organization leaks at compile time — branded types refuse to
 * land a value that wasn't routed through `parsePortalID()` first.
 *
 * Mirrors the pattern established for `DeploymentID` in
 * `shared/types/deployment.ts` (issue #749).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (never compromise on quality)
 * the parsers reject the empty string and any non-string input — the
 * shapes the bug class would actually take in practice.
 */

export type PortalID = string & { readonly __brand: 'PortalID' }

/**
 * Two portal kinds — exhaustive list matching the back-end registry
 * enum (wire values are the contract; do not rename them).
 *
 *   • 'otech' — the Sovereign operator tier. Renders the existing
 *     post-handover Sovereign Console (Apps / Jobs / Cloud / Users
 *     [UserAccess CRD] / Settings).
 *   • 'org'   — the Organization-admin tier (issue #795 epic). Renders the
 *     Organization user CRUD + Roles pages; Keycloak realm is the
 *     Organization-vcluster realm (different from otech). The discriminant
 *     wire VALUE stays 'org' — it mirrors the back-end registry enum.
 */
export type PortalKind = 'otech' | 'org'

/**
 * Discovery response returned by `GET /api/v1/tenant/discover?host=...`
 * (legacy BE wire path). The `tenant_id` / `tenant_kind` JSON keys in
 * `api/internal/handler/tenant_discover.go` are the wire contract; this
 * client-side model maps them onto org-rename-clean field names at the
 * parse boundary (`portalDiscover.ts`). Only the public-safe subset of
 * the registry row is on the wire here — no admin URLs, no
 * service-account tokens.
 */
export interface PortalDiscovery {
  host: string
  portalId: PortalID
  portalKind: PortalKind
  keycloak_realm_url: string
  keycloak_client_id: string
}

/**
 * Parse + validate a value as a `PortalID`. Throws on empty, non-
 * string, or whitespace-only inputs.
 */
export function parsePortalID(s: unknown): PortalID {
  if (typeof s !== 'string' || s.trim().length === 0) {
    const preview = typeof s === 'string' ? `"${s.slice(0, 32)}"` : `<${typeof s}>`
    throw new Error(`invalid PortalID: ${preview}`)
  }
  return s as PortalID
}

/**
 * Parse + validate a value as a `PortalKind`. Throws on anything
 * outside the closed set above.
 */
export function parsePortalKind(s: unknown): PortalKind {
  if (s === 'otech' || s === 'org') return s
  const preview = typeof s === 'string' ? `"${s.slice(0, 32)}"` : `<${typeof s}>`
  throw new Error(`invalid PortalKind: ${preview}`)
}

/**
 * Type guard for `PortalKind`.
 */
export function isPortalKind(s: unknown): s is PortalKind {
  return s === 'otech' || s === 'org'
}
