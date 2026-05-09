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

/* ── /rbac/access-matrix wire shapes (A2) ─────────────────────────── */

export interface AccessMatrixGrant {
  tier: RBACTier
  userAccessRef: string
  scopes?: RBACScope[]
}

export interface AccessMatrixUser {
  id: string
  email?: string
  /** "keycloak" | "azure_ad_federated" | <federation IdP alias>. */
  source: KCUserSource
  /** application name → grant. The synthetic key `*` represents a
   *  global grant (no openova.io/application scope). */
  access: Record<string, AccessMatrixGrant>
  warnings?: string[]
}

export interface AccessMatrixResponse {
  users: AccessMatrixUser[]
  applications: string[]
  tiers: RBACTier[]
}

/** Optional filters mapping to the GET /rbac/access-matrix query string. */
export interface AccessMatrixQuery {
  org?: string
  application?: string
}

export async function getAccessMatrix(
  sovereignId: string,
  query: AccessMatrixQuery = {},
): Promise<AccessMatrixResponse> {
  const params = new URLSearchParams()
  if (query.org) params.set('org', query.org)
  if (query.application) params.set('application', query.application)
  const qs = params.toString()
  const url = qs
    ? `${rbacBase(sovereignId)}/access-matrix?${qs}`
    : `${rbacBase(sovereignId)}/access-matrix`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`rbac/access-matrix: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

/* ── /audit/rbac wire shapes (U8) ─────────────────────────────────── */

/** Canonical audit-type names mirrored from internal/audit/audit.go. */
export const RBAC_AUDIT_TYPES = [
  'rbac-grant-created',
  'rbac-grant-updated',
  'rbac-grant-deleted',
  'rbac-tier-changed',
] as const
export type RBACAuditType = (typeof RBAC_AUDIT_TYPES)[number]

export interface AuditEventScope {
  key: string
  value: string
}

export interface AuditEvent {
  auditType: string
  ts: string
  actor?: string
  sovereignId?: string
  result?: 'ok' | 'denied' | 'error' | string
  targetUser?: string
  targetUserEmail?: string
  targetApp?: string
  tier?: RBACTier | string
  previousTier?: RBACTier | string
  scopes?: AuditEventScope[]
  userAccessRef?: string
  detail?: string
}

export interface AuditListResponse {
  items: AuditEvent[]
  nextOffset?: number
  total: number
}

export interface AuditListQuery {
  limit?: number
  offset?: number
  actor?: string
  /** RFC3339 timestamp; only events at or after this are returned. */
  since?: string
}

export async function listRBACAudit(
  sovereignId: string,
  q: AuditListQuery = {},
): Promise<AuditListResponse> {
  const params = new URLSearchParams()
  if (q.limit) params.set('limit', String(q.limit))
  if (q.offset) params.set('offset', String(q.offset))
  if (q.actor) params.set('actor', q.actor)
  if (q.since) params.set('since', q.since)
  const qs = params.toString()
  const url = qs
    ? `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/audit/rbac?${qs}`
    : `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/audit/rbac`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok && res.status !== 503) {
    const detail = await res.text().catch(() => '')
    throw new Error(`audit/rbac: HTTP ${res.status} ${detail}`)
  }
  if (res.status === 503) {
    // Audit bus not wired — surface as empty list. The UI renders an
    // "audit disabled" hint on top of an empty table so the operator
    // can tell the difference between "no events" and "no bus".
    return { items: [], total: 0 }
  }
  return res.json()
}

/** SSE URL helper for /audit/rbac/stream. The caller composes their
 *  own EventSource — this just centralizes the endpoint construction
 *  per INVIOLABLE-PRINCIPLES #4 (no hardcoded URLs in components). */
export function rbacAuditStreamURL(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/audit/rbac/stream`
}

/** Pretty audit-type labels for UI. */
export const AUDIT_TYPE_LABELS: Record<string, string> = {
  'rbac-grant-created': 'Grant created',
  'rbac-grant-updated': 'Grant updated',
  'rbac-grant-deleted': 'Grant deleted',
  'rbac-tier-changed': 'Tier rotated',
}

/* ── Action verb helper (used by audit row renderer + tests) ──────── */

/** auditActionVerb maps an audit-type to a short verb the audit page
 *  uses for the "action" column. Lifts the type → verb mapping into a
 *  pure function so the renderer + the row test both share one source
 *  of truth (matches the slice U pattern of palette-helpers in compliance.api). */
export function auditActionVerb(auditType: string): string {
  return AUDIT_TYPE_LABELS[auditType] ?? auditType
}

/* ── DELETE UserAccess CR (used by Members tab Remove button) ─────── */

/** deleteUserAccess removes the UserAccess CR by name. Reuses the
 *  legacy /admin/user-access surface so we don't duplicate the
 *  delete path. The existing handler is at
 *  DELETE /api/v1/deployments/{depId}/admin/user-access/{name}. */
export async function deleteUserAccess(deploymentId: string, name: string): Promise<void> {
  const url = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/admin/user-access/${encodeURIComponent(name)}`
  const res = await authedFetch(url, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok && res.status !== 204) {
    const detail = await res.text().catch(() => '')
    throw new Error(`user-access delete: HTTP ${res.status} ${detail}`)
  }
}
