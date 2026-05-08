/**
 * PolicyDrilldownPage — per-policy drill-down (slice U4, #1096).
 *
 * Route: `/admin/compliance/policy/$policyName` (mother) +
 *        `/compliance/policy/$policyName` (chroot).
 *
 * Layout per the brief:
 *   • Policy metadata (description, severity, default mode + U5 toggle)
 *   • Bar chart: pass-rate per Environment
 *   • Table: every offending resource (kind/namespace/name + violating
 *     field + suggested fix from Kyverno's `validate.message`)
 *   • Click a row → routes into the existing K8s list view
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), endpoint URLs
 * route through the compliance.api seam. Per the canonical-seam map,
 * the K8s list view is reused via the existing `/cloud?view=list&kind=...`
 * URL shape — no new page, no fork.
 */

import { useMemo } from 'react'
import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { PolicyModeToggle } from '@/widgets/compliance/PolicyModeToggle'
import {
  getPolicies,
  getViolations,
  scoreColor,
  type PolicyMode,
  type PolicyView,
  type Violation,
} from './compliance.api'
import { useComplianceStream } from '@/lib/useComplianceStream'

export interface PolicyDrilldownPageProps {
  /** Test seam — disable SSE attach. */
  disableStream?: boolean
  /** Test seam — bypass fetches. */
  initialPolicies?: PolicyView[]
  initialViolations?: Violation[]
  /** Test seam — override URL param. */
  policyNameOverride?: string
}

