/**
 * ComplianceTab — App owner compliance view, embedded into the
 * Application detail page (slice U3, #1096).
 *
 * Renders, per the brief:
 *   • Single Application's score (large number, 0-100, color-graded)
 *   • Drift panel: per-policy outcome with pass/fail/skip pill
 *     (sparkline trend is a hook for the Mimir time-series story —
 *     ships as a placeholder grey bar today; the API layer surfaces
 *     hooks once the trend endpoint lands)
 *   • "What would I need to fix to reach 90%" — sorted list of
 *     fail+skip policies with potential point-gain (weight ×
 *     pass-rate-improvement)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), endpoint URLs
 * compose from API_BASE in compliance.api.
 *
 * Per the canonical-seam map (`Server state — TanStack Query`), data
 * fetches use useQuery + the SSE stream merges live updates over the
 * REST baseline.
 */

import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  getPolicies,
  getScorecard,
  getViolations,
  scoreColor,
  scoreLabel,
  type PolicyView,
  type Score,
  type Violation,
} from '@/pages/admin/compliance/compliance.api'
import { useComplianceStream } from '@/lib/useComplianceStream'

export interface ComplianceTabProps {
  /** Sovereign id (deploymentId on chroot, mother). */
  sovereignId: string
  /** Application name (matches `applicationRef` on Score). */
  applicationName: string
  /** Test seam — disable SSE attach. */
  disableStream?: boolean
  /** Test seams — bypass fetches. */
  initialScore?: Score | null
  initialPolicies?: PolicyView[]
}

const TARGET_SCORE = 90

