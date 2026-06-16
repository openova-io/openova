/**
 * fleet.api.ts — typed REST client for the EPIC-6 Slice U-Fleet (#1101)
 * multi-Sovereign aggregator endpoints.
 *
 * Wire path:
 *
 *   browser ──/api/v1/fleet/...──▶ catalyst-api ──▶ per-Sovereign K8s reads
 *
 * Per ADR-0001 §2.7 (K8s-native) the catalyst-api reads each Sovereign's
 * Application + Continuum + Organization CRs LIVE — there is no
 * separate fleet database. The proxy hop is hidden from the UI; we hit
 * `/api/v1/fleet/*` and let the server fan out.
 *
 * Three endpoints shipped here mirror the three brief deliverables:
 *
 *   useFleet()                       → GET /api/v1/fleet/sovereigns
 *   useFleetSovereignSummary(id)     → GET /api/v1/fleet/sovereigns/{id}/summary
 *   useFleetApplications(filters)    → GET /api/v1/fleet/applications
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL derives from
 * `API_BASE` so the contabo strip-sovereign / direct-Sovereign
 * distinction is resolved at config-time, not in components.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Wire types ──────────────────────────────────────────────────── */

/**
 * SovereignHealth — 4-state vocabulary the dashboard renders. Mirrors
 * the Go `healthFromDeploymentStatus` map. Anything outside this set
 * is a contract bug — callers should fall back to "unknown".
 */
export type SovereignHealth = 'green' | 'yellow' | 'red' | 'unknown'

/**
 * SovereignSummary — slim row returned by `/fleet/sovereigns`.
 */
export interface SovereignSummary {
  id: string
  fqdn?: string
  region?: string
  health: SovereignHealth
  providerType?: string
  /** RFC3339 timestamp of when this Sovereign was first provisioned. */
  createdAt?: string
}

/**
 * SovereignsResponse — body shape of `GET /fleet/sovereigns`.
 */
export interface SovereignsResponse {
  sovereigns: SovereignSummary[]
  total: number
  page: number
  pageSize: number
}

/**
 * ApplicationCounts — Application rollup numbers per Sovereign.
 */
export interface ApplicationCounts {
  total: number
  active: number
  failing: number
}

/**
 * SovereignDetail — body shape of `GET /fleet/sovereigns/{id}/summary`.
 *
 * `alerts` is reserved for EPIC-1's score aggregator integration; today
 * the server returns 0 (the field exists so consumers don't pay a wire
 * change when it lights up).
 *
 * `configuredRegions` (qa-loop iter-16 Fix #88, Path B) — the SUPERSET
 * of every region the operator declared at provision time AND every
 * region carrying a live Application. The catalyst-ui dashboard
 * SovereignCard renders the difference (`configuredRegions \ regions`)
 * as muted "configured · no peer cluster" chips so multi-region tokens
 * (`fsn1`, `hz-hel-rtz-prod`, `hel`) resolve on a single-region QA
 * cluster without provisioning a real second-region cluster (the
 * provisioner currently materialises only the first region as a live
 * cluster — true multi-region with Cilium ClusterMesh is Path A
 * follow-up work).
 *
 * Optional on the wire: pre-Fix-#88 catalyst-api responses omit the
 * field; the UI treats absence as "single-region only" (no extra
 * chips) so older Sovereigns keep rendering cleanly.
 */
export interface SovereignDetail {
  sovereign: SovereignSummary
  orgs: number
  applications: ApplicationCounts
  regions: string[]
  configuredRegions?: string[]
  alerts: number
  /** RFC3339 timestamp of the most recent Application creation in this Sov. */
  lastActivity?: string
}

/**
 * TopologyMode — the CANONICAL topology vocabulary the backend validates
 * (`render/topology.go`): `singleton | active-active | active-hot-standby
 * | active-passive`. #3375 §3(f) — this type previously carried the
 * legacy editor dialect (`single-region` / `active-hotstandby`) and the
 * fleet filter posted it raw, so `active-hotstandby ≠ active-hot-standby`
 * and `single-region ≠ singleton` silently dropped the filter. Every
 * topology value sent to the backend now routes through
 * `canonicalizeTopologyMode` (below).
 */
export type TopologyMode = 'singleton' | 'active-active' | 'active-hot-standby' | 'active-passive'

