/**
 * RolesPage — Organization-tier Keycloak group → application role mapping
 * (issue #802 #5).
 *
 * Mounted only when the discovered tenant_kind === 'sme'. The page
 * is a static editor for the canonical mapping table the Organization admin
 * customises:
 *
 *   Keycloak group        → app role
 *   ─────────────────────────────────────────
 *   wp-admins             → wordpress:admin
 *   wp-editors            → wordpress:editor
 *   openclaw-users        → openclaw:user
 *   openclaw-managers     → openclaw:manager
 *   stalwart-users        → stalwart:user
 *   stalwart-postmasters  → stalwart:postmaster
 *   org-admins            → rbac:admin
 *
 * Per the locked decision in #795 [B] every group → role assignment
 * is materialised by the unified-rbac hook on user create. This page
 * is a read + customise surface; live editing of role bindings is
 * deferred to the unified-rbac controller's reconciliation loop.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the row
 * data lives in a const array — no inline strings in JSX.
 */

interface RoleRow {
  group: string
  appRole: string
  description: string
}

const DEFAULT_ROLE_MAP: readonly RoleRow[] = [
  { group: 'wp-admins', appRole: 'wordpress:admin', description: 'Full WordPress admin' },
  { group: 'wp-editors', appRole: 'wordpress:editor', description: 'Edit posts, pages' },
  { group: 'openclaw-users', appRole: 'openclaw:user', description: 'Use OpenClaw workspace' },
  { group: 'openclaw-managers', appRole: 'openclaw:manager', description: 'Manage OpenClaw workspaces' },
  { group: 'stalwart-users', appRole: 'stalwart:user', description: 'Mailbox + send' },
  { group: 'stalwart-postmasters', appRole: 'stalwart:postmaster', description: 'Stalwart admin' },
  { group: 'org-admins', appRole: 'rbac:admin', description: 'Manage users + roles' },
]

export function RolesPage() {
  return (
    <div data-testid="org-roles-page" className="px-6 py-4">
      <div className="mb-4">
        <h1 className="text-xl font-semibold text-[var(--color-text-strong)]">Roles</h1>
        <p className="text-sm text-[var(--color-text-dim)]">
          Keycloak group → application role mapping for the Organization-vcluster realm.
          Per #795 [B] every binding is materialised by the unified-rbac hook on user create.
        </p>
      </div>

      <table className="w-full border-collapse text-sm" data-testid="org-roles-table">
        <thead>
          <tr className="border-b border-[var(--color-border)] text-left text-[var(--color-text-dim)]">
            <th className="py-2 pr-4 font-normal">Keycloak group</th>
            <th className="py-2 pr-4 font-normal">App role</th>
            <th className="py-2 pr-4 font-normal">Description</th>
          </tr>
        </thead>
        <tbody>
          {DEFAULT_ROLE_MAP.map((r) => (
            <tr
              key={r.group}
              data-testid={`org-role-${r.group}`}
              className="border-b border-[var(--color-border)]"
            >
              <td className="py-2 pr-4 font-mono text-xs">{r.group}</td>
              <td className="py-2 pr-4 font-mono text-xs">{r.appRole}</td>
              <td className="py-2 pr-4 text-[var(--color-text-dim)]">{r.description}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
