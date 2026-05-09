/**
 * AccessMatrixPage — Manara-style users × applications × tier grid
 * (EPIC-3 #1098 slice U7).
 *
 * Route: `/admin/rbac/matrix` (mothership) + `/rbac/matrix` (chroot).
 *
 * Source: A2's GET /api/v1/sovereigns/{id}/rbac/access-matrix.
 *
 * Per row: one user. Per column: one Application name (plus the
 * synthetic `*` global column when any user has a global grant).
 * Per cell: tier name, color-coded, with a warning icon when the CR
 * fails the documented Manara contract (per A2's `warnings[]`).
 *
 * Click a cell → opens a modal that mounts MultiGrantEditPage pre-
 * filled with the user × application combo, so the operator can edit
 * the grant inline without leaving the matrix.
 *
 * Filters: org dropdown + application dropdown — both wire to A2's
 * query string.
 *
 * Per INVIOLABLE-PRINCIPLES #5 the page requires tier-admin or higher
 * (the API's gate is the source of truth; this page surfaces the 403
 * as an error toast).
 */

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import {
  RBAC_TIERS,
  getAccessMatrix,
  type AccessMatrixGrant,
  type AccessMatrixResponse,
  type AccessMatrixUser,
  type RBACTier,
} from './rbac.api'
import { tierColors, tierLabel } from './roleHelpers'

export interface AccessMatrixPageProps {
  /** Test seam — pre-fill deployment id without TanStack Router. */
  initialDeploymentId?: string
  /** Test seam — pre-fill matrix response without network. */
  initialMatrix?: AccessMatrixResponse
  /** Test seam — auto-open the cell editor for a (user, app) pair. */
  forceOpenEditor?: { userId: string; application: string }
}

