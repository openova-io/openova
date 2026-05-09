/**
 * membersListHelpers.ts — pure helpers used by MembersList (slice U5
 * + U6).
 *
 * Extracted so MembersList.tsx's exports stay component-only
 * (lint react-refresh/only-export-components).
 */

import type {
  AccessMatrixGrant,
  AccessMatrixResponse,
  AccessMatrixUser,
} from './rbac.api'

export type MembersScope =
  | { kind: 'application'; value: string }
  | { kind: 'organization'; value: string }

export interface MemberRowData {
  user: AccessMatrixUser
  grant: AccessMatrixGrant
}

/** flattenForScope maps the access-matrix shape (user → application
 *  → grant) to one row per user that has a grant on the requested
 *  scope. Wildcards (`*`) and exact-app matches both qualify.
 *
 *  Defends against `users: null` from a Go zero-value slice — qa-loop
 *  iter-4 cluster `users-page-null-map-and-open-redirect` (the
 *  /users-page sibling crash). */
export function flattenForScope(
  matrix: AccessMatrixResponse,
  scope: MembersScope,
): MemberRowData[] {
  const out: MemberRowData[] = []
  for (const u of matrix.users ?? []) {
    const grant = grantForScope(u, scope)
    if (grant) out.push({ user: u, grant })
  }
  return out
}

/** grantForScope picks the most-specific grant for the scope. */
export function grantForScope(
  user: AccessMatrixUser,
  scope: MembersScope,
): AccessMatrixGrant | undefined {
  const access = user.access ?? {}
  if (scope.kind === 'application') {
    // Direct match on the application key, OR fallback to the
    // synthetic '*' (global) grant.
    return access[scope.value] ?? access['*']
  }
  // For organization scope, the matrix already pre-filtered to the
  // org via the ?org=<slug> query — so any grant on the user is in
  // scope. Surface the highest-tier grant.
  const tiers = Object.values(access)
  return tiers[0]
}

/** defaultScopesForScope returns the scope set the Add modal applies
 *  when granting in this scope. Application scope adds the matching
 *  application= scope key; organization scope adds the org= key. */
export function defaultScopesForScope(scope: MembersScope) {
  if (scope.kind === 'application') {
    return [{ key: 'openova.io/application', value: scope.value }]
  }
  return [{ key: 'openova.io/org', value: scope.value }]
}
