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

/* ── EPIC-2 Slice T+O+P (#1097) — update / delete / topology /
 *      upgrade / publish / curate ────────────────────────────────── */

/**
 * ApplicationUpdateRequest — partial body of PUT
 * /api/v1/sovereigns/{id}/applications/{name}.
 *
 * All fields optional; missing fields leave the existing value
 * unchanged. The server enforces topology safety rules (active-active →
 * single-region requires `?force=true`).
 */
export interface ApplicationUpdateRequest {
  blueprintRef?: { name?: string; version: string }
  parameters?: Record<string, unknown>
  placement?: { mode: string; regions: string[] }
}

export interface ApplicationUpdateResponse {
  name: string
  namespace: string
  uid: string
  status?: Record<string, unknown>
}

export interface ApplicationDeleteResponse {
  name: string
  namespace: string
  message: string
}

export async function updateApplication(
  sovereignId: string,
  name: string,
  body: ApplicationUpdateRequest,
  opts: { namespace?: string; force?: boolean } = {},
): Promise<ApplicationUpdateResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  if (opts.force) params.set('force', 'true')
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`update: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function deleteApplication(
  sovereignId: string,
  name: string,
  opts: { namespace?: string } = {},
): Promise<ApplicationDeleteResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`delete: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

/**
 * Topology / upgrade preview body — fields default to the existing
 * CR's values server-side. The shape is forward-compatible with the
 * install preview (intentionally identical response).
 */
export interface ApplicationChangePreviewRequest {
  placement?: { mode: string; regions: string[] }
  parameters?: Record<string, unknown>
  blueprintRef?: { name?: string; version: string }
  environmentRef?: string
}

export async function previewTopologyChange(
  sovereignId: string,
  name: string,
  body: ApplicationChangePreviewRequest,
  opts: { namespace?: string } = {},
): Promise<ApplicationPreviewResponse> {
  const params = new URLSearchParams()
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}/topology/preview${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`topology-preview: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export async function previewUpgrade(
  sovereignId: string,
  name: string,
  targetVersion: string,
  body: ApplicationChangePreviewRequest = {},
  opts: { namespace?: string } = {},
): Promise<ApplicationPreviewResponse> {
  const params = new URLSearchParams()
  params.set('targetVersion', targetVersion)
  if (opts.namespace) params.set('namespace', opts.namespace)
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}/upgrade/preview?${qs}`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`upgrade-preview: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

/* ── Blueprint publish + curate (slice P) ──────────────────────── */

function blueprintsBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/blueprints`
}

export interface BlueprintPublishRequest {
  org: string
  name: string
  version: string
  blueprintYaml: string
  chartTarball?: string
}

export interface BlueprintPublishResponse {
  org: string
  name: string
  version: string
  repo: string
  path: string
  url: string
  message: string
}

export async function publishBlueprint(
  sovereignId: string,
  body: BlueprintPublishRequest,
): Promise<BlueprintPublishResponse> {
  const res = await authedFetch(`${blueprintsBase(sovereignId)}/publish`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`publish: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export interface BlueprintCurateRequest {
  sourceOrg: string
  blueprintName: string
}

export interface BlueprintCurateResponse {
  blueprintName: string
  sourceOrg: string
  targetOrg: string
  message: string
}

export async function curateBlueprint(
  sovereignId: string,
  body: BlueprintCurateRequest,
): Promise<BlueprintCurateResponse> {
  const res = await authedFetch(`${blueprintsBase(sovereignId)}/curate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`curate: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}

export interface CuratableBlueprint {
  org: string
  name: string
  version: string
  title?: string
}

export interface CuratableBlueprintsResponse {
  items: CuratableBlueprint[]
}

export async function listCuratableBlueprints(
  sovereignId: string,
  orgs: string[],
): Promise<CuratableBlueprintsResponse> {
  const params = new URLSearchParams()
  if (orgs.length > 0) params.set('orgs', orgs.join(','))
  const url = `${blueprintsBase(sovereignId)}/curatable?${params.toString()}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`curatable: HTTP ${res.status}`)
  }
  return res.json()
}

/* ── Edit-PR (slice Z3 follow-up) ──────────────────────────────── */

export interface BlueprintEditPRRequest {
  org: string
  path: string
  content: string
  message?: string
  title?: string
}

export interface BlueprintEditPRResponse {
  prURL: string
  prNumber: number
  branch: string
  repo: string
  path: string
  message: string
}

/**
 * editPRBlueprint — opens a Gitea PR with the supplied content for a
 * flux-managed K8s resource. Wired by YamlEditor's flux branch (per
 * `widgets/cloud-list/YamlEditor.tsx`) so the operator's edit lands via
 * the GitOps flow rather than side-stepping flux with a direct /apply.
 *
 * Server-side gates: tier-admin or higher (mirrors /blueprints/publish);
 * server is the authoritative gate even though the UI hides "Apply" for
 * unauthorised callers.
 */
export async function editPRBlueprint(
  sovereignId: string,
  body: BlueprintEditPRRequest,
): Promise<BlueprintEditPRResponse> {
  const res = await authedFetch(`${blueprintsBase(sovereignId)}/edit-pr`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`edit-pr: HTTP ${res.status} ${detail}`)
  }
  return res.json()
}