export function AccessMatrixPage({
  initialDeploymentId,
  initialMatrix,
  forceOpenEditor,
}: AccessMatrixPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = initialDeploymentId ?? resolvedId ?? ''
  const [orgFilter, setOrgFilter] = useState('')
  const [appFilter, setAppFilter] = useState('')
  const [editor, setEditor] = useState<{ userId: string; application: string } | null>(
    forceOpenEditor ?? null,
  )

  const matrixQ = useQuery({
    queryKey: ['rbac-matrix', deploymentId, 'page', orgFilter, appFilter],
    queryFn: () =>
      getAccessMatrix(deploymentId, {
        org: orgFilter || undefined,
        application: appFilter || undefined,
      }),
    enabled: !!deploymentId && !initialMatrix,
    staleTime: 15_000,
  })

  const matrix: AccessMatrixResponse = useMemo(
    () =>
      initialMatrix ??
      matrixQ.data ?? {
        users: [],
        applications: [],
        tiers: RBAC_TIERS as unknown as RBACTier[],
      },
    [initialMatrix, matrixQ.data],
  )

  const orgChoices = useMemo(() => extractOrgs(matrix), [matrix])

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Access matrix">
      <div data-testid="access-matrix-page" className="mx-auto max-w-7xl px-6 py-4">
        <div className="mb-4 flex items-center justify-between">
          <div>
            <h1 className="text-base font-semibold text-[var(--color-text-strong)]">Access matrix</h1>
            <p className="text-xs text-[var(--color-text-dim)]">
              Users × applications × tier — sourced from UserAccess CRs. Click a cell to edit.
            </p>
          </div>
          <div className="flex gap-2">
            <select
              data-testid="matrix-filter-org"
              value={orgFilter}
              onChange={(e) => setOrgFilter(e.target.value)}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs"
            >
              <option value="">All organizations</option>
              {orgChoices.map((o) => (
                <option key={o} value={o}>
                  org={o}
                </option>
              ))}
            </select>
            <select
              data-testid="matrix-filter-app"
              value={appFilter}
              onChange={(e) => setAppFilter(e.target.value)}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs"
            >
              <option value="">All applications</option>
              {matrix.applications
                .filter((a) => a !== '*')
                .map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
            </select>
          </div>
        </div>

        {matrixQ.isError ? (
          <div data-testid="matrix-err" className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 p-3 text-xs text-red-300">
            {(matrixQ.error as Error).message}
          </div>
        ) : null}

        <div className="overflow-x-auto rounded-md border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-[var(--color-border)] bg-[var(--color-bg-2)] text-left text-xs uppercase text-[var(--color-text-dim)]">
                <th className="sticky left-0 bg-[var(--color-bg-2)] px-3 py-2">User</th>
                {matrix.applications.map((app) => (
                  <th key={app} className="px-3 py-2 text-center" data-testid={`matrix-col-${app}`}>
                    <code className="font-mono">{app === '*' ? 'global' : app}</code>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {matrixQ.isLoading && matrix.users.length === 0 ? (
                <tr>
                  <td
                    colSpan={Math.max(matrix.applications.length + 1, 2)}
                    data-testid="matrix-loading"
                    className="px-3 py-6 text-center text-xs text-[var(--color-text-dim)]"
                  >
                    Loading…
                  </td>
                </tr>
              ) : matrix.users.length === 0 ? (
                <tr>
                  <td
                    colSpan={Math.max(matrix.applications.length + 1, 2)}
                    data-testid="matrix-empty"
                    className="px-3 py-6 text-center text-xs text-[var(--color-text-dim)]"
                  >
                    No grants in this Sovereign. Use the multi-grant editor to grant one.
                  </td>
                </tr>
              ) : (
                matrix.users.map((u) => (
                  <UserRow
                    key={u.id}
                    user={u}
                    applications={matrix.applications}
                    onCellClick={(app) => setEditor({ userId: u.id, application: app })}
                  />
                ))
              )}
            </tbody>
          </table>
        </div>

        {editor ? (
          <CellEditorModal
            sovereignId={deploymentId}
            userId={editor.userId}
            application={editor.application}
            user={matrix.users.find((u) => u.id === editor.userId)}
            onClose={() => setEditor(null)}
          />
        ) : null}
      </div>
    </PortalShell>
  )
}

/* ── Sub-components ───────────────────────────────────────────────── */

function UserRow({
  user,
  applications,
  onCellClick,
}: {
  user: AccessMatrixUser
  applications: string[]
  onCellClick: (app: string) => void
}) {
  return (
    <tr data-testid={`matrix-row-${user.id}`} className="border-b border-[var(--color-border)] last:border-b-0">
      <th className="sticky left-0 bg-[var(--color-bg)] px-3 py-2 text-left">
        <span className="font-mono text-[var(--color-text)]">{user.email ?? user.id}</span>
        {user.warnings && user.warnings.length > 0 ? (
          <span
            className="ml-1 inline-flex items-center text-amber-400"
            title={user.warnings.join('\n')}
            data-testid={`matrix-row-warning-${user.id}`}
          >
            ⚠
          </span>
        ) : null}
      </th>
      {applications.map((app) => {
        const grant = user.access[app]
        const hasWarning = (user.warnings ?? []).some((w) => w.includes(app))
        return (
          <td key={app} className="px-3 py-2 text-center">
            <MatrixCell
              grant={grant}
              hasWarning={hasWarning}
              onClick={() => onCellClick(app)}
              testId={`matrix-cell-${user.id}-${app}`}
            />
          </td>
        )
      })}
    </tr>
  )
}

export function MatrixCell({
  grant,
  hasWarning,
  onClick,
  testId,
}: {
  grant: AccessMatrixGrant | undefined
  hasWarning: boolean
  onClick: () => void
  testId: string
}) {
  if (!grant) {
    return (
      <button
        type="button"
        onClick={onClick}
        data-testid={testId}
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-0.5 text-xs text-[var(--color-text-dim)] hover:border-[var(--color-accent)]"
      >
        —
      </button>
    )
  }
  const palette = tierColors(grant.tier as RBACTier)
  return (
    <button
      type="button"
      onClick={onClick}
      data-testid={testId}
      style={{ background: palette.bg, color: palette.fg, borderColor: palette.border }}
      className="rounded-md border px-2 py-0.5 text-xs font-mono uppercase hover:opacity-90"
    >
      {tierLabel(grant.tier as RBACTier)}
      {hasWarning ? <span className="ml-1 text-amber-300" aria-label="warning">*</span> : null}
    </button>
  )
}

function CellEditorModal({
  sovereignId,
  userId,
  application,
  user,
  onClose,
}: {
  sovereignId: string
  userId: string
  application: string
  user: AccessMatrixUser | undefined
  onClose: () => void
}) {
  return (
    <div
      data-testid="matrix-editor-modal"
      role="dialog"
      aria-modal="true"
      aria-label="Edit grant"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="w-full max-w-2xl rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-4 shadow-xl">
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-[var(--color-text-strong)]">
            Edit grant —{' '}
            <code className="font-mono">{user?.email ?? userId}</code> ×{' '}
            <code className="font-mono">{application}</code>
          </h3>
          <button
            type="button"
            data-testid="matrix-editor-close"
            onClick={onClose}
            aria-label="Close"
            className="rounded-md px-2 py-1 text-xs text-[var(--color-text-dim)] hover:bg-[var(--color-bg-2)]"
          >
            ✕
          </button>
        </div>
        <p className="mb-3 text-xs text-[var(--color-text-dim)]">
          Use the multi-grant editor to change this user's tier or scopes for{' '}
          <code className="font-mono">{application}</code>. Edits land via /rbac/assign — the
          matrix refreshes when the modal closes.
        </p>
        <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3">
          <p className="mb-2 text-xs uppercase text-[var(--color-text-dim)]">Pre-filled</p>
          <ul className="space-y-1 text-xs">
            <li>
              User: <code className="font-mono">{user?.email ?? userId}</code>
            </li>
            <li>
              Application:{' '}
              <code className="font-mono">{application === '*' ? 'global' : application}</code>
            </li>
            <li>
              Source: <code className="font-mono">{user?.source ?? 'keycloak'}</code>
            </li>
          </ul>
        </div>
        <div className="mt-4 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:bg-[var(--color-bg-2)]"
          >
            Close
          </button>
          <a
            href={`/sovereign/provision/${sovereignId}/rbac/grant`}
            data-testid="matrix-editor-open-multigrant"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs font-medium text-white hover:opacity-90"
          >
            Open multi-grant editor
          </a>
        </div>
      </div>
    </div>
  )
}

/* ── Helpers ──────────────────────────────────────────────────────── */

function extractOrgs(matrix: AccessMatrixResponse): string[] {
  const orgs = new Set<string>()
  for (const u of matrix.users) {
    for (const grant of Object.values(u.access)) {
      for (const s of grant.scopes ?? []) {
        if (s.key === 'openova.io/org' || s.key === 'openova.io/organization') {
          orgs.add(s.value)
        }
      }
    }
  }
  return Array.from(orgs).sort()
}
