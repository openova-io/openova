/**
 * RetryJobButton.test.tsx — the §5c per-row remediation control (issue
 * #3646): clicking POSTs the retry endpoint; 200 → "Requested"; 403 →
 * "Not permitted"; the label is kind-specific ("Run now" for a cron).
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { RetryJobButton } from './RetryJobButton'

let nextStatus = 200

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: (_url: string, _init?: RequestInit) =>
    Promise.resolve(
      new Response(JSON.stringify({ executionId: 'e1', action: 'annotated' }), {
        status: nextStatus,
        headers: { 'Content-Type': 'application/json' },
      }),
    ),
}))

beforeEach(() => {
  nextStatus = 200
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