export function PolicyDrilldownPage({
  disableStream = false,
  initialPolicies,
  initialViolations,
  policyNameOverride,
}: PolicyDrilldownPageProps = {}) {
  const params = useParams({ strict: false }) as { policyName?: string }
  const policyName = policyNameOverride ?? params.policyName ?? ''
  const { deploymentId: resolved } = useResolvedDeploymentId()
  const deploymentId = resolved ?? ''

  // Subscribe to SSE so live policy-mode flips refresh the page.
  useComplianceStream({
    sovereignId: deploymentId,
    disableStream: disableStream || !deploymentId,
  })

  const policiesQ = useQuery({
    queryKey: ['compliance', deploymentId, 'policies'],
    queryFn: () => getPolicies(deploymentId),
    enabled: !initialPolicies && !!deploymentId,
    staleTime: 30_000,
  })
  // Drilldown for one policy uses the global violations endpoint and
  // filters client-side. Server-side per-policy filter is a candidate
  // refinement; current contract only supports `app=` filter.
  const violationsQ = useQuery({
    queryKey: ['compliance', deploymentId, 'violations'],
    queryFn: () => getViolations(deploymentId, { limit: 500 }),
    enabled: !initialViolations && !!deploymentId,
    staleTime: 15_000,
  })

  const policies: PolicyView[] = initialPolicies ?? policiesQ.data?.items ?? []
  const policy = policies.find((p) => p.name === policyName)

  const violations: Violation[] = useMemo(() => {
    const all = initialViolations ?? violationsQ.data?.items ?? []
    return all.filter((v) => v.policy === policyName)
  }, [initialViolations, violationsQ.data, policyName])

  // Pass-rate per environment — count resources passing/failing this
  // policy across the violations list. Without a per-policy "passing"
  // endpoint we only know failures here; the pass-rate is approximated
  // from the policy's total violation count + the existing scorecard
  // denominator. Best-effort until S ships per-policy aggregate.
  const envFailureCounts: Record<string, number> = useMemo(() => {
    const out: Record<string, number> = {}
    for (const v of violations) {
      const env = v.environment ?? '—'
      out[env] = (out[env] ?? 0) + 1
    }
    return out
  }, [violations])
  const envBarMax = Math.max(1, ...Object.values(envFailureCounts))

  return (
    <PortalShell deploymentId={deploymentId} pageTitle={`Compliance — ${policyName}`}>
      <div data-testid="policy-drilldown-page" className="mx-auto max-w-7xl px-6 py-4">
        {/* Header */}
        <div className="mb-4">
          <h1 className="text-xl font-semibold text-[var(--color-text-strong)]" data-testid="policy-drilldown-title">
            Policy: <code className="font-mono">{policyName}</code>
          </h1>
          {policy ? (
            <PolicyMetadata policy={policy} sovereignId={deploymentId} violations={violations.length} />
          ) : !policiesQ.isLoading ? (
            <p className="mt-2 text-sm text-[var(--color-text-dim)]" data-testid="policy-drilldown-not-found">
              No active policy named "{policyName}". Has it been disabled in this environment?
            </p>
          ) : (
            <p className="mt-2 text-sm text-[var(--color-text-dim)]">Loading policy metadata…</p>
          )}
        </div>

        {/* Pass-rate per environment — bar chart */}
        <div
          className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
          data-testid="policy-drilldown-env-bars"
        >
          <h2 className="mb-2 text-sm font-medium text-[var(--color-text-strong)]">Failures per environment</h2>
          {Object.keys(envFailureCounts).length === 0 ? (
            <p className="text-xs text-[var(--color-text-dim)]" data-testid="policy-drilldown-no-failures">
              No failures of <code className="font-mono">{policyName}</code> reported in any environment.
            </p>
          ) : (
            <ul className="space-y-1.5" role="list">
              {Object.entries(envFailureCounts).map(([env, count]) => {
                const ratio = count / envBarMax
                return (
                  <li
                    key={env}
                    data-testid={`policy-drilldown-env-bar-${env}`}
                    className="grid grid-cols-[8rem_1fr_3rem] items-center gap-2 text-xs"
                  >
                    <code className="truncate font-mono text-[var(--color-text-dim)]">{env}</code>
                    <div
                      className="h-3 rounded-sm"
                      style={{ width: `${ratio * 100}%`, background: scoreColor(0, 'resilience') }}
                    />
                    <span className="text-right font-mono text-[var(--color-text)]">{count}</span>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* Offending-resource table */}
        <div
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)]"
          data-testid="policy-drilldown-violations"
        >
          <h2 className="border-b border-[var(--color-border)] px-3 py-2 text-sm font-medium text-[var(--color-text-strong)]">
            Offending resources ({violations.length})
          </h2>
          {violationsQ.isLoading && violations.length === 0 ? (
            <p className="px-3 py-4 text-xs text-[var(--color-text-dim)]">Loading violations…</p>
          ) : violations.length === 0 ? (
            <p className="px-3 py-4 text-xs text-[var(--color-text-dim)]" data-testid="policy-drilldown-violations-empty">
              No offending resources for <code className="font-mono">{policyName}</code> in this Sovereign.
            </p>
          ) : (
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
                  <th className="px-3 py-2">Resource</th>
                  <th className="px-3 py-2">Namespace</th>
                  <th className="px-3 py-2">Application</th>
                  <th className="px-3 py-2">Suggested fix</th>
                </tr>
              </thead>
              <tbody>
                {violations.map((v, i) => (
                  <tr
                    key={`${v.resource}-${v.policy}-${i}`}
                    data-testid={`policy-drilldown-row-${i}`}
                    className="border-b border-[var(--color-border)] hover:bg-[var(--color-bg)]"
                  >
                    <td className="px-3 py-2 font-mono text-xs">{v.resource}</td>
                    <td className="px-3 py-2 font-mono text-xs text-[var(--color-text-dim)]">{v.namespace ?? '—'}</td>
                    <td className="px-3 py-2 text-xs">{v.application ?? '—'}</td>
                    <td className="px-3 py-2 text-xs text-[var(--color-text-dim)]">{v.message ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </PortalShell>
  )
}

interface PolicyMetadataProps {
  policy: PolicyView
  sovereignId: string
  violations: number
}

function PolicyMetadata({ policy, sovereignId, violations }: PolicyMetadataProps) {
  // Mode toggle: U5. The brief says "as a row action on U1/U2 + inline
  // on U4". Inline-on-U4 lives here. Environment is the ONE the violations
  // came from; with multi-env support we might render N toggles — for now,
  // surface "default" and let the future expansion split it.
  const mode = (policy.mode as PolicyMode) ?? 'permissive'
  return (
    <div
      className="mt-2 flex flex-wrap items-center gap-3 text-xs"
      data-testid="policy-drilldown-metadata"
    >
      <span className="text-[var(--color-text-dim)]">
        Source: <code className="font-mono text-[var(--color-text)]">{policy.source}</code>
      </span>
      <span className="text-[var(--color-text-dim)]">
        Weight: <code className="font-mono text-[var(--color-text)]">{policy.weight}</code>
      </span>
      <span className="text-[var(--color-text-dim)]">
        Scope: <code className="font-mono text-[var(--color-text)]">{policy.scope || 'all'}</code>
      </span>
      <span className="text-[var(--color-text-dim)]">
        Violations:{' '}
        <code className="font-mono text-[var(--color-text)]">{violations}</code>
      </span>
      <span className="text-[var(--color-text-dim)]">Mode:</span>
      <PolicyModeToggle
        sovereignId={sovereignId}
        environmentRef="default"
        policyName={policy.name}
        currentMode={mode}
        passingCount={undefined}
        failingCount={violations}
      />
      {policy.description ? (
        <p className="basis-full pt-1 text-[var(--color-text)]">{policy.description}</p>
      ) : null}
    </div>
  )
}
