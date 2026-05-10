/**
 * MembersList — shared per-scope member view (EPIC-3 #1098 slice U5,
 * U6; reused by EPIC-2 Slice O Org self-service Members page).
 *
 * Parameterized by `scope`:
 *
 *   { kind: 'application', value: '<app-name>' }  → U5 (App Members tab)
 *   { kind: 'organization', value: '<org-slug>' } → U6 (per-Org page)
 *
 * Both wire to A2's GET /rbac/access-matrix with the matching
 * filter. Per row the operator can change tier inline (POST
 * /rbac/assign) or remove the grant (DELETE on the UserAccess CR).
 * The "Add member" affordance opens a modal that mirrors the
 * MultiGrantEdit form for the chosen scope.
 *
 * Per the canonical-seam map (`Sub-page tab pattern`) this component
 * lives under `pages/admin/rbac/` so the per-Org route, the AppDetail
 * tab, and the future EPIC-2 Slice O Members page all import the
 * same surface.
 */

import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { KCUserPicker } from '@/widgets/rbac/KCUserPicker'
import {
  RBAC_TIERS,
  TIER_AUTO_INJECTED_SCOPES,
  deleteUserAccess,
  getAccessMatrix,
  rbacAssign,
  type AccessMatrixGrant,
  type AccessMatrixResponse,
  type AccessMatrixUser,
  type KCUser,
  type RBACTier,
} from './rbac.api'
import {
  defaultScopesForScope,
  flattenForScope,
  grantForScope,
  type MembersScope,
} from './membersListHelpers'

// Re-export the type for callers that import it from MembersList for
// historical reasons. Type re-exports are exempt from the
// react-refresh/only-export-components rule (types disappear at runtime).
export type { MembersScope }

export interface MembersListProps {
  sovereignId: string
  scope: MembersScope
  /** Test seam — pre-fill data, skip network. */
  initialMatrix?: AccessMatrixResponse
  /** Test seam — auto-open the Add modal. */
  forceOpenAddModal?: boolean
}

