/**
 * FailbackPanel — EPIC-6 Slice U-DR-1 (#1101).
 *
 * Failback handler UI shown WHEN
 * `Continuum.status.lastSwitchover.result == 'Success'`. Surfaces:
 *
 *   - "Failback to Primary" button (owner tier on Application)
 *   - Confirm dialog → POST /continuums/{name}/failback
 *   - When `spec.failback.approvalRequired = true`, an "Awaiting
 *     approval" pill appears with an Approve button (sovereign-admin
 *     only — gate enforced server-side; UI hides the button when the
 *     caller doesn't claim admin/owner)
 *   - On approve → POST /continuums/{name}/failback/approve
 *
 * The "owner-only" + "sovereign-admin-only" decisions are evaluated
 * client-side as a UX convenience (show / hide); the server is the
 * single source of truth for authorization.
 */

import { useState } from 'react'

import { approveFailback, requestFailback } from '@/lib/continuum.api'

export interface FailbackPanelProps {
  sovereignId: string
  continuumName: string
  namespace?: string
  /** True when the caller's tier permits the failback request. */
  isOwner: boolean
  /** True when the caller's tier permits approving the failback. */
  isSovereignAdmin: boolean
  /** Whether spec.failback.approvalRequired is set. */
  approvalRequired: boolean
  /** Whether spec.failback.requested has already been set. */
  failbackRequested: boolean
  /** Whether spec.failback.approved has been set. */
  failbackApproved: boolean
  /** Test seam — bypass the network call. */
  disableNetwork?: boolean
  /** Fired after a successful POST so the parent can refetch. */
  onChanged?: () => void
}

export function FailbackPanel({
  sovereignId,
  continuumName,
  namespace,
  isOwner,
  isSovereignAdmin,
  approvalRequired,
  failbackRequested,
  failbackApproved,
  disableNetwork = false,
  onChanged,
}: FailbackPanelProps) {
  const [busy, setBusy] = useState<'request' | 'approve' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [info, setInfo] = useState<string | null>(null)

  const onRequest = async () => {
    setError(null)
    setInfo(null)
    if (disableNetwork) {
      setInfo('Failback requested (test seam).')
      onChanged?.()
      return
    }
    setBusy('request')
    try {
      const resp = await requestFailback(sovereignId, continuumName, {}, { namespace })
      setInfo(resp.message)
      onChanged?.()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  const onApprove = async () => {
    setError(null)
    setInfo(null)
    if (disableNetwork) {
      setInfo('Failback approved (test seam).')
      onChanged?.()
      return
    }
    setBusy('approve')
    try {
      const resp = await approveFailback(sovereignId, continuumName, { namespace })
      setInfo(resp.message)
      onChanged?.()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(null)
    }
  }

  return (
    <div
      className="continuum-failback rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
      data-testid="continuum-failback-panel"
    >
      <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
        Failback
      </h4>
      <p className="mb-2 text-xs text-[var(--color-text-dim)]">
        Returns the primary role to the original region after a switchover.
      </p>

      {!failbackRequested ? (
        isOwner ? (
          <button
            type="button"
            data-testid="continuum-failback-request-btn"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
            onClick={onRequest}
            disabled={busy !== null}
          >
            {busy === 'request' ? 'Requesting…' : 'Failback to Primary'}
          </button>
        ) : (
          <div
            data-testid="continuum-failback-request-disabled"
            className="text-xs text-[var(--color-text-dim)]"
          >
            Owner tier required to request failback.
          </div>
        )
      ) : approvalRequired && !failbackApproved ? (
        <div
          data-testid="continuum-failback-pending"
          className="rounded-md border border-yellow-500/40 bg-yellow-500/10 p-2 text-xs text-yellow-300"
        >
          <div className="mb-2 font-semibold">Awaiting sovereign-admin approval</div>
          {isSovereignAdmin ? (
            <button
              type="button"
              data-testid="continuum-failback-approve-btn"
              className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
              onClick={onApprove}
              disabled={busy !== null}
            >
              {busy === 'approve' ? 'Approving…' : 'Approve'}
            </button>
          ) : (
            <div data-testid="continuum-failback-approve-hidden">
              Approval requires sovereign-admin (admin or owner tier).
            </div>
          )}
        </div>
      ) : (
        <div
          data-testid="continuum-failback-in-progress"
          className="text-xs text-[var(--color-text-dim)]"
        >
          Failback requested; reconciler is executing the reverse switchover.
        </div>
      )}

      {error ? (
        <div
          data-testid="continuum-failback-error"
          className="mt-2 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
        >
          {error}
        </div>
      ) : null}
      {info ? (
        <div
          data-testid="continuum-failback-info"
          className="mt-2 rounded-md border border-green-500/40 bg-green-500/10 px-3 py-2 text-xs text-green-400"
        >
          {info}
        </div>
      ) : null}
    </div>
  )
}
