/**
 * OrgMembersPage — per-Organization member list (EPIC-3 #1098 slice U6).
 *
 * Route: `/admin/organizations/{orgId}/members` (mothership)
 *        `/organizations/{orgId}/members` (chroot Sovereign Console)
 *
 * Reuses the U5 MembersList component parameterized for organization
 * scope. Same A2 GET /access-matrix data source, same /rbac/assign
 * mutation surface.
 *
 * Note for EPIC-2 Slice O (Org self-service): this page IS the Org
 * Members surface — Slice O can fully reuse this with no changes.
 * Confirmed in the canonical-seam map row added by U5-U8.
 */

import { useParams } from '@tanstack/react-router'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { MembersList } from './MembersList'

export interface OrgMembersPageProps {
  /** Test seam — pre-fill orgId without TanStack Router. */
  initialOrgId?: string
  /** Test seam — pre-fill deploymentId. */
  initialDeploymentId?: string
}

export function OrgMembersPage({ initialOrgId, initialDeploymentId }: OrgMembersPageProps = {}) {
  const params = useParams({ strict: false }) as { orgId?: string; deploymentId?: string }
  const orgId = initialOrgId ?? params.orgId ?? ''
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = initialDeploymentId ?? params.deploymentId ?? resolvedId ?? ''

  return (
    <PortalShell deploymentId={deploymentId} pageTitle={`Members — ${orgId}`}>
      <div data-testid="org-members-page" className="mx-auto max-w-4xl px-6 py-4">
        <h1 className="mb-1 text-base font-semibold text-[var(--color-text-strong)]">
          Members — <code className="font-mono">{orgId}</code>
        </h1>
        <p className="mb-4 text-xs text-[var(--color-text-dim)]">
          Operators with grants scoped to this Organization. Source: UserAccess CRs whose scope
          includes <code className="font-mono">openova.io/org={orgId}</code>.
        </p>
        <MembersList sovereignId={deploymentId} scope={{ kind: 'organization', value: orgId }} />
      </div>
    </PortalShell>
  )
}