export function MembersList({
  sovereignId,
  scope,
  initialMatrix,
  forceOpenAddModal = false,
}: MembersListProps) {
  const qc = useQueryClient()
  const [addOpen, setAddOpen] = useState(forceOpenAddModal)
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; msg: string } | null>(null)

  // A2's filter convention: ?application=<name> for app scope, ?org=<slug> for org.
  const matrixQ = useQuery({
    queryKey: ['rbac-matrix', sovereignId, scope.kind, scope.value],
    queryFn: () =>
      getAccessMatrix(sovereignId, {
        application: scope.kind === 'application' ? scope.value : undefined,
        org: scope.kind === 'organization' ? scope.value : undefined,
      }),
    enabled: !!sovereignId && !initialMatrix,
    staleTime: 15_000,
  })

  const matrix: AccessMatrixResponse = useMemo(
    () =>
      initialMatrix ??
      matrixQ.data ?? {
        users: [],
        applications: [],
        tiers: [],
      },
    [initialMatrix, matrixQ.data],
  )

  // Flatten the matrix users to one row per user × this-scope.
  const rows = useMemo(() => flattenForScope(matrix, scope), [matrix, scope])

  const updateTier = useMutation({
    mutationFn: async (vars: { user: AccessMatrixUser; tier: RBACTier }) => {
      const grant = grantForScope(vars.user, scope)
      const baseScopes = grant?.scopes ?? defaultScopesForScope(scope)
      return rbacAssign(sovereignId, {
        user: { email: vars.user.email, keycloakSubject: vars.user.id },
        tier: vars.tier,
        scope: baseScopes,
      })
    },
    onSuccess: (resp) => {
      const verb = resp.applied === 'no-op' ? 'Already' : 'Updated'
      setToast({ kind: 'ok', msg: `${verb}: tier=${resp.tierClusterRole}` })
      qc.invalidateQueries({ queryKey: ['rbac-matrix', sovereignId, scope.kind, scope.value] })
    },
    onError: (err: Error) => setToast({ kind: 'err', msg: err.message }),
  })

  const removeMember = useMutation({
    mutationFn: async (vars: { user: AccessMatrixUser; userAccessRef: string }) => {
      return deleteUserAccess(sovereignId, vars.userAccessRef)
    },
    onSuccess: (_, vars) => {
      setToast({ kind: 'ok', msg: `Removed ${vars.user.email ?? vars.user.id}` })
      qc.invalidateQueries({ queryKey: ['rbac-matrix', sovereignId, scope.kind, scope.value] })
    },
    onError: (err: Error) => setToast({ kind: 'err', msg: err.message }),
  })

  return (
    <div data-testid={`members-list-${scope.kind}`}>
      {toast ? (
        <div
          data-testid={toast.kind === 'ok' ? 'members-toast-ok' : 'members-toast-err'}
          className={`mb-2 rounded-md border px-3 py-2 text-sm ${
            toast.kind === 'ok'
              ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
              : 'border-red-500/40 bg-red-500/10 text-red-300'
          }`}
        >
          {toast.msg}
        </div>
      ) : null}
      <div className="mb-3 flex items-center justify-between">
        <span className="text-xs text-[var(--color-text-dim)]" data-testid="members-count">
          {rows.length} member{rows.length === 1 ? '' : 's'}
        </span>
        <button
          type="button"
          data-testid="members-add"
          onClick={() => setAddOpen(true)}
          className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs font-medium text-white hover:opacity-90"
        >
          + Add Member
        </button>
      </div>

      <div className="overflow-x-auto rounded-md border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-[var(--color-border)] bg-[var(--color-bg-2)] text-left text-xs uppercase text-[var(--color-text-dim)]">
              <th className="px-3 py-2">User</th>
              <th className="px-3 py-2">Tier</th>
              <th className="px-3 py-2">Scopes</th>
              <th className="px-3 py-2">Source</th>
              <th className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {matrixQ.isLoading && rows.length === 0 ? (
              <tr>
                <td colSpan={5} data-testid="members-loading" className="px-3 py-4 text-center text-xs text-[var(--color-text-dim)]">
                  Loading members…
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={5} data-testid="members-empty" className="px-3 py-4 text-center text-xs text-[var(--color-text-dim)]">
                  No members yet for this {scope.kind}. Click "Add Member" to grant access.
                </td>
              </tr>
            ) : (
              rows.map(({ user, grant }) => (
                <MemberRow
                  key={user.id}
                  user={user}
                  grant={grant}
                  onTierChange={(tier) => updateTier.mutate({ user, tier })}
                  onRemove={() => removeMember.mutate({ user, userAccessRef: grant.userAccessRef })}
                  busy={updateTier.isPending || removeMember.isPending}
                />
              ))
            )}
          </tbody>
        </table>
      </div>

      {addOpen ? (
        <AddMemberModal
          sovereignId={sovereignId}
          scope={scope}
          onClose={() => setAddOpen(false)}
          onSuccess={(msg) => {
            setToast({ kind: 'ok', msg })
            setAddOpen(false)
            qc.invalidateQueries({
              queryKey: ['rbac-matrix', sovereignId, scope.kind, scope.value],
            })
          }}
        />
      ) : null}
    </div>
  )
}

/* ── Row component ────────────────────────────────────────────────── */

