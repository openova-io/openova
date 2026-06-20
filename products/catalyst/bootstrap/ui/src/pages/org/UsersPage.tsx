/**
 * UsersPage — Organization-tier user CRUD UI (issue #802).
 *
 * Mounted only when the discovered tenant_kind === 'sme'. Shows:
 *
 *   • Header + "+ New user" CTA
 *   • Create form (inline) — fires POST /api/v1/org/users which
 *     orchestrates the ADR-0003 3-step hook (Keycloak → NewAPI →
 *     K8s Secret). Renders a 3-step progress bar so the Organization admin
 *     sees each step transition pending → done in real time.
 *   • List table — Email | Status (with mini step indicator) |
 *     Created | Last error | (Delete)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * goes through org.api.ts → apiUrl(). Per #2 (never compromise on
 * quality), the page paints partial state on hook failure rather
 * than swallowing the error: each step's failed/done badge is
 * surfaced verbatim from the back end's response.
 */

import { useEffect, useState } from 'react'
import { Trash2, UserPlus } from 'lucide-react'
import {
  createOrgUser,
  deleteOrgUser,
  listOrgUsers,
  type OrgUser,
  type OrgStepState,
} from './org.api'

export interface UsersPageProps {
  /** Test seam — supplies an initial list synchronously. */
  initialItems?: OrgUser[]
  /** Test seam — disables the fetch effect entirely. */
  disableFetch?: boolean
}