/**
 * canonicalizeTopologyMode maps BOTH the legacy editor dialect
 * (`single-region` / `active-hotstandby`) AND the canonical vocabulary
 * (`singleton` / `active-hot-standby` / `active-passive`) onto the SINGLE
 * canonical token the backend understands. #3375 §3(f) — the one
 * vocabulary, applied at every topology post site. Pass-through for an
 * already-canonical or unrecognised value (the backend then returns a
 * clean invalid-topology error rather than the UI silently mangling it).
 */
export function canonicalizeTopologyMode(raw: string): string {
  switch (raw.trim().toLowerCase()) {
    case 'single-region':
    case 'singleton':
      return 'singleton'
    case 'active-hotstandby':
    case 'active-hot-standby':
      return 'active-hot-standby'
    case 'active-passive':
      return 'active-passive'
    case 'active-active':
      return 'active-active'
    default:
      return raw.trim()
  }
}

/**
 * DRPosture — 4-way classification surfaced on the cross-Sovereign
 * Applications table. See `deriveDRPosture` (Go) for the matrix.
 */
export type DRPosture = '—' | 'DR active' | 'DR alert' | 'Misconfigured'

/**
 * ApplicationIdent — identity triplet on an Application row.
 */
export interface ApplicationIdent {
  name: string
  blueprint?: string
  version?: string
}

/**
 * ApplicationRow — one row of `GET /fleet/applications`. The UI uses
 * `sovereign.id` + `app.name` + `namespace` to compute the chroot URL
 * for the click-through to AppDetail.
 */
export interface ApplicationRow {
  sovereign: SovereignSummary
  app: ApplicationIdent
  regions: string[]
  topology: TopologyMode | string
  drPosture: DRPosture
  status?: string
  org?: string
  namespace?: string
}

export interface ApplicationsResponse {
  applications: ApplicationRow[]
  total: number
}

/* ── REST endpoint URLs ──────────────────────────────────────────── */

function fleetSovereignsURL(page = 1, pageSize = 25): string {
  const sp = new URLSearchParams()
  if (page) sp.set('page', String(page))
  if (pageSize) sp.set('pageSize', String(pageSize))
  return `${API_BASE}/v1/fleet/sovereigns?${sp.toString()}`
}

function fleetSovereignSummaryURL(id: string): string {
  return `${API_BASE}/v1/fleet/sovereigns/${encodeURIComponent(id)}/summary`
}

export interface FleetApplicationsFilters {
  org?: string
  topology?: TopologyMode | string
  drPosture?: DRPosture | string
}

function fleetApplicationsURL(filters: FleetApplicationsFilters = {}): string {
  const sp = new URLSearchParams()
  if (filters.org) sp.set('org', filters.org)
  // #3375 §3(f) — canonicalise the topology filter so a legacy-dialect
  // value (`active-hotstandby`) matches the backend's canonical
  // `active-hot-standby` instead of silently filtering nothing.
  if (filters.topology) sp.set('topology', canonicalizeTopologyMode(filters.topology))
  if (filters.drPosture) sp.set('drPosture', filters.drPosture)
  const qs = sp.toString()
  return qs ? `${API_BASE}/v1/fleet/applications?${qs}` : `${API_BASE}/v1/fleet/applications`
}

/* ── Fetchers (used by hooks + tests) ────────────────────────────── */

async function fetchJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const r = await authedFetch(url, { method: 'GET', signal })
  if (!r.ok) {
    let detail = ''
    try {
      const j = await r.json()
      detail = (j && (j.detail || j.error)) || ''
    } catch {
      /* body wasn't JSON — that's ok */
    }
    throw new Error(`fleet api ${r.status}${detail ? ` — ${detail}` : ''}`)
  }
  return (await r.json()) as T
}

export function listSovereigns(
  page = 1,
  pageSize = 25,
  signal?: AbortSignal,
): Promise<SovereignsResponse> {
  return fetchJSON<SovereignsResponse>(fleetSovereignsURL(page, pageSize), signal)
}

export function getSovereignSummary(
  id: string,
  signal?: AbortSignal,
): Promise<SovereignDetail> {
  return fetchJSON<SovereignDetail>(fleetSovereignSummaryURL(id), signal)
}

export function listApplications(
  filters: FleetApplicationsFilters = {},
  signal?: AbortSignal,
): Promise<ApplicationsResponse> {
  return fetchJSON<ApplicationsResponse>(fleetApplicationsURL(filters), signal)
}

/* ── Fleet treemap (TBD-E14) ───────────────────────────────────────
 *
 * Mothership-only single-layer treemap where each cell is one
 * Sovereign. Wire shape is identical to the per-Sovereign
 * `/api/v1/dashboard/treemap` response (TreemapItem in
 * treemap.types.ts) so the existing recharts renderer consumes both
 * with zero new component code. See backend handler
 * `fleet_treemap.go` for the contract + why a separate endpoint.
 */

