/**
 * PolicyModeToggle — permissive ↔ enforcing toggle widget for one
 * (policy, environment) pair (slice U5, #1096).
 *
 * UX:
 *   • Audit | Enforce switch (visible mode = current).
 *   • On flip: confirmation dialog with diff describing impact:
 *       "Currently passing: <N>, currently failing: <M>.
 *        Flipping to Enforce will block any NEW violators at admission
 *        within 30s. Existing failures remain (audit-only)."
 *   • On confirm: PUT /api/v1/sovereigns/{id}/environments/{env}/policy
 *     with the updated `EnvironmentPolicy.spec.compliance.modes.<policy>`.
 *   • Audit event published to NATS by the server (handler-side in
 *     slice S; UI just calls).
 *
 * RBAC gating:
 *   • The brief specifies `admin` or `owner` tier. We use the optional
 *     `disabled` prop so the parent decides — keeps the widget pure
 *     and testable. Pages decide based on UserAccess role.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — endpoint URL composed from API_BASE in the
 *      sibling `compliance.api.ts` `putEnvironmentPolicyMode()`.
 */

import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  putEnvironmentPolicyMode,
  type PolicyMode,
} from '@/pages/admin/compliance/compliance.api'

export interface PolicyModeToggleProps {
  sovereignId: string
  environmentRef: string
  policyName: string
  /** Current mode (from PolicyView.mode or EnvironmentPolicy.spec.compliance.modes). */
  currentMode: PolicyMode | string
  /** Used in the confirmation dialog "currently passing / failing" copy. */
  passingCount?: number
  failingCount?: number
  /** When true, the toggle is read-only — RBAC gate failed. */
  disabled?: boolean
  /** Emitted after a successful flip (test seam + sibling-state refresh). */
  onModeChanged?: (newMode: PolicyMode) => void
  /** Test seam — prevents the hidden confirmation dialog from auto-firing. */
  initialOpenForTest?: boolean
}

