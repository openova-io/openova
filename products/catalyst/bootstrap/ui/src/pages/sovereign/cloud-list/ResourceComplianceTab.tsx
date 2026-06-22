/**
 * ResourceComplianceTab — #4085.
 *
 * Per-resource compliance findings for the cloud-list resource detail
 * view's Compliance tab. Renders a TABLE of THAT resource's own
 * policies/checks + pass/fail/skip status, severity, and message —
 * sourced from `/api/v1/sovereigns/{id}/compliance/resource?kind&ns&name`
 * which projects the resource's own per-policy verdicts joined with the
 * live ClusterPolicy metadata.
 *
 * Before this, the tab only showed a one-line blurb + a link to the
 * sovereign-wide `/sre/compliance` dashboard — the founder bug this
 * fixes: a resource's Compliance tab must show ITS OWN findings, not the
 * global view.
 *
 * Empty-state matrix (per docs/INVIOLABLE-PRINCIPLES.md #2 — never seed
 * synthetic data):
 *   • found=false → "No policies have evaluated this resource yet"
 *   • found=true  → score hero + per-policy findings table
 *
 * The "Open in Compliance Dashboard" deep-link is kept as a secondary
 * affordance below the table (drill-out to the full dashboard), not as
 * the primary content.
 */

import { useQuery } from '@tanstack/react-query'
import {
  getResourceCompliance,
  scoreColor,
  scoreLabel,
  type ResourceComplianceResponse,
  type ResourceFinding,
} from '@/lib/compliance.api'

export interface ResourceComplianceTabProps {
  /** Sovereign id (deploymentId on chroot). */
  sovereignId: string
  /** Canonical (singular) kind, e.g. "deployment". */
  kind: string
  /** Namespace ('' for cluster-scoped). */
  ns: string
  /** Resource name. */
  name: string
  /** Test seam — bypass the live fetch. */
  initialData?: ResourceComplianceResponse
}

export function ResourceComplianceTab({
  sovereignId,
  kind,
  ns,
  name,
  initialData,
}: ResourceComplianceTabProps) {
  const q = useQuery({
    queryKey: ['compliance', sovereignId, 'resource', kind, ns, name],
    queryFn: () => getResourceCompliance(sovereignId, kind, ns || undefined, name),
    enabled: !initialData && !!sovereignId && !!kind && !!name,
    staleTime: 30_000,
  })

  const data = initialData ?? q.data ?? null
  const drilldownHref = `/sre/compliance?resource=${encodeURIComponent(`${kind}/${ns}/${name}`)}`

  return (
    <div
      data-testid="resource-detail-compliance"
      className="space-y-4"
    >
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
        <h3 className="mb-1 text-sm font-semibold text-[var(--color-text-strong)]">
          Per-resource policy compliance
        </h3>
        <p className="text-xs text-[var(--color-text-dim)]">
          Kyverno + custom-evaluator policy reports for this{' '}
          <code className="font-mono">
            {kind}/{ns ? `${ns}/` : ''}
            {name}
          </code>
          .
        </p>
      </div>

      {/* Loading / error / empty / table. */}
      {q.isLoading && !data ? (
        <div
          data-testid="resource-compliance-loading"
          className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
        >
          Loading compliance findings…
        </div>
      ) : q.isError && !data ? (
        <div
          data-testid="resource-compliance-error"
          className="rounded-lg border border-rose-500 bg-[var(--color-bg-2)] p-6 text-sm text-rose-300"
        >
          {q.error instanceof Error ? q.error.message : 'Failed to load compliance findings.'}
        </div>
      ) : !data || !data.found || data.findings.length === 0 ? (
        <div
          data-testid="resource-compliance-empty"
          className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
        >
          No policies have evaluated this {kind} yet. Kyverno PolicyReports for{' '}
          <code className="ml-1 font-mono">
            {ns ? `${ns}/` : ''}
            {name}
          </code>{' '}
          will appear here as admission webhooks evaluate this resource.
        </div>
      ) : (
        <>
          <ScoreHero data={data} />
          <FindingsTable findings={data.findings} />
        </>
      )}

      {/* Secondary affordance: drill out to the full dashboard. */}
      <a
        href={drilldownHref}
        className="inline-block rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-xs text-[var(--color-text)] no-underline hover:bg-[var(--color-surface-hover)]"
        data-testid="resource-detail-compliance-drilldown"
      >
        Open in Compliance Dashboard →
      </a>
    </div>
  )
}

