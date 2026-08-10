/**
 * notifications.tsx — global, app-wide notification surface.
 *
 * Founder mandate (#475, 2026-05-01): page-level status banners pollute
 * the surface they're attached to. Status must surface via a global
 * affordance.
 *
 * Founder mandate (#531, 2026-05-02): the bottom-right toast tray is
 * gone. Notifications now appear in a bell icon at the top-right of
 * the PortalShell header (next to the ThemeToggle); clicking the bell
 * opens a dropdown panel listing every active notification, and the
 * same list is permanently visible at the dedicated `/notifications`
 * page so an operator can step through past failures even when no
 * toast is on screen.
 *
 * Public API:
 *   • <NotificationProvider>      — wraps the app, holds the in-memory list
 *   • <NotificationBell />        — bell icon + count badge + dropdown panel
 *   • <NotificationListPanel />   — full list, used by the bell dropdown AND
 *                                   the standalone /notifications page
 *   • useNotifications()          — returns { notify, dismiss, items }
 *   • notify({ id?, level, title, body?, actions? })
 *
 * If `id` is supplied, calls with the same id REPLACE the existing
 * notification (so a deployment-failure update doesn't stack — it
 * edits in-place).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every visual
 * value flows through CSS variables (`--color-danger`, `--color-warn`,
 * `--color-accent`, …). No inlined hex.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { Bell } from 'lucide-react'

export type NotificationLevel = 'info' | 'warn' | 'error' | 'success'

export interface NotificationAction {
  /** Visible label, e.g. "Retry stream" or "Cancel & Wipe". */
  label: string
  /** Click handler — triggered before the notification is dismissed. */
  onClick: () => void
  /** Visual emphasis. `primary` matches the canonical accent button. */
  variant?: 'primary' | 'danger' | 'ghost'
  /** When true (default) the notification is dismissed after `onClick` fires. */
  dismissOnClick?: boolean
  /** data-testid to expose for E2E. */
  testId?: string
}

export interface Notification {
  /** Stable id — pass to replace an existing notification in-place. Auto-assigned if omitted. */
  id: string
  level: NotificationLevel
  title: string
  /** Optional secondary body (string or pre-formatted error text). */
  body?: string
  /** Action buttons rendered at the bottom of the card. */
  actions?: NotificationAction[]
  /**
   * Render a `<pre>` raw block (e.g. server error trace). Set this
   * separately from `body` so the layout can use a monospaced rail
   * without pre-wrapping arbitrary copy.
   */
  raw?: string
  /** Wall-clock timestamp the notification was first surfaced. Used by
   *  the standalone /notifications page to render a relative time. */
  createdAt: number
}

