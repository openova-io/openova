/**
 * compliance.api.ts — typed REST client wrappers for catalyst-api
 * compliance endpoints (slice S, #1096).
 *
 * Wire shape mirrors `internal/handler/compliance.go`:
 *   - Score                 (per-resource + rollup)
 *   - PolicyView            (one row per active policy)
 *   - Violation             (offending resource + policy + message)
 *   - ScorecardResponse     (sovereign + orgs + envs + apps in one doc)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * derives from the central `API_BASE` constant. Per the canonical-seam
 * map, API calls go through `authedFetch` so the OIDC Authorization
 * header is attached on chroot consoles.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/* ── Wire types ──────────────────────────────────────────────────── */

export type ComplianceResult = 'pass' | 'fail' | 'skip' | 'warn' | 'na'
export type PolicyMode = 'permissive' | 'enforcing'
export type PolicySource = 'kyverno' | 'evaluator'
export type ScoreScope =
  | 'resource'
  | 'application'
  | 'environment'
  | 'organization'
  | 'sovereign'

/**
 * Score — backend Score struct. `total` is `*int` server-side: JSON null
 * encodes "no data yet" so the UI can grey the cell.
 */
export interface Score {
  scope: ScoreScope
  id: string
  resource?: string
  total: number | null
  policyResults?: Record<string, ComplianceResult | string>
  environmentRef?: string
  organizationRef?: string
  applicationRef?: string
  numerator: number
  denominator: number
  violations?: number
  updatedAt: string
}

export interface PolicyView {
  name: string
  weight: number
  scope: string // stateful | stateless | all
  mode: PolicyMode | string
  violations: number
  source: PolicySource | string
  description?: string
  /** Per `policies.kyverno.io/severity` annotation: low | medium | high | critical. */
  severity?: string
  /** ClusterPolicy `spec.rules[].name` — the per-rule list the drill-down renders. */
  rules?: string[]
  /** Per `policies.kyverno.io/title` annotation — human-readable label. */
  title?: string
  /** Per `policies.kyverno.io/category` annotation — Pod Security Standards bucket. */
  category?: string
}

export interface Violation {
  resource: string
  namespace?: string
  policy: string
  rule?: string
  result: string
  message?: string
  application?: string
  environment?: string
  time: string
}

export interface ScorecardResponse {
  sovereign: Score
  organizations: Score[]
  environments: Score[]
  applications: Score[]
  generatedAt: string
}

export interface PoliciesResponse {
  items: PolicyView[]
  count: number
}

export interface ViolationsResponse {
  items: Violation[]
  total: number
  offset: number
  limit: number
}

/**
 * EnvironmentPolicyModeUpdate — body of the U5 toggle PUT.
 *
 * The endpoint receives the full set of (policy → mode) overrides the
 * UI wants applied in one shot, so the operator's diff in the
 * confirmation dialog matches exactly what the controller will write.
 */
export interface EnvironmentPolicyModeUpdate {
  modes: Record<string, PolicyMode>
}

/* ── Endpoint helpers ────────────────────────────────────────────── */

function complianceBase(sovereignId: string): string {
  return `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/compliance`
}

export function streamURL(sovereignId: string, accessToken?: string): string {
  const params = new URLSearchParams()
  if (accessToken) params.set('access_token', accessToken)
  const qs = params.toString()
  return `${complianceBase(sovereignId)}/stream${qs ? '?' + qs : ''}`
}

/* ── REST calls ──────────────────────────────────────────────────── */

export async function getScorecard(sovereignId: string): Promise<ScorecardResponse> {
  const res = await authedFetch(`${complianceBase(sovereignId)}/scorecard`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`scorecard: HTTP ${res.status}`)
  }
  const raw = (await res.json()) as Partial<ScorecardResponse> | null
  return normalizeScorecard(raw)
}

/**
 * normalizeScorecard — defensively coerce a partial / nullable wire
 * payload into a fully-shaped ScorecardResponse.
 *
 * Why: the catalyst-api Go handler returns nil slices when a sovereign
 * has no rollup data yet (cold-start, fresh cluster, scorecard not
 * computed). Go's `encoding/json` serializes a nil `[]Score` to JSON
 * `null` rather than `[]`, so consumers see `applications: null` on
 * the wire. Calling `.map()` / `.filter()` / `.length` on `null`
 * crashes the React render — surfacing the global "Something went
 * wrong" fallback for the whole compliance dashboard.
 *
 * Coercing here gives every downstream caller (this hook, the SSE
 * merge, the treemap helper) a guaranteed array shape, so a
 * not-yet-computed scorecard renders the empty state instead of
 * crashing. Mirrors the same pattern other API wrappers in this
 * codebase use to absorb Go nil-slice quirks.
 */
export function normalizeScorecard(raw: Partial<ScorecardResponse> | null | undefined): ScorecardResponse {
  const safe = raw ?? {}
  const fallbackSovereign: Score = {
    scope: 'sovereign',
    id: '',
    total: null,
    numerator: 0,
    denominator: 0,
    updatedAt: new Date().toISOString(),
  }
  return {
    sovereign: safe.sovereign ?? fallbackSovereign,
    organizations: Array.isArray(safe.organizations) ? safe.organizations : [],
    environments: Array.isArray(safe.environments) ? safe.environments : [],
    applications: Array.isArray(safe.applications) ? safe.applications : [],
    generatedAt: safe.generatedAt ?? new Date().toISOString(),
  }
}