export function PolicyModeToggle({
  sovereignId,
  environmentRef,
  policyName,
  currentMode,
  passingCount,
  failingCount,
  disabled = false,
  onModeChanged,
  initialOpenForTest = false,
}: PolicyModeToggleProps) {
  const isEnforcing = currentMode === 'enforcing'
  const [pendingMode, setPendingMode] = useState<PolicyMode | null>(
    initialOpenForTest ? (isEnforcing ? 'permissive' : 'enforcing') : null,
  )
  const [error, setError] = useState<string | null>(null)
  const queryClient = useQueryClient()

  const mutation = useMutation({
    mutationFn: async (newMode: PolicyMode) => {
      await putEnvironmentPolicyMode(sovereignId, environmentRef, {
        modes: { [policyName]: newMode },
      })
      return newMode
    },
    onSuccess: (newMode) => {
      // Invalidate compliance queries so the freshly-flipped mode
      // surfaces on the next render across the page.
      queryClient.invalidateQueries({ queryKey: ['compliance', sovereignId] })
      setPendingMode(null)
      setError(null)
      onModeChanged?.(newMode)
    },
    onError: (err: Error) => {
      setError(err.message)
    },
  })

  function handleFlipRequest() {
    if (disabled) return
    setError(null)
    setPendingMode(isEnforcing ? 'permissive' : 'enforcing')
  }

  function handleConfirm() {
    if (pendingMode === null) return
    mutation.mutate(pendingMode)
  }

  function handleCancel() {
    setPendingMode(null)
    setError(null)
  }

  return (
    <span className="inline-flex items-center gap-2" data-testid={`policy-mode-toggle-${policyName}`}>
      <button
        type="button"
        onClick={handleFlipRequest}
        disabled={disabled}
        aria-pressed={isEnforcing}
        data-testid={`policy-mode-toggle-button-${policyName}`}
        title={
          disabled
            ? 'Insufficient permissions to change policy mode'
            : `Flip ${policyName} from ${isEnforcing ? 'enforcing' : 'permissive'} to ${
                isEnforcing ? 'permissive' : 'enforcing'
              }`
        }
        className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-medium transition-colors ${
          disabled
            ? 'cursor-not-allowed border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-dim)]'
            : isEnforcing
              ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300 hover:border-emerald-400'
              : 'border-amber-500/40 bg-amber-500/10 text-amber-300 hover:border-amber-400'
        }`}
      >
        <span
          className={`h-2 w-2 rounded-full ${isEnforcing ? 'bg-emerald-400' : 'bg-amber-400'}`}
          aria-hidden="true"
        />
        {isEnforcing ? 'Enforce' : 'Audit'}
      </button>

      {pendingMode !== null ? (
        <ConfirmDialog
          policyName={policyName}
          environmentRef={environmentRef}
          fromMode={isEnforcing ? 'enforcing' : 'permissive'}
          toMode={pendingMode}
          passingCount={passingCount}
          failingCount={failingCount}
          isPending={mutation.isPending}
          error={error}
          onConfirm={handleConfirm}
          onCancel={handleCancel}
        />
      ) : null}
    </span>
  )
}

interface ConfirmDialogProps {
  policyName: string
  environmentRef: string
  fromMode: PolicyMode
  toMode: PolicyMode
  passingCount?: number
  failingCount?: number
  isPending: boolean
  error: string | null
  onConfirm: () => void
  onCancel: () => void
}

function ConfirmDialog({
  policyName,
  environmentRef,
  fromMode,
  toMode,
  passingCount,
  failingCount,
  isPending,
  error,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  // Diff copy depends on direction. Tightest description per the brief
  // "10 currently-passing resources, 3 failing — flipping to Enforce
  // will block any NEW violators at admission within 30s".
  const diffMessage = useMemo(() => {
    const total = (passingCount ?? 0) + (failingCount ?? 0)
    if (toMode === 'enforcing') {
      return `Currently ${passingCount ?? '?'} passing, ${failingCount ?? '?'} failing across ${total} resources. Flipping to Enforce blocks any NEW violators at admission within 30 seconds. Existing failures remain (the toggle does not retroactively delete resources).`
    }
    return `Flipping to Audit will allow new violators to admit but record them as failing. Existing ${failingCount ?? '?'} failures continue to surface in this dashboard. The change reaches the cluster within 30 seconds.`
  }, [passingCount, failingCount, toMode])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={`policy-mode-confirm-title-${policyName}`}
      data-testid={`policy-mode-confirm-${policyName}`}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
    >
      <div className="w-full max-w-md rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5 shadow-xl">
        <h2
          id={`policy-mode-confirm-title-${policyName}`}
          className="text-base font-semibold text-[var(--color-text-strong)]"
        >
          Flip {policyName} to {toMode === 'enforcing' ? 'Enforce' : 'Audit'}?
        </h2>
        <p className="mt-1 text-xs text-[var(--color-text-dim)]">
          Environment: <code className="font-mono">{environmentRef}</code>
        </p>
        <p className="mt-3 text-sm leading-relaxed text-[var(--color-text)]">
          {diffMessage}
        </p>
        <p className="mt-2 text-xs text-[var(--color-text-dim)]">
          Current mode:{' '}
          <code className="font-mono">{fromMode}</code> →{' '}
          <code className="font-mono text-[var(--color-text-strong)]">{toMode}</code>
        </p>
        {error ? (
          <div
            data-testid={`policy-mode-confirm-error-${policyName}`}
            className="mt-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300"
          >
            {error}
          </div>
        ) : null}
        <div className="mt-4 flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            disabled={isPending}
            data-testid={`policy-mode-confirm-cancel-${policyName}`}
            className="rounded-md border border-[var(--color-border)] px-3 py-1.5 text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={isPending}
            data-testid={`policy-mode-confirm-apply-${policyName}`}
            className={`rounded-md px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50 ${
              toMode === 'enforcing'
                ? 'bg-emerald-600 hover:bg-emerald-500'
                : 'bg-amber-600 hover:bg-amber-500'
            }`}
          >
            {isPending ? 'Applying…' : `Confirm: switch to ${toMode}`}
          </button>
        </div>
      </div>
    </div>
  )
}
