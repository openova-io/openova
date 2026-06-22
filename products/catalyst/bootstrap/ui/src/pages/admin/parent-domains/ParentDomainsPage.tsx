/**
 * ParentDomainsPage — admin "Parent Domains" surface (issue #829, parent
 * epic #825).
 *
 * Mounted at /organizations/domains in the SovereignConsoleLayout (the
 * legacy /parent-domains route redirects here — see router.tsx
 * ORGANIZATIONS_REDIRECTS, #3378). Visible only to operator-admin role
 * (gating handled at route level via the same RequireSession guard the
 * rest of /console/* uses).
 *
 * Issue #4089 (founder UX-polish): the inner content now lives in the
 * reusable `ParentDomainsSection` so the SAME surface can render BOTH as
 * this standalone page AND as the `#parent-domains` anchor section of the
 * unified /settings page (consistent with #dns, #marketplace, …). This
 * page is now a thin PortalShell wrapper — zero functional change to the
 * standalone route; the table / propagation drawer / add-domain modal /
 * remove flow all live in ParentDomainsSection.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall, target-state shape): the section renders the empty
 *       state on first paint while the fetch runs in the background.
 *   #4 (never hardcode): every URL flows through the
 *       parentDomains.api.ts wrappers; no inline `/api/...` strings.
 *   #10 (credential hygiene): the registrarToken field uses input
 *       type="password"; the token is sent once on POST and never
 *       returned in the GET list response.
 */

import {
  ParentDomainsSection,
  type ParentDomainsSectionProps,
} from './ParentDomainsSection'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

export type ParentDomainsPageProps = ParentDomainsSectionProps

export function ParentDomainsPage({
  initialItems,
  disableFetch = false,
}: ParentDomainsPageProps = {}) {
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''
  const { deploymentId } = useResolvedDeploymentId()

  return (
    <PortalShell
      deploymentId={deploymentId ?? ''}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Parent Domains"
    >
      <ParentDomainsSection initialItems={initialItems} disableFetch={disableFetch} />
    </PortalShell>
  )
}
