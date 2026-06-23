/**
 * BillingSectionNav — the in-page section switcher for the Billing menu.
 *
 * Issue #4196: the Billing menu is ONE first-class console surface with
 * three native sections — Vouchers · Orders · Revenue. This component is
 * the horizontal sub-nav rendered in each section page's `headerSlotLeft`
 * (where the legacy "← Back to BSS overview" crumb used to live), so the
 * operator can move between sections without leaving the Billing menu.
 *
 * The active section is derived from the current pathname so the tab
 * highlight follows the URL (and survives a deep-link / refresh). Tokens
 * + active-pill treatment mirror the SovereignSidebar nav items so the
 * sub-nav reads as a sibling affordance, not bespoke chrome
 * (docs/INVIOLABLE-PRINCIPLES.md #4 — never hardcode; every label/route
 * lives in the BILLING_SECTIONS constant in billing-sections.ts).
 */

import { Link, useRouterState } from '@tanstack/react-router'
import { BILLING_SECTIONS, type BillingSection } from './billing-sections'

function deriveSection(pathname: string): BillingSection {
  if (/^\/billing\/orders(\/|$)/.test(pathname)) return 'orders'
  if (/^\/billing\/revenue(\/|$)/.test(pathname)) return 'revenue'
  // /billing and /billing/vouchers both land on Vouchers (the default).
  return 'vouchers'
}

export function BillingSectionNav() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const active = deriveSection(pathname)
  return (
    <nav
      data-testid="billing-section-nav"
      aria-label="Billing sections"
      className="flex items-center gap-1"
    >
      {BILLING_SECTIONS.map((s) => {
        const isActive = active === s.id
        const cls = isActive
          ? 'bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
          : 'text-[var(--color-text-dim)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-text)]'
        return (
          <Link
            key={s.id}
            to={s.to as never}
            data-testid={`billing-section-nav-${s.id}`}
            aria-current={isActive ? 'page' : undefined}
            className={`rounded-md px-2.5 py-1 text-xs font-medium no-underline transition-colors ${cls}`}
          >
            {s.label}
          </Link>
        )
      })}
    </nav>
  )
}
