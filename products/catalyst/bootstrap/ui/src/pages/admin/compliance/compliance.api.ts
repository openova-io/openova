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

/**
 * CategoryScore — backend per-category headline rollup (security / sre /
 * baseline). Always present on the wire (zero-denominator entries
 * surface `score:0`, never null). Mirrors
 * `internal/handler/compliance.go.CategoryScore`.
 */
export interface CategoryScore {
  score: number
  numerator: number
  denominator: number
  policyCount: number
}

export interface ScorecardResponse {
  sovereign: Score
  organizations: Score[]
  environments: Score[]
  applications: Score[]
  /**
   * Server-computed per-category headline scorecard. Keys are the
   * canonical scoring domains: `security`, `sre`, `baseline`. Present
   * on every response — empty domains surface `score:0`. UI fallback:
   * when `applications: []` (per-app rollup not yet populated — typical
   * on a fresh Sovereign before workloads carry catalyst app labels)
   * the treemap synthesises one cell per non-zero category so operators
   * see the real compliance distribution instead of an empty surface
   * (G86b #2633, 2026-06-01).
   */
  categoryScores?: Record<string, CategoryScore>
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
    categoryScores:
      safe.categoryScores && typeof safe.categoryScores === 'object'
        ? (safe.categoryScores as Record<string, CategoryScore>)
        : undefined,
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

/* ── Wave-2 Family-E: runtime + supply-chain compliance ─────────────
 *
 * Three additional surfaces wired by compliance_runtime.go:
 *   - Falco runtime alerts        (C11-008)
 *   - Trivy SBOM + CVE rollups    (C11-010)
 *   - Framework filter catalog    (C11-009)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the framework list lives here
 * as a single source of truth so the chip strip + URL deep-link
 * parser + per-app evidence packs all read from the same catalogue.
 */

export type ComplianceFrameworkId =
  | 'pci'
  | 'iso27001'
  | 'soc2'
  | 'gdpr'
  | 'hipaa'
  | 'dora'
  | 'nis2'
  | 'fedramp'

export interface ComplianceFramework {
  id: ComplianceFrameworkId
  label: string
  description: string
}

/**
 * COMPLIANCE_FRAMEWORKS — supported regulatory frameworks. The chip
 * strip (FrameworkFilter) iterates over this list in order. Adding a
 * new framework requires (1) appending here and (2) tagging policy
 * rules with the framework id in the Kyverno chart annotations.
 */
export const COMPLIANCE_FRAMEWORKS: ReadonlyArray<ComplianceFramework> = [
  { id: 'pci', label: 'PCI DSS', description: 'Payment Card Industry Data Security Standard v4.0' },
  { id: 'iso27001', label: 'ISO 27001', description: 'Information security management — ISO/IEC 27001:2022' },
  { id: 'soc2', label: 'SOC 2', description: 'AICPA SOC 2 Trust Services Criteria (Security/Availability/Confidentiality)' },
  { id: 'gdpr', label: 'GDPR', description: 'EU General Data Protection Regulation (Reg. 2016/679)' },
  { id: 'hipaa', label: 'HIPAA', description: 'US Health Insurance Portability and Accountability Act Security Rule' },
  { id: 'dora', label: 'DORA', description: 'EU Digital Operational Resilience Act (Reg. 2022/2554)' },
  { id: 'nis2', label: 'NIS 2', description: 'EU Network and Information Security Directive 2 (Dir. 2022/2555)' },
  { id: 'fedramp', label: 'FedRAMP', description: 'US Federal Risk and Authorization Management Program (Moderate baseline)' },
]

/* ── Falco runtime alerts (C11-008) ─────────────────────────────── */

export interface FalcoEvent {
  time: string
  priority: string // EMERGENCY | ALERT | CRITICAL | ERROR | WARNING | NOTICE | INFO | DEBUG
  rule: string
  output: string
  source?: string
  namespace?: string
  pod?: string
  container?: string
  tags?: string[]
  hostname?: string
}

export interface FalcoEventsResponse {
  items: FalcoEvent[]
  total: number
  installed: boolean
  source: string
  updatedAt: string
}

export async function getFalcoEvents(
  sovereignId: string,
  opts: { limit?: number; priorities?: readonly string[] } = {},
): Promise<FalcoEventsResponse> {
  const params = new URLSearchParams()
  if (opts.limit !== undefined) params.set('limit', String(opts.limit))
  if (opts.priorities && opts.priorities.length > 0) {
    params.set('prio', opts.priorities.join(','))
  }
  const qs = params.toString()
  const url = `${complianceBase(sovereignId)}/falco${qs ? '?' + qs : ''}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`falco: HTTP ${res.status}`)
  }
  const raw = (await res.json()) as Partial<FalcoEventsResponse> | null
  const safe = raw ?? {}
  return {
    items: Array.isArray(safe.items) ? safe.items : [],
    total: typeof safe.total === 'number' ? safe.total : 0,
    installed: !!safe.installed,
    source: safe.source ?? 'empty',
    updatedAt: safe.updatedAt ?? new Date().toISOString(),
  }
}

/* ── Trivy SBOM + CVE (C11-010) ─────────────────────────────────── */

export interface VulnerabilitySeverityCounts {
  critical: number
  high: number
  medium: number
  low: number
  unknown: number
  total: number
}

export interface SBOMComponent {
  name: string
  version?: string
  type?: string // library | application | operating-system
  purl?: string
  licenses?: string
}

export interface SBOMContainerEntry {
  container: string
  image?: string
  digest?: string
  severity: VulnerabilitySeverityCounts
  components?: SBOMComponent[]
  reportName?: string
  scanCompletedAt?: string
}

export interface SBOMPodResponse {
  pod: string
  namespace: string
  containers: SBOMContainerEntry[]
  countsByContainer: Record<string, VulnerabilitySeverityCounts>
  totalCounts: VulnerabilitySeverityCounts
  updatedAt: string
  installed: boolean
}

export interface SBOMSummaryResponse {
  total: VulnerabilitySeverityCounts
  byNamespace: Record<string, VulnerabilitySeverityCounts>
  byImage: Record<string, VulnerabilitySeverityCounts>
  pods: number
  containers: number
  installed: boolean
  updatedAt: string
}

function emptyCounts(): VulnerabilitySeverityCounts {
  return { critical: 0, high: 0, medium: 0, low: 0, unknown: 0, total: 0 }
}

export async function getSBOMForPod(
  sovereignId: string,
  namespace: string,
  podName: string,
): Promise<SBOMPodResponse> {
  const params = new URLSearchParams()
  params.set('ns', namespace)
  params.set('pod', podName)
  const url = `${complianceBase(sovereignId)}/sbom?${params.toString()}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    throw new Error(`sbom: HTTP ${res.status}`)
  }
  const raw = (await res.json()) as Partial<SBOMPodResponse> | null
  const safe = raw ?? {}
  return {
    pod: safe.pod ?? podName,
    namespace: safe.namespace ?? namespace,
    containers: Array.isArray(safe.containers) ? safe.containers : [],
    countsByContainer: safe.countsByContainer ?? {},
    totalCounts: safe.totalCounts ?? emptyCounts(),
    updatedAt: safe.updatedAt ?? new Date().toISOString(),
    installed: !!safe.installed,
  }
}