export async function getPolicies(sovereignId: string): Promise<PoliciesResponse> {
  const res = await authedFetch(`${complianceBase(sovereignId)}/policies`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`policies: HTTP ${res.status}`)
  }
  return res.json()
}

export async function getViolations(
  sovereignId: string,
  opts: { app?: string; limit?: number; offset?: number } = {},
): Promise<ViolationsResponse> {
  const params = new URLSearchParams()
  if (opts.app) params.set('app', opts.app)
  if (opts.limit !== undefined) params.set('limit', String(opts.limit))
  if (opts.offset !== undefined) params.set('offset', String(opts.offset))
  const qs = params.toString()
  const url = `${complianceBase(sovereignId)}/violations${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`violations: HTTP ${res.status}`)
  }
  return res.json()
}

/**
 * putEnvironmentPolicyMode — U5 toggle PUT call.
 *
 * Per the brief, this writes the updated `EnvironmentPolicy.spec.compliance.modes.<policy>`
 * field. The catalyst-api PUT handler is wired by a follow-up slice;
 * this client surface is in place so the UI is ready the moment the
 * backend ships.
 */
export async function putEnvironmentPolicyMode(
  sovereignId: string,
  environmentRef: string,
  body: EnvironmentPolicyModeUpdate,
): Promise<void> {
  const url = `${API_BASE}/v1/sovereigns/${encodeURIComponent(sovereignId)}/environments/${encodeURIComponent(environmentRef)}/policy`
  const res = await authedFetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`set policy mode: HTTP ${res.status} ${detail}`)
  }
}

/* ── Score helpers ───────────────────────────────────────────────── */

/**
 * scoreLabel — human-readable score (0-100 or "—" for null total).
 */
export function scoreLabel(total: number | null): string {
  if (total === null || total === undefined) return '—'
  return `${Math.round(total)}`
}

/**
 * scoreColor — pass-rate gradient. Returns a CSS color string.
 *
 * Two palettes are exposed:
 *   - 'resilience' — red (0%) → yellow (50%) → green (100%)
 *   - 'security'   — red (0%) → amber (50%) → blue (100%)
 *
 * Null total → desaturated grey. Documented choice: security uses a
 * cooler high-end (blue) so the SecLead dashboard reads at a glance as
 * a different surface from the SRE Lead one.
 */
export type ColorPalette = 'resilience' | 'security'

const NULL_FILL = 'rgba(125, 125, 125, 0.45)'

export function scoreColor(total: number | null, palette: ColorPalette = 'resilience'): string {
  if (total === null || total === undefined) return NULL_FILL
  const pct = Math.max(0, Math.min(100, total))
  if (palette === 'security') {
    // red (0) → amber (50) → blue (100)
    if (pct < 50) {
      const t = pct / 50
      return mixRgb([220, 38, 38], [245, 158, 11], t)
    }
    const t = (pct - 50) / 50
    return mixRgb([245, 158, 11], [37, 99, 235], t)
  }
  // resilience: red (0) → yellow (50) → green (100)
  if (pct < 50) {
    const t = pct / 50
    return mixRgb([220, 38, 38], [234, 179, 8], t)
  }
  const t = (pct - 50) / 50
  return mixRgb([234, 179, 8], [22, 163, 74], t)
}

function mixRgb(a: [number, number, number], b: [number, number, number], t: number): string {
  const r = Math.round(a[0] + (b[0] - a[0]) * t)
  const g = Math.round(a[1] + (b[1] - a[1]) * t)
  const bl = Math.round(a[2] + (b[2] - a[2]) * t)
  return `rgb(${r}, ${g}, ${bl})`
}

/**
 * filterByPolicyDomain — filter a Score's PolicyResults to only the
 * named policies. Used by the Security-Lead view to slice the rollup
 * by policy-domain.
 *
 * Rollup scopes (org/env/app/sovereign) leave `policyResults` empty,
 * so this helper is a no-op for them — the UI relies on the per-resource
 * scoreboard for security-domain slicing.
 */
export function filterScoreToPolicies(score: Score, policyNames: ReadonlySet<string>): Score {
  if (!score.policyResults) return score
  const filtered: Record<string, string> = {}
  for (const [k, v] of Object.entries(score.policyResults)) {
    if (policyNames.has(k)) filtered[k] = v
  }
  return { ...score, policyResults: filtered }
}

/**
 * SECURITY_DOMAIN_POLICIES — set of policy names tagged
 * policy-domain: security per the master brief §K1+K2 listing. Used
 * by the SecLead dashboard to slice the rollup payload.
 */
export const SECURITY_DOMAIN_POLICIES: ReadonlySet<string> = new Set([
  'cilium-l7-mtls',
  'network-policy-present',
  'run-as-non-root',
  'cosign-verified',
  'secret-not-in-env',
])
