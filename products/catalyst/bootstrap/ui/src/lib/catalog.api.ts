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

import { API_BASE, BASE } from '@/shared/config/urls'
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
  /**
   * #3668 — theme-aware icons sourced from the blueprint IaC
   * (`spec.card.iconLight` / `iconDark`). The Go API emits these as
   * `iconLight` / `iconDark` (CatalogBlueprintCard). The console resolves
   * the IaC icon FIRST (theme-aware), then the build-time vendored asset,
   * then the letter-mark — so an admin edit to the icon actually renders
   * (it previously round-tripped to storage but never to the screen).
   */
  iconLight?: string
  iconDark?: string
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

/**
 * #3668 §5D — the full-CR catalog IaC editor save. Commits the WHOLE
 * blueprint.yaml the admin edited (spec.source / manifests / sso /
 * placementSchema / endpoints / shareable / contextSchema — fields the
 * 7-field card form can never touch) to the SAME catalog-sovereign Gitea
 * file the card-edit path writes, via `PUT /api/v1/catalog/{name}/iac`. The
 * git write is the success criterion: a non-2xx means the IaC source did not
 * accept it, so the editor must surface the failure (never a false "Saved").
 */
export interface CatalogIaCEditResponse {
  slug: string
  path: string
  committed: boolean
  reason?: string
}

export async function saveCatalogBlueprintIaC(
  name: string,
  blueprintYaml: string,
): Promise<CatalogIaCEditResponse> {
  const url = `${catalogBase()}/${encodeURIComponent(name)}/iac`
  const res = await authedFetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ blueprintYaml }),
  })
  const detail = await res.text().catch(() => '')
  if (!res.ok) {
    throw new Error(`catalog IaC edit: HTTP ${res.status} ${detail}`.trim())
  }
  try {
    return JSON.parse(detail) as CatalogIaCEditResponse
  } catch {
    return { slug: name.replace(/^bp-/, ''), path: '', committed: true }
  }
}

/**
 * getCatalogBlueprintIaC — read the CURRENTLY-COMMITTED
 * catalog-sovereign/<repo>/blueprint.yaml via `GET /api/v1/catalog/{name}/iac`,
 * the source of truth the PUT save writes (#5124). The Edit-IaC editor seeds
 * from this so committing never silently reverts card-form edits already in the
 * committed file. Returns `null` on 404 (nothing committed yet) or any transport
 * error so the caller can fall back to its store `raw` — the seed is then never
 * WORSE than today, only better when a committed file exists.
 */
