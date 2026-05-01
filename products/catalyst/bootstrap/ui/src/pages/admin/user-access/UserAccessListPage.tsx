/**
 * UserAccessListPage — Sovereign IAM list view (issue #323).
 *
 * Layout:
 *   • PortalShell wrapper (sidebar + top header band) for visual
 *     parity with every other admin surface.
 *   • Page heading "User Access" + tagline + "+ New" CTA.
 *   • Table: Name | User | Sovereign | Grants | Bindings | Created |
 *     (Delete) — click a row to navigate to the edit page.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, not iterative
 * MVP) the page renders the empty-state on first paint while the
 * fetch runs in the background; the table replaces the empty state
 * once the response lands.
 *
 * Per #4 (never hardcode), the API call goes through the
 * userAccess.api.ts wrappers which read API_BASE from the central
 * shared/config/urls module.
 */

import { useEffect, useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import { PortalShell } from '@/pages/sovereign/PortalShell'
import {
  deleteUserAccess,
  grantsSummary,
  listUserAccess,
  userLabel,
  type UserAccessItem,
} from './userAccess.api'

export interface UserAccessListPageProps {
  /** Test seam — when set, supplies the initial list synchronously
   *  without firing fetch. */
  initialItems?: UserAccessItem[]
  /** Test seam — disables the fetch effect entirely. */
  disableFetch?: boolean
}

export function UserAccessListPage({
  initialItems,
  disableFetch = false,
}: UserAccessListPageProps = {}) {
  const params = useParams({ from: '/provision/$deploymentId/users' as never }) as {
    deploymentId: string
  }
  const deploymentId = params.deploymentId

  const [items, setItems] = useState<UserAccessItem[]>(initialItems ?? [])
  const [loading, setLoading] = useState<boolean>(!initialItems && !disableFetch)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (disableFetch) return
    let cancelled = false
    setLoading(true)
    listUserAccess(deploymentId)
      .then((rows) => {
        if (!cancelled) {
          setItems(rows)
          setError(null)
        }
      })
      .catch((err: Error) => {
        if (!cancelled) setError(err.message)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [deploymentId, disableFetch])

  async function onDelete(item: UserAccessItem) {
    const subject = userLabel(item.spec.user)
    if (!confirm(`Delete UserAccess "${item.name}"? This will revoke ${subject}'s access.`)) {
      return
    }
    try {
      await deleteUserAccess(deploymentId, item.name)
      setItems((rows) => rows.filter((r) => r.name !== item.name))
    } catch (err) {
      setError((err as Error).message)
    }
  }

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="User Access">
      <div data-testid="user-access-list-page" className="px-6 py-4">
        <div className="mb-4 flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold text-[var(--color-text-strong)]">User Access</h1>
            <p className="text-sm text-[var(--color-text-dim)]">
              Per-user access to Sovereigns × Applications × Namespaces × Roles.
            </p>
          </div>
          <Link
            to={'/provision/$deploymentId/users/new' as never}
            params={{ deploymentId } as never}
            data-testid="user-access-new-cta"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-sm font-medium text-white hover:opacity-90"
          >
            + New
          </Link>
        </div>

        {error ? (
          <div data-testid="user-access-error" className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
            {error}
          </div>
        ) : null}

        {loading ? (
          <div data-testid="user-access-loading" className="text-sm text-[var(--color-text-dim)]">Loading…</div>
        ) : items.length === 0 ? (
          <div
            data-testid="user-access-empty-state"
            className="rounded-md border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-text-dim)]"
          >
            No user access entries yet. Click "+ New" to grant access to a Keycloak user or group.
          </div>
        ) : (
          <table data-testid="user-access-table" className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-[var(--color-border)] text-left text-xs uppercase text-[var(--color-text-dim)]">
                <th className="py-2 pr-3">Name</th>
                <th className="py-2 pr-3">User / Groups</th>
                <th className="py-2 pr-3">Sovereign</th>
                <th className="py-2 pr-3">Grants</th>
                <th className="py-2 pr-3">Bindings</th>
                <th className="py-2 pr-3">Created</th>
                <th className="py-2 pr-3" aria-label="actions"></th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr
                  key={item.name}
                  data-testid={`user-access-row-${item.name}`}
                  className="border-b border-[var(--color-border)] hover:bg-[var(--color-bg-2)]"
                >
                  <td className="py-2 pr-3 font-mono text-[var(--color-text)]">
                    <Link
                      to={'/provision/$deploymentId/users/$name' as never}
                      params={{ deploymentId, name: item.name } as never}
                      className="hover:underline"
                    >
                      {item.name}
                    </Link>
                  </td>
                  <td className="py-2 pr-3">{userLabel(item.spec.user)}</td>
                  <td className="py-2 pr-3 font-mono text-xs">{item.spec.sovereignRef}</td>
                  <td className="py-2 pr-3 text-xs">{grantsSummary(item.spec.applications)}</td>
                  <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
                    {item.status?.rolebindingsCreated ?? '—'}
                  </td>
                  <td className="py-2 pr-3 text-xs text-[var(--color-text-dim)]">
                    {item.creationTimestamp ?? '—'}
                  </td>
                  <td className="py-2 pr-3 text-right">
                    <button
                      type="button"
                      data-testid={`user-access-delete-${item.name}`}
                      onClick={() => onDelete(item)}
                      className="text-xs text-red-400 hover:underline"
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </PortalShell>
  )
}
