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
  getScorecard,
  scoreColor,
  scoreLabel,
  type ColorPalette,
  type Score,
  type ScorecardResponse,
} from './compliance.api'

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

  const [orgFilter, setOrgFilter] = useState<string | null>(null)
  const [envFilter, setEnvFilter] = useState<string | null>(null)

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
  const merged: ScorecardResponse | undefined = useMemo(() => {
    const base = initialDataOverride ?? query.data
    if (!base) return undefined
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

  // Apply org / env filters to applications before treemap render.
  const filteredApps: Score[] = useMemo(() => {
    if (!merged) return []
    return merged.applications.filter((a) => {
      if (orgFilter && a.organizationRef !== orgFilter) return false
      if (envFilter && a.environmentRef !== envFilter) return false
      return true
    })
  }, [merged, orgFilter, envFilter])

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

  const isEmpty =
    (!!initialDataOverride && initialDataOverride.applications.length === 0) ||
    (!query.isLoading && !!merged && merged.applications.length === 0)

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Compliance">
      <div data-testid={`compliance-dashboard-${routeKey}`} className="mx-auto max-w-7xl px-6 py-4">
        <div className="mb-4 flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold text-[var(--color-text-strong)]" data-testid="compliance-dashboard-title">
              {title}
            </h1>
            <p className="text-sm text-[var(--color-text-dim)]">
              Fleet view: Sovereign × Organization × Application × score. Cells are sized by
              policy weight, colored by pass-rate.
            </p>
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
                No compliance data yet.
              </p>
              <p>
                Once Kyverno reports + custom evaluators report back, applications will appear here.
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

        {/* Legend */}
        <ComplianceLegend palette={palette} />
      </div>
    </PortalShell>
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