function ScoreHero({ data }: { data: ResourceComplianceResponse }) {
  const total = data.score ?? data.total ?? null
  const pillColor = scoreColor(total, 'resilience')
  return (
    <div className="flex items-center gap-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
      <div
        className="flex h-16 w-16 items-center justify-center rounded-md font-mono text-xl font-bold text-white"
        style={{ background: pillColor }}
        data-testid="resource-compliance-score-hero"
      >
        {scoreLabel(total)}
        {total !== null ? '%' : ''}
      </div>
      <div className="flex-1">
        <div className="text-sm font-semibold text-[var(--color-text-strong)]">
          Compliance score
        </div>
        <p className="text-xs text-[var(--color-text-dim)]">
          {data.numerator} of {data.denominator} policy weight passing ·{' '}
          <span data-testid="resource-compliance-violation-count">
            {data.violations} violation{data.violations === 1 ? '' : 's'}
          </span>
        </p>
      </div>
    </div>
  )
}

function FindingsTable({ findings }: { findings: ResourceFinding[] }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)]">
      <table className="w-full text-sm" data-testid="resource-compliance-table">
        <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
          <tr className="border-b border-[var(--color-border)] text-left">
            <th className="px-3 py-2">Policy</th>
            <th className="px-3 py-2">Severity</th>
            <th className="px-3 py-2">Status</th>
            <th className="px-3 py-2">Message</th>
          </tr>
        </thead>
        <tbody>
          {findings.map((f, i) => (
            <tr
              key={`${f.policy}-${f.rule ?? ''}-${i}`}
              data-testid={`resource-compliance-row-${f.policy}`}
              className="border-t border-[var(--color-border)] align-top"
            >
              <td className="px-3 py-2">
                <code className="font-mono text-[var(--color-text)]">{f.policy}</code>
                {f.rule ? (
                  <span className="ml-2 text-xs text-[var(--color-text-dim)]">/ {f.rule}</span>
                ) : null}
                {f.category ? (
                  <div className="mt-0.5 text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                    {f.category}
                  </div>
                ) : null}
              </td>
              <td className="px-3 py-2">
                <SeverityPill severity={f.severity} />
              </td>
              <td className="px-3 py-2">
                <ResultPill result={f.result} />
              </td>
              <td className="px-3 py-2 text-xs text-[var(--color-text-dim)]">
                {f.message || '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function ResultPill({ result }: { result: string }) {
  const r = (result || '').toLowerCase()
  const palette = resultPalette(r)
  return (
    <span
      data-testid={`resource-compliance-result-${r}`}
      className="inline-flex items-center justify-center rounded-md px-2 py-0.5 text-[10px] font-semibold uppercase"
      style={{ background: palette.bg, color: palette.fg, border: `1px solid ${palette.border}` }}
    >
      {r || 'unknown'}
    </span>
  )
}

function SeverityPill({ severity }: { severity?: string }) {
  if (!severity) {
    return <span className="text-xs text-[var(--color-text-dim)]">—</span>
  }
  const s = severity.toLowerCase()
  const palette = severityPalette(s)
  return (
    <span
      data-testid={`resource-compliance-severity-${s}`}
      className="inline-flex items-center justify-center rounded-md px-2 py-0.5 text-[10px] font-semibold uppercase"
      style={{ background: palette.bg, color: palette.fg, border: `1px solid ${palette.border}` }}
    >
      {s}
    </span>
  )
}

function resultPalette(result: string): { bg: string; fg: string; border: string } {
  switch (result) {
    case 'pass':
      return { bg: 'rgba(34, 197, 94, 0.10)', fg: '#86efac', border: 'rgba(34, 197, 94, 0.40)' }
    case 'fail':
      return { bg: 'rgba(239, 68, 68, 0.12)', fg: '#fca5a5', border: 'rgba(239, 68, 68, 0.45)' }
    case 'skip':
    case 'na':
      return { bg: 'rgba(125, 125, 125, 0.20)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.50)' }
    case 'warn':
      return { bg: 'rgba(245, 158, 11, 0.12)', fg: '#fcd34d', border: 'rgba(245, 158, 11, 0.45)' }
    default:
      return { bg: 'rgba(125, 125, 125, 0.10)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.30)' }
  }
}

function severityPalette(severity: string): { bg: string; fg: string; border: string } {
  switch (severity) {
    case 'critical':
      return { bg: 'rgba(220, 38, 38, 0.18)', fg: '#fecaca', border: 'rgba(220, 38, 38, 0.50)' }
    case 'high':
      return { bg: 'rgba(249, 115, 22, 0.18)', fg: '#fed7aa', border: 'rgba(249, 115, 22, 0.50)' }
    case 'medium':
      return { bg: 'rgba(245, 158, 11, 0.15)', fg: '#fcd34d', border: 'rgba(245, 158, 11, 0.45)' }
    case 'low':
      return { bg: 'rgba(59, 130, 246, 0.12)', fg: '#bfdbfe', border: 'rgba(59, 130, 246, 0.45)' }
    default:
      return { bg: 'rgba(125, 125, 125, 0.15)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.35)' }
  }
}
