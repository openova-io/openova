/**
 * BillingModeGate — mode-aware billing (issue #3378 B4 + DoD 5 + §5).
 *
 * The billing pages are mode-filtered, never separate systems (§2.6):
 *   • real    → the full payment flows (the wrapped page renders as-is).
 *   • showback/chargeback → consumption only, ZERO payment actions: the
 *     gate renders the ShowbackPanel + the §5 empty-state explaining that
 *     real billing exists when the marketplace sells to external orgs,
 *     and does NOT mount the payment page at all (so no Issue-voucher /
 *     checkout CTA is reachable).
 *
 * The parent org is showback (§5), so on a single-org Sovereign the
 * billing routes render the showback view by default — real billing is
 * dormant until the first external customer. The mode is resolved from
 * the parent org's billingMode (listOrganizations → the parent row);
 * tolerant of failure (defaults to showback so payment actions never
 * leak on an unresolved estate).
 *
 * This wraps the moved bss billing/orders/revenue/vouchers pages at the
 * ROUTE level — their internals are untouched (#3378 non-negotiables:
 * "page internals untouched except where a DoD box names the change").
 */

import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ShowbackPanel } from './ShowbackPanel'
import {
  listOrganizations,
  type OrgBillingMode,
  type OrgRow,
} from '@/lib/organizations.api'

export interface BillingModeGateProps {
  /** The wrapped payment page (rendered only in real mode). */
  children: ReactNode
  /** Test seam — force the resolved mode. */
  modeOverride?: OrgBillingMode
}

export function BillingModeGate({ children, modeOverride }: BillingModeGateProps) {
  const query = useQuery<OrgRow[]>({
    queryKey: ['organizations-directory'],
    queryFn: listOrganizations,
    staleTime: 30_000,
    enabled: !modeOverride,
    placeholderData: (prev) => prev,
  })

  // Resolve the parent org's billing mode. Default showback so payment
  // actions never leak before the estate resolves.
  const parent = query.data?.find((o) => o.isParent)
  const mode: OrgBillingMode = modeOverride ?? parent?.billingMode ?? 'showback'

  if (mode === 'real') {
    return <>{children}</>
  }

  // showback / chargeback → consumption only, zero payment actions.
  return (
    <div data-testid="billing-mode-gate" data-mode={mode} className="px-6 py-4">
      <div
        data-testid="billing-showback-notice"
        className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-4 py-3 text-sm text-[var(--color-text-dim)]"
      >
        This organization is in{' '}
        <span className="font-medium text-[var(--color-text)]">{mode}</span> mode —
        consumption is attributed, not invoiced. Real billing (invoices, vouchers,
        checkout) exists when the marketplace sells to external organizations.
      </div>
      <ShowbackPanel />
    </div>
  )
}
