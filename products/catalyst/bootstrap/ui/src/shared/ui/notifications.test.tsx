/**
 * notifications.test.tsx — global toast surface (founder #475).
 *
 *   • Provider mounts with no toasts initially → tray DOM is absent
 *   • notify() pushes a toast → renders title + body + raw + actions
 *   • notify() with the same id REPLACES the existing toast in-place
 *   • dismiss(id) removes the toast
 *   • action onClick fires + auto-dismisses unless dismissOnClick=false
 *   • level=error renders role="alert", everything else role="status"
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  render,
  screen,
  cleanup,
  fireEvent,
  act,
} from '@testing-library/react'
import {
  NotificationProvider,
  useNotifications,
  type NotificationLevel,
} from './notifications'

afterEach(() => cleanup())

interface PushOpts {
  level?: NotificationLevel
  title?: string
  body?: string
  raw?: string
  id?: string
  onRetry?: () => void
  onWipe?: () => void
}

/**
 * Test harness — exposes a button that triggers `notify()` with the
 * supplied opts. Re-rendering with new opts gives the test direct
 * control over the next call's payload.
 */
function PushButton({ opts }: { opts: PushOpts }) {
  const { notify } = useNotifications()
  return (
    <button
      type="button"
      data-testid="harness-push"
      onClick={() =>
        notify({
          id: opts.id,
          level: opts.level ?? 'error',
          title: opts.title ?? 'Provisioning failed',
          body: opts.body,
          raw: opts.raw,
          actions:
            opts.onRetry || opts.onWipe
              ? [
                  ...(opts.onRetry
                    ? [
                        {
                          label: 'Retry stream',
                          variant: 'primary' as const,
                          testId: 'sov-failure-retry',
                          onClick: opts.onRetry,
                          dismissOnClick: false,
                        },
                      ]
                    : []),
                  ...(opts.onWipe
                    ? [
                        {
                          label: 'Cancel & Wipe',
                          variant: 'danger' as const,
                          testId: 'sov-failure-wipe',
                          onClick: opts.onWipe,
                        },
                      ]
                    : []),
                ]
              : undefined,
        })
      }
    >
      push
    </button>
  )
}

function renderHarness(opts: PushOpts = {}) {
  return render(
    <NotificationProvider>
      <PushButton opts={opts} />
    </NotificationProvider>,
  )
}

describe('NotificationProvider — empty state', () => {
  it('does not render the tray when there are no notifications', () => {
    renderHarness()
    expect(screen.queryByTestId('notification-tray')).toBeNull()
  })
})

describe('NotificationProvider — push', () => {
  it('renders a toast with title, body, raw block, and action buttons', () => {
    renderHarness({
      id: 'deployment-failure:d-1',
      level: 'error',
      title: 'Provisioning failed',
      body: 'The catalyst-api emitted a terminal failure for deployment d-1.',
      raw: 'tofu apply: rg busy',
      onRetry: () => undefined,
      onWipe: () => undefined,
    })
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.getByTestId('notification-tray')).toBeTruthy()
    expect(screen.getByText('Provisioning failed')).toBeTruthy()
    expect(screen.getByText(/terminal failure for deployment d-1/)).toBeTruthy()
    expect(
      screen.getByTestId('notification-deployment-failure:d-1-raw').textContent,
    ).toContain('tofu apply: rg busy')
    expect(screen.getByTestId('sov-failure-retry')).toBeTruthy()
    expect(screen.getByTestId('sov-failure-wipe')).toBeTruthy()
  })

  it('uses role="alert" for level=error and role="status" for warn/info', () => {
    const { rerender } = render(
      <NotificationProvider>
        <PushButton opts={{ id: 't1', level: 'error', title: 'boom' }} />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.getByTestId('notification-t1').getAttribute('role')).toBe('alert')

    rerender(
      <NotificationProvider>
        <PushButton opts={{ id: 't2', level: 'warn', title: 'careful' }} />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.getByTestId('notification-t2').getAttribute('role')).toBe('status')
  })
})

describe('NotificationProvider — id-based replace', () => {
  it('replaces an existing toast when notify() is called with the same id', () => {
    function Harness() {
      const { notify } = useNotifications()
      return (
        <>
          <button
            data-testid="push-a"
            onClick={() =>
              notify({ id: 'same', level: 'error', title: 'first' })
            }
          >
            a
          </button>
          <button
            data-testid="push-b"
            onClick={() =>
              notify({ id: 'same', level: 'error', title: 'second' })
            }
          >
            b
          </button>
        </>
      )
    }
    render(
      <NotificationProvider>
        <Harness />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('push-a'))
    fireEvent.click(screen.getByTestId('push-b'))
    expect(screen.getAllByTestId('notification-same').length).toBe(1)
    expect(screen.getByText('second')).toBeTruthy()
    expect(screen.queryByText('first')).toBeNull()
  })
})

describe('NotificationProvider — dismiss', () => {
  it('removes the toast when the close button is clicked', () => {
    renderHarness({ id: 'x', level: 'info', title: 'hi' })
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.queryByTestId('notification-x')).toBeTruthy()
    fireEvent.click(screen.getByTestId('notification-x-dismiss'))
    expect(screen.queryByTestId('notification-x')).toBeNull()
  })

  it('action with dismissOnClick !== false auto-dismisses after firing', () => {
    const onWipe = vi.fn()
    renderHarness({ id: 'y', level: 'error', title: 'failed', onWipe })
    fireEvent.click(screen.getByTestId('harness-push'))
    fireEvent.click(screen.getByTestId('sov-failure-wipe'))
    expect(onWipe).toHaveBeenCalledOnce()
    expect(screen.queryByTestId('notification-y')).toBeNull()
  })

  it('action with dismissOnClick=false fires onClick but keeps the toast', () => {
    const onRetry = vi.fn()
    renderHarness({ id: 'z', level: 'error', title: 'failed', onRetry })
    fireEvent.click(screen.getByTestId('harness-push'))
    fireEvent.click(screen.getByTestId('sov-failure-retry'))
    expect(onRetry).toHaveBeenCalledOnce()
    expect(screen.queryByTestId('notification-z')).toBeTruthy()
  })
})

describe('NotificationProvider — context guard', () => {
  it('throws when useNotifications is called outside the provider', () => {
    function Naked() {
      useNotifications()
      return null
    }
    // React logs the boundary error; suppress for cleaner test output.
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    expect(() =>
      act(() => {
        render(<Naked />)
      }),
    ).toThrow(/NotificationProvider/)
    spy.mockRestore()
  })
})
