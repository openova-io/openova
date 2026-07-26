/**
 * retryJobFeedback.ts — the pure label + error-message contract behind the
 * per-row remediation control (RetryJobButton).
 *
 * Kept OUT of the component module so both halves can be unit-tested
 * directly (and so the component file exports only a component — the
 * react-refresh rule).
 */

import type { JobKind } from '@/lib/jobs.types'

/**
 * retryLabel — kind-specific action label. Mirrors the backend's
 * `dispatchRetry` switch so the verb the operator reads matches the action
 * the catalyst-api actually performs.
 *
 *   install / reconcile / reconciler → "Retry reconcile" (annotation bump)
 *   cron                             → "Run now"        (one-off Job)
 *   task                             → "Re-run"         (delete + recreate)
 *   step                             → "Re-run"         (cutover step re-drive)
 *   mutation                         → "Re-submit"
 *
 * `step` is a projected activity step (`cutover-step-*`, issue #3379 / UAT
 * row 165). "Re-run" is the verb row 165 names, and it is honest: the
 * backend deletes the step's stale failed Job and runs it again — it does
 * not merely nudge a reconciler.
 */
export function retryLabel(kind: JobKind): string {
  switch (kind) {
    case 'cron':
      return 'Run now'
    case 'task':
      return 'Re-run'
    case 'step':
      return 'Re-run'
    case 'mutation':
      return 'Re-submit'
    default:
      return 'Retry reconcile'
  }
}

/**
 * errorMessageFor — turns a non-2xx retry response into the message the
 * operator sees. Prefers the server's own `detail` (the backend writes an
 * actionable one for 409/422) and falls back to a fixed label per status, so
 * the control NEVER renders a green "success" for a call that failed.
 *
 * Ladder, in order:
 *   403                     → "Not permitted"   (the viewer lacks operator RBAC)
 *   any status with detail  → that detail verbatim
 *   409 without detail      → "Not retryable"
 *   anything else           → "Failed (<status>)"
 */
export async function errorMessageFor(res: Response): Promise<string> {
  let detail = ''
  try {
    const body = (await res.json()) as { detail?: unknown; error?: unknown }
    if (typeof body?.detail === 'string') detail = body.detail.trim()
    else if (typeof body?.error === 'string') detail = body.error.trim()
  } catch {
    // Non-JSON body (proxy error page) — fall through to the status label.
  }
  if (res.status === 403) return 'Not permitted'
  if (detail) return detail
  if (res.status === 409) return 'Not retryable'
  return `Failed (${res.status})`
}
