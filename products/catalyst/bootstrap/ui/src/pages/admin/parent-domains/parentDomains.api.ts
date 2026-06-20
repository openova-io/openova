/**
 * parentDomains.api.ts — typed REST client for the admin "parent
 * domains" surface (issue #829, parent epic #825).
 *
 * Wire shape mirrors the catalyst-api handler in
 * products/catalyst/bootstrap/api/internal/handler/parent_domains.go.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * derives from the central API_BASE constant — there are no inline
 * `/api/...` strings here.
 */

import { API_BASE } from '@/shared/config/urls'

export type ParentDomainRole = 'primary' | 'org-pool'

export type FlipStatus =
  | 'queued'
  | 'flipping'
  | 'flipped'
  | 'failed'
  | 'zone-creating'
  | 'cert-issuing'
  | 'ready'

export interface ParentDomain {
  name: string
  role: ParentDomainRole
  registrarKind?: string
  registrarCredsRef?: string
  flipStatus: FlipStatus
  flipMessage?: string
  addedAt: string
  flippedAt?: string
}

export interface ParentDomainListResponse {
  items: ParentDomain[]
}

export interface AddParentDomainRequest {
  name: string
  role: ParentDomainRole
  registrarKind: string
  registrarToken: string
}

export interface ResolverSpec {
  name: string
  ip: string
  geo: string
}

export type ResolverStatus = 'converged' | 'diverged' | 'error'

export interface PropagationState {
  resolver: ResolverSpec
  status: ResolverStatus
  ns: string[]
  queriedAt: string
  latencyMs: number
  error?: string
}

export interface PropagationResponse {
  domain: string
  expectedNs: string[]
  resolvers: PropagationState[]
  converged: number
  total: number
  percentage: number
  generatedAt: string
}

const PARENT_DOMAINS_BASE = `${API_BASE}/v1/sovereign/parent-domains`

export async function listParentDomains(): Promise<ParentDomain[]> {
  const res = await fetch(PARENT_DOMAINS_BASE, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`list parent-domains: HTTP ${res.status}`)
  }
  const body = (await res.json()) as ParentDomainListResponse
  return body.items ?? []
}

export async function addParentDomain(req: AddParentDomainRequest): Promise<ParentDomain> {
  const res = await fetch(PARENT_DOMAINS_BASE, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(req),
  })
  // 201 (created) or 502 (registrar/zone failure) — both bodies carry
  // the partial state. Surface the body's `detail` to the modal so the
  // operator sees the registrar's actual error message.
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // ignore non-JSON error body
    }
    throw new Error(`add parent-domain: ${detail}`)
  }
  return (await res.json()) as ParentDomain
}

export async function deleteParentDomain(name: string): Promise<void> {
  const res = await fetch(`${PARENT_DOMAINS_BASE}/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok && res.status !== 204) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // ignore
    }
    throw new Error(`delete parent-domain: ${detail}`)
  }
}

export async function getPropagation(name: string): Promise<PropagationResponse> {
  const res = await fetch(
    `${PARENT_DOMAINS_BASE}/${encodeURIComponent(name)}/propagation`,
    { headers: { Accept: 'application/json' } },
  )
  if (!res.ok) {
    throw new Error(`get propagation: HTTP ${res.status}`)
  }
  return (await res.json()) as PropagationResponse
}

/**
 * Pretty label for a FlipStatus enum value — used by the list-row
 * badge.
 */
export function flipStatusLabel(s: FlipStatus): string {
  switch (s) {
    case 'queued':
      return 'Queued'
    case 'flipping':
      return 'Flipping NS'
    case 'flipped':
      return 'NS Flipped'
    case 'zone-creating':
      return 'Creating Zone'
    case 'cert-issuing':
      return 'Issuing Cert'
    case 'ready':
      return 'Ready'
    case 'failed':
      return 'Failed'
    default:
      return s
  }
}

/**
 * Badge tone (matches global theme colour tokens) for a FlipStatus.
 */
export function flipStatusTone(s: FlipStatus): 'green' | 'amber' | 'red' | 'blue' {
  switch (s) {
    case 'ready':
    case 'flipped':
      return 'green'
    case 'queued':
    case 'flipping':
    case 'zone-creating':
    case 'cert-issuing':
      return 'amber'
    case 'failed':
      return 'red'
    default:
      return 'blue'
  }
}
