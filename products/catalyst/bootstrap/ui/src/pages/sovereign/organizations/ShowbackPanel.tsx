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
 * Reads the B3 feed (GET /api/v1/org/consumption via getConsumption).
 */

import { useQuery } from '@tanstack/react-query'
import {
  getConsumption,
  type OrgConsumption,
  type SovereignConsumption,
} from '@/lib/organizations.api'

export interface ShowbackPanelProps {
  /** Render only this org's slice (e.g. the parent detail view). When
   *  omitted the panel renders EVERY org's slice — the sovereign estate
   *  PLUS each customer Organization row (#4739 row 23: a 2nd Org's own
   *  consumption must show as its own row, distinct from Platform
   *  overhead), not just the parent. */
  org?: string
  /** Test seam — bypass the fetch with a synthetic feed. */
  initialOverride?: SovereignConsumption
}

/** OrgShowbackSlice — one org's per-app consumption block. Rendered once
 *  in the org-detail view and once per org in the directory view. */
function OrgShowbackSlice({ target }: { target: OrgConsumption }) {
  // #6114: the unowned rollup must never read "(Organization)". Its whole
  // reason for existing is that the consumption belongs to no Organization
  // — labelling it as one restates the defect on the surface that is
  // supposed to expose it, and the raw `__unowned__` sentinel would render
  // as a malformed slug.
  const label = target.isUnowned
    ? 'Unowned namespaces'
    : target.org
  return (
    <div data-testid={`showback-org-slice-${target.org}`} className="mb-4 last:mb-0">
      <p className="mb-2 text-xs text-[var(--color-text-dim)]">
        <span data-testid="showback-org" className="font-medium text-[var(--color-text)]">{label}</span>
        {target.isParent
          ? ' (parent — your own estate)'
          : target.isPlatform
            ? ' (platform overhead)'
            : target.isUnowned
              ? ' (no Organization CR — not billed to any Organization)'
              : ' (Organization)'}{' '}
        ·{' '}
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
  )
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

  // Detail view (`org` given) → that single org's slice. Directory view
  // (no `org`) → EVERY org, parent first (#4739 row 23): the customer
  // Organizations' own consumption must each render as their own row,
  // not be collapsed to the parent-only view.
  const slices: OrgConsumption[] = feed
    ? org
      ? feed.orgs.filter((o) => o.org === org)
      : feed.orgs
    : []

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

      {/* #6114 (UAT row 25): name the orphans. The directory view is where
          the sets are compared, so this is where a slug that draws
          consumption with no Organization CR behind it has to be said out
          loud — an estate can otherwise run a whole namespace's worth of
          workloads under an identity the control plane does not model, and
          nothing on any surface says so. */}
      {!org && feed && (feed.unownedOrgs?.length ?? 0) > 0 ? (
        <div
          data-testid="showback-unowned-warning"
          className="mb-3 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200"
        >
          <span className="font-semibold">No Organization CR:</span>{' '}
          <span data-testid="showback-unowned-slugs" className="font-mono">
            {feed.unownedOrgs?.join(', ')}
          </span>{' '}
          — these namespaces draw consumption but the control plane holds no
          Organization for them, so nothing is billed to an Organization.
          Reconcile or remove them.
        </div>
      ) : null}

      {loading ? (
        <div data-testid="showback-loading" className="text-sm text-[var(--color-text-dim)]">Loading…</div>
      ) : slices.length === 0 ? (
        <div data-testid="showback-empty" className="text-sm text-[var(--color-text-dim)]">
          No consumption attributed yet. Showback populates from running workloads.
        </div>
      ) : (
        <div>
          {slices.map((o) => (
            <OrgShowbackSlice key={o.org} target={o} />
          ))}
        </div>
      )}
    </section>
  )
}
