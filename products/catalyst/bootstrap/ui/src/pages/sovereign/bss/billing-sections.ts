/**
 * billing-sections.ts — the Billing menu's section catalogue (issue #4196).
 *
 * Single source of truth for the Billing sections — the three native ones
 * (Vouchers · Orders · Revenue) plus any APPLICATION section such as
 * Chargeback — consumed by BillingSectionNav (the in-page switcher)
 * and the route table. Kept in a constants-only module (no component
 * export) so the BillingSectionNav component file stays fast-refresh-clean
 * (react-refresh/only-export-components). Vouchers is first because it is
 * the menu's default landing — voucher issuance is the Phase-0
 * sovereign-admin onboarding tool reachable day-one (DoD.md Phase 0).
 */

export type BillingSection = 'vouchers' | 'orders' | 'revenue' | 'chargeback'

export interface BillingSectionDef {
  id: BillingSection
  label: string
  to: string
  /**
   * Blueprint id this section belongs to, for a section served by a separate
   * APPLICATION rather than a native console page. Undefined for the native
   * three.
   *
   * A section with `blueprint` set renders ONLY when that Blueprint is
   * actually installed on this Sovereign — see billingSectionsFor(). ADR-0014
   * D5 requires the Sovereign chargeback placement to be "linked from
   * console.<fqdn>/billing", but a Sovereign that never installed
   * bp-chargeback must not be shown a tab that dead-ends; the Blueprint's own
   * consoleUI registration is the presence signal, the same one the sidebar
   * rail already merges.
   */
  blueprint?: string
}

export const BILLING_SECTIONS: readonly BillingSectionDef[] = [
  { id: 'vouchers', label: 'Vouchers', to: '/billing/vouchers' },
  { id: 'orders', label: 'Orders', to: '/billing/orders' },
  { id: 'revenue', label: 'Revenue', to: '/billing/revenue' },
  // ADR-0014 D5 — the Sovereign BSS placement of bp-chargeback, reached from
  // the Billing menu. The route is the Blueprint's own declared sidebarRoute
  // (products/chargeback/blueprint.yaml consoleUI.sidebarRoute), so Billing
  // and the sidebar rail send the operator to the SAME surface rather than
  // drifting apart.
  { id: 'chargeback', label: 'Chargeback', to: '/apps/bp-chargeback/dashboard', blueprint: 'bp-chargeback' },
]

/**
 * billingSectionsFor — the sections to render given the Blueprint ids that
 * registered a console UI on this Sovereign (the `/console-ui/sidebar-entries`
 * view the rail already consumes).
 *
 * Native sections always render. An application section renders only when its
 * Blueprint is present, so an operator is never offered a tab that leads
 * nowhere. Passing an empty/undefined set yields the native three — the
 * pre-ADR-0014 behaviour — so a console that cannot reach that endpoint
 * degrades to exactly what it rendered before.
 */
export function billingSectionsFor(
  installedBlueprints?: ReadonlySet<string> | readonly string[],
): readonly BillingSectionDef[] {
  const present =
    installedBlueprints instanceof Set
      ? installedBlueprints
      : new Set(installedBlueprints ?? [])
  return BILLING_SECTIONS.filter((s) => !s.blueprint || present.has(s.blueprint))
}
