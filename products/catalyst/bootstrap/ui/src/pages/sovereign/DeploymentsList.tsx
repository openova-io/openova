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
 * The page is intentionally minimal — a single table with the columns
 * the operator needs to reopen a previous run (id, FQDN, status, dates,
 * error preview). Each row links to /sovereign/provision/<id> which
 * already renders the canonical Sovereign-Admin shell for both live
 * and terminated deployments (issue #689 reuses the same surface).
 *
 * No filters, no search, no per-row actions: anything beyond
 * "go look at this deployment" belongs on the deployment page itself.
 */

import { Link } from '@tanstack/react-router'
import { useSession } from '@/shared/lib/useSession'
import { useInflightDeployment, INFLIGHT_STATUSES } from '@/shared/lib/useInflightDeployment'
import type { DeploymentListEntry } from '@/shared/lib/useInflightDeployment'

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

export function DeploymentsList() {
  const session = useSession()
  const { all, loading, isError } = useInflightDeployment({
    ownerEmail: session.email,
    enabled: !session.loading,
  })

  if (session.loading || loading) {
    return (
      <div data-testid="deployments-list-loading" style={{ padding: 24 }}>
        Loading deployments…
      </div>
    )
  }

  if (!session.signedIn) {
    return (
      <div data-testid="deployments-list-anon" style={{ padding: 24 }}>
        <h1>Your deployments</h1>
        <p>
          You must <Link to="/login">sign in</Link> to view your deployments.
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

      {all.length === 0 ? (
        <p data-testid="deployments-list-empty">
          You don&rsquo;t have any deployments yet.{' '}
          <Link to="/wizard">Start the wizard</Link> to provision your first
          Sovereign.
        </p>
      ) : (
        <table
          data-testid="deployments-list-table"
          style={{
            width: '100%',
            borderCollapse: 'collapse',
            fontSize: 13,
          }}
        >
          <thead>
            <tr style={{ textAlign: 'left', color: 'var(--wiz-text-sub)' }}>
              <th style={{ padding: '8px 12px', borderBottom: '1px solid var(--wiz-border)' }}>FQDN</th>
              <th style={{ padding: '8px 12px', borderBottom: '1px solid var(--wiz-border)' }}>Status</th>
              <th style={{ padding: '8px 12px', borderBottom: '1px solid var(--wiz-border)' }}>Started</th>
              <th style={{ padding: '8px 12px', borderBottom: '1px solid var(--wiz-border)' }}>Finished</th>
              <th style={{ padding: '8px 12px', borderBottom: '1px solid var(--wiz-border)' }}>Region</th>
            </tr>
          </thead>
          <tbody>
            {all.map((d: DeploymentListEntry) => {
              const variant = statusVariant(d.status)
              const colour =
                variant === 'live' ? '#38bdf8' : variant === 'failed' ? '#f87171' : '#10b981'
              return (
                <tr
                  key={d.id}
                  data-testid={`deployments-list-row-${d.id}`}
                  style={{ cursor: 'pointer' }}
                >
                  <td style={{ padding: '10px 12px', borderBottom: '1px solid var(--wiz-border)' }}>
                    <Link
                      to={`/dashboard` as never}
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
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </div>
  )
}
