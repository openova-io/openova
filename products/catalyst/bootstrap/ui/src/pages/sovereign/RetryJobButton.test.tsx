/**
 * RetryJobButton.test.tsx — the §5c per-row remediation control (issue
 * #3646): clicking POSTs the retry endpoint; 200 → "Requested"; 403 →
 * "Not permitted"; the label is kind-specific ("Run now" for a cron).
 *
 * Extended for UAT row 165 / issue #3379 — the `step` kind (a projected
 * `cutover-step-*` row) labels "Re-run", shows a real PENDING state while
 * the POST is in flight, and surfaces the backend's own failure detail
 * rather than an optimistic green.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { RetryJobButton } from './RetryJobButton'
import { retryLabel } from './retryJobFeedback'

let nextStatus = 200
let nextBody: Record<string, unknown> = { executionId: 'e1', action: 'annotated' }
/** Resolver for the in-flight response — lets a test hold the POST open. */
let releaseResponse: (() => void) | null = null

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: async () => {
    if (releaseResponse) {
      await new Promise<void>((resolve) => {
        releaseResponse = resolve
      })
    }
    return new Response(JSON.stringify(nextBody), {
      status: nextStatus,
      headers: { 'Content-Type': 'application/json' },
    })
  },
}))

beforeEach(() => {
  nextStatus = 200
  nextBody = { executionId: 'e1', action: 'annotated' }
  releaseResponse = null
  cleanup()
})

describe('RetryJobButton', () => {
  it('labels per kind — "Run now" for a cron', () => {
    render(<RetryJobButton deploymentId="d1" jobId="d1:cron-x" kind="cron" />)
    expect(screen.getByTestId('jobs-retry-d1:cron-x').textContent).toBe('Run now')
  })

  it('labels "Retry reconcile" for an install', () => {
    render(<RetryJobButton deploymentId="d1" jobId="d1:install-velero" kind="install" />)
    expect(screen.getByTestId('jobs-retry-d1:install-velero').textContent).toBe('Retry reconcile')
  })

  it('on 200 transitions to a "Requested" confirmation', async () => {
    nextStatus = 200
    render(<RetryJobButton deploymentId="d1" jobId="d1:install-velero" kind="install" />)
    fireEvent.click(screen.getByTestId('jobs-retry-d1:install-velero'))
    await waitFor(() => {
      expect(screen.getByTestId('jobs-retry-done-d1:install-velero').textContent).toContain('Requested')
    })
  })

  it('on 403 surfaces "Not permitted"', async () => {
    nextStatus = 403
    render(<RetryJobButton deploymentId="d1" jobId="d1:install-velero" kind="install" />)
    fireEvent.click(screen.getByTestId('jobs-retry-d1:install-velero'))
    await waitFor(() => {
      expect(screen.getByTestId('jobs-retry-error-d1:install-velero').textContent).toContain('Not permitted')
    })
  })
})

describe('RetryJobButton — cutover step (UAT row 165, issue #3379)', () => {
  const jobId = 'cutover-step-egress-block-test'

  it('labels "Re-run" for a step — the verb row 165 names', () => {
    expect(retryLabel('step')).toBe('Re-run')
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="step" />)
    expect(screen.getByTestId(`jobs-retry-${jobId}`).textContent).toBe('Re-run')
  })

  it('shows a PENDING state while the re-run is dispatched, before any result', async () => {
    releaseResponse = () => {} // placeholder — replaced by the mock's resolver
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="step" />)
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))

    const btn = await screen.findByTestId(`jobs-retry-${jobId}`)
    await waitFor(() => {
      expect(btn.textContent).toBe('Requesting…')
    })
    // The pending state is a real gate, not cosmetic — a second click
    // cannot fire a duplicate re-run while the first is in flight.
    expect((btn as HTMLButtonElement).disabled).toBe(true)
    expect(btn.getAttribute('aria-busy')).toBe('true')
    // No premature success claim while the call is still open.
    expect(screen.queryByTestId(`jobs-retry-done-${jobId}`)).toBeNull()

    releaseResponse?.()
    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-done-${jobId}`).textContent).toContain('Requested')
    })
  })

  it('on 422 surfaces the backend detail verbatim — never a fake green', async () => {
    nextStatus = 422
    nextBody = {
      error: 'not-directly-retryable',
      detail: 'cutover step "no-such-step" is not among the 11 steps installed on this Sovereign',
    }
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="step" />)
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))
    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-error-${jobId}`).textContent).toContain(
        'is not among the 11 steps',
      )
    })
    expect(screen.queryByTestId(`jobs-retry-done-${jobId}`)).toBeNull()
  })

  it('on 409 surfaces the in-flight-cutover detail', async () => {
    nextStatus = 409
    nextBody = {
      error: 'cutover-in-progress',
      detail: 'a cutover run is already in flight on this catalyst-api Pod',
    }
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="step" />)
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))
    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-error-${jobId}`).textContent).toContain(
        'already in flight',
      )
    })
  })

  it('falls back to "Not retryable" on a detail-less 409', async () => {
    nextStatus = 409
    nextBody = {}
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="step" />)
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))
    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-error-${jobId}`).textContent).toContain('Not retryable')
    })
  })

  it('surfaces the status code when the body is not JSON', async () => {
    nextStatus = 502
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="step" />)
    // A body with neither detail nor error → the status-code fallback.
    nextBody = {}
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))
    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-error-${jobId}`).textContent).toContain('Failed (502)')
    })
  })
})
