/**
 * roleHelpers.ts — pure helpers used by the EPIC-3 (#1098) slice U4
 * RoleBrowserPage. Extracted so the page file's exports stay
 * component-only (lint react-refresh/only-export-components).
 */

import type { KCRole } from './rbac.api'

/** sortRolesByTierLevel — tier-level attribute (per slice T2 bootstrap)
 *  is the primary sort key; alphabetical within ties. */
export function sortRolesByTierLevel(roles: KCRole[]): KCRole[] {
  return [...roles].sort((a, b) => {
    const la = readTierLevel(a) ?? Number.MAX_SAFE_INTEGER
    const lb = readTierLevel(b) ?? Number.MAX_SAFE_INTEGER
    if (la !== lb) return la - lb
    return a.name.localeCompare(b.name)
  })
}

/** readTierLevel — pulls the integer `tier-level` attribute (slice T2)
 *  off a Keycloak realm role. Returns null when absent or non-numeric. */
export function readTierLevel(r: KCRole): number | null {
  const raw = r.attributes?.['tier-level']?.[0]
  if (!raw) return null
  const n = Number.parseInt(raw, 10)
  return Number.isFinite(n) ? n : null
}
