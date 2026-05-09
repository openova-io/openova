/**
 * rbac.api.ts — typed REST client wrappers for the EPIC-3 (#1098)
 * RBAC management surfaces (slice U1+U2+U3+U4).
 *
 * Wire shape mirrors the Go handlers:
 *   - rbac_assign.go  (A1, merged)            → POST /rbac/assign
 *   - rbac_matrix.go  (A2, merged)            → GET  /rbac/access-matrix
 *   - keycloak_proxy.go (this slice)          → GET  /keycloak/users/groups/roles
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) every URL
 * derives from API_BASE. All API calls go through `authedFetch` so the
 * OIDC bearer is attached on chroot consoles.
 *
 * Per the canonical-seam map (Frontend Compliance UI patterns), this
 * file follows the `compliance.api.ts` shape: one wire type per
 * endpoint, REST baseline, TanStack Query consumes from `rbac` hook
 * file.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Tier vocab ───────────────────────────────────────────────────── */

/** The 5 catalog tiers per docs/EPICS-1-6-unified-design.md §6.2. */
export type RBACTier = 'viewer' | 'developer' | 'operator' | 'admin' | 'owner'

export const RBAC_TIERS: readonly RBACTier[] = [
  'viewer',
  'developer',
  'operator',
  'admin',
  'owner',
] as const

/** Tier numeric precedence, mirrors core/controllers/internal/labels TierLevel. */
export const TIER_LEVEL: Record<RBACTier, number> = {
  viewer: 10,
  developer: 20,
  operator: 30,
  admin: 40,
  owner: 50,
}

/**
 * TIER_ACTION_SETS — frozen action-set table per docs/EPICS-1-6-unified-
 * design.md §6.2. Used by the multi-grant editor to render tooltip
 * previews per tier. Each tier inherits from the prior (transitive
 * via Keycloak composite realm-role chain in slice T2 #1146).
 *
 * Per INVIOLABLE-PRINCIPLES #4 these are the canonical contract. If the
 * design doc table evolves, this table is the single point of update
 * on the UI side.
 */
export const TIER_ACTION_SETS: Record<RBACTier, readonly string[]> = {
  viewer: ['*.read'],
  developer: [
    '… inherits viewer',
    'workloads.exec',
    'workloads.console',
    'tickets.create',
    'tickets.update',
    'sessions.playback',
  ],
  operator: [
    '… inherits developer',
    'console.connect.admin',
    'sam.manage',
    'patches.manage',
    'tickets.accept',
  ],
  admin: [
    '… inherits operator',
    'compute.* (except delete)',
    'credentials.*',
    'applications.*',
    'actions.*',
    'accounts.*',
    'networks.*',
    'sessions.*',
  ],
  owner: ['… inherits admin', 'rbac.*', 'organization.*'],
}

/**
 * TIER_AUTO_INJECTED_SCOPES — scopes the backend's useraccess-controller
 * auto-injects when a UserAccess CR is created with the matching tier
 * label. Surface on the UI so the operator sees "developer always
 * scoped to env-type=dev — even if you don't add it" before submit.
 */
export const TIER_AUTO_INJECTED_SCOPES: Partial<Record<RBACTier, ReadonlyArray<RBACScope>>> = {
  developer: [{ key: 'openova.io/env-type', value: 'dev' }],
}

/* ── Scope vocab (NAMING-CONVENTION.md §6) ────────────────────────── */

/**
 * RBAC_SCOPE_KEYS — the canonical label-key vocabulary the backend
 * accepts on a UserAccess scope per docs/NAMING-CONVENTION.md §6.1+§6.2.
 * The multi-grant editor validates user-typed keys against this set
 * (per INVIOLABLE-PRINCIPLES #4 — never invent label keys).
 *
 * Sources of truth:
 *   §6.1 — cloud-resource tags (provider/region/building-block/env-type/cluster/managed-by)
 *   §6.2 — Catalyst resource tags (sovereign/organization/environment/vcluster/host-cluster/application/blueprint/blueprint-version)
 *
 * Synthetic keys also supported by the Manara matcher: `*` (wildcard,
 * valid in scopeMatchesAny but rejected on user input — wildcard scope
 * is the empty-array case at the UserAccess CR level, not a typed
 * scope key).
 */
export const RBAC_SCOPE_KEYS: readonly string[] = [
  // §6.1 — cloud resource tags
  'openova.io/provider',
  'openova.io/region',
  'openova.io/building-block',
  'openova.io/env-type',
  'openova.io/cluster',
  'openova.io/managed-by',
  // §6.2 — catalyst resource tags
  'openova.io/sovereign',
  'openova.io/organization',
  'openova.io/org', // alias, used by access-matrix filters
  'openova.io/environment',
  'openova.io/vcluster',
  'openova.io/host-cluster',
  'openova.io/application',
  'openova.io/blueprint',
  'openova.io/blueprint-version',
] as const

const RBAC_SCOPE_KEY_SET: ReadonlySet<string> = new Set(RBAC_SCOPE_KEYS)

/** validateScopeKey — returns null on valid, error string on invalid. */
export function validateScopeKey(key: string): string | null {
  const trimmed = key.trim()
  if (!trimmed) return 'scope key is required'
  if (!RBAC_SCOPE_KEY_SET.has(trimmed)) {
    return `unknown scope key: ${trimmed} — valid: ${RBAC_SCOPE_KEYS.join(', ')}`
  }
  return null
}