function MemberRow({
  user,
  grant,
  onTierChange,
  onRemove,
  busy,
}: {
  user: AccessMatrixUser
  grant: AccessMatrixGrant
  onTierChange: (tier: RBACTier) => void
  onRemove: () => void
  busy: boolean
}) {
  const [confirmRemove, setConfirmRemove] = useState(false)
  return (
    <tr data-testid={`members-row-${user.id}`} className="border-b border-[var(--color-border)] last:border-b-0">
      <td className="px-3 py-2">
        <span className="font-mono text-[var(--color-text)]">{user.email ?? user.id}</span>
      </td>
      <td className="px-3 py-2">
        <select
          data-testid={`members-tier-${user.id}`}
          value={grant.tier}
          disabled={busy}
          onChange={(e) => onTierChange(e.target.value as RBACTier)}
          className="rounded border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 text-xs font-mono"
        >
          {RBAC_TIERS.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
      </td>
      <td className="px-3 py-2">
        <div className="flex flex-wrap gap-1">
          {(grant.scopes ?? []).length === 0 ? (
            <span className="text-xs text-[var(--color-text-dim)]">— global —</span>
          ) : (
            (grant.scopes ?? []).map((s, i) => (
              <span
                key={i}
                className="inline-flex items-center rounded-full border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-0.5 text-[10px] font-mono"
              >
                {s.key}={s.value}
              </span>
            ))
          )}
        </div>
      </td>
      <td className="px-3 py-2">
        <SourceBadge source={user.source} />
      </td>
      <td className="px-3 py-2 text-right">
        {confirmRemove ? (
          <span className="inline-flex items-center gap-1">
            <button
              type="button"
              data-testid={`members-remove-confirm-${user.id}`}
              onClick={onRemove}
              disabled={busy}
              className="rounded-md bg-red-500/20 px-2 py-1 text-[11px] text-red-300 hover:bg-red-500/30 disabled:opacity-50"
            >
              Confirm
            </button>
            <button
              type="button"
              onClick={() => setConfirmRemove(false)}
              className="rounded-md border border-[var(--color-border)] px-2 py-1 text-[11px] text-[var(--color-text-dim)] hover:bg-[var(--color-bg-2)]"
            >
              Cancel
            </button>
          </span>
        ) : (
          <button
            type="button"
            data-testid={`members-remove-${user.id}`}
            onClick={() => setConfirmRemove(true)}
            disabled={busy}
            className="rounded-md border border-[var(--color-border)] px-2 py-1 text-[11px] text-[var(--color-text-dim)] hover:bg-[var(--color-bg-2)] disabled:opacity-50"
          >
            Remove
          </button>
        )}
      </td>
    </tr>
  )
}

/* ── Add modal ────────────────────────────────────────────────────── */

function AddMemberModal({
  sovereignId,
  scope,
  onClose,
  onSuccess,
}: {
  sovereignId: string
  scope: MembersScope
  onClose: () => void
  onSuccess: (msg: string) => void
}) {
  const [user, setUser] = useState<KCUser | null>(null)
  const [tier, setTier] = useState<RBACTier>('viewer')

  const mutation = useMutation({
    mutationFn: async () => {
      if (!user) throw new Error('Pick a user')
      return rbacAssign(sovereignId, {
        user: { email: user.email, keycloakSubject: user.id },
        tier,
        scope: defaultScopesForScope(scope),
      })
    },
    onSuccess: (resp) => {
      const verb = resp.applied === 'created' ? 'Granted' : resp.applied === 'updated' ? 'Updated' : 'Already granted'
      onSuccess(`${verb} ${tier} to ${user?.email ?? user?.id}`)
    },
  })

  return (
    <div
      data-testid="members-add-modal"
      role="dialog"
      aria-modal="true"
      aria-label="Add Member"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="w-full max-w-md rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-4 shadow-xl">
        <h3 className="mb-3 text-sm font-semibold text-[var(--color-text-strong)]">
          Add Member —{' '}
          <code className="font-mono text-[var(--color-text)]">{scope.value}</code>
        </h3>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          Pick a Keycloak user and a tier. The grant will scope to{' '}
          <code className="font-mono">openova.io/{scope.kind === 'application' ? 'application' : 'org'}={scope.value}</code>
          {TIER_AUTO_INJECTED_SCOPES[tier]?.length
            ? ` (controller will auto-inject ${TIER_AUTO_INJECTED_SCOPES[tier]!
                .map((s) => `${s.key}=${s.value}`)
                .join(', ')})`
            : ''}
          .
        </p>
        <div className="mb-3">
          <label className="mb-1 block text-xs uppercase text-[var(--color-text-dim)]">User</label>
          <KCUserPicker sovereignId={sovereignId} onSelect={(u) => setUser(u)} />
          {user ? (
            <p data-testid="members-add-user" className="mt-1 text-xs text-[var(--color-text-dim)]">
              Selected: <code className="font-mono">{user.email ?? user.id}</code>
            </p>
          ) : null}
        </div>
        <div className="mb-4">
          <label className="mb-1 block text-xs uppercase text-[var(--color-text-dim)]">Tier</label>
          <select
            data-testid="members-add-tier"
            value={tier}
            onChange={(e) => setTier(e.target.value as RBACTier)}
            className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs font-mono"
          >
            {RBAC_TIERS.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        {mutation.isError ? (
          <p data-testid="members-add-err" className="mb-2 text-xs text-red-300">
            {(mutation.error as Error).message}
          </p>
        ) : null}
        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            data-testid="members-add-cancel"
            onClick={onClose}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:bg-[var(--color-bg-2)]"
          >
            Cancel
          </button>
          <button
            type="button"
            data-testid="members-add-apply"
            onClick={() => mutation.mutate()}
            disabled={!user || mutation.isPending}
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50"
          >
            {mutation.isPending ? 'Granting…' : 'Grant'}
          </button>
        </div>
      </div>
    </div>
  )
}

/* ── Local component-only helpers ─────────────────────────────────── */

function SourceBadge({ source }: { source: string }) {
  if (source === 'azure_ad_federated') {
    return (
      <span className="rounded-full border border-blue-500/40 bg-blue-500/10 px-2 py-0.5 text-[10px] uppercase text-blue-300">
        azure-ad
      </span>
    )
  }
  return (
    <span className="rounded-full border border-[var(--color-border)] px-2 py-0.5 text-[10px] uppercase text-[var(--color-text-dim)]">
      {source || 'keycloak'}
    </span>
  )
}