export function UsersPage({
  initialItems,
  disableFetch = false,
}: UsersPageProps = {}) {
  const [items, setItems] = useState<OrgUser[]>(initialItems ?? [])
  const [loading, setLoading] = useState<boolean>(!initialItems && !disableFetch)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState<boolean>(false)
  const [creating, setCreating] = useState<boolean>(false)
  const [newEmail, setNewEmail] = useState<string>('')
  const [recentlyCreated, setRecentlyCreated] = useState<OrgUser | null>(null)

  useEffect(() => {
    if (disableFetch) return
    let cancelled = false
    setLoading(true)
    listOrgUsers()
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
  }, [disableFetch])

  async function onCreate(e: React.FormEvent) {
    e.preventDefault()
    if (!newEmail.trim()) return
    setCreating(true)
    setError(null)
    try {
      const created = await createOrgUser({ email: newEmail.trim() })
      setItems((rows) => [created, ...rows])
      setRecentlyCreated(created)
      setNewEmail('')
      setShowCreate(false)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setCreating(false)
    }
  }

  async function onDelete(item: OrgUser) {
    if (!confirm(`Delete user ${item.email}? This revokes Keycloak + NewAPI access.`)) {
      return
    }
    try {
      await deleteOrgUser(item.uuid)
      setItems((rows) => rows.filter((r) => r.uuid !== item.uuid))
    } catch (err) {
      setError((err as Error).message)
    }
  }

  return (
    <div data-testid="org-users-page" className="px-6 py-4">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-[var(--color-text-strong)]">Users</h1>
          <p className="text-sm text-[var(--color-text-dim)]">
            Tenant-scoped user CRUD. Create fires the unified-rbac hook to materialise the user across Keycloak, NewAPI, and the K8s Secret store.
          </p>
        </div>
        <button
          type="button"
          data-testid="org-users-new-cta"
          onClick={() => setShowCreate((v) => !v)}
          className="inline-flex items-center gap-2 rounded-md bg-[var(--color-accent)] px-3 py-2 text-sm font-medium text-white hover:bg-[var(--color-accent-strong)]"
        >
          <UserPlus className="h-4 w-4" />
          {showCreate ? 'Cancel' : 'New user'}
        </button>
      </div>

      {error && (
        <div data-testid="org-users-error" className="mb-4 rounded-md border border-[var(--color-danger)] bg-[var(--color-danger-soft)] p-3 text-sm text-[var(--color-danger)]">
          {error}
        </div>
      )}

      {showCreate && (
        <form
          data-testid="org-users-create-form"
          onSubmit={onCreate}
          className="mb-4 flex items-end gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-4"
        >
          <label className="flex-1 text-sm">
            <span className="mb-1 block text-[var(--color-text-dim)]">Email</span>
            <input
              type="email"
              required
              value={newEmail}
              onChange={(e) => setNewEmail(e.target.value)}
              placeholder="alice@example.com"
              className="w-full rounded-md border border-[var(--color-border)] bg-[var(--color-input)] px-3 py-2 text-sm"
            />
          </label>
          <button
            type="submit"
            disabled={creating}
            className="rounded-md bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
          >
            {creating ? 'Provisioning…' : 'Create'}
          </button>
        </form>
      )}

      {recentlyCreated && (
        <div
          data-testid="org-users-progress"
          className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] p-4"
        >
          <div className="mb-2 text-sm font-medium">
            Provisioning {recentlyCreated.email}
          </div>
          <ProgressSteps steps={recentlyCreated.steps} />
          {recentlyCreated.last_error && (
            <div className="mt-2 text-xs text-[var(--color-danger)]">
              {recentlyCreated.last_error}
            </div>
          )}
        </div>
      )}

      {loading ? (
        <div className="text-sm text-[var(--color-text-dim)]">Loading users…</div>
      ) : items.length === 0 ? (
        <div data-testid="org-users-empty" className="rounded-md border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-dim)]">
          No users yet. Click "New user" to create the first one.
        </div>
      ) : (
        <table className="w-full border-collapse text-sm" data-testid="org-users-table">
          <thead>
            <tr className="border-b border-[var(--color-border)] text-left text-[var(--color-text-dim)]">
              <th className="py-2 pr-4 font-normal">Email</th>
              <th className="py-2 pr-4 font-normal">Status</th>
              <th className="py-2 pr-4 font-normal">Created</th>
              <th className="py-2 pr-4 font-normal"></th>
            </tr>
          </thead>
          <tbody>
            {items.map((u) => (
              <tr
                key={u.uuid}
                data-testid={`org-users-row-${u.uuid}`}
                className="border-b border-[var(--color-border)]"
              >
                <td className="py-2 pr-4 font-medium">{u.email}</td>
                <td className="py-2 pr-4">
                  <ProgressSteps steps={u.steps} compact />
                </td>
                <td className="py-2 pr-4 text-[var(--color-text-dim)]">
                  {new Date(u.created_at).toLocaleDateString()}
                </td>
                <td className="py-2 pr-4 text-right">
                  <button
                    type="button"
                    onClick={() => onDelete(u)}
                    className="inline-flex items-center gap-1 text-xs text-[var(--color-danger)] hover:underline"
                  >
                    <Trash2 className="h-3 w-3" />
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function ProgressSteps({
  steps,
  compact = false,
}: {
  steps: { kc: OrgStepState; newapi: OrgStepState; secret: OrgStepState }
  compact?: boolean
}) {
  const items: { label: string; state: OrgStepState }[] = [
    { label: 'Keycloak', state: steps.kc },
    { label: 'NewAPI', state: steps.newapi },
    { label: 'Secret', state: steps.secret },
  ]
  return (
    <div className="flex items-center gap-2">
      {items.map((s, i) => (
        <span key={s.label} className="flex items-center gap-1">
          <span
            className={
              'inline-block h-2 w-2 rounded-full ' +
              (s.state === 'done'
                ? 'bg-[var(--color-success,#16a34a)]'
                : s.state === 'failed'
                  ? 'bg-[var(--color-danger,#dc2626)]'
                  : 'bg-[var(--color-text-dim,#9ca3af)]')
            }
            data-testid={`org-step-${s.label.toLowerCase()}-${s.state}`}
            aria-label={`${s.label}: ${s.state}`}
          />
          {!compact && <span className="text-xs">{s.label}</span>}
          {!compact && i < items.length - 1 && (
            <span className="text-xs text-[var(--color-text-dim)]">→</span>
          )}
        </span>
      ))}
    </div>
  )
}
