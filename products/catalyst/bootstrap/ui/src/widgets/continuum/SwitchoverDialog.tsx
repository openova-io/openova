/**
 * SwitchoverDialog — EPIC-6 Slice U-DR-1 (#1101) + armed preflight (#4552).
 *
 * Confirm dialog the operator clicks through before the catalyst-api
 * patches the Continuum CR's `spec.switchover.requested = true`. Shows:
 *
 *   - The diff: "Primary will move from <fromRegion> → <toRegion>"
 *   - A read-only RPO/health PREFLIGHT (#4552): the live WAL replication
 *     lag + every blocking check the switchover would hit. The
 *     [ Confirm Switchover ] button is armed ONLY when the preflight is
 *     promotable (no blocking checks) — a lagging / mid-switchover /
 *     already-primary target keeps Confirm disabled with the reason shown.
 *   - The 7-step list from K-Cont-2's Sequencer (SWITCHOVER_STEPS)
 *   - Estimated duration <60s, write disruption <5s
 *   - Cancel / Confirm buttons
 *
 * On Confirm → POST /api/v1/sovereigns/{id}/continuums/{name}/switchover
 * (via continuum.api.requestSwitchover). The K-Cont-2 reconciler picks up
 * the patch on its next reconcile pass and runs the 7-step
 * cordon-before-promote sequence (or, when no Continuum CR backs the app,
 * catalyst-api drives the proven live cnpg-pair `spec.replica.enabled`
 * flip directly). The handler returns HTTP 200 even on a no-op / failure —
 * the body's `applied` / `error` carry the truth, so the dialog only
 * closes on a real apply and surfaces the reason otherwise.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #5 the underlying handler enforces
 * owner/operator tier on the Application server-side. The widget renders
 * regardless of tier; the parent hides the entry-point button when the
 * caller lacks owner. On a 403 from the API the dialog surfaces the error
 * inline.
 *
 * Disable-network seam mirrors TopologyEditor — tests render the dialog
 * without a real fetch (preflight is skipped and Confirm is armed so the
 * seam stays a pure UI harness).
 */

import { useEffect, useState } from 'react'

import {
  getSwitchoverPreview,
  requestSwitchover,
  SWITCHOVER_STEPS,
  lagBucket,
  type ContinuumSwitchoverPreview,
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
  /**
   * Live WAL replication lag in seconds (from the Topology tab's
   * replication-status poll). Displayed in the preflight header while the
   * authoritative preview resolves; the preview's `currentLagSec` wins once
   * it lands. Optional.
   */
  lagSeconds?: number | null
  /** Live sync/streaming state (e.g. "sync" / "async") — display only. */
  syncState?: string
  /** Fired when the operator dismisses the dialog (cancel or after success). */
  onClose: () => void
  /** Fired on a successful POST. */
  onConfirmed?: (resp: ContinuumSwitchoverResponse) => void
  /** Test seam — bypass the network calls (preflight + confirm). */
  disableNetwork?: boolean
}

