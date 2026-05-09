/**
 * catalog.api.ts — typed REST client wrappers for the catalyst-catalog
 * proxy hop on catalyst-api (EPIC-2 slice I, #1097).
 *
 * Wire path:
 *
 *   browser ──/api/v1/sovereigns/{id}/catalog/...──▶ catalyst-api ──▶ catalyst-catalog
 *
 * The proxy is the only sanctioned path from the UI: catalyst-catalog
 * runs behind the catalyst-api Cilium-Gateway HTTPRoute (per slice L's
 * DESIGN.md §"Auth model"). A direct browser-to-catalog call would
 * force CORS + duplicate token-handling for no architectural gain.
 *
 * For slice I we ship the install + preview endpoints, plus the
 * catalog list/get/get-by-version endpoints under the same /sovereigns/{id}
 * path. catalyst-api exposes:
 *
 *   GET  /api/v1/catalog                         — list (per slice L)
 *   GET  /api/v1/catalog/{name}                  — get
 *   GET  /api/v1/catalog/{name}/versions/{ver}   — get version
 *   POST /api/v1/sovereigns/{id}/applications              — install
 *   POST /api/v1/sovereigns/{id}/applications/preview      — preview
 *   GET  /api/v1/sovereigns/{id}/applications/{name}/status — status
 *   GET  /api/v1/sovereigns/{id}/applications/{name}/stream — SSE
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from
 * `API_BASE` so the contabo strip-sovereign / direct-Sovereign
 * distinction is resolved at config-time, not in components.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Wire types ──────────────────────────────────────────────────── */

export type CatalogOrigin = 1 | 2 | 3
export type CatalogSource = 'public' | 'sovereign' | 'org-private'

/**
 * BlueprintCard mirrors `Blueprint.spec.card` per the CRD shape.
 */
export interface BlueprintCard {
  title: string
  summary?: string
  description?: string
  tagline?: string
  icon?: string
  category?: string
  family?: string
  tags?: string[]
  license?: string
  docs?: string
}

/**
 * BlueprintPlacement mirrors the placement-schema subset surfaced on
 * each catalog item.
 */
export interface BlueprintPlacement {
  modes?: string[]
  default?: string
  minRegions?: number
  maxRegions?: number
}

/**
 * CatalogItem — wire shape returned by `GET /api/v1/catalog`. Mirrors
 * `core/services/catalyst-catalog/internal/source.Blueprint`.
 *
 * `raw` is populated only by the per-version endpoint
 * (`GET /api/v1/catalog/{name}/versions/{version}`) — that's the one
 * the install flow uses to pull `spec.configSchema` for the auto-form.
 */
export interface CatalogItem {
  name: string
  version: string
  visibility?: string
  card: BlueprintCard
  placementSchema?: BlueprintPlacement
  upgradeFrom?: string[]
  upgradeBlocks?: string[]
  origin: CatalogOrigin
  source: CatalogSource
  org?: string
  raw?: Record<string, unknown>
}

export interface CatalogListResponse {
  items: CatalogItem[]
}

export interface CatalogVersionsResponse {
  name: string
  versions: { version: string; origin: string; org?: string }[]
  upgradeMatrix: Record<string, string[]>
}

/* ── Endpoint helpers ────────────────────────────────────────────── */

function catalogBase(): string {
  return `${API_BASE}/v1/catalog`
}

function applicationsBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/applications`
}

/* ── REST calls (catalog read) ───────────────────────────────────── */

export async function listCatalog(opts: { org?: string } = {}): Promise<CatalogListResponse> {
  const params = new URLSearchParams()
  if (opts.org) params.set('org', opts.org)
  const qs = params.toString()
  const url = `${catalogBase()}${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`catalog list: HTTP ${res.status}`)
  }
  return res.json()
}

export async function getCatalogItem(name: string): Promise<CatalogItem> {
  const url = `${catalogBase()}/${encodeURIComponent(name)}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`catalog get: HTTP ${res.status}`)
  }
  return res.json()
}

export async function getCatalogVersions(name: string): Promise<CatalogVersionsResponse> {
  const url = `${catalogBase()}/${encodeURIComponent(name)}/versions`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`catalog versions: HTTP ${res.status}`)
  }
  return res.json()
}

export async function getCatalogItemVersion(name: string, version: string): Promise<CatalogItem> {
  const url = `${catalogBase()}/${encodeURIComponent(name)}/versions/${encodeURIComponent(version)}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`catalog get-version: HTTP ${res.status}`)
  }
  return res.json()
}

/* ── REST calls (install + preview + status) ─────────────────────── */

/**
 * ApplicationInstallRequest mirrors the catalyst-api wire shape per
 * EPIC-2 brief §I3.
 */
export interface ApplicationInstallRequest {
  blueprintRef: { name: string; version: string }
  name: string
  organizationRef: string
  environmentRef: string
  parameters?: Record<string, unknown>
  placement: { mode: string; regions: string[] }
}

/** ApplicationInstallResponse — body of 201 Created. */
export interface ApplicationInstallResponse {
  name: string
  namespace: string
  uid: string
  status?: Record<string, unknown>
}

/** ApplicationStatusResponse — body of GET status. */
export interface ApplicationStatusResponse {
  name: string
  namespace: string
  phase?: string
  status?: Record<string, unknown>
}

/** PreviewManifest — one rendered file in the preview output. */
export interface PreviewManifest {
  path: string
  content: string
}

/**
 * ApplicationPreviewResponse — body of POST .../applications/preview.
 *
 * EPIC-2 T (topology editor) reuses this contract for "preview before
 * topology change". The shape is intentionally future-proof: `diff`
 * may be empty when no current state exists, and `warnings` carries
 * non-fatal advisory messages.
 */
export interface ApplicationPreviewResponse {
  manifests: PreviewManifest[]
  diff: string
  blueprint: { name: string; version: string }
  warnings: string[]
}

export async function installApplication(
  sovereignId: string,
  body: ApplicationInstallRequest,
): Promise<ApplicationInstallResponse> {
  const res = await authedFetch(applicationsBase(sovereignId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`install: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function previewApplication(
  sovereignId: string,
  body: ApplicationInstallRequest,
): Promise<ApplicationPreviewResponse> {
  const res = await authedFetch(`${applicationsBase(sovereignId)}/preview`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`preview: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function getApplicationStatus(
  sovereignId: string,
  name: string,
  namespace?: string,
): Promise<ApplicationStatusResponse> {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}/status${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`status: HTTP ${res.status}`)
  }
  return res.json()
}

export function applicationStreamURL(
  sovereignId: string,
  name: string,
  accessToken?: string,
): string {
  const params = new URLSearchParams()
  if (accessToken) params.set('access_token', accessToken)
  const qs = params.toString()
  return `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}/stream${qs ? '?' + qs : ''}`
}
