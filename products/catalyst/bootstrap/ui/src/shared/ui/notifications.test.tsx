/**
 * notifications.test.tsx — global notification surface (founder #475 +
 * #531).
 *
 * Founder #531 retired the bottom-right toast tray in favour of a
 * top-right bell-icon dropdown + a standalone /notifications page,
 * both rendering the same in-memory list. The provider itself no
 * longer renders any visible chrome — its only job is to hold state
 * and expose `notify` / `dismiss` / `dismissAll` via context.
 *
 *   • Provider mounts with no notifications → no visible surface
 *   • notify() pushes an entry → bell + list panel render it
 *   • notify() with the same id REPLACES the existing entry in-place
 *   • dismiss(id) removes the entry, dismissAll() clears the list
 *   • action onClick fires + auto-dismisses unless dismissOnClick=false
 *   • level=error renders role="alert", everything else role="status"
 *   • Bell exposes an unread-count badge and toggles the dropdown
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
  NotificationBell,
  NotificationListPanel,
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

/**
 * List harness — pulls the live items + dismiss out of context and
 * renders them through the same `<NotificationListPanel />` the bell
 * dropdown and the standalone /notifications page consume. Asserting
 * against this surface keeps the tests independent of the bell's
 * dropdown open/close timing.
 */
function ListHarness() {
  const { items, dismiss } = useNotifications()
  return <NotificationListPanel items={items} dismiss={dismiss} variant="page" />
}

function renderHarness(opts: PushOpts = {}) {
  return render(
    <NotificationProvider>
      <PushButton opts={opts} />
      <ListHarness />
    </NotificationProvider>,
  )
}

describe('NotificationProvider — empty state', () => {
  it('renders the empty-state placeholder when there are no notifications', () => {
    renderHarness()
    expect(screen.getByTestId('notification-list-empty')).toBeTruthy()
    expect(screen.queryByTestId('notification-list')).toBeNull()
  })
})

describe('NotificationProvider — push', () => {
  it('renders a card with title, body, raw block, and action buttons', () => {
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
    expect(screen.getByTestId('notification-list')).toBeTruthy()
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
        <ListHarness />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.getByTestId('notification-t1').getAttribute('role')).toBe('alert')

    rerender(
      <NotificationProvider>
        <PushButton opts={{ id: 't2', level: 'warn', title: 'careful' }} />
        <ListHarness />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.getByTestId('notification-t2').getAttribute('role')).toBe('status')
  })
})

describe('NotificationProvider — id-based replace', () => {
  it('replaces an existing entry when notify() is called with the same id', () => {
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
        <ListHarness />
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
  it('removes the entry when the close button is clicked', () => {
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

  it('action with dismissOnClick=false fires onClick but keeps the entry', () => {
    const onRetry = vi.fn()
    renderHarness({ id: 'z', level: 'error', title: 'failed', onRetry })
    fireEvent.click(screen.getByTestId('harness-push'))
    fireEvent.click(screen.getByTestId('sov-failure-retry'))
    expect(onRetry).toHaveBeenCalledOnce()
    expect(screen.queryByTestId('notification-z')).toBeTruthy()
  })

  it('dismissAll() clears every entry at once', () => {
    function Harness() {
      const { notify, dismissAll } = useNotifications()
      return (
        <>
          <button
            data-testid="push-1"
            onClick={() => notify({ id: 'a', level: 'info', title: 'a' })}
          />
          <button
            data-testid="push-2"
            onClick={() => notify({ id: 'b', level: 'info', title: 'b' })}
          />
          <button data-testid="clear" onClick={() => dismissAll()} />
        </>
      )
    }
    render(
      <NotificationProvider>
        <Harness />
        <ListHarness />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('push-1'))
    fireEvent.click(screen.getByTestId('push-2'))
    expect(screen.queryByTestId('notification-a')).toBeTruthy()
    expect(screen.queryByTestId('notification-b')).toBeTruthy()
    fireEvent.click(screen.getByTestId('clear'))
    expect(screen.queryByTestId('notification-a')).toBeNull()
    expect(screen.queryByTestId('notification-b')).toBeNull()
  })
})

/* ── Bell icon + dropdown panel ─────────────────────────────────── */

describe('NotificationBell', () => {
  it('renders without a count badge when there are no notifications', () => {
    render(
      <NotificationProvider>
        <NotificationBell />
      </NotificationProvider>,
    )
    const bell = screen.getByTestId('notification-bell')
    expect(bell).toBeTruthy()
    expect(bell.getAttribute('data-count')).toBe('0')
    expect(screen.queryByTestId('notification-bell-badge')).toBeNull()
  })

  it('shows the count badge when notifications are present', () => {
    render(
      <NotificationProvider>
        <PushButton opts={{ id: 'one', level: 'error', title: 'boom' }} />
        <NotificationBell />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    expect(screen.getByTestId('notification-bell-badge').textContent).toBe('1')
  })

  it('toggles the dropdown panel on click and renders the active list', () => {
    render(
      <NotificationProvider>
        <PushButton opts={{ id: 'p1', level: 'info', title: 'tick' }} />
        <NotificationBell />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    // Closed by default — panel not in DOM.
    expect(screen.queryByTestId('notification-bell-panel')).toBeNull()
    fireEvent.click(screen.getByTestId('notification-bell'))
    expect(screen.getByTestId('notification-bell-panel')).toBeTruthy()
    expect(screen.getByText('tick')).toBeTruthy()
    // Click again → closes.
    fireEvent.click(screen.getByTestId('notification-bell'))
    expect(screen.queryByTestId('notification-bell-panel')).toBeNull()
  })

  it('clear-all button in the dropdown empties the list', () => {
    render(
      <NotificationProvider>
        <PushButton opts={{ id: 'q', level: 'error', title: 'boom' }} />
        <NotificationBell />
      </NotificationProvider>,
    )
    fireEvent.click(screen.getByTestId('harness-push'))
    fireEvent.click(screen.getByTestId('notification-bell'))
    fireEvent.click(screen.getByTestId('notification-bell-clear-all'))
    expect(screen.queryByTestId('notification-q')).toBeNull()
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