/** validateScopeValue — non-empty after trim. The backend further
 * validates against per-key vocab where applicable; the UI only
 * enforces presence to keep the form responsive.
 */
export function validateScopeValue(value: string): string | null {
  return value.trim() ? null : 'scope value is required'
}

/* ── /rbac/assign wire shapes ─────────────────────────────────────── */

export interface RBACScope {
  key: string
  value: string
}

export interface RBACAssignUser {
  email?: string
  keycloakSubject?: string
}

export interface RBACAssignRequest {
  user: RBACAssignUser
  tier: RBACTier
  scope: RBACScope[]
}

export interface RBACAssignResponse {
  userAccess: { name: string; uid: string; namespace: string }
  tierClusterRole: string
  applied: 'created' | 'updated' | 'no-op'
}

/* ── /keycloak/users wire shapes ──────────────────────────────────── */

export type KCUserSource = 'keycloak' | 'azure_ad_federated' | string

export interface KCUser {
  id: string
  username: string
  email?: string
  firstName?: string
  lastName?: string
  /** "keycloak" | "azure_ad_federated" | <federation IdP alias>. */
  source: KCUserSource
}

export interface KCUserListResponse {
  items: KCUser[]
}

/* ── /keycloak/groups wire shapes ─────────────────────────────────── */

export interface KCGroup {
  id?: string
  name: string
  path?: string
  attributes?: Record<string, string[]>
  subGroups?: KCGroup[]
}

export interface KCGroupListResponse {
  items: KCGroup[]
}

export interface KCGroupCreateRequest {
  name: string
  parentId?: string
  attributes?: Record<string, string[]>
}

export interface KCGroupUpdateRequest {
  attributes?: Record<string, string[]>
}

/* ── /keycloak/roles wire shapes ──────────────────────────────────── */

export interface KCRole {
  id?: string
  name: string
  description?: string
  composite?: boolean
  clientRole?: boolean
  containerId?: string
  attributes?: Record<string, string[]>
}

export interface KCRoleListResponse {
  items: KCRole[]
}

export interface KCRoleMembersResponse {
  role: string
  items: KCUser[]
}

/* ── Endpoint helpers ─────────────────────────────────────────────── */

function rbacBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/rbac`
}

function kcBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/keycloak`
}

/* ── /rbac/assign ─────────────────────────────────────────────────── */

export async function rbacAssign(
  sovereignId: string,
  body: RBACAssignRequest,
): Promise<RBACAssignResponse> {
  const res = await authedFetch(`${rbacBase(sovereignId)}/assign`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`rbac/assign: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

/* ── /keycloak/users (U2) ─────────────────────────────────────────── */

export async function searchKCUsers(
  sovereignId: string,
  search: string,
  limit = 20,
): Promise<KCUser[]> {
  const params = new URLSearchParams()
  params.set('search', search)
  params.set('limit', String(limit))
  const res = await authedFetch(`${kcBase(sovereignId)}/users?${params.toString()}`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/users: HTTP ${res.status} ${detail}`)
  }
  const body: KCUserListResponse = await res.json()
  return body.items ?? []
}

/* ── /keycloak/groups (U3) ────────────────────────────────────────── */

export async function listKCGroups(sovereignId: string): Promise<KCGroup[]> {
  const res = await authedFetch(`${kcBase(sovereignId)}/groups`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/groups: HTTP ${res.status} ${detail}`)
  }
  const body: KCGroupListResponse = await res.json()
  return body.items ?? []
}

export async function createKCGroup(
  sovereignId: string,
  body: KCGroupCreateRequest,
): Promise<KCGroup> {
  const res = await authedFetch(`${kcBase(sovereignId)}/groups`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/groups create: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function updateKCGroup(
  sovereignId: string,
  groupId: string,
  body: KCGroupUpdateRequest,
): Promise<KCGroup> {
  const res = await authedFetch(
    `${kcBase(sovereignId)}/groups/${encodeURIComponent(groupId)}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify(body),
    },
  )
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/groups update: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function deleteKCGroup(sovereignId: string, groupId: string): Promise<void> {
  const res = await authedFetch(
    `${kcBase(sovereignId)}/groups/${encodeURIComponent(groupId)}`,
    { method: 'DELETE', headers: { Accept: 'application/json' } },
  )
  if (!res.ok && res.status !== 204) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/groups delete: HTTP ${res.status} ${detail}`)
  }
}

/* ── /keycloak/roles (U4) ─────────────────────────────────────────── */

export async function listKCRoles(sovereignId: string): Promise<KCRole[]> {
  const res = await authedFetch(`${kcBase(sovereignId)}/roles`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/roles: HTTP ${res.status} ${detail}`)
  }
  const body: KCRoleListResponse = await res.json()
  return body.items ?? []
}

export async function listKCRoleMembers(
  sovereignId: string,
  roleName: string,
): Promise<KCRoleMembersResponse> {
  const res = await authedFetch(
    `${kcBase(sovereignId)}/roles/${encodeURIComponent(roleName)}/members`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/roles/members: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function listKCClientRoles(
  sovereignId: string,
  clientUUID: string,
): Promise<KCRole[]> {
  const res = await authedFetch(
    `${kcBase(sovereignId)}/clients/${encodeURIComponent(clientUUID)}/roles`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`keycloak/clients/roles: HTTP ${res.status} ${detail}`)
  }
  const body: KCRoleListResponse = await res.json()
  return body.items ?? []
}