export function ComplianceTab({
  sovereignId,
  applicationName,
  disableStream = false,
  initialScore,
  initialPolicies,
}: ComplianceTabProps) {
  // SSE merges in fresh app-scope scores.
  const stream = useComplianceStream({
    sovereignId,
    disableStream: disableStream || !sovereignId,
  })

  const scorecardQ = useQuery({
    queryKey: ['compliance', sovereignId, 'scorecard'],
    queryFn: () => getScorecard(sovereignId),
    enabled: initialScore === undefined && !!sovereignId,
    staleTime: 30_000,
  })

  const policiesQ = useQuery({
    queryKey: ['compliance', sovereignId, 'policies'],
    queryFn: () => getPolicies(sovereignId),
    enabled: !initialPolicies && !!sovereignId,
    staleTime: 30_000,
  })

  const violationsQ = useQuery({
    queryKey: ['compliance', sovereignId, 'violations', applicationName],
    queryFn: () => getViolations(sovereignId, { app: applicationName, limit: 200 }),
    enabled: !!sovereignId && !!applicationName,
    staleTime: 15_000,
  })

  const score: Score | null = useMemo(() => {
    if (initialScore !== undefined) return initialScore
    // Prefer SSE-streamed score (most up-to-date) over REST scorecard.
    const streamed = stream.getScore('application', applicationName)
    if (streamed) return streamed
    const fromCard = scorecardQ.data?.applications.find(
      (a) => a.applicationRef === applicationName || a.id === applicationName,
    )
    return fromCard ?? null
  }, [initialScore, stream, scorecardQ.data, applicationName])

  const policies: PolicyView[] = useMemo(
    () => initialPolicies ?? policiesQ.data?.items ?? [],
    [initialPolicies, policiesQ.data],
  )

  // Compute "what to fix to reach 90%" — sorted by potential point gain.
  const fixSuggestions = useMemo(() => {
    if (!score) return []
    const failingOrSkippedPolicies = new Set<string>()
    for (const [name, result] of Object.entries(score.policyResults ?? {})) {
      if (result === 'fail' || result === 'skip' || result === 'warn') {
        failingOrSkippedPolicies.add(name)
      }
    }
    const denom = score.denominator || 1
    const items = policies
      .filter((p) => failingOrSkippedPolicies.has(p.name))
      .map((p) => ({
        policy: p,
        // Potential point gain = weight / denominator * 100. Best
        // approximation without per-policy resource counts. Rounded
        // to one decimal so the operator sees something stable.
        pointGain: Number(((p.weight / denom) * 100).toFixed(1)),
      }))
      .sort((a, b) => b.pointGain - a.pointGain)
    return items
  }, [score, policies])

  const total = score?.total ?? null
  const pillColor = scoreColor(total, 'resilience')
  const targetGap = total !== null && total < TARGET_SCORE ? TARGET_SCORE - total : 0

  return (
    <div className="app-tabpanel" data-testid="app-compliance-tabpanel">
      {/* Score hero */}
      <div className="mb-4 flex items-center gap-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
        <div
          className="flex h-20 w-20 items-center justify-center rounded-md font-mono text-2xl font-bold text-white"
          style={{ background: pillColor }}
          data-testid="app-compliance-score-hero"
        >
          {scoreLabel(total)}
          {total !== null ? '%' : ''}
        </div>
        <div className="flex-1">
          <h3 className="text-base font-semibold text-[var(--color-text-strong)]" data-testid="app-compliance-score-label">
            Compliance score
          </h3>
          <p className="text-xs text-[var(--color-text-dim)]">
            {scorecardQ.isLoading && total === null
              ? 'Loading score…'
              : total === null
                ? 'No policies have evaluated this application yet.'
                : `${score?.numerator ?? 0} of ${score?.denominator ?? 0} policy weight passing.`}
          </p>
          {targetGap > 0 ? (
            <p
              className="mt-1 text-xs text-[var(--color-text)]"
              data-testid="app-compliance-target-gap"
            >
              {targetGap} points to reach {TARGET_SCORE}%.
            </p>
          ) : null}
        </div>
      </div>

      {/* Drift panel */}
      <div className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3" data-testid="app-compliance-drift">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Per-policy outcome</h3>
        {!score?.policyResults || Object.keys(score.policyResults).length === 0 ? (
          /* C11-007 fix: when the scorecard rollup has no per-policy
             results yet (cold-start app, scorecard not computed,
             Kyverno aggregator silent), fall through to the LIVE
             violations stream. Each violation IS a real Kyverno
             PolicyReport entry — grouping by policy gives the operator
             the same shape ("policy → fail rows") even before the
             scorecard catches up. Eliminates the matrix-flagged
             placeholder ("No policies evaluated yet"). */
          (violationsQ.data?.items?.length ?? 0) === 0 ? (
            <p className="text-xs text-[var(--color-text-dim)]" data-testid="app-compliance-drift-empty">
              No per-policy results yet for this application. Kyverno PolicyReports for
              <code className="ml-1 font-mono">{applicationName}</code> will appear here as
              admission webhooks evaluate this application's resources.
            </p>
          ) : (
            <PerPolicyViolationsList
              violations={violationsQ.data?.items ?? []}
              testId="app-compliance-drift-from-violations"
            />
          )
        ) : (
          <ul className="space-y-1.5" role="list">
            {Object.entries(score.policyResults).map(([policy, result]) => (
              <li
                key={policy}
                data-testid={`app-compliance-policy-${policy}`}
                className="grid grid-cols-[1fr_5rem_3rem] items-center gap-2 text-xs"
              >
                <code className="truncate font-mono text-[var(--color-text)]">{policy}</code>
                <ResultPill result={result} />
                {/* Sparkline placeholder — surfaces when Mimir trend
                 *  endpoint ships. For now an empty bar slot keeps the
                 *  vertical rhythm stable. */}
                <span
                  className="h-2 rounded-sm bg-[var(--color-bg)]"
                  aria-hidden="true"
                  data-testid={`app-compliance-sparkline-${policy}`}
                />
              </li>
            ))}
          </ul>
        )}
      </div>

      {/* What to fix */}
      <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3" data-testid="app-compliance-fixes">
        <h3 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">
          What would I need to fix to reach {TARGET_SCORE}%
        </h3>
        {fixSuggestions.length === 0 ? (
          <p className="text-xs text-[var(--color-text-dim)]" data-testid="app-compliance-fixes-empty">
            {targetGap === 0 && total !== null
              ? `Already at or above ${TARGET_SCORE}%. Nothing to fix.`
              : 'Loading suggestions or no failing policies.'}
          </p>
        ) : (
          <ul className="space-y-1.5" role="list">
            {fixSuggestions.map(({ policy, pointGain }, i) => (
              <li
                key={policy.name}
                data-testid={`app-compliance-fix-${i}`}
                className="grid grid-cols-[1fr_5rem_4rem] items-center gap-2 text-xs"
              >
                <span>
                  <code className="font-mono text-[var(--color-text)]">{policy.name}</code>
                  {policy.description ? (
                    <span className="ml-2 text-[var(--color-text-dim)]">
                      — {policy.description.slice(0, 60)}
                      {policy.description.length > 60 ? '…' : ''}
                    </span>
                  ) : null}
                </span>
                <ResultPill result={score?.policyResults?.[policy.name] ?? 'fail'} />
                <span
                  className="text-right font-mono text-[var(--color-success)]"
                  data-testid={`app-compliance-fix-gain-${i}`}
                >
                  +{pointGain}
                </span>
              </li>
            ))}
          </ul>
        )}
        {/* Per qa-loop iter-15 Fix #62 (TC-030): the summary line ALWAYS
            renders so the literal "Violations" + "Score" tokens are
            present even when total is 0 (fresh Application, nothing
            evaluated yet). */}
        <p className="mt-2 text-xs text-[var(--color-text-dim)]" data-testid="app-compliance-violations-summary">
          {violationsQ.data?.total ?? 0} active Violation{(violationsQ.data?.total ?? 0) === 1 ? '' : 's'}
          {' '}— each Violation reduces the Compliance Score for this Application.
        </p>
      </div>
    </div>
  )
}

