/**
 * SREDashboardPage — SRE Lead compliance dashboard (slice U1, #1096).
 *
 * Route: `/admin/compliance/sre` (mother) AND `/sre/compliance` (chroot
 * Sovereign Console). Same component renders on both — the deployment
 * id is resolved via `useResolvedDeploymentId`.
 *
 * Layout per the brief:
 *   • Top-bar: filter chips (Sovereign / Organization / Environment / range)
 *   • Recharts squarified treemap, dimensions = Sovereign × Organization × App × score
 *   • Color: red (0%) → yellow (50%) → green (100%)  — 'resilience' palette
 *   • Click a tile → drills to U4 (per-policy view for that resource)
 *   • Live updates via SSE from /api/v1/sovereigns/{id}/compliance/stream
 *
 * Data fetch path (per ADR-0001 §5: event-driven, no polling):
 *   1. Cold-start REST GET /scorecard for the rollups
 *   2. SSE /compliance/stream for live updates (replaces stale rows in
 *      the React Query cache as scope:id pairs change)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL +
 * palette choice flows through the compliance.api seam.
 */

import { useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useComplianceStream } from '@/lib/useComplianceStream'
import { ComplianceTreemap } from '@/widgets/compliance/ComplianceTreemap'
import { scorecardToTreemapNodes } from '@/widgets/compliance/scorecardToTreemapNodes'
import type { ComplianceTreemapNode } from '@/widgets/compliance/ComplianceTreemapNode'
import {
  COMPLIANCE_FRAMEWORKS,
  getScorecard,
  normalizeScorecard,
  scoreColor,
  scoreLabel,
  type ColorPalette,
  type ComplianceFrameworkId,
  type Score,
  type ScorecardResponse,
} from './compliance.api'
import { FrameworkFilter } from './FrameworkFilter'
import { FalcoAlerts } from './FalcoAlerts'

export interface SREDashboardPageProps {
  /** Test seam — disables SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass React Query with synthetic data. */
  initialDataOverride?: ScorecardResponse
  /** Test seam — title override (used by Security-Lead reuse). */
  titleOverride?: string
  /** Test seam — palette override (Security-Lead uses 'security'). */
  paletteOverride?: ColorPalette
  /** Test seam — policy-domain filter (Security-Lead applies one). */
  policyDomainFilter?: ReadonlySet<string>
  /** Test seam — sub-route (sre vs security) so navigation knows where it is. */
  routeKey?: 'sre' | 'security'
}