interface NotificationsContextValue {
  items: readonly Notification[]
  /** Push a new notification OR replace an existing one with the same id. */
  notify: (n: Omit<Notification, 'id' | 'createdAt'> & { id?: string }) => string
  /** Dismiss by id. No-op if the id is unknown. */
  dismiss: (id: string) => void
  /** Dismiss every notification at once. */
  dismissAll: () => void
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

/**
 * Internal "soft" variant of `useNotifications()` — returns null when
 * no provider is mounted. Used by `<NotificationBell />` so the
 * PortalShell can render the bell unconditionally without forcing
 * every test fixture (which renders pages outside the global root
 * layout) to mount its own NotificationProvider. Production never
 * hits the `null` branch — the bell is a no-op stub in that case.
 */
export function useOptionalNotifications(): NotificationsContextValue | null {
  return useContext(NotificationsContext)
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

  const dismissAll = useCallback(() => {
    setItems([])
  }, [])

  const notify = useCallback<NotificationsContextValue['notify']>((n) => {
    const id = n.id ?? `auto-${++nextAutoId}`
    setItems((prev) => {
      const idx = prev.findIndex((p) => p.id === id)
      if (idx === -1) {
        const created: Notification = { ...n, id, createdAt: Date.now() }
        return [...prev, created]
      }
      const existing = prev[idx]!
      const next: Notification = { ...n, id, createdAt: existing.createdAt }
      const copy = [...prev]
      copy[idx] = next
      return copy
    })
    return id
  }, [])

  const value = useMemo<NotificationsContextValue>(
    () => ({ items, notify, dismiss, dismissAll }),
    [items, notify, dismiss, dismissAll],
  )

  // The provider intentionally does NOT render any visible surface.
  // The bell + dropdown lives in the PortalShell header and the
  // standalone page at /notifications consumes the same context. Per
  // founder mandate #531 the bottom-right toast tray is removed.
  return (
    <NotificationsContext.Provider value={value}>
      {children}
    </NotificationsContext.Provider>
  )
}

/* ── Bell icon with dropdown panel ─────────────────────────────────── */

export interface NotificationBellProps {
  /** Optional class override for the trigger button — defaults match the
   *  ThemeToggle chrome so the two sit side-by-side cleanly. */
  className?: string
  /** Icon size in px — defaults to 14, matching the ThemeToggle. */
  size?: number
}

/**
 * Bell icon button that lives at the right side of the PortalShell
 * header. Renders an unread-count badge when there are active
 * notifications and opens a panel listing them on click. The panel is
 * the same `<NotificationListPanel />` rendered on the dedicated
 * /notifications page so the surface is consistent across both
 * affordances.
 */
export function NotificationBell({ className, size = 14 }: NotificationBellProps) {
  // `useOptionalNotifications` returns null when the bell is rendered
  // outside a provider (e.g. unit-test fixtures that mount a single
  // page without the RootLayout chrome). In that case the bell is
  // visually present but inert — production always has the provider
  // mounted via RootLayout.
  const ctx = useOptionalNotifications()
  const items = ctx?.items ?? []
  const dismiss = ctx?.dismiss ?? (() => undefined)
  const dismissAll = ctx?.dismissAll ?? (() => undefined)
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement | null>(null)

  // Close on click-outside / Esc — same pattern as the cloud chip
  // popover and the row-actions menu.
  useEffect(() => {
    if (!open) return
    function onDoc(ev: MouseEvent) {
      if (!wrapRef.current?.contains(ev.target as Node)) setOpen(false)
    }
    function onKey(ev: KeyboardEvent) {
      if (ev.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDoc)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const count = items.length
  const triggerClass =
    className ??
    'inline-flex items-center justify-center rounded-md border border-[var(--color-border)] bg-transparent text-[var(--color-text-dim)] hover:text-[var(--color-text-strong)] hover:border-[var(--color-accent)]/50 hover:bg-[var(--color-surface-hover)] transition-colors relative'

  return (
    <div ref={wrapRef} className="relative" data-testid="notification-bell-wrap">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-label={
          count > 0
            ? `${count} notification${count === 1 ? '' : 's'} — open list`
            : 'No notifications'
        }
        aria-haspopup="menu"
        aria-expanded={open}
        title={
          count > 0
            ? `${count} notification${count === 1 ? '' : 's'}`
            : 'No notifications'
        }
        data-testid="notification-bell"
        data-count={count}
        className={triggerClass}
        style={{ width: 30, height: 30, padding: 0 }}
      >
        <Bell size={size} aria-hidden />
        {count > 0 ? (
          <span
            data-testid="notification-bell-badge"
            className="absolute -top-1 -right-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-[var(--color-danger)] px-1 text-[10px] font-semibold leading-none text-white"
            aria-hidden
          >
            {count > 9 ? '9+' : count}
          </span>
        ) : null}
      </button>
      {open && (
        <div
          data-testid="notification-bell-panel"
          role="menu"
          aria-label="Notifications"
          className="absolute right-0 top-[calc(100%+6px)] z-[2000] w-[26rem] max-w-[calc(100vw-1rem)] overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] shadow-2xl"
        >
          <div className="flex items-center justify-between border-b border-[var(--color-border)] px-3 py-2">
            <span className="text-sm font-semibold text-[var(--color-text-strong)]">
              Notifications
            </span>
            {count > 0 ? (
              <button
                type="button"
                data-testid="notification-bell-clear-all"
                onClick={() => dismissAll()}
                className="rounded-md border border-[var(--color-border)] bg-transparent px-2 py-0.5 text-[11px] text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
              >
                Clear all
              </button>
            ) : null}
          </div>
          <div className="max-h-[60vh] overflow-y-auto p-2">
            <NotificationListPanel
              items={items}
              dismiss={dismiss}
              variant="dropdown"
            />
          </div>
        </div>
      )}
    </div>
  )
}

/* ── List panel — used by both the bell dropdown and the page ──────── */

interface NotificationListPanelProps {
  items: readonly Notification[]
  dismiss: (id: string) => void
  /** `dropdown` keeps cards compact for the bell; `page` adds the
   *  relative timestamp row and per-card padding. */
  variant?: 'dropdown' | 'page'
}

export function NotificationListPanel({
  items,
  dismiss,
  variant = 'page',
}: NotificationListPanelProps) {
  if (items.length === 0) {
    return (
      <div
        data-testid="notification-list-empty"
        className="flex flex-col items-center justify-center gap-1 rounded-md border border-dashed border-[var(--color-border)] bg-[var(--color-bg)] p-6 text-center text-sm text-[var(--color-text-dim)]"
      >
        <p className="font-medium text-[var(--color-text)]">No notifications</p>
        <p className="text-xs">
          Provisioning failures and other status updates will land here.
        </p>
      </div>
    )
  }
  return (
    <div
      data-testid="notification-list"
      className="flex flex-col gap-2"
      aria-live="polite"
    >
      {items.map((n) => (
        <NotificationCard
          key={n.id}
          item={n}
          dismiss={dismiss}
          variant={variant}
        />
      ))}
    </div>
  )
}

interface NotificationCardProps {
  item: Notification
  dismiss: (id: string) => void
  variant: 'dropdown' | 'page'
}

function NotificationCard({ item, dismiss, variant }: NotificationCardProps) {
  const tone = TONE_BY_LEVEL[item.level]
  return (
    <div
      role={item.level === 'error' ? 'alert' : 'status'}
      data-testid={`notification-${item.id}`}
      data-level={item.level}
      className={`rounded-xl border ${tone.border} ${tone.surface} ${
        variant === 'page' ? 'p-4' : 'p-3'
      } text-sm text-[var(--color-text)]`}
    >
      <div className="flex items-start gap-2">
        <h3 className={`m-0 flex-1 text-sm font-semibold ${tone.title}`}>
          {item.title}
        </h3>
        {variant === 'page' ? (
          <span className="text-[11px] text-[var(--color-text-dim)]">
            {formatRelativeTime(item.createdAt)}
          </span>
        ) : null}
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

/**
 * Tiny relative-time formatter — avoids pulling in date-fns just to
 * say "2 minutes ago" in the notifications page. Cap at "just now"
 * for sub-minute durations and fall back to a locale string after a
 * day.
 */
function formatRelativeTime(ts: number): string {
  const delta = Math.max(0, Date.now() - ts)
  const sec = Math.floor(delta / 1000)
  if (sec < 60) return 'just now'
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} minute${min === 1 ? '' : 's'} ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr} hour${hr === 1 ? '' : 's'} ago`
  return new Date(ts).toLocaleString()
}