export function SwitchoverDialog({
  sovereignId,
  continuumName,
  namespace,
  fromRegion,
  toRegion,
  applicationName,
  lagSeconds,
  syncState,
  onClose,
  onConfirmed,
  disableNetwork = false,
}: SwitchoverDialogProps) {
  const [reason, setReason] = useState<string>('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // ── RPO/health preflight (#4552) ──────────────────────────────────
  // A read-only dry-run against the live Continuum + CNPGPair status. The
  // Confirm button is armed ONLY when `preflight.promotable` is true.
  const [preflight, setPreflight] = useState<ContinuumSwitchoverPreview | null>(null)
  const [preflightLoading, setPreflightLoading] = useState(!disableNetwork)
  const [preflightError, setPreflightError] = useState<string | null>(null)
  // Bump to re-run the preflight after a transient network error.
  const [preflightTick, setPreflightTick] = useState(0)

  // Kick off the preflight on open (and on each retry). State updates happen
  // ONLY in the async callbacks — the effect body itself calls no setState
  // (the "loading" reset lives in the retry click handler / the initial
  // useState), so this stays clear of cascading synchronous renders.
  useEffect(() => {
    if (disableNetwork) return
    let cancelled = false
    getSwitchoverPreview(
      sovereignId,
      continuumName,
      { targetRegion: toRegion || undefined },
      { namespace },
    )
      .then((rep) => {
        if (cancelled) return
        setPreflight(rep)
        setPreflightError(null)
      })
      .catch((e) => {
        if (cancelled) return
        setPreflightError((e as Error).message)
      })
      .finally(() => {
        if (cancelled) return
        setPreflightLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [disableNetwork, sovereignId, continuumName, toRegion, namespace, preflightTick])

  // The live lag: prefer the authoritative preview reading, fall back to the
  // parent-supplied replication-status lag while the preview is in flight.
  const shownLag: number | null =
    preflight != null ? preflight.currentLagSec : lagSeconds ?? null
  const shownLagColor = lagBucket(shownLag)

  // Confirm is armed only when the preflight passed. Under disableNetwork the
  // gate is skipped so the seam stays a pure UI harness.
  const preflightPassed = disableNetwork || (preflight != null && preflight.promotable)
  const confirmDisabled =
    busy || (!disableNetwork && (preflightLoading || !preflightPassed))

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
    // Belt-and-braces: never fire the mutation when the preflight has not
    // cleared (the button is already disabled, but guard the handler too).
    if (!preflightPassed) {
      setError('preflight has not passed — resolve the blocking checks first')
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

        {/* ── RPO/health preflight (#4552) ─────────────────────────────── */}
        {!disableNetwork ? (
          <div
            className="mb-4 rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
            data-testid="continuum-switchover-dialog-preflight"
          >
            <div className="mb-2 flex items-baseline justify-between">
              <h4 className="text-xs font-semibold uppercase tracking-wide text-[var(--color-text-dim)]">
                RPO / health preflight
              </h4>
              {preflightLoading ? (
                <span
                  className="text-[10px] text-[var(--color-text-dim)]"
                  data-testid="continuum-switchover-dialog-preflight-loading"
                >
                  checking…
                </span>
              ) : preflightError ? (
                <span
                  className="rounded bg-red-500/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-red-400"
                  data-testid="continuum-switchover-dialog-preflight-status"
                >
                  ○ check failed
                </span>
              ) : preflight?.promotable ? (
                <span
                  className="rounded bg-green-500/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-green-400"
                  data-testid="continuum-switchover-dialog-preflight-status"
                >
                  ● ready to promote
                </span>
              ) : (
                <span
                  className="rounded bg-yellow-500/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-yellow-400"
                  data-testid="continuum-switchover-dialog-preflight-status"
                >
                  ○ not promotable
                </span>
              )}
            </div>

            {/* Live replication lag (the RPO reading). */}
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
              <span className="flex items-center gap-1.5">
                <span className="text-[var(--color-text-dim)]">Replication lag</span>
                <span
                  className={`inline-flex items-center rounded-md px-2 py-0.5 font-semibold tabular-nums ${
                    shownLagColor === 'green'
                      ? 'bg-green-500/10 text-green-400'
                      : shownLagColor === 'yellow'
                        ? 'bg-yellow-500/10 text-yellow-400'
                        : shownLagColor === 'red'
                          ? 'bg-red-500/10 text-red-400'
                          : 'bg-[var(--color-bg)] text-[var(--color-text-dim)]'
                  }`}
                  data-testid="continuum-switchover-dialog-preflight-lag"
                >
                  {shownLag == null ? '—' : `${shownLag.toFixed(1)} s`}
                </span>
              </span>
              {syncState ? (
                <span className="text-[var(--color-text-dim)]">
                  replication: <span className="text-[var(--color-text)]">{syncState}</span>
                </span>
              ) : null}
              {preflight?.estimatedDuration ? (
                <span className="text-[var(--color-text-dim)]">
                  est. failover:{' '}
                  <span className="text-[var(--color-text)]">{preflight.estimatedDuration}</span>
                </span>
              ) : null}
            </div>

            {/* Blocking checks — Confirm stays disabled while any is present. */}
            {preflightError ? (
              <div
                className="mt-2 flex items-center justify-between gap-2 text-xs text-red-400"
                data-testid="continuum-switchover-dialog-preflight-error"
              >
                <span>Preflight could not run: {preflightError}</span>
                <button
                  type="button"
                  className="rounded-md border border-[var(--color-border)] px-2 py-0.5 text-[10px] hover:border-[var(--color-accent)]"
                  onClick={() => {
                    setPreflight(null)
                    setPreflightError(null)
                    setPreflightLoading(true)
                    setPreflightTick((t) => t + 1)
                  }}
                  data-testid="continuum-switchover-dialog-preflight-retry"
                >
                  Re-run
                </button>
              </div>
            ) : preflight && preflight.blockingChecks.length > 0 ? (
              <ul
                className="mt-2 space-y-1 text-xs text-yellow-300"
                data-testid="continuum-switchover-dialog-preflight-checks"
              >
                {preflight.blockingChecks.map((c, i) => (
                  <li
                    key={i}
                    data-testid={`continuum-switchover-dialog-preflight-check-${i}`}
                    className="flex items-start gap-1.5"
                  >
                    <span aria-hidden>⚠</span>
                    <span>{c}</span>
                  </li>
                ))}
              </ul>
            ) : preflight ? (
              <p className="mt-2 text-xs text-green-400">
                All preflight checks passed — the standby is caught up and
                promotable.
              </p>
            ) : null}
          </div>
        ) : null}

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
            className="rounded-md bg-[var(--color-accent)] px-3 py-1.5 text-xs text-[var(--color-bg)] hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"
            onClick={onConfirm}
            disabled={confirmDisabled}
            title={
              confirmDisabled && !busy
                ? 'The RPO/health preflight must pass before switchover can be confirmed.'
                : undefined
            }
          >
            {busy ? 'Confirming…' : 'Confirm Switchover'}
          </button>
        </div>
      </div>
    </div>
  )
}
