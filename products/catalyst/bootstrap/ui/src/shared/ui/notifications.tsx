/**
 * notifications.tsx — global, app-wide toast surface.
 *
 * Founder mandate (#475, 2026-05-01): page-level status banners (e.g. the
 * "Provisioning failed" banner that used to render above the apps grid)
 * pollute the surface they're attached to. The Apps page must show ONLY
 * the apps grid; deployment status must surface via a global notification
 * affordance — bottom-right toasts stack here.
 *
 * The provider is mounted once in `RootLayout` so toasts are visible
 * across every tab (Apps / Jobs / Dashboard / Cloud / Users) and survive
 * client-side navigation.
 *
 * Public API:
 *   • <NotificationProvider>      — wraps the app, renders the toast tray
 *   • useNotifications()          — returns { notify, dismiss, items }
 *   • notify({ id?, level, title, body?, actions? })
 *
 * If `id` is supplied, calls with the same id REPLACE the existing toast
 * (so a deployment-failure update doesn't stack — it edits in-place).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every visual
 * value flows through CSS variables (`--color-danger`, `--color-warn`,
 * `--color-accent`, …). No inlined hex.
 */

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'

export type NotificationLevel = 'info' | 'warn' | 'error' | 'success'

export interface NotificationAction {
  /** Visible label, e.g. "Retry stream" or "Cancel & Wipe". */
  label: string
  /** Click handler — triggered before the toast is dismissed. */
  onClick: () => void
  /** Visual emphasis. `primary` matches the canonical accent button. */
  variant?: 'primary' | 'danger' | 'ghost'
  /** When true (default) the toast is dismissed after `onClick` fires. */
  dismissOnClick?: boolean
  /** data-testid to expose for E2E. */
  testId?: string
}

export interface Notification {
  /** Stable id — pass to replace an existing toast in-place. Auto-assigned if omitted. */
  id: string
  level: NotificationLevel
  title: string
  /** Optional secondary body (string or pre-formatted error text). */
  body?: string
  /** Action buttons rendered at the bottom of the toast. */
  actions?: NotificationAction[]
  /**
   * Render a `<pre>` raw block (e.g. server error trace). Set this
   * separately from `body` so the layout can use a monospaced rail
   * without pre-wrapping arbitrary copy.
   */
  raw?: string
}

interface NotificationsContextValue {
  items: readonly Notification[]
  /** Push a new toast OR replace an existing one with the same id. */
  notify: (n: Omit<Notification, 'id'> & { id?: string }) => string
  /** Dismiss by id. No-op if the id is unknown. */
  dismiss: (id: string) => void
}

const NotificationsContext = createContext<NotificationsContextValue | null>(null)

export function useNotifications(): NotificationsContextValue {
  const ctx = useContext(NotificationsContext)
  if (!ctx) {
    throw new Error(
      'useNotifications() must be called inside a <NotificationProvider>',
    )
  }
  return ctx
}

interface NotificationProviderProps {
  children: ReactNode
}

let nextAutoId = 0

export function NotificationProvider({ children }: NotificationProviderProps) {
  const [items, setItems] = useState<readonly Notification[]>([])

  const dismiss = useCallback((id: string) => {
    setItems((prev) => prev.filter((n) => n.id !== id))
  }, [])

  const notify = useCallback<NotificationsContextValue['notify']>((n) => {
    const id = n.id ?? `auto-${++nextAutoId}`
    const next: Notification = { ...n, id }
    setItems((prev) => {
      const idx = prev.findIndex((p) => p.id === id)
      if (idx === -1) return [...prev, next]
      const copy = [...prev]
      copy[idx] = next
      return copy
    })
    return id
  }, [])

  const value = useMemo<NotificationsContextValue>(
    () => ({ items, notify, dismiss }),
    [items, notify, dismiss],
  )

  return (
    <NotificationsContext.Provider value={value}>
      {children}
      <NotificationTray items={items} dismiss={dismiss} />
    </NotificationsContext.Provider>
  )
}

interface NotificationTrayProps {
  items: readonly Notification[]
  dismiss: (id: string) => void
}