export async function getCatalogBlueprintIaC(name: string): Promise<string | null> {
  const url = `${catalogBase()}/${encodeURIComponent(name)}/iac`
  try {
    const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
    if (!res.ok) {
      return null
    }
    const body = (await res.json()) as { blueprintYaml?: string }
    return typeof body.blueprintYaml === 'string' && body.blueprintYaml.length > 0
      ? body.blueprintYaml
      : null
  } catch {
    return null
  }
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

/* ── G117.2 #2741 — Blueprint-class drill-down ────────────────────── */

/**
 * GET /catalyst/v1/catalog/{blueprint}/instances → ApplicationInstanceSummary[].
 * The wire envelope type `ListBlueprintInstancesResponse` is declared
 * below alongside `ApplicationInstanceSummary` (line ~748); this fn
 * unwraps the `items` field for callers that just want the list.
 */
export async function listBlueprintInstancesByName(
  blueprint: string,
  opts: { org?: string } = {},
): Promise<ApplicationInstanceSummary[]> {
  const bare = blueprint.replace(/^bp-/, '')
  const params = new URLSearchParams()
  if (opts.org) params.set('org', opts.org)
  const qs = params.toString()
  // The /catalyst/v1/... route group is registered WITHOUT the /api/
  // prefix at cmd/api/main.go:1412 (HandleListBlueprintInstances). Use
  // BASE (not API_BASE) so we don't ship /api/catalyst/v1/... which chi
  // doesn't match and which returns 404 (caught live on hw86 walk
  // 2026-06-03 after #2876 deploy — CatalogDetail's instancesQuery
  // surfaced the 404 in the rendered error state).
  const url = `${BASE}catalyst/v1/catalog/${encodeURIComponent(bare)}/instances${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`listBlueprintInstancesByName: HTTP ${res.status}`)
  }
  const body = (await res.json()) as ListBlueprintInstancesResponse
  return body.items ?? []
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
  /**
   * #3599 — the create-time placement projected from the Application
   * CR's spec so the Topology tab reflects it without waiting for the
   * controller to populate status.placement. `placement` is the editor
   * posture string (single-region | active-active | active-hotstandby).
   */
  spec?: {
    placement?: string
    regions?: string[]
    environmentRef?: string
    blueprintRef?: { name?: string; version?: string }
  }
}

/**
 * ApplicationDetailResponse — body of GET
 * /sovereigns/{id}/applications/{name}. Lifts the same fields the
 * Sovereign Console's AppDetail page reads in one round-trip:
 * identity + spec + roll-up status. Stable shape so the matrix-asserted
 * contract (TC-068, TC-095, TC-106) and the SPA's
 * findApplicationByName fallback consume the same JSON without
 * per-caller post-processing.
 *
 * qa-loop iter-11 Fix #45 Cluster-C.
 */
export interface ApplicationDetailResponse {
  name: string
  namespace: string
  blueprint?: string
  version?: string
  environmentRef?: string
  placement?: string
  regions?: string[]
  parameters?: Record<string, unknown>
  phase?: string
  primaryRegion?: string
  giteaRepo?: string
  lastReconciledAt?: string
  conditions: Array<Record<string, unknown>>
  regionStatuses?: Array<Record<string, unknown>>
  installedBlueprint?: Record<string, unknown>
  /**
   * Family B (2026-05-17 t10 founder bugs C4-005/007): Actual K8s
   * install location + label selector. Use these for ResourcesTab /
   * LogsTab queries instead of guessing "default" + `instance=<name>`.
   * Backend populates from HR `spec.targetNamespace` / `spec.releaseName`
   * / chart name (bootstrap-kit) or Application CR `spec.targetNamespace`
   * (wizard installs).
   */
  targetNamespace?: string
  releaseName?: string
  installLabelSelector?: string
  /**
   * Family B (C4-004): true when synthesised from a HelmRelease with
   * no companion Application CR — i.e. bootstrap-kit installs that
   * are NOT expected to exist in /catalog/apps/<slug>. The SPA uses
   * this to render the publish chip as "Bootstrap blueprint (not in
   * marketplace)" instead of "Catalog status unavailable".
   */
  bootstrap?: boolean
  /**
   * Family B (C4-003): HR-Ready overlay telemetry. When `hrReady=true`
   * the backend promoted `phase` to "Ready" because the matching
   * HelmRelease reported Ready=True even though the Application CR's
   * own `status.phase` is stale (`phaseFromCR`). The SPA surfaces this
   * in the source-of-truth D19 chip so the operator knows the CR is
   * behind its HR — the canonical signal for a lagging
   * application-controller. The chip also matches what /sovereign/apps
   * shows (which queries HRs directly), eliminating the founder-flagged
   * desync.
   */
  hrReady?: boolean
  phaseFromCR?: string
  /**
   * G90 (2026-06-01, founder UX gap on hw86): front-door URL the
   * operator clicks to open this installed Application in a new browser
   * tab with their Keycloak SSO session active. Backend resolves by
   * joining (targetNamespace, releaseName) against the cluster's
   * HTTPRoute set; empty when the app has no externally-exposed route
   * (controllers, operators, internal components). FE renders an
   * "External URL" row on AppDetail Overview when non-empty.
   */
  externalURL?: string
  /**
   * G117.4 #2743 AC3 (2026-06-03): Application CR's metadata.uid.
   * Required by catalyst-api `GET /catalyst/v1/apps/{uid}/launch-url`
   * (per endpoint_handler.go:529 HandleGetLaunchURL — findApplicationByUID).
   * Backend MUST populate this from `obj.GetUID()` on the GET-Application
   * response so the FE's Launch button can mint a silent-SSO URL with
   * `prompt=none&kc_idp_hint=catalyst-pin`. Empty/undefined → FE falls
   * back to the legacy `externalURL` direct link.
   */
  uid?: string
  /**
   * #3370 — Contexts on a SHAREABLE instance (the first-class child
   * entities consumer applications occupy), projected generically from
   * the instance's declared IaC values per the Blueprint's
   * contextSchema. Drives the AppDetail Contexts tab; absent for
   * non-shareable blueprints.
   */
  contexts?: InstanceContext[]
  /**
   * #3370 — consumer-side backing edges: which instance this
   * Application depends on and which Context it occupies there
   * (`Depends on: shared-pg / db:gitea`). From the Application CR's
   * spec.dependsOn, or derived from the Flux HR dependsOn graph for
   * bootstrap consumers.
   */
  dependsOn?: AppDependsOn[]
}

/** Mirrors the Go `dependsOnDetail` (#3370). */
export interface AppDependsOn {
  instance: string
  namespace?: string
  /** The occupied Context as `<kind>/<name>` (e.g. db/gitea). */
  context?: string
}

/**
 * G117.4 #2743 AC3 — response body of GET
 * /catalyst/v1/apps/{uid}/launch-url[?endpoint=<name>]. Backend appends
 * `prompt=none&kc_idp_hint=catalyst-pin` so the operator lands inside
 * the app without a Keycloak login form (silent-SSO budget &lt;500ms per
 * locked decision #3). 60s expiry.
 */
export interface LaunchURLResponse {
  url: string
  expiresAt: string
  endpoint: string
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

/**
 * getApplication — fetch full Application detail by name.
 * Returns null on 404 (not-an-error in the SPA-fallback context).
 * qa-loop iter-11 Fix #45 Cluster-C.
 */
export async function getApplication(
  sovereignId: string,
  name: string,
  namespace?: string,
): Promise<ApplicationDetailResponse | null> {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 404) return null
  if (!res.ok) {
    throw new Error(`getApplication: HTTP ${res.status}`)
  }
  return res.json()
}

/**
 * G117.4 #2743 AC3 — fetch a one-shot silent-SSO Launch URL for an
 * installed Application instance. Returns null on 404 / 409 (sso-not-
 * enabled) / 503 (cluster-unavailable) so callers can gracefully fall
 * back to the legacy `externalURL` direct link without surfacing a
 * scary error toast for the common no-SSO case (controllers, scaffolds).
 * Throws on other 4xx/5xx so genuine network/auth issues surface.
 */
export async function getLaunchURL(
  uid: string,
  endpoint?: string,
): Promise<LaunchURLResponse | null> {
  const qs = endpoint ? `?endpoint=${encodeURIComponent(endpoint)}` : ''
  // G117.4 #2878-followup (2026-06-03): the /catalyst/v1/... route group
  // is registered at cmd/api/main.go:1410 (HandleGetLaunchURL) WITHOUT
  // the /api/ prefix. `${API_BASE}/...` produces /api/catalyst/v1/...
  // which chi 404s — getLaunchURL then returns null per the 404 branch
  // below, and LaunchButton falls through to fallbackURL with NO
  // prompt=none&kc_idp_hint=catalyst-pin appended. Caught live on hw86
  // 2026-06-03 via authenticated Playwright walk: API returned the
  // correct silent-SSO URL when called WITHOUT /api/ prefix, but the
  // bundle was calling /api/catalyst/v1/.../launch-url which 404'd.
  // Mirrors the PR #2878 fix for listBlueprintInstancesByName.
  const url = `${BASE}catalyst/v1/apps/${encodeURIComponent(uid)}/launch-url${qs}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 404 || res.status === 409 || res.status === 503) return null
  if (!res.ok) {
    throw new Error(`getLaunchURL: HTTP ${res.status}`)
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

/**
 * RuntimePlacementResponse — #3982: the REAL placement targets derived
 * from where the component's workloads actually run, across BOTH region
 * clusters the catalyst-api holds in k8scache. Body of GET
 * /sovereigns/{id}/applications/{name}/placement.
 *
 * `targets[]` is the #3969 PlacementTarget shape (region × cluster ×
 * vCluster × role Primary|Standby, standby type Hot|Cold). The FE feeds it
 * straight into `derivePattern` so the Topology tab renders the TRUE
 * pattern (active-active for grafana/keycloak across 2 regions,
 * active-hot-standby for a CNPG pair, singleton ONLY for a genuinely
 * single-region component). Works for bootstrap HelmReleases (no
 * Application CR) AND Application-CR installs because it keys on live Pods.
 */
export interface RuntimePlacementResponse {
  targets: Array<{
    region: string
    cluster: string
    vcluster?: string
    role: 'Primary' | 'Standby'
    standbyType?: 'Hot' | 'Cold'
  }>
  derivedFromRuntime: boolean
}

/**
 * getApplicationPlacement — #3982. Fetch the runtime-derived placement
 * targets for a component. Generic across every component the Topology tab
 * renders. Returns null on any non-OK status so the caller can fall back to
 * the legacy spec/status projection without throwing (the endpoint 200s
 * with empty targets when the data plane is unavailable, so a thrown error
 * here means a genuine transport failure).
 */
export async function getApplicationPlacement(
  sovereignId: string,
  name: string,
  namespace?: string,
): Promise<RuntimePlacementResponse | null> {
  const params = new URLSearchParams()
  if (namespace) params.set('namespace', namespace)
  const qs = params.toString()
  const url = `${applicationsBase(sovereignId)}/${encodeURIComponent(name)}/placement${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) return null
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
  /**
   * #3969 — the placement desired state. `targets[]` is the canonical
   * shape (region × cluster × vCluster × role Primary|Standby, standby
   * type Hot|Cold) plus per-owned-dep cascade overrides. `mode`/`regions`
   * remain accepted for back-compat with older clients; the server projects
   * whichever is present. The pattern is DERIVED, never sent.
   */
  placement?: {
    mode?: string
    regions?: string[]
    targets?: Array<{
      region: string
      cluster: string
      vcluster?: string
      role: 'Primary' | 'Standby'
      standbyType?: 'Hot' | 'Cold'
    }>
    ownedDependencies?: Array<{ name: string; follow: boolean }>
  }
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

/* ── G117.2 #2741 — Multi-instance Application surface ─────────────
 *
 * Wire shapes mirror catalyst-api's `internal/instances.CreateInstanceRequest`
 * and the OpenAPI `Application` / `ApplicationSummary` projections at
 * `internal/handler/endpoint_handler.go:117-142`.
 *
 *   GET  /catalyst/v1/catalog/{blueprint}/instances[?org=]
 *        → ListBlueprintInstancesResponse
 *
 *   POST /catalyst/v1/apps/instances
 *        body: CreateApplicationInstanceRequest
 *        → CreateApplicationInstanceResponse
 *
 * The path lives under /catalyst/v1/ (not /api/v1/) — the bp-catalyst-
 * platform HTTPRoute (PR #2741 chart bump 1.4.447) wires
 * PathPrefix /catalyst/ on the console hostname to forward to the
 * catalyst-api Service. Prior to that bump the prefix fell through to
 * the React shell and returned index.html.
 */

/** Mirrors `applicationSummary` at endpoint_handler.go:117. */
export interface ApplicationInstanceSummary {
  id: string
  name: string
  blueprint: string
  org: string
  topology: string
  status: string
  createdAt?: string
  /**
   * Mirrors Go `Contexts []contextRow` (`contexts,omitempty`) — #3370.
   * The Contexts on a SHAREABLE instance, projected generically from
   * the instance's declared IaC values per the Blueprint's
   * contextSchema. Absent for non-shareable blueprints.
   */
  contexts?: InstanceContext[]
}

/**
 * Mirrors the Go `contextRow` (endpoint_handler.go) VERBATIM —
 * lowercase camelCase per the `json:` tags (memory
 * feedback_ts_field_casing_vs_go_json_tag). One Context on a shareable
 * instance: the first-class child entity one consumer application
 * occupies (#3370).
 */
export interface InstanceContext {
  name: string
  /** Kind label from the Blueprint contextSchema (db | topic | bucket | keyspace). */
  kind: string
  /** Consumer blueprint (bp- prefix stripped); empty when undeclared. */
  occupiedBy?: string
  /** Reflected credential Secret declared by the context entry. */
  credential?: string
  /** ready | pending | declared — derived from the materialized IaC. */
  status: string
}

/** Mirrors `application` at endpoint_handler.go:130 (Summary + perCluster). */
export interface ApplicationInstance extends ApplicationInstanceSummary {
  perCluster?: ApplicationClusterStatus[]
}

/** Mirrors `applicationClusterStatus` at endpoint_handler.go:136. */
export interface ApplicationClusterStatus {
  cluster: string
  role: string
  status: string
  hr?: string
  message?: string
}

/** Mirrors `listInstancesResponse` at endpoint_handler.go:153. */
export interface ListBlueprintInstancesResponse {
  items: ApplicationInstanceSummary[]
}

/**
 * Mirrors `instances.CreateInstanceRequest` at
 * products/catalyst/bootstrap/api/internal/instances/create.go.
 *
 * `blueprint`, `org`, `name` are required. `topology` is optional —
 * server falls back to the Blueprint's per-Sovereign topology default
 * (single-region vs multi-region per locked decision #7). `values`
 * carries the Blueprint configSchema parameters; per-app form
 * surfaces those in the topology-picker dialog as a JSON-Schema-driven
 * form (out of scope for the v1 dialog — see PR follow-up TBD).
 */
export interface CreateApplicationInstanceRequest {
  blueprint: string
  org: string
  name: string
  topology?: string
  isolationLevel?: 'namespace' | 'vcluster'
  values?: Record<string, unknown>
  /**
   * #3373 / #3599 — instance placement (WHERE the instance runs). Maps to
   * the OBJECT form of the Application CR's `spec.placement`. When set,
   * the backend stamps `spec.placement = {mode: topology, vcluster,
   * regions, clusters}` + `spec.regions = regions`, which the post-create
   * Topology tab reads back. `vcluster` ∈ {host, mgmt, dmz, rtz}.
   */
  placement?: InstancePlacement
  /**
   * #3370 — backing-service selections, one per required backing
   * service (the generic Create new / Reuse existing selector).
   */
  backing?: BackingSelection[]
}

/** Mirrors the Go `instances.InstancePlacementRequest` (#3373). */
export interface InstancePlacement {
  /** Target vCluster/zone: "" | host | mgmt | dmz | rtz. */
  vcluster?: string
  /** Catalyst region ids the instance runs in (spec.regions). */
  regions?: string[]
  /** Explicit cluster ids (advanced; usually derived from regions). */
  clusters?: string[]
}

/** Mirrors `instances.BackingSelection` (#3370). */
export interface BackingSelection {
  /** Backing Blueprint id (bp- prefix optional). */
  blueprint: string
  /** "create" (default) — own card auto-created; "reuse" — Context on an existing instance. */
  mode?: 'create' | 'reuse'
  /** Existing instance name (required when mode=reuse). */
  instance?: string
}

/** Mirrors `application` 201-response body at endpoint_handler.go:729. */
export type CreateApplicationInstanceResponse = ApplicationInstance

/**
 * getApplicationInstances — list every Application installed from a
 * given Blueprint. Returns the empty-items envelope on 404. Throws on
 * other 4xx/5xx so genuine network / auth issues surface.
 *
 * `appUID` is unused by the wire path (the listing is per-Blueprint,
 * not per-Application) but the FE call-site threads it as a stable
 * cache key so multiple AppDetail tabs with the same Blueprint share
 * the same in-flight request.
 */
export async function getApplicationInstances(
  blueprint: string,
  opts: { org?: string } = {},
): Promise<ListBlueprintInstancesResponse> {
  const bp = blueprint.replace(/^bp-/, '')
  const params = new URLSearchParams()
  if (opts.org) params.set('org', opts.org)
  const qs = params.toString()
  // Same /api/ prefix bug pattern as PR #2878 (listBlueprintInstancesByName)
  // and PR #2884 (getLaunchURL): the /catalyst/v1/... route group at
  // cmd/api/main.go:1412 is registered WITHOUT the /api/ prefix.
  // ${API_BASE}/... produces /api/catalyst/v1/... which chi 404s — pre-fix
  // every call returned the 404-empty-items path silently. Use BASE.
  const url = `${BASE}catalyst/v1/catalog/${encodeURIComponent(bp)}/instances${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 404) return { items: [] }
  if (!res.ok) {
    throw new Error(`getApplicationInstances: HTTP ${res.status}`)
  }
  return res.json()
}

/**
 * createApplicationInstance — POST /catalyst/v1/apps/instances.
 *
 * On 4xx the server returns a typed error body
 * `{code: string, message: string}`. The error message surfaces verbatim
 * so the dialog can render the operator-meaningful copy (e.g.
 * "max instances per Org exceeded", "isolationLevel not in {namespace,
 * vcluster}").
 */
export async function createApplicationInstance(
  body: CreateApplicationInstanceRequest,
): Promise<CreateApplicationInstanceResponse> {
  // #3370 — same /api/ prefix bug class as PR #2878 (GET instances) and
  // PR #2884 (launch-url): the /catalyst/v1/* route group is registered
  // WITHOUT the /api prefix, so `${API_BASE}/catalyst/...` produced
  // /api/catalyst/v1/apps/instances which chi 404s. Every dialog submit
  // failed silently with HTTP 404 until the #3370 local walk exercised
  // the POST through the wire. Use BASE like getApplicationInstances.
  const res = await authedFetch(`${BASE}catalyst/v1/apps/instances`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    let code = ''
    let message = `HTTP ${res.status}`
    try {
      const detail = (await res.json()) as { code?: string; message?: string }
      if (detail?.code) code = detail.code
      if (detail?.message) message = detail.message
    } catch {
      /* fall through */
    }
    const err = new Error(message) as Error & { status?: number; code?: string }
    err.status = res.status
    err.code = code
    throw err
  }
  return res.json()
}

/* ─── #2742 (G117.3) Endpoints / Ingress (Git-IaC PR pipeline) ─────────
 *
 * Full editable-endpoints surface ported from the dead Svelte console
 * (`products/catalyst/console/src/components/EndpointsTab.svelte`) into
 * the production React tree. Backed by catalyst-api's endpoint_handler.go:
 *
 *   GET    /catalyst/v1/apps/{id}/endpoints         → { items: ResolvedEndpoint[] }
 *   POST   /catalyst/v1/apps/{id}/endpoints         → EndpointPR
 *   PATCH  /catalyst/v1/apps/{id}/endpoints/{name}  → EndpointPR
 *   DELETE /catalyst/v1/apps/{id}/endpoints/{name}  → EndpointPR
 *
 * Every mutation opens a GOVERNED PR against gitea.<sov>/<org>/iac;
 * kyverno-admission / cert-manager-precheck / dns-conflict-precheck
 * status checks gate auto-merge (locked decision #4).
 *
 * CRITICAL (per memory feedback_ts_field_casing_vs_go_json_tag): every
 * field name below MUST match the Go `json:"..."` tag VERBATIM —
 * `res.json()` returns wire keys exactly, so a casing mismatch yields a
 * silently-undefined field. The Go shapes are `resolvedEndpoint`,
 * `endpointMutationRequest`, `endpointPRResponse`, and
 * `endpointPreCheckResults` in endpoint_handler.go:78-115.
 */

export type EndpointProtocol = 'https' | 'http' | 'grpc' | 'tcp' | 'udp'
export type EndpointVisibility = 'public' | 'private' | 'internal'

/**
 * ResolvedEndpoint mirrors the Go `resolvedEndpoint` (endpoint_handler.go:102)
 * VERBATIM. `status`/`name`/`hostname` are always present; the rest are
 * `omitempty` on the wire so they may be absent.
 */
export interface ResolvedEndpoint {
  name: string
  hostnameTemplate: string
  hostname: string
  port?: number
  protocol?: EndpointProtocol
  tls?: boolean
  visibility?: EndpointVisibility
  launchDefault?: boolean
  ssoEnabled?: boolean
  status: string
  certificateStatus?: string
  launchURL?: string
}

/**
 * EndpointSummary — retained as a back-compat alias of ResolvedEndpoint
 * for any existing importer. New code should use ResolvedEndpoint.
 */
export type EndpointSummary = ResolvedEndpoint

/** Mirrors Go `listEndpointsResponse` (endpoint_handler.go:157). */
export interface ListEndpointsResponse {
  items: ResolvedEndpoint[]
}

/**
 * EndpointMutationRequest mirrors the Go `endpointMutationRequest`
 * (endpoint_handler.go:78). `name` IS serialised in the POST body — the
 * backend's precheck.ValidateMutation REQUIRES a non-empty, RFC-1123-ish
 * endpoint name on create (`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`). On PATCH the
 * name is taken from the path param, so the body may omit it.
 */
export interface EndpointMutationRequest {
  name: string
  hostname?: string
  port?: number
  protocol?: EndpointProtocol
  tls?: boolean
  visibility?: EndpointVisibility
  ssoEnabled?: boolean
}

/** Mirrors Go `endpointPreCheckResults` (endpoint_handler.go:95). */
export interface EndpointPreCheckResults {
  kyverno?: string
  certManager?: string
  dnsConflict?: string
}

/**
 * EndpointPR mirrors the Go `endpointPRResponse` (endpoint_handler.go:89).
 * `status` is one of `open` | `merged` | `closed` | `failed-precheck`.
 */
export interface EndpointPR {
  prURL: string
  status: string
  preCheckResults?: EndpointPreCheckResults
}

/** Back-compat alias — older call-sites import `EndpointMutationResponse`. */
export type EndpointMutationResponse = EndpointPR

/**
 * listEndpoints — GET /catalyst/v1/apps/{id}/endpoints.
 * BASE (not API_BASE) per the /catalyst/v1 no-/api/-prefix rule
 * (same fix as getApplicationInstances / getLaunchURL). 404 → empty.
 */
export async function listEndpoints(appId: string): Promise<ListEndpointsResponse> {
  const url = `${BASE}catalyst/v1/apps/${encodeURIComponent(appId)}/endpoints`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 404) return { items: [] }
  if (!res.ok) throw new Error(`listEndpoints: HTTP ${res.status}`)
  return res.json()
}

/** Shared typed-error decode for the endpoint mutation calls. */
async function endpointMutationError(res: Response): Promise<Error & { status?: number; code?: string }> {
  let message = `HTTP ${res.status}`
  let code = ''
  try {
    const detail = (await res.json()) as { code?: string; message?: string }
    if (detail?.message) message = detail.message
    if (detail?.code) code = detail.code
  } catch {
    /* non-JSON body — keep the HTTP-status message */
  }
  const err = new Error(message) as Error & { status?: number; code?: string }
  err.status = res.status
  err.code = code
  return err
}

/**
 * createAppEndpoint — POST /catalyst/v1/apps/{id}/endpoints.
 * Opens a governed Git-IaC PR for a NEW endpoint. The full body is sent
 * (name + hostname + port + protocol + tls + visibility + ssoEnabled);
 * `name` is REQUIRED by the backend precheck.
 */
export async function createAppEndpoint(
  appId: string,
  body: EndpointMutationRequest,
): Promise<EndpointPR> {
  const url = `${BASE}catalyst/v1/apps/${encodeURIComponent(appId)}/endpoints`
  const res = await authedFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw await endpointMutationError(res)
  return res.json()
}

/**
 * patchAppEndpoint — PATCH /catalyst/v1/apps/{id}/endpoints/{name}.
 * Opens a governed Git-IaC PR editing an EXISTING endpoint. The endpoint
 * name is the path param (immutable); the body carries the new values.
 */
export async function patchAppEndpoint(
  appId: string,
  name: string,
  body: EndpointMutationRequest,
): Promise<EndpointPR> {
  const url = `${BASE}catalyst/v1/apps/${encodeURIComponent(appId)}/endpoints/${encodeURIComponent(name)}`
  const res = await authedFetch(url, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw await endpointMutationError(res)
  return res.json()
}

/**
 * deleteAppEndpoint — DELETE /catalyst/v1/apps/{id}/endpoints/{name}.
 * Opens a governed Git-IaC PR removing the endpoint manifest (the
 * cert-manager Certificate + Gateway HTTPRoute are removed on merge).
 */
export async function deleteAppEndpoint(appId: string, name: string): Promise<EndpointPR> {
  const url = `${BASE}catalyst/v1/apps/${encodeURIComponent(appId)}/endpoints/${encodeURIComponent(name)}`
  const res = await authedFetch(url, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) throw await endpointMutationError(res)
  return res.json()
}

/**
 * mutateEndpoint — back-compat convenience wrapper kept for the prior
 * call-site shape. POST (create) when `opts.name` is absent, PATCH (edit
 * by name) when present. Prefer the explicit createAppEndpoint /
 * patchAppEndpoint for new code.
 */
export async function mutateEndpoint(
  appId: string,
  body: EndpointMutationRequest,
  opts: { name?: string } = {},
): Promise<EndpointPR> {
  return opts.name
    ? patchAppEndpoint(appId, opts.name, body)
    : createAppEndpoint(appId, body)
}