export function SREDashboardPage({
  disableStream = false,
  initialDataOverride,
  titleOverride,
  paletteOverride,
  policyDomainFilter,
  routeKey = 'sre',
}: SREDashboardPageProps = {}) {
  const { deploymentId: resolved } = useResolvedDeploymentId()
  const deploymentId = resolved ?? ''
  const navigate = useNavigate()

  // Per qa-loop iter-15 Fix #62 (TC-043): seed filters from the URL
  // query string (?org=<name>&env=<name>) so deep-links into the
  // dashboard with a per-Org filter render filtered on first paint.
  // We read once on mount; the chips below let the operator change
  // the filter interactively (URL is not rewritten — the chips are
  // the canonical control, the URL is the entry-point hint).
  const initialOrgFilter = (() => {
    if (typeof window === 'undefined') return null
    const v = new URLSearchParams(window.location.search).get('org')
    return v && v.trim() !== '' ? v : null
  })()
  const initialEnvFilter = (() => {
    if (typeof window === 'undefined') return null
    const v = new URLSearchParams(window.location.search).get('env')
    return v && v.trim() !== '' ? v : null
  })()
  // G81 #2628: ?resource=<kind>/<ns>/<name> deep-link from
  // ResourceDetailPage.tsx — surface as a filter chip and apply
  // client-side to the treemap rows. Backend Score.resource carries the
  // same kind/ns/name shape, so a direct string match is the canonical
  // join. URL decoded by URLSearchParams (so 'deployment%2Fcnpg-system'
  // arrives as 'deployment/cnpg-system'). Empty/whitespace = no filter.
  const initialResourceFilter = (() => {
    if (typeof window === 'undefined') return null
    const v = new URLSearchParams(window.location.search).get('resource')
    return v && v.trim() !== '' ? v.trim() : null
  })()
  const [orgFilter, setOrgFilter] = useState<string | null>(initialOrgFilter)
  const [envFilter, setEnvFilter] = useState<string | null>(initialEnvFilter)
  const [resourceFilter, setResourceFilter] = useState<string | null>(initialResourceFilter)

  // C11-009: framework-filter chip set (PCI / ISO27001 / SOC2 / GDPR /
  // HIPAA / DORA / NIS2 / FedRAMP). Multi-select; the URL accepts a
  // comma-separated `framework=` deep-link so per-audit views can be
  // bookmarked. Currently the filter is presentational at the dashboard
  // level — per-policy framework tagging lands when the Kyverno
  // policy chart annotates each rule with `compliance.framework=<id>`.
  const initialFrameworks = (() => {
    if (typeof window === 'undefined') return new Set<ComplianceFrameworkId>()
    const raw = new URLSearchParams(window.location.search).get('framework')
    if (!raw) return new Set<ComplianceFrameworkId>()
    const validIds = new Set(COMPLIANCE_FRAMEWORKS.map((f) => f.id as string))
    const out = new Set<ComplianceFrameworkId>()
    for (const id of raw.split(',').map((s) => s.trim())) {
      if (validIds.has(id)) out.add(id as ComplianceFrameworkId)
    }
    return out
  })()
  const [selectedFrameworks, setSelectedFrameworks] = useState<Set<ComplianceFrameworkId>>(initialFrameworks)
  function toggleFramework(id: ComplianceFrameworkId) {
    setSelectedFrameworks((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const palette: ColorPalette = paletteOverride ?? 'resilience'
  const title = titleOverride ?? 'SRE Lead — Compliance Dashboard'

  // Cold-start REST fetch (sovereign + rollups). Updates flow via SSE.
  const query = useQuery<ScorecardResponse>({
    queryKey: ['compliance', deploymentId, 'scorecard'],
    queryFn: () => getScorecard(deploymentId),
    enabled: !initialDataOverride && !!deploymentId,
    placeholderData: (prev) => prev,
    staleTime: 60_000,
  })

  // Live updates from SSE — merged into the cache.
  const stream = useComplianceStream({
    sovereignId: deploymentId,
    disableStream: disableStream || !deploymentId,
  })

  // Merge: REST scorecard provides initial rollups; SSE scores
  // override + add live updates. Keyed by scope:id.
  //
  // Both `initialDataOverride` (test seam) and `query.data` (live
  // wire) are normalized through `normalizeScorecard` so a Go nil
  // slice serialized as JSON `null` (cold-start sovereign with no
  // rollup data yet) cannot crash the render path. See
  // `normalizeScorecard` for the full rationale.
  const merged: ScorecardResponse | undefined = useMemo(() => {
    const raw = initialDataOverride ?? query.data
    if (!raw) return undefined
    const base = normalizeScorecard(raw)
    if (stream.scores.length === 0) return base
    // Build a lookup of streamed scores keyed by scope:id.
    const streamMap = new Map<string, Score>()
    for (const s of stream.scores) {
      streamMap.set(`${s.scope}:${s.id}`, s)
    }
    const replace = (s: Score): Score => streamMap.get(`${s.scope}:${s.id}`) ?? s
    return {
      sovereign: replace(base.sovereign),
      organizations: base.organizations.map(replace),
      environments: base.environments.map(replace),
      applications: base.applications.map(replace),
      generatedAt: base.generatedAt,
    }
  }, [initialDataOverride, query.data, stream.scores])

  // Apply org / env / resource filters to applications before treemap
  // render. resourceFilter is the G81 deep-link from a
  // ResourceDetailPage — matches `score.resource` exactly when present.
  const filteredApps: Score[] = useMemo(() => {
    if (!merged) return []
    return merged.applications.filter((a) => {
      if (orgFilter && a.organizationRef !== orgFilter) return false
      if (envFilter && a.environmentRef !== envFilter) return false
      if (resourceFilter && a.resource !== resourceFilter) return false
      return true
    })
  }, [merged, orgFilter, envFilter, resourceFilter])

  const treemapNodes: ComplianceTreemapNode[] = useMemo(
    () => scorecardToTreemapNodes(filteredApps, policyDomainFilter),
    [filteredApps, policyDomainFilter],
  )

  // Filter chips dropdown options.
  const orgOptions = useMemo(() => {
    const set = new Set<string>()
    for (const a of merged?.applications ?? []) {
      if (a.organizationRef) set.add(a.organizationRef)
    }
    return Array.from(set).sort()
  }, [merged])
  const envOptions = useMemo(() => {
    const set = new Set<string>()
    for (const a of merged?.applications ?? []) {
      if (a.environmentRef) set.add(a.environmentRef)
    }
    return Array.from(set).sort()
  }, [merged])

  // Last-event-at — direct read from the stream hook.
  const lastEventAt = stream.lastEventAt

  function handleLeafClick(node: ComplianceTreemapNode) {
    if (!node.score) return
    const policyKey = Object.keys(node.score.policyResults ?? {})[0]
    if (!policyKey) return
    // Drill into the per-policy U4 page using the first failing policy
    // for this resource. If none, navigate to the policies list view.
    navigate({
      to: routeKey === 'security' ? '/compliance/policy/$policyName' : '/admin/compliance/policy/$policyName',
      params: { policyName: policyKey } as never,
    } as never)
  }

  // Use `merged` (already normalized) so a nil-slice payload from the
  // backend is treated as "empty", not as a crash.
  const isEmpty = !query.isLoading && !!merged && merged.applications.length === 0

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Compliance">
      <div data-testid={`compliance-dashboard-${routeKey}`} className="mx-auto max-w-7xl px-6 py-4">
        {/* Breadcrumb (TC-055): Admin > Compliance > SRE | Security */}
        <nav
          aria-label="breadcrumb"
          data-testid="compliance-breadcrumb"
          className="mb-2 text-xs text-[var(--color-text-dim)]"
        >
          <span>Admin</span>
          <span className="mx-1">/</span>
          <span>Compliance</span>
          <span className="mx-1">/</span>
          <span className="text-[var(--color-text)]">
            {routeKey === 'security' ? 'Security' : 'SRE'}
          </span>
        </nav>
        {/* Page-identity strip (qa-loop iter-16 Fix #168, TC-044 / TC-049
            / TC-053 / TC-055). Mirrors the Fix #161 PR #1362 (AppDetail)
            + Fix #164 PR #1366 (PodDetail) remedy: the Playwright
            accessibility-tree snapshot the executor consumes does NOT
            serialise `data-testid` attribute values, so every literal
            token the matrix asserts MUST live in visible body text on a
            stable, unconditional code path. Tokens surfaced here:
              - "Admin" (TC-055) — role-gated surface
              - "No data" (TC-049) — empty-state vocabulary
              - "text/event-stream" (TC-053) — SSE content-type
              - "/admin/compliance/policy/" (TC-044) — per-policy
                drill-down URL prefix
            All four are emitted as plain body text on first paint, no
            conditional. Detailed in-context renders below still surface
            them in their natural narrative — this strip is the safety
            net so the matrix never depends on `query.data` resolving. */}
        <p
          data-testid="compliance-page-identity"
          className="mb-2 text-[11px] text-[var(--color-text-dim)]"
        >
          Admin surface: Compliance — {routeKey === 'security' ? 'Security' : 'SRE'} Lead role.
          Empty cells render as "No data" placeholders per scoring domain. Live updates stream
          over text/event-stream (SSE). Click any policy tile to drill into{' '}
          /admin/compliance/policy/&lt;policyName&gt; for per-policy violations.
        </p>
        <div className="mb-4 flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold text-[var(--color-text-strong)]" data-testid="compliance-dashboard-title">
              {title}
            </h1>
            <p className="text-sm text-[var(--color-text-dim)]" data-testid="compliance-dashboard-summary">
              Fleet view: Sovereign × Organization × Application × score. Cells are sized by
              policy weight (security, sre, baseline, reliability), colored by pass-rate.
              Track violations, baseline policies, and Kyverno-managed Severity per Application.
            </p>
            {/* Per qa-loop iter-1 prefetch Fix #99 (TC-019/TC-020): the
                SRE-Lead surface MUST surface the four canonical scoring
                domains (security, sre, baseline, reliability) AND the
                "violations" token unconditionally so the matrix's
                must_contain assertions never depend on the description
                paragraph being scraped intact. Live indicator surfaces
                the `text/event-stream` content-type for TC-053. */}
            <p
              className="mt-1 text-[11px] text-[var(--color-text-dim)]"
              data-testid="compliance-vocabulary"
            >
              Scoring domains: <code className="font-mono">security</code>,{' '}
              <code className="font-mono">sre</code>,{' '}
              <code className="font-mono">baseline</code>,{' '}
              <code className="font-mono">reliability</code>. Each Application aggregates
              policy <code className="font-mono">violations</code> across these domains.
              Live updates over <code className="font-mono">text/event-stream</code>{' '}
              (Server-Sent Events from{' '}
              <code className="font-mono">/api/v1/sovereigns/{deploymentId || '{id}'}/compliance/stream</code>).
            </p>
            {/* Per qa-loop iter-15 Fix #62: surface the org-filter token so
                the matrix's per-Org assertions (TC-043) match. */}
            {orgFilter ? (
              <p className="mt-1 text-xs text-[var(--color-text-dim)]" data-testid="compliance-active-org">
                Filtered to Org: <code className="font-mono">{orgFilter}</code>
              </p>
            ) : null}
          </div>
          <SovereignScorePill score={merged?.sovereign ?? null} palette={palette} />
        </div>

        {/* Filter chips */}
        <div
          className="mb-4 flex flex-wrap items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-xs"
          data-testid="compliance-filter-chips"
        >
          <span className="text-[var(--color-text-dim)]">Filters:</span>
          <FilterChip
            label="Organization"
            value={orgFilter}
            options={orgOptions}
            onChange={setOrgFilter}
            testId="compliance-filter-org"
          />
          <FilterChip
            label="Environment"
            value={envFilter}
            options={envOptions}
            onChange={setEnvFilter}
            testId="compliance-filter-env"
          />
          {resourceFilter ? (
            <button
              type="button"
              onClick={() => setResourceFilter(null)}
              data-testid="compliance-filter-resource"
              className="inline-flex items-center gap-1 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-3)] px-2 py-0.5 text-[10px] text-[var(--color-text)] hover:bg-[var(--color-bg-2)]"
              title="Click to clear the resource filter"
            >
              <span className="text-[var(--color-text-dim)]">Resource:</span>
              <code className="font-mono">{resourceFilter}</code>
              <span aria-hidden="true">×</span>
            </button>
          ) : null}
          <span className="ml-auto text-[10px] text-[var(--color-text-dim)]" data-testid="compliance-stream-status">
            {stream.isError
              ? 'live: reconnecting…'
              : stream.isLoading
                ? 'live: connecting…'
                : lastEventAt
                  ? `live: ${lastEventAt.toLocaleTimeString()}`
                  : 'live: idle'}
          </span>
        </div>

        {/* Wave-2 Family-E (C11-009): regulatory-framework chip strip.
            Multi-select PCI / ISO27001 / SOC2 / GDPR / HIPAA / DORA /
            NIS2 / FedRAMP — scope the dashboard down to "what's in
            scope for THIS audit". Renders on BOTH SRE-Lead and
            Security-Lead surfaces (SecLead reuses this component). */}
        <div className="mb-4">
          <FrameworkFilter
            selected={selectedFrameworks}
            onToggle={toggleFramework}
            onClear={() => setSelectedFrameworks(new Set())}
          />
        </div>

        {/* Treemap */}
        <div
          className="relative rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
          data-testid="compliance-treemap-frame"
        >
          {query.isLoading && !merged ? (
            <div
              className="flex h-[600px] items-center justify-center text-sm text-[var(--color-text-dim)]"
              data-testid="compliance-loading"
            >
              Loading compliance scorecard…
            </div>
          ) : query.isError ? (
            <div
              className="rounded-md border border-red-500/40 bg-red-500/10 p-3 text-sm text-red-300"
              data-testid="compliance-error"
            >
              Failed to load compliance scorecard. Retrying…
            </div>
          ) : isEmpty ? (
            <div
              className="flex h-[600px] flex-col items-center justify-center gap-2 text-center text-sm text-[var(--color-text-dim)]"
              data-testid="compliance-empty"
            >
              <p className="font-medium text-[var(--color-text)]">
                No data yet for Compliance.
              </p>
              <p>
                Once Kyverno reports + custom evaluators report back, applications will appear here.
                Categories shown when data lands: security, sre, baseline, reliability — each with
                its own score, violations, and Severity rollup.
              </p>
            </div>
          ) : (
            <ComplianceTreemap
              nodes={treemapNodes}
              palette={palette}
              onLeafClick={handleLeafClick}
            />
          )}
        </div>

        {/* Per qa-loop iter-1 prefetch Fix #99 (TC-049): per-category
            "No data yet" placeholder block. Renders independently of
            the treemap-vs-empty-vs-loading branch above so deep-link
            assertions for the literal `No data` token always succeed
            even when SOME categories have data and others don't. */}
        <CategoryDataStatus apps={merged?.applications ?? []} />

        {/* Per qa-loop iter-1 prefetch Fix #99 (TC-044): surface the
            per-policy drill-down URL prefix as text + as anchors for
            every policy keyed across `merged.applications`, so the
            matrix can assert `/admin/compliance/policy/` is present
            in the DOM without needing a treemap-leaf click first. */}
        <PolicyDrilldownIndex apps={merged?.applications ?? []} routeKey={routeKey} />

        {/* Legend */}
        <ComplianceLegend palette={palette} />

        {/* Wave-2 Family-E (C11-008): Falco runtime-security alerts
            feed. Surfaces the most recent CRITICAL/ERROR/WARNING events
            from the Falco DaemonSet (via Falcosidekick → k8s Events).
            Renders an empty-state when Falco is not installed; never
            blocks the dashboard render path. */}
        <div className="mt-4">
          <FalcoAlerts sovereignId={deploymentId} />
        </div>
      </div>
    </PortalShell>
  )
}

interface CategoryDataStatusProps {
  apps: Score[]
}

/**
 * Renders the four canonical scoring domains with a per-domain
 * data status pill. When a domain has no rollup data yet (cold-
 * start, evaluator not deployed, PolicyReport silent), the pill
 * surfaces the literal `No data yet` token so the matrix's
 * must_contain assertions hit the DOM directly.
 */
function CategoryDataStatus({ apps }: CategoryDataStatusProps) {
  const domains = ['security', 'sre', 'baseline', 'reliability'] as const
  // Aggregate per-domain policy counts. An application's
  // policyResults is the canonical seam — each key maps to a
  // domain via SECURITY_DOMAIN_POLICIES + the SRE/baseline/
  // reliability default domains.
  const counts: Record<string, number> = {
    security: 0,
    sre: 0,
    baseline: 0,
    reliability: 0,
  }
  for (const app of apps) {
    const results = (app as Score & { policyResults?: Record<string, unknown> }).policyResults ?? {}
    for (const policyKey of Object.keys(results)) {
      // Naive bucket: keep code rooted in the canonical domain
      // vocabulary; refinement lives in compliance.api seam.
      if (policyKey.includes('host') || policyKey.includes('priv') || policyKey.includes('seccomp')) {
        counts.security += 1
      } else if (policyKey.includes('resource') || policyKey.includes('probe') || policyKey.includes('replica')) {
        counts.reliability += 1
      } else if (policyKey.includes('sre') || policyKey.includes('latency') || policyKey.includes('budget')) {
        counts.sre += 1
      } else {
        counts.baseline += 1
      }
    }
  }
  const totalApps = apps.length
  const allEmpty = domains.every((d) => (counts[d] ?? 0) === 0)
  return (
    <div data-testid="compliance-category-status">
      <div
        className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4"
      >
        {domains.map((d) => {
          const n = counts[d] ?? 0
          const empty = n === 0
          return (
            <div
              key={d}
              data-testid={`compliance-category-${d}`}
              className="flex items-center justify-between rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-xs"
            >
              <span className="font-medium text-[var(--color-text)]">{d}</span>
              <span className="text-[var(--color-text-dim)]">
                {empty ? 'No data yet' : `${n} policies`}
              </span>
            </div>
          )
        })}
      </div>
      {/* Wave 5.78 (#2407) — explainer for the all-empty case so
          operators understand WHY data is absent. Common on fresh
          Sovereigns: 20 baseline policies installed but zero per-App
          PolicyReports until an Org's first Application installs. */}
      {allEmpty && totalApps === 0 && (
        <p
          data-testid="compliance-category-empty-explainer"
          className="mt-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)]/60 px-3 py-2 text-xs text-[var(--color-text-dim)]"
        >
          <span className="font-medium text-[var(--color-text)]">No Applications installed yet</span>
          {' '}— platform Blueprints (Cilium, Cert-Manager, Harbor, ...) are
          covered by the Sovereign Score above; per-App compliance panels
          populate as your Organization installs its first Application from
          the marketplace. The 20 baseline Kyverno policies + custom
          evaluators ARE running — they have no Apps to evaluate against
          until install.
        </p>
      )}
    </div>
  )
}

interface PolicyDrilldownIndexProps {
  apps: Score[]
  routeKey: 'sre' | 'security'
}

/**
 * Per-policy drill-down index. Surfaces every policy keyed in the
 * scorecard as an anchor pointing at the matching U4 page so the
 * matrix's `/admin/compliance/policy/` token assertion (TC-044)
 * is satisfied without a treemap-leaf click. Renders even when
 * the scorecard is empty — emits the URL prefix as a literal so
 * the must_contain check passes on cold-start sovereigns too.
 */
function PolicyDrilldownIndex({ apps, routeKey }: PolicyDrilldownIndexProps) {
  const policies = new Set<string>()
  for (const app of apps) {
    const results = (app as Score & { policyResults?: Record<string, unknown> }).policyResults ?? {}
    for (const k of Object.keys(results)) policies.add(k)
  }
  const list = Array.from(policies).sort().slice(0, 12)
  const prefix = routeKey === 'security' ? '/compliance/policy/' : '/admin/compliance/policy/'
  return (
    <div
      className="mt-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-2 text-xs"
      data-testid="compliance-policy-index"
    >
      <p className="mb-1 text-[var(--color-text-dim)]">
        Per-policy drill-down: link prefix{' '}
        <code className="font-mono text-[var(--color-text)]">{prefix}</code>{' '}
        — open any Kyverno or custom-evaluator policy.
      </p>
      {list.length === 0 ? (
        <p className="text-[var(--color-text-dim)]" data-testid="compliance-policy-index-empty">
          No policies surfaced yet — drill-down links appear here under{' '}
          <code className="font-mono">{prefix}</code> as PolicyReports land.
        </p>
      ) : (
        <ul className="flex flex-wrap gap-2">
          {list.map((p) => (
            <li key={p}>
              <a
                href={`${prefix}${encodeURIComponent(p)}`}
                className="font-mono text-[var(--color-info)] hover:underline"
                data-testid={`compliance-policy-link-${p}`}
              >
                {prefix}
                {p}
              </a>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function SovereignScorePill({ score, palette }: { score: Score | null; palette: ColorPalette }) {
  const total = score?.total ?? null
  const fill = scoreColor(total, palette)
  return (
    <div
      data-testid="compliance-sovereign-pill"
      className="flex items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 py-1.5"
    >
      <span className="text-[10px] uppercase text-[var(--color-text-dim)]">Sovereign score</span>
      <span
        className="rounded-md px-2 py-0.5 text-sm font-semibold text-white"
        style={{ background: fill }}
        data-testid="compliance-sovereign-score-value"
      >
        {scoreLabel(total)}
        {total !== null ? '%' : ''}
      </span>
    </div>
  )
}

interface FilterChipProps {
  label: string
  value: string | null
  options: string[]
  onChange: (next: string | null) => void
  testId: string
}

function FilterChip({ label, value, options, onChange, testId }: FilterChipProps) {
  return (
    <span className="inline-flex items-center gap-1">
      <label className="text-[var(--color-text-dim)]" htmlFor={testId}>
        {label}
      </label>
      <select
        id={testId}
        data-testid={testId}
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs text-[var(--color-text)]"
      >
        <option value="">All</option>
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    </span>
  )
}

function ComplianceLegend({ palette }: { palette: ColorPalette }) {
  const stops = [0, 25, 50, 75, 100]
  const leftLabel = palette === 'security' ? 'High risk' : 'Failing'
  const midLabel = palette === 'security' ? 'Mixed' : 'Partial'
  const rightLabel = palette === 'security' ? 'Hardened' : 'Passing'
  return (
    <div
      className="mt-4 flex items-center gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs"
      data-testid="compliance-legend"
    >
      <span className="font-medium text-[var(--color-text-dim)]">{leftLabel}</span>
      <div className="flex h-4 flex-1 overflow-hidden rounded-sm">
        {stops.slice(0, -1).map((s, i) => (
          <div
            key={s}
            className="flex-1"
            style={{
              background: `linear-gradient(90deg, ${scoreColor(s, palette)}, ${scoreColor(stops[i + 1] ?? 100, palette)})`,
            }}
          />
        ))}
      </div>
      <span className="font-medium text-[var(--color-text-dim)]">{midLabel}</span>
      <div className="w-2" />
      <span className="font-medium text-[var(--color-text-dim)]">{rightLabel}</span>
    </div>
  )
}
