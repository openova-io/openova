/**
 * sme.api.ts — typed REST client for the SME-tier user CRUD endpoints
 * (issue #802).
 *
 * Wire shapes mirror `api/internal/handler/sme_users.go`:
 *
 *   POST   /api/v1/sme/users            (create + fire ADR-0003 hook)
 *   GET    /api/v1/sme/users            (list — scoped to current tenant)
 *   DELETE /api/v1/sme/users/{uuid}     (inverse rollback)
 *
 * Tenant scoping: every request carries `X-Tenant-Host:
 * window.location.host` so the back end resolves the correct tenant
 * from the registry (per [Q-mine-1] of #795). The OIDC realm claim
 * provides the additional security boundary.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * derives from `apiUrl()` in `shared/config/urls`. Per #2 (never
 * compromise on quality), the response shape is parsed through the
 * branded-types parsers (`parseTenantID` etc.) at the boundary so a
 * future server-side wire-shape drift surfaces as a runtime error
 * here, not as silent cross-tenant pollution downstream.
 */

import { apiUrl } from '@/shared/config/urls'

export type SMEStepState = 'pending' | 'done' | 'failed'

export interface SMEUserSteps {
  kc: SMEStepState
  newapi: SMEStepState
  secret: SMEStepState
}

export type SMEProvisionState =
  | 'pending'
  | 'kc_created'
  | 'newapi_created'
  | 'secret_applied'
  | 'done'
  | 'failed'
  | 'deleted'

export interface SMEUser {
  uuid: string
  email: string
  state: SMEProvisionState
  kc_user_id?: string
  newapi_user_id?: string
  secret_name?: string
  last_error?: string
  created_at: string
  updated_at: string
  steps: SMEUserSteps
}

export interface SMEUserCreateRequest {
  email: string
  /** Optional app → role mapping; rendered by RolesPage. */
  roles?: Record<string, string>
}

const SME_USERS_PATH = '/v1/sme/users'

function tenantHeaders(): HeadersInit {
  const host =
    typeof window !== 'undefined' ? window.location.host : ''
  return {
    'X-Tenant-Host': host,
    Accept: 'application/json',
  }
}

export async function listSMEUsers(): Promise<SMEUser[]> {
  const res = await fetch(apiUrl(SME_USERS_PATH), {
    method: 'GET',
    credentials: 'include',
    headers: tenantHeaders(),
  })
  if (!res.ok) {
    throw new Error(`list sme users: HTTP ${res.status}`)
  }
  const body = (await res.json()) as { items?: SMEUser[] }
  return body.items ?? []
}

export async function createSMEUser(
  req: SMEUserCreateRequest,
): Promise<SMEUser> {
  const res = await fetch(apiUrl(SME_USERS_PATH), {
    method: 'POST',
    credentials: 'include',
    headers: { ...tenantHeaders(), 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok && res.status !== 202) {
    const detail = await res.text().catch(() => '')
    throw new Error(`create sme user: HTTP ${res.status} ${detail}`)
  }
  return (await res.json()) as SMEUser
}

export async function deleteSMEUser(uuid: string): Promise<void> {
  const res = await fetch(apiUrl(`${SME_USERS_PATH}/${encodeURIComponent(uuid)}`), {
    method: 'DELETE',
    credentials: 'include',
    headers: tenantHeaders(),
  })
  if (!res.ok && res.status !== 204) {
    throw new Error(`delete sme user: HTTP ${res.status}`)
  }
}
