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
 *   mutation                         → "Re-submit"
 *
 * The control is OBSERVE-ONLY-NO-MORE: clicking it re-drives the actual
 * reconcile via the backend (annotation bump / one-off Job), never a
 * client-side kubectl. The button optimistically shows "Requesting…",
 * then "Requested" on 200 (the live /jobs backfill reflects the new
 * Execution + real state on its next poll), or the error on failure. A
 * 403 surfaces "Not permitted" (the viewer lacks operator RBAC); a 409
 * surfaces "Not retryable".
 */

import { useState } from 'react'
import { authedFetch } from '@/shared/lib/authedFetch'
import { API_BASE } from '@/shared/config/urls'
import type { JobKind } from '@/lib/jobs.types'

interface RetryJobButtonProps {
  deploymentId: string
  jobId: string
  kind: JobKind
}

type RetryPhase = 'idle' | 'requesting' | 'done' | 'error'

/** Kind-specific action label (mirrors the backend dispatch). */
function retryLabel(kind: JobKind): string {
  switch (kind) {
    case 'cron':
      return 'Run now'
    case 'task':
      return 'Re-run'
    case 'mutation':
      return 'Re-submit'
    default:
      return 'Retry reconcile'
  }
}

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
      if (res.status === 403) {
        setPhase('error')
        setMessage('Not permitted')
        return
      }
      if (res.status === 409) {
        setPhase('error')
        setMessage('Not retryable')
        return
      }
      if (!res.ok) {
        setPhase('error')
        setMessage(`Failed (${res.status})`)
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

  if (phase === 'done') {
    return (
      <span
        className="jobs-retry-result jobs-retry-done"
        data-testid={`jobs-retry-done-${jobId}`}
        title="Reconcile requested — the live state refreshes on the next poll"
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
        title={`${label} — re-drives the reconcile via the catalyst-api (Flux-native, no kubectl)`}
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
