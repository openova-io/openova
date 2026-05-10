/**
 * networking.api.ts — typed REST client wrappers for catalyst-api
 * networking endpoints (qa-loop iter-11 Fix #48).
 *
 * Wire shape mirrors `internal/handler/networking.go`:
 *   - networkingPoliciesResponse → NetworkingPoliciesResponse
 *   - clusterMeshResponse        → ClusterMeshResponse
 *   - netbirdResponse            → NetBirdResponse
 *   - dmzResponse                → DMZResponse
 *   - hubbleResponse             → HubbleResponse
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) every URL
 * derives from the central `API_BASE` constant. All fetches go
 * through `authedFetch` so the OIDC Authorization header is attached
 * on chroot consoles.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Wire types ──────────────────────────────────────────────────── */

export interface PolicyRow {
  kind: string
  name: string
  namespace: string
  ingress_rules?: number
  egress_rules?: number
  labels?: Record<string, string>
  created_at: string
}

export interface NetworkingPoliciesResponse {
  items: PolicyRow[]
  counts_by_kind: Record<string, number>
  counts_by_namespace: Record<string, number>
  total: number
}

export interface ClusterMeshPeer {
  name: string
  connected: boolean
}

export interface ClusterMeshResponse {
  clusters: ClusterMeshPeer[]
  sources: string[]
  total: number
  mesh_keys_present: boolean
  self_cluster_name?: string
  self_cluster_id?: string
}

export interface ComponentDeployment {
  name: string
  namespace: string
  ready: number
  desired: number
  available: boolean
}

export interface NetBirdPeer {
  id: string
  hostname: string
  ip: string
  online: boolean
}

export interface NetBirdResponse {
  installed: boolean
  deployments: ComponentDeployment[]
  peers: NetBirdPeer[]
  hostname_hint?: string
}

export interface DMZVCluster {
  name: string
  namespace: string
  phase: string
  running: boolean
}

export interface DMZResponse {
  installed: boolean
  vclusters: DMZVCluster[]
  isolation_cnps: PolicyRow[]
  total: number
}

export interface HubbleResponse {
  hubble_enabled: boolean
  relay_ready: boolean
  ui_ready: boolean
  relay_listen?: string
  deployments: ComponentDeployment[]
}

/* ── Client wrappers ─────────────────────────────────────────────── */

function url(sovereignId: string, slug: string): string {
  return `${API_BASE}/api/v1/sovereigns/${encodeURIComponent(sovereignId)}/networking/${slug}`
}

async function getJSON<T>(u: string): Promise<T> {
  const r = await authedFetch(u)
  if (!r.ok) {
    throw new Error(`networking API ${u} returned ${r.status}`)
  }
  return (await r.json()) as T
}

export const getNetworkingPolicies = (sovereignId: string) =>
  getJSON<NetworkingPoliciesResponse>(url(sovereignId, 'policies'))

export const getNetworkingClusterMesh = (sovereignId: string) =>
  getJSON<ClusterMeshResponse>(url(sovereignId, 'clustermesh'))

export const getNetworkingNetBird = (sovereignId: string) =>
  getJSON<NetBirdResponse>(url(sovereignId, 'netbird'))

export const getNetworkingDMZ = (sovereignId: string) =>
  getJSON<DMZResponse>(url(sovereignId, 'dmz'))

export const getNetworkingHubble = (sovereignId: string) =>
  getJSON<HubbleResponse>(url(sovereignId, 'hubble'))
