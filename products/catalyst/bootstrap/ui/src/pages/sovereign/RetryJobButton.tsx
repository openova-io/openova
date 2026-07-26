/**
 * RetryJobButton — the per-row remediation control (issue #3646 §5c).
 *
 * Renders a single action button on a Failed/degraded activity row that
 * POSTs the generic Flux-native retry endpoint
 *
 *   POST /api/v1/deployments/{depId}/jobs/{jobId}/retry
 *
 * and reflects the result inline. The label is kind-specific (the backend
 * dispatches on the kind too):
 *
 *   install / reconcile / reconciler → "Retry reconcile"
 *   cron                             → "Run now"
 *   task                             → "Re-run"
 *   step                             → "Re-run"
 *   mutation                         → "Re-submit"
 *
 * `step` is the self-sovereign-cutover leg (issue #3379, UAT row 165): a
 * FAILED `cutover-step-*` row re-drives the cutover engine in operator-retry
 * mode, which deletes the step's stale failed Job, re-runs it, and resumes
 * the chain. The row is only offered the control when it is Failed
 * (`isJobRetryable`), and the backend independently rejects a non-failed row
 * with 409 — so a Succeeded/Running/Pending step can never be re-driven.
 *
 * The control is OBSERVE-ONLY-NO-MORE: clicking it re-drives the actual
 * reconcile via the backend (annotation bump / one-off Job / cutover
 * re-run), never a client-side kubectl.
 *
 * # Honest feedback, never optimistic green
 *
 * The button shows a PENDING "Requesting…" state while the POST is in
 * flight, and only claims success on a real 2xx — at which point the label
 * is "Requested", NOT "Succeeded": the request was accepted, the live /jobs
 * backfill reports the actual outcome on its next poll. Every non-2xx is
 * surfaced verbatim rather than swallowed:
 *
 *   403 → "Not permitted"      (the viewer lacks operator RBAC)
 *   409 → "Not retryable"      (nothing to re-run) / the server's detail
 *                               when a cutover run is already in flight
 *   422 → the server's `detail` (e.g. "cutover step … is not among the …")
 *   any other status / network error → the status code or "Network error"
 *
 * The label + error-message ladders themselves live in `retryJobFeedback.ts`
 * so they are unit-testable on their own.
 */

import { useState } from 'react'
import { authedFetch } from '@/shared/lib/authedFetch'
import { API_BASE } from '@/shared/config/urls'
import type { JobKind } from '@/lib/jobs.types'
import { retryLabel, errorMessageFor } from './retryJobFeedback'

interface RetryJobButtonProps {
  deploymentId: string
  jobId: string
  kind: JobKind
}

type RetryPhase = 'idle' | 'requesting' | 'done' | 'error'

export function RetryJobButton({ deploymentId, jobId, kind }: RetryJobButtonProps) {
  const [phase, setPhase] = useState<RetryPhase>('idle')
  const [message, setMessage] = useState<string>('')

  async function onClick() {
    setPhase('requesting')
    setMessage('')
    try {
      const res = await authedFetch(
        `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/jobs/${encodeURIComponent(
          jobId,
        )}/retry`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: '{}',
        },
      )
      if (!res.ok) {
        // Honest failure — surface the server's own reason. Never a
        // green confirmation for a call that did not succeed.
        setPhase('error')
        setMessage(await errorMessageFor(res))
        return
      }
      setPhase('done')
      setMessage('Requested')
    } catch {
      setPhase('error')
      setMessage('Network error')
    }
  }

  const label = retryLabel(kind)
  const hint =
    kind === 'step'
      ? `${label} — re-drives this cutover step via the catalyst-api (the failed step's Job is deleted and run again)`
      : `${label} — re-drives the reconcile via the catalyst-api (Flux-native, no kubectl)`

  if (phase === 'done') {
    return (
      <span
        className="jobs-retry-result jobs-retry-done"
        data-testid={`jobs-retry-done-${jobId}`}
        title="Request accepted — the live state refreshes on the next poll"
      >
        ✓ {message}
      </span>
    )
  }

  return (
    <span className="jobs-retry-wrap">
      <button
        type="button"
        className="jobs-retry-btn"
        data-testid={`jobs-retry-${jobId}`}
        data-kind={kind}
        disabled={phase === 'requesting'}
        onClick={onClick}
        title={hint}
        aria-busy={phase === 'requesting'}
      >
        {phase === 'requesting' ? 'Requesting…' : label}
      </button>
      {phase === 'error' ? (
        <span
          className="jobs-retry-result jobs-retry-error"
          data-testid={`jobs-retry-error-${jobId}`}
        >
          {message}
        </span>
      ) : null}
    </span>
  )
}
