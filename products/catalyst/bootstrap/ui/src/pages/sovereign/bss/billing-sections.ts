/**
 * billing-sections.ts — the Billing menu's section catalogue (issue #4196).
 *
 * Single source of truth for the three native Billing sections — Vouchers ·
 * Orders · Revenue — consumed by BillingSectionNav (the in-page switcher)
 * and the route table. Kept in a constants-only module (no component
 * export) so the BillingSectionNav component file stays fast-refresh-clean
 * (react-refresh/only-export-components). Vouchers is first because it is
 * the menu's default landing — voucher issuance is the Phase-0
 * sovereign-admin onboarding tool reachable day-one (DoD.md Phase 0).
 */

export type BillingSection = 'vouchers' | 'orders' | 'revenue'

export interface BillingSectionDef {
  id: BillingSection
  label: string
  to: string
}

export const BILLING_SECTIONS: readonly BillingSectionDef[] = [
  { id: 'vouchers', label: 'Vouchers', to: '/billing/vouchers' },
  { id: 'orders', label: 'Orders', to: '/billing/orders' },
  { id: 'revenue', label: 'Revenue', to: '/billing/revenue' },
]
