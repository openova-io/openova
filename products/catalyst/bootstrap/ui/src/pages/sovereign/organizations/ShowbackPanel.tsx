/**
 * ShowbackPanel — the B3 parent self-showback surface (issue #3378
 * DoD 3 + §5).
 *
 * Renders the sovereign's own per-application cost attribution: the
 * parent org's consumption broken down per app/namespace. Works day one
 * on a zero-sub-org estate (100% attributes to the parent), which is the
 * whole point of showback (§5). Mode-aware: this panel is the showback /
 * chargeback view (attributed consumption, zero payment actions); real-
 * billing orgs render the payment flows elsewhere.
 *
 * Reads the B3 feed (GET /api/v1/sme/consumption via getConsumption).
 */

import { useQuery } from '@tanstack/react-query'
import {
  getConsumption,
  type OrgConsumption,
  type SovereignConsumption,
} from '@/lib/organizations.api'

export interface ShowbackPanelProps {
  /** Render only this org's slice (e.g. the parent). When omitted the
   *  panel shows the parent (first org) row. */
  org?: string
  /** Test seam — bypass the fetch with a synthetic feed. */
  initialOverride?: SovereignConsumption
}

export function ShowbackPanel({ org, initialOverride }: ShowbackPanelProps) {
  const query = useQuery<SovereignConsumption>({
    queryKey: ['consumption'],
    queryFn: getConsumption,
    staleTime: 30_000,
    enabled: !initialOverride,
    placeholderData: (prev) => prev,
  })
  const feed = initialOverride ?? query.data
  const loading = !initialOverride && query.isLoading

  const target: OrgConsumption | undefined = feed
    ? org
      ? feed.orgs.find((o) => o.org === org)
      : feed.orgs.find((o) => o.isParent) ?? feed.orgs[0]
    : undefined

  return (
    <section
      data-testid="showback-panel"
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4"
    >
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-sm font-semibold text-[var(--color-text-strong)]">
          Showback — per-app consumption
        </h2>
        {feed?.pending ? (
          <span
            data-testid="showback-pending"
            className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-300"
          >
            Metering warming up
          </span>
        ) : null}
      </div>

      {loading ? (
        <div data-testid="showback-loading" className="text-sm text-[var(--color-text-dim)]">Loading…</div>
      ) : !target ? (
        <div data-testid="showback-empty" className="text-sm text-[var(--color-text-dim)]">
          No consumption attributed yet. Showback populates from running workloads.
        </div>
      ) : (
        <div>
          <p className="mb-3 text-xs text-[var(--color-text-dim)]">
            <span data-testid="showback-org" className="font-medium text-[var(--color-text)]">{target.org}</span>
            {target.isParent ? ' (parent — your own estate)' : ''} ·{' '}
            <span data-testid="showback-total" className="font-mono">{target.costUnits}</span> units ·{' '}
            {target.cpuMilli}m CPU · {target.memoryGiB} GiB mem · {target.storageGiB} GiB storage
          </p>
          {target.apps.length === 0 ? (
            <div data-testid="showback-no-apps" className="text-sm text-[var(--color-text-dim)]">
              No applications attributed yet.
            </div>
          ) : (
            <div className="overflow-x-auto rounded-lg border border-[var(--color-border)]">
              <table data-testid="showback-table" className="w-full border-collapse text-sm">
                <thead>
                  <tr className="border-b border-[var(--color-border)] text-left text-[0.7rem] uppercase tracking-wide text-[var(--color-text-dim)]">
                    <th className="px-3 py-2 font-semibold">Application</th>
                    <th className="px-3 py-2 font-semibold">Namespace</th>
                    <th className="px-3 py-2 font-semibold text-right">CPU (m)</th>
                    <th className="px-3 py-2 font-semibold text-right">Mem (GiB)</th>
                    <th className="px-3 py-2 font-semibold text-right">Share</th>
                  </tr>
                </thead>
                <tbody>
                  {target.apps.map((a) => (
                    <tr key={`${a.namespace}/${a.application}`} data-testid={`showback-app-${a.application}`} className="border-b border-[var(--color-border)] last:border-b-0">
                      <td className="px-3 py-2 text-[var(--color-text-strong)]">{a.application}</td>
                      <td className="px-3 py-2 font-mono text-xs text-[var(--color-text-dim)]">{a.namespace}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{a.cpuMilli}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{a.memoryGiB}</td>
                      <td className="px-3 py-2 text-right tabular-nums" data-testid={`showback-app-pct-${a.application}`}>{a.percent}%</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </section>
  )
}
