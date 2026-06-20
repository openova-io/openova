/**
 * useCatalogAdmin — #3603 (EPIC #3597): is the current operator allowed to
 * EDIT catalog entries?
 *
 * The catalog-edit write path (PUT/POST /api/v1/org/commerce/apps → the Organization
 * commerce catalog's /catalog/admin/* CRUD) is gated SERVER-SIDE on
 * superadmin OR sovereign-admin (core/services/catalog requireAdmin). This
 * hook is the matching UI gate so a non-admin operator never sees the Edit
 * affordance in the first place — it does NOT replace the server gate.
 *
 * The tier derivation mirrors AppDetail's #3375 `callerTier` verbatim
 * (prefer the explicit `tier` claim, else map the catalyst-<tier> realm
 * roles whoami returns) so "who is an admin" is computed identically
 * everywhere. A sovereign-admin operator authenticates with tier `admin`
 * or `owner` (the catalyst-owner / catalyst-admin realm roles).
 */

import { useMemo } from 'react'
import { useSession } from './useSession'

/**
 * deriveCallerTier — the canonical tier resolution shared with AppDetail
 * (#3375). Exported so a component that already holds a SessionState can
 * reuse it without a second useSession call.
 */
export function deriveCallerTier(tier: string, roles: string[]): string {
  if (tier) return tier
  const lower = roles.map((r) => r.toLowerCase())
  if (lower.includes('catalyst-owner')) return 'owner'
  if (lower.includes('catalyst-admin')) return 'admin'
  if (lower.includes('catalyst-operator')) return 'operator'
  if (lower.includes('catalyst-developer')) return 'developer'
  if (lower.includes('catalyst-viewer')) return 'viewer'
  return ''
}

/**
 * useCatalogAdmin returns true when the operator's tier is `admin` or
 * `owner` — the sovereign-admin tier the catalog-edit write path requires.
 */
export function useCatalogAdmin(): boolean {
  const session = useSession()
  return useMemo(() => {
    const tier = deriveCallerTier(session.tier, session.roles)
    return tier === 'admin' || tier === 'owner'
  }, [session.tier, session.roles])
}
