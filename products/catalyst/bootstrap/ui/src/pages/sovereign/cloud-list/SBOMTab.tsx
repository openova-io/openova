/**
 * SBOMTab — per-Pod SBOM + CVE drill-down for the cloud-list resource
 * detail page (slice C11-010, Wave-2 Family-E).
 *
 * Reads `/api/v1/sovereigns/{id}/compliance/sbom?ns=<ns>&pod=<pod>`
 * which projects trivy-operator VulnerabilityReports + SBOMReports
 * into one structured envelope per Pod:
 *
 *   • per-Container severity counts (Critical / High / Medium / Low / Unknown)
 *   • per-Container image + digest + scan timestamp
 *   • per-Container SBOM component list (name / version / type / licenses)
 *
 * Empty-state matrix:
 *   • installed=false       → "Trivy operator not yet deployed"
 *   • installed=true, no reports for this Pod → "No scan yet — Trivy
 *     scans new Pods within ~5 minutes of admission"
 *   • installed=true, reports present → severity pills + component table
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 we never seed synthetic data;
 * empty means empty, no fixture rows.
 *
 * Mounted by the ResourceDetailPage Pod-tab set (see Family C's
 * coordination note in the brief) AND embedded directly in AppDetail
 * for the per-App SBOM rollup tile.
 */

import { useQuery } from '@tanstack/react-query'
import {
  getSBOMForPod,
  type SBOMContainerEntry,
  type SBOMPodResponse,
  type VulnerabilitySeverityCounts,
} from '@/lib/compliance.api'

export interface SBOMTabProps {
  /** Sovereign id (deploymentId on chroot). */
  sovereignId: string
  /** Pod namespace. */
  namespace: string
  /** Pod name. */
  podName: string
  /** Test seam — bypass fetch. */
  initialData?: SBOMPodResponse
}

const SEV_PALETTE: Record<keyof VulnerabilitySeverityCounts, { bg: string; fg: string; label: string }> = {
  critical: { bg: 'rgba(220, 38, 38, 0.18)', fg: '#fecaca', label: 'CRITICAL' },
  high:     { bg: 'rgba(249, 115, 22, 0.18)', fg: '#fed7aa', label: 'HIGH' },
  medium:   { bg: 'rgba(245, 158, 11, 0.15)', fg: '#fcd34d', label: 'MEDIUM' },
  low:      { bg: 'rgba(59, 130, 246, 0.12)', fg: '#bfdbfe', label: 'LOW' },
  unknown:  { bg: 'rgba(125, 125, 125, 0.15)', fg: '#cbd5e1', label: 'UNKNOWN' },
  total:    { bg: 'rgba(255, 255, 255, 0.06)', fg: '#e2e8f0', label: 'TOTAL' },
}

export function SBOMTab({ sovereignId, namespace, podName, initialData }: SBOMTabProps) {
  const q = useQuery({
    queryKey: ['compliance', sovereignId, 'sbom', namespace, podName],
    queryFn: () => getSBOMForPod(sovereignId, namespace, podName),
    enabled: !initialData && !!sovereignId && !!namespace && !!podName,
    staleTime: 60_000,
  })

  const data = initialData ?? q.data
  const installed = data?.installed ?? false
  const containers = data?.containers ?? []

  return (
    <div data-testid="sbom-tab-panel" className="space-y-4 p-2">
      <header className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3">
        <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
          SBOM &amp; CVE — <code className="font-mono">{namespace}/{podName}</code>
        </h2>
        <p className="mt-0.5 text-[11px] text-[var(--color-text-dim)]" data-testid="sbom-tab-subtitle">
          Software Bill of Materials and per-Container vulnerability summary from{' '}
          <code className="font-mono">aquasecurity.github.io/v1alpha1</code> VulnerabilityReport
          and SBOMReport CRs (Trivy operator). Updated {data?.updatedAt ?? '—'}.
        </p>
      </header>

      {!installed ? (
        <p className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs text-[var(--color-text-dim)]" data-testid="sbom-tab-empty-not-installed">
          Trivy operator is not yet deployed in this Sovereign. Install{' '}
          <code className="font-mono">bp-trivy-operator</code> via the marketplace to begin
          per-Container vulnerability scans and SBOM generation.
        </p>
      ) : containers.length === 0 ? (
        <p className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs text-[var(--color-text-dim)]" data-testid="sbom-tab-empty-no-reports">
          Trivy is running, but no VulnerabilityReport or SBOMReport CR exists yet for this Pod.
          Trivy typically scans new Pods within ~5 minutes of admission — check back shortly.
        </p>
      ) : (
        <>
          {/* Pod-level severity rollup */}
          {data ? (
            <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3" data-testid="sbom-tab-totals">
              <h3 className="mb-1.5 text-sm font-medium text-[var(--color-text-strong)]">
                Pod-level CVE rollup
              </h3>
              <SeverityRow counts={data.totalCounts} testIdPrefix="sbom-tab-totals" />
            </div>
          ) : null}

          {/* Per-container blocks */}
          {containers.map((c, i) => (
            <ContainerBlock key={c.container} entry={c} index={i} />
          ))}
        </>
      )}
    </div>
  )
}

