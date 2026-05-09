/**
 * roleHelpers.ts — pure helpers used by the EPIC-3 (#1098) slice U4
 * RoleBrowserPage. Extracted so the page file's exports stay
 * component-only (lint react-refresh/only-export-components).
 */

import type { KCRole, RBACTier } from './rbac.api'

/** tierColors — palette per tier for the access-matrix cells.
 *  Mirrors the catalog tier order (viewer < developer < operator <
 *  admin < owner) with a cool→warm gradient so an operator can scan
 *  the matrix at a glance without reading labels. Pure function for
 *  test-friendly snapshotting. */
export function tierColors(tier: RBACTier | string): { bg: string; fg: string; border: string } {
  switch (tier) {
    case 'viewer':
      return { bg: 'rgba(125, 125, 125, 0.18)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.45)' }
    case 'developer':
      return { bg: 'rgba(56, 189, 248, 0.14)', fg: '#7dd3fc', border: 'rgba(56, 189, 248, 0.45)' }
    case 'operator':
      return { bg: 'rgba(34, 197, 94, 0.14)', fg: '#86efac', border: 'rgba(34, 197, 94, 0.45)' }
    case 'admin':
      return { bg: 'rgba(245, 158, 11, 0.14)', fg: '#fcd34d', border: 'rgba(245, 158, 11, 0.45)' }
    case 'owner':
      return { bg: 'rgba(239, 68, 68, 0.14)', fg: '#fca5a5', border: 'rgba(239, 68, 68, 0.45)' }
    default:
      return { bg: 'rgba(125, 125, 125, 0.10)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.30)' }
  }
}

/** tierLabel — short uppercase label shown inside matrix cells. Pure
 *  function — exported for test snapshotting. */
export function tierLabel(tier: RBACTier | string): string {
  return String(tier ?? '').toUpperCase().slice(0, 6)
}

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
