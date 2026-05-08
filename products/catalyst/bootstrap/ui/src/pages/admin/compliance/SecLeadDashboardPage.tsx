/**
 * SecLeadDashboardPage — Security Lead compliance dashboard
 * (slice U2, #1096).
 *
 * Same layout as the SRE Lead dashboard (U1) but:
 *   • Sliced by `policy-domain: security` only (filter on policyResults
 *     keys via `SECURITY_DOMAIN_POLICIES` set).
 *   • Color palette: red (0%) → amber (50%) → blue (100%) — security
 *     uses a cooler high-end so the SecLead dashboard reads at a
 *     glance as a different surface from the SRE Lead one.
 *
 * Route: `/admin/compliance/security` (mother) AND `/sec/compliance`
 * (chroot).
 *
 * Implementation: thin wrapper over `SREDashboardPage` that injects the
 * security palette + SECURITY_DOMAIN_POLICIES filter. Keeps both
 * dashboards pixel-byte-byte identical apart from those two knobs;
 * future surface evolution lives in one place.
 */

import { SREDashboardPage } from './SREDashboardPage'
import { SECURITY_DOMAIN_POLICIES, type ScorecardResponse } from './compliance.api'

export interface SecLeadDashboardPageProps {
  disableStream?: boolean
  initialDataOverride?: ScorecardResponse
}

export function SecLeadDashboardPage(props: SecLeadDashboardPageProps = {}) {
  return (
    <SREDashboardPage
      {...props}
      titleOverride="Security Lead — Compliance Dashboard"
      paletteOverride="security"
      policyDomainFilter={SECURITY_DOMAIN_POLICIES}
      routeKey="security"
    />
  )
}