export async function getSBOMSummary(sovereignId: string): Promise<SBOMSummaryResponse> {
  const res = await authedFetch(`${complianceBase(sovereignId)}/sbom/summary`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`sbom summary: HTTP ${res.status}`)
  }
  const raw = (await res.json()) as Partial<SBOMSummaryResponse> | null
  const safe = raw ?? {}
  return {
    total: safe.total ?? emptyCounts(),
    byNamespace: safe.byNamespace ?? {},
    byImage: safe.byImage ?? {},
    pods: typeof safe.pods === 'number' ? safe.pods : 0,
    containers: typeof safe.containers === 'number' ? safe.containers : 0,
    installed: !!safe.installed,
    updatedAt: safe.updatedAt ?? new Date().toISOString(),
  }
}

/* ── Per-name policy lookup (C11-003 fix) ───────────────────────── */

/**
 * getPolicyByName — fetch one policy directly by name from the live
 * cluster. Falls through to a 404 when the policy isn't deployed.
 *
 * The PolicyDrilldownPage uses this AFTER the bulk getPolicies()
 * miss, so the page survives policies that exist on the cluster but
 * weren't surfaced by the cached aggregator (e.g. compliance-tier
 * policies installed AFTER the page first loaded, or
 * non-baseline-tier ClusterPolicies the aggregator doesn't track).
 */
export async function getPolicyByName(
  sovereignId: string,
  policyName: string,
): Promise<PolicyView | null> {
  const url = `${complianceBase(sovereignId)}/policies/${encodeURIComponent(policyName)}`
  const res = await authedFetch(url, { headers: { Accept: 'application/json' } })
  if (res.status === 404) return null
  if (!res.ok) {
    throw new Error(`policy: HTTP ${res.status}`)
  }
  return (await res.json()) as PolicyView
}
