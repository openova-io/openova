/**
 * SwitchoverDialog — EPIC-6 Slice U-DR-1 (#1101).
 *
 * Confirm dialog the operator clicks through before the catalyst-api
 * patches the Continuum CR's `spec.switchover.requested = true`. Shows:
 *
 *   - The diff: "Primary will move from <fromRegion> → <toRegion>"
 *   - The 7-step list from K-Cont-2's Sequencer (SWITCHOVER_STEPS)
 *   - Estimated duration <60s, write disruption <5s
 *   - Cancel / Confirm buttons
 *
 * On Confirm → POST /api/v1/sovereigns/{id}/continuums/{name}/switchover
 * (via continuum.api.requestSwitchover). Returns 202 Accepted; the
 * K-Cont-2 reconciler picks up the patch on its next loop iteration
 * and emits the 7-step audit events on NATS.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #5 the underlying handler enforces
 * owner tier on the Application server-side. The widget renders
 * regardless of tier; the parent (DRSection) hides the entry-point
 * button when the caller lacks owner. On a 403 from the API the dialog
 * surfaces the error inline.
 *
 * Disable-network seam mirrors TopologyEditor — tests render the
 * dialog without a real fetch.
 */

import { useState } from 'react'

import {
  requestSwitchover,
  SWITCHOVER_STEPS,
  type ContinuumSwitchoverResponse,
} from '@/lib/continuum.api'

export interface SwitchoverDialogProps {
  /** Sovereign id (deploymentId on chroot, mother). */
  sovereignId: string
  /** Continuum CR name. */
  continuumName: string
  /** Org namespace (optional; falls back to handler-side cross-namespace lookup). */
  namespace?: string
  /** Current primary region (for the diff). */
  fromRegion: string
  /** Switchover target — typically the first hot-standby region. */
  toRegion: string
  /** Application name surfaced in the dialog header for context. */
  applicationName: string
  /** Fired when the operator dismisses the dialog (cancel or after success). */
  onClose: () => void
  /** Fired on a successful POST. */
  onConfirmed?: (resp: ContinuumSwitchoverResponse) => void
  /** Test seam — bypass the network call. */
  disableNetwork?: boolean
}

export function SwitchoverDialog({
  sovereignId,
  continuumName,
  namespace,
  fromRegion,
  toRegion,
  applicationName,
  onClose,
  onConfirmed,
  disableNetwork = false,
}: SwitchoverDialogProps) {
  const [reason, setReason] = useState<string>('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const onConfirm = async () => {
    setError(null)
    if (disableNetwork) {
      const stub: ContinuumSwitchoverResponse = {
        name: continuumName,
        namespace: namespace ?? '',
        targetRegion: toRegion,
        reason,
        requestedAt: new Date().toISOString(),
        requestedBy: 'test-seam',
        message: 'switchover requested (test seam)',
      }
      onConfirmed?.(stub)
      onClose()
      return
    }
    setBusy(true)
    try {
      const resp = await requestSwitchover(
        sovereignId,
        continuumName,
        { targetRegion: toRegion, reason: reason.trim() || undefined },
        { namespace },
      )
      // The handler returns HTTP 200 even on failure/no-op (the body
      // carries the truth). A 200 alone is NOT success — surface the error
      // and keep the dialog open when the switchover was not applied (e.g.
      // "no-live-dr-pair": the app isn't placed active-hot-standby on a
      // 2-region Sovereign yet). Only close + invalidate on a real apply.
      if (resp.error || resp.applied === false) {
        setError(resp.message || resp.error || 'switchover was not applied')
        return
      }
      onConfirmed?.(resp)
      onClose()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      data-testid="continuum-switchover-dialog"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
    >
      <div className="max-h-[85vh] w-full max-w-2xl overflow-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-5">
        <div className="mb-3 flex items-baseline justify-between">
          <h3 className="text-base font-semibold text-[var(--color-text)]">
            Switchover — {applicationName}
          </h3>
          <button
            type="button"
            data-testid="continuum-switchover-dialog-close"
            className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
            onClick={onClose}
          >
            Close
          </button>
        </div>

        <div
          className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-sm"
          data-testid="continuum-switchover-dialog-diff"
        >
          <p className="font-medium text-[var(--color-text)]">
            Primary will move{' '}
            <code className="font-mono text-[var(--color-accent)]">
              {fromRegion || 'the current primary'}
            </code>
            {' → '}
            <code className="font-mono text-[var(--color-accent)]">
              {toRegion || 'the standby region'}
            </code>
          </p>
          {!toRegion ? (
            <p className="mt-1 text-xs text-[var(--color-text-dim)]">
              The standby region is resolved automatically from the declared
              hot-standby placement.
            </p>
          ) : null}
        </div>

        <div className="mb-4">
          <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
            What happens (7 steps)
          </h4>
          <ol
            data-testid="continuum-switchover-dialog-steps"
            className="space-y-1.5 text-xs text-[var(--color-text)]"
          >
            {SWITCHOVER_STEPS.map((s) => (
              <li
                key={s.id}
                data-testid={`continuum-switchover-dialog-step-${s.id}`}
                className="grid grid-cols-[1.25rem_8rem_1fr] items-baseline gap-2"
              >
                <span className="font-mono text-[var(--color-text-dim)]">{s.id}.</span>
                <code className="font-mono text-[var(--color-accent)]">{s.name}</code>
                <span className="text-[var(--color-text-dim)]">{s.description}</span>
              </li>
            ))}
          </ol>
        </div>

        <div
          className="mb-4 grid grid-cols-2 gap-3 rounded-md border border-yellow-500/40 bg-yellow-500/10 px-3 py-2 text-xs text-yellow-300"
          data-testid="continuum-switchover-dialog-estimates"
        >
          <div>
            <strong>Estimated duration</strong>: &lt;60s (happy path)
          </div>
          <div>
            <strong>Write disruption</strong>: &lt;5s
          </div>
        </div>

        <label className="mb-3 block text-xs">
          <span className="text-[var(--color-text-dim)]">
            Reason (optional, surfaced on the audit row)
          </span>
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="e.g. fsn-hetzner-az-degraded"
            data-testid="continuum-switchover-dialog-reason"
            className="mt-1 w-full rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 font-mono text-xs text-[var(--color-text)] focus:border-[var(--color-accent)] focus:outline-none"
          />
        </label>

        {error ? (
          <div
            className="mb-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400"
            data-testid="continuum-switchover-dialog-error"
          >
            {error}
          </div>
        ) : null}

        <div className="flex justify-end gap-2">
          <button
            type="button"
            data-testid="continuum-switchover-dialog-cancel"
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs hover:border-[var(--color-accent)]"
            onClick={onClose}
            disabled={busy}
          >
            Cancel
          </button>
          <button
            type="button"
            data-testid="continuum-switchover-dialog-confirm"
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:opacity-40"
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? 'Confirming…' : 'Confirm Switchover'}
          </button>
        </div>
      </div>
    </div>
  )
}
