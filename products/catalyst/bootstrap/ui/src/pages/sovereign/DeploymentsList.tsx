/**
 * DeploymentsList — operator's history of every deployment they own,
 * minus the adopted ones (those live on the customer Sovereign now).
 *
 * Reachable from:
 *   - /sovereign/wizard  → header "View previous deployments" link
 *     (rendered by the wizard banner when the operator has at least
 *     one historical row).
 *   - /sovereign/deployments  (this route).
 *
 * Issue #178 adds two operator features:
 *
 *   - Sortable columns: click any column header to sort the table by
 *     that field. Second click reverses the direction. The default
 *     sort is "Started DESC" so the newest deployment appears at the
 *     top. Sort is purely client-side (the list is small enough that
 *     adding ?sort=&order= query params would just add round-trip cost
 *     without operator benefit).
 *
 *   - Per-row Delete with a two-mode confirmation modal:
 *       1. Delete record only — drops the catalyst-api row but leaves
 *          the Hetzner Sovereign running.
 *       2. Delete record AND wipe Sovereign — chains POST /wipe (the
 *          existing destructive purge) followed by an implicit record
 *          delete on the wipe handler's way out.
 *     Destructive mode requires typing the FQDN to confirm (same
 *     safety pattern WipeDeploymentModal uses).
 *
 * Each row's FQDN cell still links to /sovereign/provision/<id> which
 * renders the canonical Sovereign-Admin shell for both live and
 * terminated deployments (issue #689 reuses the same surface).
 */

import { useEffect, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useSession } from '@/shared/lib/useSession'
import { useInflightDeployment, INFLIGHT_STATUSES } from '@/shared/lib/useInflightDeployment'
import type { DeploymentListEntry } from '@/shared/lib/useInflightDeployment'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { DeleteDeploymentModal } from '@/components/CrudModals'

