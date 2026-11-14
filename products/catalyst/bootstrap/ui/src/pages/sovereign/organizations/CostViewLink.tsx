/**
 * CostViewLink — the console's link to the cost view of the billing
 * engine (CTL-355).
 *
 * Usage and cost per Organization and per Application are metered,
 * priced and shown by the billing engine, Catalyst BSS (the Blueprint
 * bp-chargeback), in its own console. This console keeps no meter of its
 * own: where it used to draw a cost table it renders this link.
 *
 * The target is not written here. It is the route of the Billing menu's
 * application section (BILLING_SECTIONS in ../bss/billing-sections.ts),
 * which is the route the Blueprint declares in consoleUI.sidebarRoute.
 * So this link, the Billing menu and the sidebar rail open one surface.
 *
 * The link renders only when the Blueprint registered its console UI on
 * this Sovereign: the presence signal BillingSectionNav uses, read with
 * the same query key so both share one request. While that is not known,
 * or when the billing engine is not installed, nothing is rendered: a
 * link that leads nowhere is worse than no link.
 */

import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { billingSectionsFor } from '../bss/billing-sections'
import { getSidebarEntries } from '@/lib/console-ui.api'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export interface CostViewLinkProps {
  /** Classes added to the card, for the spacing its place needs. */
  className?: string
  /** Test seam — the Blueprint ids taken as installed; nothing is fetched. */
  installedOverride?: readonly string[]
}

export function CostViewLink({ className, installedOverride }: CostViewLinkProps) {
  const { deploymentId } = useResolvedDeploymentId()
  const entries = useQuery({
    queryKey: ['billing-nav-sidebar-entries', deploymentId],
    enabled: !installedOverride && !!deploymentId,
    staleTime: 60_000,
    retry: false,
    queryFn: () => getSidebarEntries(deploymentId as string),
  })
  const installed =
    installedOverride ?? (entries.data ?? []).filter((e) => e.enabled).map((e) => e.id)
  const section = billingSectionsFor(installed).find((s) => s.id === 'chargeback')
  if (!section) return null

  return (
    <section
      data-testid="cost-view-link"
      className={`rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-4 ${className ?? ''}`.trim()}
    >
      <h2 className="mb-1 text-sm font-semibold text-[var(--color-text-strong)]">Usage and cost</h2>
      <p className="mb-3 text-sm text-[var(--color-text-dim)]">
        Usage and cost per Organization and per Application are metered and priced in
        Catalyst BSS.
      </p>
      <Link
        to={section.to as never}
        data-testid="cost-view-link-open"
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2.5 py-1 text-xs font-medium text-[var(--color-accent)] no-underline transition-colors hover:border-[var(--color-accent)]"
      >
        Open the cost view
      </Link>
    </section>
  )
}
