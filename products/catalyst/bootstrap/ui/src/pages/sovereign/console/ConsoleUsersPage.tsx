/**
 * ConsoleUsersPage — Sovereign Console /console/users
 *
 * User Access editor for this Sovereign. Reuses the same visual shape
 * as UserAccessListPage.tsx without the deploymentId param.
 *
 * Phase 8b placeholder — full IAM wiring is covered by #322/#323.
 *
 * Related: GitHub issue #607
 */

import { Users } from 'lucide-react'

export function ConsoleUsersPage() {
  return (
    <div data-testid="console-users-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Users</h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Manage user access and roles for this Sovereign.
        </p>
      </div>

      {/* Placeholder — #322/#323 wires the Keycloak user list */}
      <div
        className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-10 text-center"
        data-testid="users-placeholder"
      >
        <Users className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
        <p className="text-sm font-medium text-[var(--color-text)]">User Access</p>
        <p className="mt-1 text-xs text-[var(--color-text-dim)]">
          Keycloak user management integration pending (#322/#323).
        </p>
      </div>
    </div>
  )
}
