/**
 * MembersTab — per-Application member list embedded in the AppDetail
 * page (EPIC-3 #1098 slice U5).
 *
 * Renders a table of users granted access to this Application — one
 * row per UserAccess CR whose scope includes
 * `openova.io/application=<this-app>` (or a global grant). Per row:
 *
 *   - User (email or KC subject)
 *   - Tier (inline tier-picker; on change → POST /rbac/assign)
 *   - Scopes (chip list)
 *   - Source (KC | Azure-AD-federated)
 *   - Actions (Remove → DELETE on the UserAccess CR)
 *
 * Top action: Add member → modal with KCUserPicker (U2) + tier-picker
 * + scope-chips → POST /rbac/assign.
 *
 * Data source: A2's GET /access-matrix filtered to this Application.
 * Reuses the same MembersList component as the per-Org Members page
 * (slice U6) — see ../../admin/rbac/MembersList.tsx for the shared
 * shape. EPIC-2 Slice O Members page ALSO consumes this same shape
 * (canonical-seam map row added below).
 *
 * Sub-page tab pattern per the seam map (slice U / ComplianceTab):
 * tab files live in a sibling directory next to the page.
 */

import { MembersList } from '@/pages/admin/rbac/MembersList'

export interface MembersTabProps {
  /** Sovereign (deployment) id. */
  sovereignId: string
  /** Application name (matches access.openova.io/application scope). */
  applicationName: string
  /** Test seam — bypass network calls. */
  initialMatrix?: import('@/pages/admin/rbac/rbac.api').AccessMatrixResponse | null
}

export function MembersTab({ sovereignId, applicationName, initialMatrix }: MembersTabProps) {
  return (
    <div className="app-tabpanel" data-testid="app-members-tabpanel">
      <p className="mb-3 text-xs text-[var(--color-text-dim)]">
        Operators with access to{' '}
        <code className="font-mono text-[var(--color-text)]">{applicationName}</code>. Source:
        UserAccess CRs whose scope binds them to this Application or grants them global access.
      </p>
      <MembersList
        sovereignId={sovereignId}
        scope={{ kind: 'application', value: applicationName }}
        initialMatrix={initialMatrix ?? undefined}
      />
    </div>
  )
}