function formatDate(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

function statusVariant(status: string): 'live' | 'done' | 'failed' {
  if (INFLIGHT_STATUSES.has(status)) return 'live'
  if (status === 'failed' || status === 'wiped') return 'failed'
  return 'done'
}

type SortKey =
  | 'sovereignFQDN'
  | 'status'
  | 'startedAt'
  | 'finishedAt'
  | 'region'
type SortOrder = 'asc' | 'desc'

const SORT_LABELS: Record<SortKey, string> = {
  sovereignFQDN: 'FQDN',
  status: 'Status',
  startedAt: 'Started',
  finishedAt: 'Finished',
  region: 'Region',
}

/**
 * Comparator factory — empty / undefined sort lowest in ASC.
 * Dates are parsed once per key, strings compared case-insensitively.
 */
function makeComparator(key: SortKey, order: SortOrder) {
  const dir = order === 'asc' ? 1 : -1
  return (a: DeploymentListEntry, b: DeploymentListEntry): number => {
    const av = a[key]
    const bv = b[key]
    // Treat undefined / empty as "lowest" — they pile up at the bottom
    // in ASC and the top in DESC, which matches operator intuition
    // ("show me rows with real values first").
    if (!av && !bv) return 0
    if (!av) return 1
    if (!bv) return -1
    if (key === 'startedAt' || key === 'finishedAt') {
      const ta = Date.parse(av)
      const tb = Date.parse(bv)
      if (Number.isNaN(ta) && Number.isNaN(tb)) return 0
      if (Number.isNaN(ta)) return 1
      if (Number.isNaN(tb)) return -1
      return (ta - tb) * dir
    }
    return av.toString().toLowerCase().localeCompare(bv.toString().toLowerCase()) * dir
  }
}

interface SortHeaderProps {
  label: string
  sortKey: SortKey
  active: SortKey
  order: SortOrder
  onSort: (k: SortKey) => void
}
function SortHeader({ label, sortKey, active, order, onSort }: SortHeaderProps) {
  const isActive = active === sortKey
  const arrow = isActive ? (order === 'asc' ? ' ▲' : ' ▼') : ''
  return (
    <th
      data-testid={`deployments-list-sort-${sortKey}`}
      data-sort-active={isActive ? 'true' : 'false'}
      data-sort-order={isActive ? order : ''}
      onClick={() => onSort(sortKey)}
      style={{
        padding: '8px 12px',
        borderBottom: '1px solid var(--wiz-border)',
        cursor: 'pointer',
        userSelect: 'none',
      }}
    >
      {label}
      <span aria-hidden style={{ opacity: isActive ? 1 : 0.3 }}>{arrow || ' ↕'}</span>
    </th>
  )
}

export function DeploymentsList() {
  const session = useSession()
  const queryClient = useQueryClient()
  const { all, loading, isError } = useInflightDeployment({
    ownerEmail: session.email,
    enabled: !session.loading,
  })

  const [sortKey, setSortKey] = useState<SortKey>('startedAt')
  const [sortOrder, setSortOrder] = useState<SortOrder>('desc')
  const [deleteTarget, setDeleteTarget] = useState<DeploymentListEntry | null>(null)

  const sorted = useMemo(() => {
    const copy = [...all]
    copy.sort(makeComparator(sortKey, sortOrder))
    return copy
  }, [all, sortKey, sortOrder])

  function onSort(key: SortKey) {
    if (key === sortKey) {
      setSortOrder((prev) => (prev === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(key)
      // Default: dates DESC (newest-first), strings ASC.
      setSortOrder(key === 'startedAt' || key === 'finishedAt' ? 'desc' : 'asc')
    }
  }

  // Refs #2544 (founder bug 2026-05-27): operator session times out
  // (default 4h per auth/session.go ClearSessionCookie path) → stale
  // tab returns to "You must sign in" placeholder text + manual click.
  // Auto-redirect to the sign-in flow when the session expires while the
  // page is mounted, preserving the deployments-list path as the return
  // target via ?next= — the canonical pattern used everywhere else
  // (InstallPage etc.). Founder bug 2026-06-07: the prior
  // `/oauth/initiate?return_to=` target 404'd — no backend route ever
  // served it — so the operator hit a bare "404 page not found" instead
  // of a login prompt. `/login?next=` is the real, routed sign-in entry
  // that prompts login then returns the operator to where they were.
  useEffect(() => {
    if (session.loading) return
    if (!session.signedIn) {
      const next = encodeURIComponent(window.location.pathname + window.location.search)
      window.location.replace(`/login?next=${next}`)
    }
  }, [session.loading, session.signedIn])

  if (session.loading || loading) {
    return (
      <div data-testid="deployments-list-loading" style={{ padding: 24 }}>
        Loading deployments…
      </div>
    )
  }

  if (!session.signedIn) {
    // useEffect above triggers OIDC redirect; this placeholder renders
    // for the single tick before navigation fires. Per Principle 9 the
    // text stays as defense-in-depth — if the redirect URL ever
    // returns a 404 (handler off), the operator still has a click path.
    return (
      <div data-testid="deployments-list-anon" style={{ padding: 24 }}>
        <h1>Your deployments</h1>
        <p>
          Session expired — refreshing sign-in…{' '}
          <Link to="/login">click to sign in manually</Link> if the redirect doesn&rsquo;t happen.
        </p>
      </div>
    )
  }

  if (isError) {
    return (
      <div data-testid="deployments-list-error" style={{ padding: 24 }}>
        <h1>Your deployments</h1>
        <p>Failed to load — try refreshing the page.</p>
      </div>
    )
  }

  return (
    <div data-testid="deployments-list" style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <header style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 24 }}>
        <h1 style={{ margin: 0, fontSize: 22, fontWeight: 700 }}>Your deployments</h1>
        <Link
          to="/wizard"
          data-testid="deployments-list-new-deployment"
          style={{
            marginLeft: 'auto',
            padding: '6px 14px',
            borderRadius: 7,
            background: 'rgba(56,189,248,0.15)',
            color: '#38bdf8',
            fontSize: 13,
            fontWeight: 600,
            textDecoration: 'none',
            border: '1px solid rgba(56,189,248,0.35)',
          }}
        >
          + New deployment
        </Link>
      </header>

      {sorted.length === 0 ? (
        <p data-testid="deployments-list-empty">
          You don&rsquo;t have any deployments yet.{' '}
          <Link to="/wizard">Start the wizard</Link> to provision your first
          Sovereign.
        </p>
      ) : (
        <table
          data-testid="deployments-list-table"
          data-sort-key={sortKey}
          data-sort-order={sortOrder}
          style={{
            width: '100%',
            borderCollapse: 'collapse',
            fontSize: 13,
          }}
        >
          <thead>
            <tr style={{ textAlign: 'left', color: 'var(--wiz-text-sub)' }}>
              <SortHeader label={SORT_LABELS.sovereignFQDN} sortKey="sovereignFQDN" active={sortKey} order={sortOrder} onSort={onSort} />
              <SortHeader label={SORT_LABELS.status} sortKey="status" active={sortKey} order={sortOrder} onSort={onSort} />
              <SortHeader label={SORT_LABELS.startedAt} sortKey="startedAt" active={sortKey} order={sortOrder} onSort={onSort} />
              <SortHeader label={SORT_LABELS.finishedAt} sortKey="finishedAt" active={sortKey} order={sortOrder} onSort={onSort} />
              <SortHeader label={SORT_LABELS.region} sortKey="region" active={sortKey} order={sortOrder} onSort={onSort} />
              <th style={{ padding: '8px 12px', borderBottom: '1px solid var(--wiz-border)', width: 90 }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {sorted.map((d: DeploymentListEntry) => {
              const variant = statusVariant(d.status)
              const colour =
                variant === 'live' ? '#38bdf8' : variant === 'failed' ? '#f87171' : '#10b981'
              return (
                <tr
                  key={d.id}
                  data-testid={`deployments-list-row-${d.id}`}
                >
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    <Link
                      to={
                        (DETECTED_MODE.mode === 'sovereign'
                          ? `/dashboard`
                          : `/provision/${d.id}/dashboard`) as never
                      }
                      style={{ color: 'var(--wiz-text-hi)', textDecoration: 'none', fontWeight: 600 }}
                    >
                      {d.sovereignFQDN || d.id}
                    </Link>
                  </td>
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    <span
                      style={{
                        display: 'inline-block',
                        padding: '2px 8px',
                        borderRadius: 999,
                        background: `${colour}22`,
                        color: colour,
                        fontWeight: 600,
                        fontSize: 11,
                        textTransform: 'uppercase',
                        letterSpacing: 0.4,
                      }}
                    >
                      {d.status}
                    </span>
                  </td>
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    {formatDate(d.startedAt)}
                  </td>
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    {formatDate(d.finishedAt)}
                  </td>
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    {d.region || '—'}
                  </td>
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    <button
                      type="button"
                      data-testid={`deployments-list-delete-${d.id}`}
                      onClick={() => setDeleteTarget(d)}
                      // In-flight deployments can't be deleted (the API
                      // refuses 409); reflect that in the button.
                      disabled={INFLIGHT_STATUSES.has(d.status)}
                      title={
                        INFLIGHT_STATUSES.has(d.status)
                          ? 'Wait for terminal state before deleting'
                          : 'Delete deployment'
                      }
                      style={{
                        padding: '4px 10px',
                        fontSize: 11,
                        fontWeight: 600,
                        borderRadius: 5,
                        border: '1px solid rgba(248,113,113,0.4)',
                        background: 'rgba(248,113,113,0.1)',
                        color: '#f87171',
                        cursor: INFLIGHT_STATUSES.has(d.status) ? 'not-allowed' : 'pointer',
                        opacity: INFLIGHT_STATUSES.has(d.status) ? 0.5 : 1,
                      }}
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}

      {deleteTarget ? (
        <DeleteDeploymentModal
          open
          deploymentId={deleteTarget.id}
          sovereignFQDN={deleteTarget.sovereignFQDN ?? null}
          onClose={() => setDeleteTarget(null)}
          onDeleted={() => {
            setDeleteTarget(null)
            // Invalidate every cached "deployments list" query so the
            // table re-fetches without the just-deleted row.
            queryClient.invalidateQueries({ queryKey: ['catalyst', 'deployments', 'list'] })
          }}
        />
      ) : null}
    </div>
  )
}