export type FleetTreemapSizeBy = 'apps' | 'age'
export type FleetTreemapColorBy = 'health' | 'age'

export interface FleetTreemapItem {
  id: string | null
  name: string
  count: number
  percentage: number | null
  size_value?: number
  children?: FleetTreemapItem[]
}

export interface FleetTreemapResponse {
  items: FleetTreemapItem[]
  total_count: number
}

function fleetTreemapURL(opts: {
  sizeBy?: FleetTreemapSizeBy
  colorBy?: FleetTreemapColorBy
} = {}): string {
  const sp = new URLSearchParams()
  if (opts.sizeBy) sp.set('size_by', opts.sizeBy)
  if (opts.colorBy) sp.set('color_by', opts.colorBy)
  const qs = sp.toString()
  return qs ? `${API_BASE}/v1/fleet/treemap?${qs}` : `${API_BASE}/v1/fleet/treemap`
}

export function getFleetTreemap(
  opts: { sizeBy?: FleetTreemapSizeBy; colorBy?: FleetTreemapColorBy } = {},
  signal?: AbortSignal,
): Promise<FleetTreemapResponse> {
  return fetchJSON<FleetTreemapResponse>(fleetTreemapURL(opts), signal)
}

/* ── Display helpers (single source of truth for UI palette) ─────── */

/**
 * healthBadgeColor — palette mapping for SovereignSummary.health. Used
 * by SovereignCard + table-cell badges so palette is consistent across
 * the dashboard.
 *
 * Green = healthy (Deployment.Status == ready);
 * Yellow = in-flight or unknown-but-trying;
 * Red = failed;
 * Slate = legitimately unknown (no signal at all yet).
 */
export function healthBadgeColor(h: SovereignHealth): string {
  switch (h) {
    case 'green':
      return 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'
    case 'yellow':
      return 'bg-amber-500/15 text-amber-400 border-amber-500/30'
    case 'red':
      return 'bg-rose-500/15 text-rose-400 border-rose-500/30'
    case 'unknown':
    default:
      return 'bg-slate-500/15 text-slate-400 border-slate-500/30'
  }
}

/**
 * healthLabel — human-readable label for SovereignSummary.health. Same
 * single-source rule as healthBadgeColor.
 */
export function healthLabel(h: SovereignHealth): string {
  switch (h) {
    case 'green':
      return 'Healthy'
    case 'yellow':
      return 'In flight'
    case 'red':
      return 'Failed'
    case 'unknown':
    default:
      return 'Unknown'
  }
}

/**
 * drPostureBadgeColor — palette mapping for the cross-Sovereign table
 * DR posture column.
 */
export function drPostureBadgeColor(p: DRPosture | string): string {
  switch (p) {
    case 'DR active':
      return 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'
    case 'DR alert':
      return 'bg-rose-500/15 text-rose-400 border-rose-500/30'
    case 'Misconfigured':
      return 'bg-amber-500/15 text-amber-400 border-amber-500/30'
    case '—':
    default:
      return 'bg-slate-500/15 text-slate-400 border-slate-500/30'
  }
}

/**
 * sovereignChrootURL — compute the URL the Sovereign card click navigates
 * to. The UI is mode-aware (see src/shared/config/urls.ts): on
 * Catalyst-Zero (mothership) we deep-link to the per-deployment shell;
 * on a chroot Sovereign we stay on the same host and skip the redirect.
 *
 * `appName` and `namespace` are optional — passed for the cross-Sovereign
 * row click-through that opens AppDetail in the relevant Sovereign.
 */
export function sovereignChrootURL(
  sov: SovereignSummary,
  opts: { appName?: string; namespace?: string } = {},
): string {
  // Customer console host: console.<sov.fqdn>; falls back to the
  // mothership's per-deployment shell when no FQDN is wired (test path
  // for the empty-state Sovereign rows).
  if (!sov.fqdn) {
    return `${API_BASE.replace(/\/api$/, '')}/provision/${encodeURIComponent(sov.id)}/dashboard`
  }
  const consoleHost = sov.fqdn.startsWith('console.')
    ? sov.fqdn
    : `console.${sov.fqdn}`
  if (opts.appName) {
    return `https://${consoleHost}/app/${encodeURIComponent(opts.appName)}`
  }
  return `https://${consoleHost}/dashboard`
}