/**
 * Bottom-right stacked toast tray. Fixed positioning so the surface is
 * visible regardless of which page or tab is rendered. Tray is
 * unmounted entirely when there are no items, keeping the DOM clean
 * (and keeping the `role="alert"` / `role="status"` count at zero on
 * the page chrome).
 */
function NotificationTray({ items, dismiss }: NotificationTrayProps) {
  if (items.length === 0) return null
  return (
    <div
      data-testid="notification-tray"
      className="fixed bottom-4 right-4 z-[100] flex max-w-[26rem] flex-col gap-2"
      aria-live="polite"
    >
      {items.map((n) => (
        <NotificationCard key={n.id} item={n} dismiss={dismiss} />
      ))}
    </div>
  )
}

interface NotificationCardProps {
  item: Notification
  dismiss: (id: string) => void
}

function NotificationCard({ item, dismiss }: NotificationCardProps) {
  const tone = TONE_BY_LEVEL[item.level]
  return (
    <div
      role={item.level === 'error' ? 'alert' : 'status'}
      data-testid={`notification-${item.id}`}
      data-level={item.level}
      className={`rounded-xl border ${tone.border} ${tone.surface} p-3 text-sm text-[var(--color-text)] shadow-lg`}
    >
      <div className="flex items-start gap-2">
        <h3 className={`m-0 flex-1 text-sm font-semibold ${tone.title}`}>
          {item.title}
        </h3>
        <button
          type="button"
          aria-label="Dismiss notification"
          data-testid={`notification-${item.id}-dismiss`}
          onClick={() => dismiss(item.id)}
          className="-m-1 ml-2 rounded p-1 text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
        >
          <svg viewBox="0 0 24 24" width={14} height={14} fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M18 6 6 18" />
            <path d="m6 6 12 12" />
          </svg>
        </button>
      </div>
      {item.body ? (
        <p className="m-0 mt-1 text-[var(--color-text-dim)]">{item.body}</p>
      ) : null}
      {item.raw ? (
        <pre
          data-testid={`notification-${item.id}-raw`}
          className="my-2 max-h-32 overflow-auto rounded bg-[var(--color-bg)] p-2 text-[11px] text-[var(--color-text-dim)]"
        >
          {item.raw}
        </pre>
      ) : null}
      {item.actions && item.actions.length > 0 ? (
        <div className="mt-2 flex flex-wrap gap-2">
          {item.actions.map((a, i) => (
            <button
              key={`${a.label}-${i}`}
              type="button"
              data-testid={a.testId}
              onClick={() => {
                a.onClick()
                if (a.dismissOnClick !== false) dismiss(item.id)
              }}
              className={ACTION_CLASS[a.variant ?? 'primary']}
            >
              {a.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}

const TONE_BY_LEVEL: Record<
  NotificationLevel,
  { border: string; surface: string; title: string }
> = {
  error: {
    border: 'border-[var(--color-danger)]/40',
    surface: 'bg-[var(--color-danger)]/10',
    title: 'text-[var(--color-danger)]',
  },
  warn: {
    border: 'border-[var(--color-warn)]/40',
    surface: 'bg-[var(--color-warn)]/10',
    title: 'text-[var(--color-warn)]',
  },
  info: {
    border: 'border-[var(--color-accent)]/40',
    surface: 'bg-[var(--color-accent)]/10',
    title: 'text-[var(--color-accent)]',
  },
  success: {
    border: 'border-[var(--color-success)]/40',
    surface: 'bg-[var(--color-success)]/10',
    title: 'text-[var(--color-success)]',
  },
}

const ACTION_CLASS: Record<NonNullable<NotificationAction['variant']>, string> = {
  primary:
    'rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)] px-3 py-1 text-xs font-semibold text-white hover:bg-[var(--color-accent-hover)]',
  danger:
    'rounded-md border border-[var(--color-danger)] bg-[var(--color-danger)] px-3 py-1 text-xs font-semibold text-white hover:opacity-90',
  ghost:
    'rounded-md border border-[var(--color-border)] bg-transparent px-3 py-1 text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]',
}