function ContainerBlock({ entry, index }: { entry: SBOMContainerEntry; index: number }) {
  return (
    <div
      className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
      data-testid={`sbom-container-${index}`}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">
          Container <code className="font-mono">{entry.container}</code>
        </h3>
        {entry.scanCompletedAt ? (
          <span className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]" data-testid={`sbom-container-${index}-scan`}>
            scanned {entry.scanCompletedAt}
          </span>
        ) : null}
      </div>
      {entry.image ? (
        <p className="mt-0.5 text-[11px] text-[var(--color-text-dim)]" data-testid={`sbom-container-${index}-image`}>
          Image: <code className="font-mono">{entry.image}</code>
          {entry.digest ? <> · digest <code className="font-mono">{entry.digest.slice(0, 14)}…</code></> : null}
        </p>
      ) : null}

      <div className="mt-2">
        <SeverityRow counts={entry.severity} testIdPrefix={`sbom-container-${index}-sev`} />
      </div>

      {entry.components && entry.components.length > 0 ? (
        <details className="mt-2" data-testid={`sbom-container-${index}-components`}>
          <summary className="cursor-pointer text-[11px] uppercase tracking-wide text-[var(--color-text-dim)]">
            SBOM — {entry.components.length} component{entry.components.length === 1 ? '' : 's'}
          </summary>
          <table className="mt-1.5 w-full border-collapse text-[11px]">
            <thead>
              <tr className="border-b border-[var(--color-border)] text-left uppercase text-[var(--color-text-dim)]">
                <th className="px-2 py-1">Name</th>
                <th className="px-2 py-1">Version</th>
                <th className="px-2 py-1">Type</th>
                <th className="px-2 py-1">Licenses</th>
              </tr>
            </thead>
            <tbody>
              {entry.components.slice(0, 200).map((c, j) => (
                <tr key={`${c.name}-${j}`} className="border-b border-[var(--color-border)]">
                  <td className="px-2 py-1 font-mono">{c.name}</td>
                  <td className="px-2 py-1 font-mono text-[var(--color-text-dim)]">{c.version ?? '—'}</td>
                  <td className="px-2 py-1 text-[var(--color-text-dim)]">{c.type ?? '—'}</td>
                  <td className="px-2 py-1 text-[var(--color-text-dim)]">{c.licenses ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {entry.components.length > 200 ? (
            <p className="mt-1 text-[10px] italic text-[var(--color-text-dim)]">
              Showing first 200 of {entry.components.length} components.
            </p>
          ) : null}
        </details>
      ) : null}
    </div>
  )
}

function SeverityRow({ counts, testIdPrefix }: { counts: VulnerabilitySeverityCounts; testIdPrefix: string }) {
  const cells: Array<keyof VulnerabilitySeverityCounts> = ['critical', 'high', 'medium', 'low', 'unknown', 'total']
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-[11px]" data-testid={testIdPrefix}>
      {cells.map((k) => {
        const palette = SEV_PALETTE[k]
        const v = counts[k] ?? 0
        return (
          <span
            key={k}
            data-testid={`${testIdPrefix}-${k}`}
            className="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 font-semibold"
            style={{ background: palette.bg, color: palette.fg, borderColor: 'var(--color-border)' }}
          >
            <span className="uppercase tracking-wide">{palette.label}</span>
            <span className="font-mono">{v}</span>
          </span>
        )
      })}
    </div>
  )
}
