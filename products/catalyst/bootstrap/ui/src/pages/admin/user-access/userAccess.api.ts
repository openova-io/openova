/**
 * userAccess.api.ts — typed REST client wrappers for the catalyst-api
 * UserAccess Claim endpoints (issue #323). Wire shape mirrors the
 * canonical CRD shape shipped by issue #322.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * derives from the central `API_BASE` constant — there are no
 * inline `/api/...` strings in this file.
 */

import { API_BASE } from '@/shared/config/urls'

export type UserAccessRole = 'admin' | 'editor' | 'viewer'

export interface UserAccessUser {
  keycloakSubject?: string
  keycloakGroups?: string[]
}

export interface UserAccessAppGrant {
  app: string
  role: UserAccessRole | string
  namespaces?: string[]
  vClusters?: string[]
}

export interface UserAccessSpec {
  user: UserAccessUser
  sovereignRef: string
  applications: UserAccessAppGrant[]
}

export interface UserAccessStatus {
  rolebindingsCreated?: number
  providerConfigRef?: string
}

export interface UserAccessItem {
  name: string
  spec: UserAccessSpec
  status?: UserAccessStatus
  creationTimestamp?: string
}

export interface UserAccessRequest {
  name: string
  spec: UserAccessSpec
}

export interface UserAccessListResponse {
  items: UserAccessItem[]
}

function endpoint(deploymentId: string, name?: string): string {
  const base = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/admin/user-access`
  return name ? `${base}/${encodeURIComponent(name)}` : base
}

export async function listUserAccess(deploymentId: string): Promise<UserAccessItem[]> {
  const res = await fetch(endpoint(deploymentId), {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`list user-access: HTTP ${res.status}`)
  }
  const body: UserAccessListResponse = await res.json()
  return body.items ?? []
}

export async function createUserAccess(
  deploymentId: string,
  req: UserAccessRequest,
): Promise<UserAccessItem> {
  const res = await fetch(endpoint(deploymentId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`create user-access: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function updateUserAccess(
  deploymentId: string,
  name: string,
  req: UserAccessRequest,
): Promise<UserAccessItem> {
  const res = await fetch(endpoint(deploymentId, name), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`update user-access: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function deleteUserAccess(deploymentId: string, name: string): Promise<void> {
  const res = await fetch(endpoint(deploymentId, name), {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok && res.status !== 204) {
    const detail = await res.text().catch(() => '')
    throw new Error(`delete user-access: HTTP ${res.status} ${detail}`)
  }
}

/**
 * Render an identifier label for the user — preferring the OIDC
 * subject, falling back to the comma-joined group list.
 */
export function userLabel(user: UserAccessUser): string {
  if (user.keycloakSubject) return user.keycloakSubject
  if (user.keycloakGroups && user.keycloakGroups.length > 0) {
    return user.keycloakGroups.join(', ')
  }
  return '—'
}

/**
 * Compact summary of an UserAccess Claim's grants for the list view:
 *   "helmwatch (editor), catalyst (admin)"
 */
export function grantsSummary(applications: UserAccessAppGrant[]): string {
  return applications.map((g) => `${g.app} (${g.role})`).join(', ')
}
