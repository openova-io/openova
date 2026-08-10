/**
 * RetryJobButton.notify-row176.test.tsx — UAT row 176, the SILENT-FAILURE half.
 *
 * WHAT THE WALK SAW. The retry POST returned 422, the button never changed
 * state, and the failure surfaced as neither a toast nor readable inline text.
 *
 * WHAT IS ACTUALLY WRONG — the control does not swallow its error, which is
 * why this survived review. It sets phase='error' and renders a role="alert"
 * span. But that span is the ONLY channel, and it is a weak one:
 *
 *   1. It renders inside the Jobs table's Actions cell, clamped by
 *      `.jobs-retry-error` to `max-width: 28ch` with `white-space: nowrap` and
 *      an ellipsis. The 422 detail for this row is ~110 characters, so the
 *      operator gets roughly a quarter of the first clause, in the rightmost
 *      column, with the rest only on hover.
 *   2. It lives in component-local useState, so it is lost whenever the row
 *      unmounts — and the Jobs view re-polls on a 5-second interval.
 *   3. The button itself reverts to its idle label immediately, so there is no
 *      persistent affordance that anything was attempted.
 *
 * A failure that is technically rendered but practically unreadable is
 * indistinguishable from one that was swallowed. The fix routes it to the
 * app-wide notification centre — the mechanism the rest of this console
 * already uses for exactly this (AppsPage, CatalogDetail, the RBAC admin
 * pages) — so the full server detail survives the poll and is reachable from
 * the bell on any page.
 *
 * The inline span stays: it is the immediate, in-context signal. These tests
 * assert BOTH channels, and the control at the bottom pins that a SUCCESS
 * still does not raise an error notification — otherwise "notify on
 * everything" would pass every assertion above.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { RetryJobButton } from './RetryJobButton'
import { NotificationProvider, useNotifications } from '@/shared/ui/notifications'

let nextStatus = 200
let nextBody: Record<string, unknown> = { executionId: 'e1', action: 'annotated' }

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: async () =>
    new Response(JSON.stringify(nextBody), {
      status: nextStatus,
      headers: { 'Content-Type': 'application/json' },
    }),
}))

beforeEach(() => {
  nextStatus = 200
  nextBody = { executionId: 'e1', action: 'annotated' }
  cleanup()
})

const jobId = 'd1:task-syft-sbom'

/** NotificationProbe — reads the live notification context and flattens every
 *  raised item into one node, so a raised notification is OBSERVED rather than
 *  assumed. Reading the context directly keeps these tests independent of the
 *  bell/panel presentation components and their props. */
function NotificationProbe() {
  const { items } = useNotifications()
  return (
    <div data-testid="notification-list">
      {items.map((n) => (
        <div key={n.id} data-level={n.level}>
          {n.title}
          {n.body ?? ''}
          {n.raw ?? ''}
        </div>
      ))}
    </div>
  )
}

/** Renders the button alongside the probe. */
function renderWithNotifications(kind: 'task' | 'step' = 'task') {
  return render(
    <NotificationProvider>
      <RetryJobButton deploymentId="d1" jobId={jobId} kind={kind} />
      <NotificationProbe />
    </NotificationProvider>,
  )
}

describe('row 176 — a failed re-run is not silent', () => {
  it('raises a notification carrying the server detail on 422', async () => {
    nextStatus = 422
    nextBody = {
      error: 'not-directly-retryable',
      detail:
        'CronJob syft-grype/syft-grype backing the "syft-sbom" row is not installed: reconciler is aggregate or operator-managed; not directly retryable',
    }
    renderWithNotifications()
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))

    // The notification centre carries the FULL detail — not a 28ch prefix.
    await waitFor(() => {
      expect(screen.getByTestId('notification-list').textContent).toContain(
        'not directly retryable',
      )
    })
    expect(screen.getByTestId('notification-list').textContent).toContain(
      'CronJob syft-grype/syft-grype',
    )
  })

  it('still shows the inline in-context error too — both channels', async () => {
    nextStatus = 422
    nextBody = { error: 'not-directly-retryable', detail: 'nothing to re-run here' }
    renderWithNotifications()
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))

    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-error-${jobId}`).textContent).toContain(
        'nothing to re-run here',
      )
    })
  })

  it('notifies on a network failure too, where there is no server detail', async () => {
    nextStatus = 500
    nextBody = {}
    renderWithNotifications()
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))

    await waitFor(() => {
      expect(screen.getByTestId('notification-list').textContent).toContain('Failed (500)')
    })
  })

  // CONTROL — a SUCCESSFUL re-run raises no error notification. Without this,
  // a component that notified unconditionally would satisfy every assertion
  // above while burying the operator in false alarms.
  it('raises no error notification when the re-run succeeds', async () => {
    nextStatus = 200
    renderWithNotifications()
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))

    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-done-${jobId}`).textContent).toContain('Requested')
    })
    expect(screen.queryByTestId('notification-list')?.textContent ?? '').not.toContain(
      'Re-run failed',
    )
  })

  // CONTROL — the button must keep working outside a NotificationProvider.
  // The Jobs table is mounted under one in the real app, but a hook that
  // THREW without a provider would take the whole row down; that would turn a
  // recoverable 422 into a blank table.
  it('degrades to inline-only when no provider is mounted', async () => {
    nextStatus = 422
    nextBody = { error: 'not-directly-retryable', detail: 'no provider here' }
    render(<RetryJobButton deploymentId="d1" jobId={jobId} kind="task" />)
    fireEvent.click(screen.getByTestId(`jobs-retry-${jobId}`))

    await waitFor(() => {
      expect(screen.getByTestId(`jobs-retry-error-${jobId}`).textContent).toContain(
        'no provider here',
      )
    })
  })
})