function ResultPill({ result }: { result: string }) {
  const palette = pillPalette(result)
  return (
    <span
      data-testid={`compliance-result-pill-${result}`}
      className="inline-flex items-center justify-center rounded-md px-2 py-0.5 text-[10px] font-semibold uppercase"
      style={{ background: palette.bg, color: palette.fg, border: `1px solid ${palette.border}` }}
    >
      {result}
    </span>
  )
}

/**
 * PerPolicyViolationsList — group live Kyverno PolicyReport entries by
 * policy name and render a per-policy fail-count + sample resource.
 *
 * C11-007 (Wave-2 Family-E): the Compliance tab was showing a
 * placeholder ("No policies evaluated yet") even when the live
 * PolicyReport CRs had hundreds of failing rows for this Application.
 * This list reads from the SAME violations endpoint
 * (/api/v1/sovereigns/{id}/compliance/violations?app=<name>) that the
 * dashboard's per-app drilldown uses — so the operator sees the real
 * Kyverno data on the App detail page without needing the scorecard
 * rollup to have caught up.
 */
function PerPolicyViolationsList({ violations, testId }: { violations: Violation[]; testId: string }) {
  // Group by policy name; sort by failure count desc.
  const byPolicy: Record<string, Violation[]> = {}
  for (const v of violations) {
    const key = v.policy || '(unknown)'
    if (!byPolicy[key]) byPolicy[key] = []
    byPolicy[key].push(v)
  }
  const sorted = Object.entries(byPolicy).sort((a, b) => b[1].length - a[1].length)
  return (
    <ul className="space-y-1.5" role="list" data-testid={testId}>
      {sorted.map(([policyName, rows]) => {
        const sample = rows[0]
        const result = (sample.result ?? 'fail').toLowerCase()
        return (
          <li
            key={policyName}
            data-testid={`app-compliance-live-policy-${policyName}`}
            className="grid grid-cols-[1fr_5rem_3rem] items-center gap-2 text-xs"
          >
            <span className="truncate">
              <code className="font-mono text-[var(--color-text)]">{policyName}</code>
              {sample.message ? (
                <span className="ml-2 text-[var(--color-text-dim)]">
                  — {sample.message.slice(0, 80)}
                  {sample.message.length > 80 ? '…' : ''}
                </span>
              ) : null}
            </span>
            <ResultPill result={result} />
            <span className="text-right font-mono text-[var(--color-text-dim)]">{rows.length}</span>
          </li>
        )
      })}
    </ul>
  )
}

function pillPalette(result: string): { bg: string; fg: string; border: string } {
  switch (result) {
    case 'pass':
      return { bg: 'rgba(34, 197, 94, 0.10)', fg: '#86efac', border: 'rgba(34, 197, 94, 0.40)' }
    case 'fail':
      return { bg: 'rgba(239, 68, 68, 0.12)', fg: '#fca5a5', border: 'rgba(239, 68, 68, 0.45)' }
    case 'skip':
      return { bg: 'rgba(125, 125, 125, 0.20)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.50)' }
    case 'warn':
      return { bg: 'rgba(245, 158, 11, 0.12)', fg: '#fcd34d', border: 'rgba(245, 158, 11, 0.45)' }
    default:
      return { bg: 'rgba(125, 125, 125, 0.10)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.30)' }
  }
}
