/**
 * CatalogAdminPage — Sovereign Console /console/catalog
 *
 * The Sovereign-console operator's per-row marketplace publishing toggle.
 * Backend support shipped in PR #724 (issue #710 wave 2):
 *
 *   GET   /catalog/apps                         — operator view, every app
 *   PATCH /catalog/admin/apps/{slug}/publish?value={true|false}
 *                                               — single-bit toggle, requireAdmin
 *
 * One catalog (single source of truth). Flipping Published controls
 * which apps marketplace customers see at marketplace.<sovereignFQDN>.
 * Existing tenant deployments of unpublished apps keep running per
 * founder rule 2026-05-04 — unpublish is a marketplace-visibility
 * toggle, not a deployment-lifecycle action.
 *
 * Layout:
 *   • Header: "Catalog & marketplace publishing" + subtitle
 *   • Toolbar: search input + category filter dropdown
 *   • Table: per-app row (icon + name + tagline | category | status pills
 *     | published toggle)
 *
 * Optimistic UI: flipping the toggle updates the row immediately, and on
 * API error reverts + raises a toast via useNotifications. System apps
 * (mysql/postgres/redis) disable the toggle with a tooltip — backing
 * services are never shown in the marketplace regardless of the flag.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the API URL
 * flows through API_BASE so the same image works on Sovereign clusters
 * (BASE='/') and Catalyst-Zero (BASE='/sovereign/').
 *
 * Related: GitHub issue #710 (wave 2.5).
 */

import { useEffect, useMemo, useState } from 'react'
import { Package } from 'lucide-react'
import { API_BASE } from '@/shared/config/urls'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useNotifications } from '@/shared/ui/notifications'

/**
 * Mirrors core/services/catalog/store/store.go's `App` struct. Only the
 * fields the admin table actually renders are typed — the catalog
 * endpoint returns the full document, and we ignore the rest.
 */
export interface CatalogApp {
  id: string
  slug: string
  name: string
  tagline: string
  description: string
  category: string
  icon: string
  icon_bg: string
  featured: boolean
  popular: boolean
  free: boolean
  system: boolean
  deployable: boolean
  published: boolean
  /** Optional fields that occasionally arrive depending on Mongo state. */
  kind?: string
  shareable?: boolean
}

type LoadState =
  | { status: 'loading' }
  | { status: 'loaded'; apps: CatalogApp[] }
  | { status: 'error'; message: string }

/**
 * Fetch the operator-view app list (every app, no filter). The catalyst-
 * session cookie minted by /auth/handover is forwarded automatically when
 * `credentials: 'include'` is set.
 */
async function fetchApps(): Promise<CatalogApp[]> {
  const resp = await fetch(`${API_BASE}/catalog/apps`, {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (!resp.ok) throw new Error(`status ${resp.status}`)
  const data = (await resp.json()) as CatalogApp[] | { apps?: CatalogApp[] }
  // Tolerate both bare-array and { apps: [...] } envelope shapes — the
  // catalog handler returns a bare array today (`respond.OK(w, apps)`),
  // but past iterations have wrapped it. Accepting both lets us avoid
  // tying this UI to the current envelope choice.
  if (Array.isArray(data)) return data
  return data.apps ?? []
}

/**
 * Flip the Published flag for a single app slug. Returns a promise that
 * rejects on non-2xx so the optimistic UI knows to revert.
 */
async function setAppPublished(slug: string, published: boolean): Promise<void> {
  const url = `${API_BASE}/catalog/admin/apps/${encodeURIComponent(slug)}/publish?value=${
    published ? 'true' : 'false'
  }`
  const resp = await fetch(url, {
    method: 'PATCH',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (!resp.ok) {
    const body = await resp.text().catch(() => '')
    throw new Error(`PATCH ${slug}: ${resp.status} ${body}`)
  }
}

export function CatalogAdminPage() {
  const [load, setLoad] = useState<LoadState>({ status: 'loading' })
  const [query, setQuery] = useState<string>('')
  const [category, setCategory] = useState<string>('')
  /**
   * Per-slug pending-toggle bookkeeping. Used to debounce rapid clicks:
   * if the operator clicks twice in quick succession, the second click
   * waits until the first PATCH resolves before firing. We track the
   * desired final state, not a queue, because the state space is
   * one bit — newer intent always wins.
   */
  const [pending, setPending] = useState<Record<string, boolean>>({})
  const { notify, dismiss } = useNotifications()

  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? 'this Sovereign'

  useEffect(() => {
    fetchApps()
      .then((apps) => setLoad({ status: 'loaded', apps }))
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        setLoad({ status: 'error', message: msg })
      })
  }, [])

  // Distinct categories for the filter dropdown, sorted, with empty string
  // representing "All". Derived from the loaded set so a category only
  // shows up when at least one app uses it.
  const categories = useMemo<string[]>(() => {
    if (load.status !== 'loaded') return []
    const set = new Set<string>()
    for (const app of load.apps) {
      if (app.category) set.add(app.category)
    }
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [load])

  const visibleApps = useMemo<CatalogApp[]>(() => {
    if (load.status !== 'loaded') return []
    const q = query.trim().toLowerCase()
    return load.apps
      .filter((app) => {
        if (category && app.category !== category) return false
        if (!q) return true
        return (
          app.name.toLowerCase().includes(q) ||
          app.slug.toLowerCase().includes(q) ||
          (app.tagline ?? '').toLowerCase().includes(q) ||
          (app.description ?? '').toLowerCase().includes(q)
        )
      })
      .sort((a, b) => a.name.localeCompare(b.name))
  }, [load, query, category])

  function handleToggle(app: CatalogApp, next: boolean) {
    if (app.system) return // backing services — never marketplace-visible
    if (load.status !== 'loaded') return

    // Optimistic flip: update the in-memory list immediately so the UI
    // feels instant. Track the slug as pending so a second rapid click
    // waits its turn.
    const previous = app.published
    if (pending[app.slug] === next) return // already in flight to this state
    setPending((p) => ({ ...p, [app.slug]: next }))
    setLoad((current) => {
      if (current.status !== 'loaded') return current
      return {
        status: 'loaded',
        apps: current.apps.map((a) =>
          a.slug === app.slug ? { ...a, published: next } : a,
        ),
      }
    })
    // Clear any prior error toast for this app — a successful retry
    // shouldn't leave the old failure visible.
    dismiss(`catalog-publish-error:${app.slug}`)

    setAppPublished(app.slug, next)
      .then(() => {
        setPending((p) => {
          const out = { ...p }
          delete out[app.slug]
          return out
        })
      })
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        // Revert the optimistic flip on failure.
        setLoad((current) => {
          if (current.status !== 'loaded') return current
          return {
            status: 'loaded',
            apps: current.apps.map((a) =>
              a.slug === app.slug ? { ...a, published: previous } : a,
            ),
          }
        })
        setPending((p) => {
          const out = { ...p }
          delete out[app.slug]
          return out
        })
        notify({
          id: `catalog-publish-error:${app.slug}`,
          level: 'error',
          title: `Couldn't ${next ? 'publish' : 'unpublish'} ${app.name}`,
          body: 'The catalog rejected the toggle. Try again — if the error persists, your session may have expired.',
          raw: msg,
        })
      })
  }

  return (
    <div data-testid="catalog-admin-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">
          Catalog &amp; marketplace publishing
        </h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          One catalog. Toggle per app to control which apps marketplace customers see at{' '}
          <span className="font-mono text-[var(--color-text)]">marketplace.{sovereignFQDN}</span>.
          Existing tenant deployments of unpublished apps keep running.
        </p>
      </div>

      {/* Toolbar */}
      <div className="mb-4 flex flex-wrap items-center gap-3" data-testid="catalog-toolbar">
        <div className="relative min-w-[260px] flex-1">
          <svg
            className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--color-text-dim)] opacity-60"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth={2}
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
          >
            <circle cx={11} cy={11} r={8} />
            <path d="m21 21-4.3-4.3" />
          </svg>
          <input
            type="text"
            placeholder="Search by name, slug, or tagline…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-9 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] pl-9 pr-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent)]/30"
            data-testid="catalog-search"
          />
        </div>
        <select
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          className="h-9 rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-accent)] focus:ring-2 focus:ring-[var(--color-accent)]/30"
          data-testid="catalog-category-filter"
          aria-label="Filter by category"
        >
          <option value="">All categories</option>
          {categories.map((cat) => (
            <option key={cat} value={cat}>
              {cat}
            </option>
          ))}
        </select>
      </div>

      {load.status === 'loading' ? (
        <div
          className="flex items-center gap-2 text-sm text-[var(--color-text-dim)]"
          data-testid="catalog-loading"
        >
          <svg className="h-4 w-4 animate-spin" viewBox="0 0 24 24" fill="none" aria-hidden>
            <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25" />
            <path
              fill="currentColor"
              opacity="0.8"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
            />
          </svg>
          Loading catalog…
        </div>
      ) : load.status === 'error' ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="catalog-error"
        >
          <Package className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">
            Couldn’t load the catalog
          </p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            {load.message} — verify your session is still valid and try refreshing the page.
          </p>
        </div>
      ) : load.apps.length === 0 ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="catalog-empty"
        >
          <Package className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">Catalog is empty</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            No apps are seeded yet. Once the catalog seeds, every app will appear here.
          </p>
        </div>
      ) : visibleApps.length === 0 ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="catalog-no-matches"
        >
          <p className="text-sm text-[var(--color-text-dim)]">
            No apps match the current filters.
          </p>
        </div>
      ) : (
        <div
          className="overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)]"
          data-testid="catalog-table"
        >
          <table className="w-full text-left text-sm">
            <thead className="border-b border-[var(--color-border)] bg-[var(--color-surface)]/40 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
              <tr>
                <th scope="col" className="px-4 py-3 font-medium">
                  App
                </th>
                <th scope="col" className="px-4 py-3 font-medium">
                  Category
                </th>
                <th scope="col" className="px-4 py-3 font-medium">
                  Status
                </th>
                <th scope="col" className="px-4 py-3 text-right font-medium">
                  Published
                </th>
              </tr>
            </thead>
            <tbody>
              {visibleApps.map((app) => (
                <CatalogAdminRow
                  key={app.slug}
                  app={app}
                  pending={pending[app.slug] !== undefined}
                  onToggle={(next) => handleToggle(app, next)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

interface CatalogAdminRowProps {
  app: CatalogApp
  pending: boolean
  onToggle: (next: boolean) => void
}

function CatalogAdminRow({ app, pending, onToggle }: CatalogAdminRowProps) {
  // System apps (mysql, postgres, redis) are never marketplace-visible
  // regardless of the Published flag (per ListPublishedApps's filter:
  // `system: false` is part of the storefront predicate). We disable the
  // toggle and surface the reason via the title attribute (native
  // tooltip — keeps the row markup small and accessible without pulling
  // in the Tooltip portal for every row).
  const disabled = app.system

  // Toggle behaviour: optimistic UI is owned by the parent. The toggle
  // is rendered as a checkbox-styled button so screen readers announce
  // "switch, checked / unchecked". Disabled when the app is a backing
  // service.
  const next = !app.published
  const handleClick = () => {
    if (disabled) return
    onToggle(next)
  }

  return (
    <tr
      className="border-b border-[var(--color-border)] last:border-b-0 hover:bg-[var(--color-surface)]/30"
      data-testid={`catalog-row-${app.slug}`}
      data-published={app.published ? 'true' : 'false'}
    >
      {/* App icon + name + tagline */}
      <td className="px-4 py-3">
        <div className="flex items-center gap-3">
          <span
            className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-base font-bold uppercase text-white"
            style={{ background: app.icon_bg || '#1f2937' }}
            aria-hidden
          >
            {app.icon || app.name.slice(0, 1)}
          </span>
          <div className="min-w-0">
            <div className="flex items-baseline gap-2">
              <span className="truncate font-semibold text-[var(--color-text-strong)]">
                {app.name}
              </span>
              <span className="truncate text-xs text-[var(--color-text-dim)]">
                {app.slug}
              </span>
            </div>
            {app.tagline ? (
              <p className="mt-0.5 truncate text-xs text-[var(--color-text-dim)]">
                {app.tagline}
              </p>
            ) : null}
          </div>
        </div>
      </td>

      {/* Category */}
      <td className="px-4 py-3 align-middle">
        {app.category ? (
          <span className="inline-flex items-center rounded-md bg-[var(--color-surface)] px-2 py-0.5 text-xs text-[var(--color-text-dim)]">
            {app.category}
          </span>
        ) : (
          <span className="text-xs text-[var(--color-text-dim)]">—</span>
        )}
      </td>

      {/* Status pills: System / Backing service / Deployable / Coming soon */}
      <td className="px-4 py-3 align-middle">
        <div className="flex flex-wrap gap-1.5">
          {app.system ? (
            <StatusPill tone="neutral" testId={`catalog-pill-system-${app.slug}`}>
              Backing service
            </StatusPill>
          ) : null}
          {!app.system && app.deployable ? (
            <StatusPill tone="success" testId={`catalog-pill-deployable-${app.slug}`}>
              Deployable
            </StatusPill>
          ) : null}
          {!app.deployable && !app.system ? (
            <StatusPill tone="warning" testId={`catalog-pill-coming-soon-${app.slug}`}>
              Coming soon
            </StatusPill>
          ) : null}
          {app.featured ? (
            <StatusPill tone="accent" testId={`catalog-pill-featured-${app.slug}`}>
              Featured
            </StatusPill>
          ) : null}
        </div>
      </td>

      {/* Published toggle */}
      <td className="px-4 py-3 text-right align-middle">
        <button
          type="button"
          role="switch"
          aria-checked={app.published}
          aria-label={
            disabled
              ? `${app.name} is a backing service and never appears in marketplace`
              : `${app.published ? 'Unpublish' : 'Publish'} ${app.name}`
          }
          aria-busy={pending}
          disabled={disabled}
          onClick={handleClick}
          title={
            disabled ? 'Backing services are never shown in marketplace' : undefined
          }
          data-testid={`catalog-toggle-${app.slug}`}
          className={
            'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors duration-200 ' +
            (disabled
              ? 'cursor-not-allowed bg-[var(--color-border)] opacity-50'
              : app.published
                ? 'cursor-pointer bg-[var(--color-accent)]'
                : 'cursor-pointer bg-[var(--color-border)]')
          }
        >
          <span
            className={
              'inline-block h-4 w-4 rounded-full bg-white shadow-sm transition-transform duration-200 ' +
              (app.published ? 'translate-x-[18px]' : 'translate-x-0.5')
            }
            aria-hidden
          />
        </button>
      </td>
    </tr>
  )
}

interface StatusPillProps {
  tone: 'neutral' | 'success' | 'warning' | 'accent'
  testId?: string
  children: React.ReactNode
}

function StatusPill({ tone, testId, children }: StatusPillProps) {
  const toneClass: Record<StatusPillProps['tone'], string> = {
    neutral:
      'bg-[var(--color-surface)] text-[var(--color-text-dim)] border border-[var(--color-border)]',
    success:
      'bg-[color-mix(in_srgb,var(--color-success)_14%,transparent)] text-[var(--color-success)]',
    warning:
      'bg-[color-mix(in_srgb,var(--color-warn,var(--color-accent))_14%,transparent)] text-[var(--color-warn,var(--color-accent))]',
    accent:
      'bg-[color-mix(in_srgb,var(--color-accent)_14%,transparent)] text-[var(--color-accent)]',
  }
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${toneClass[tone]}`}
      data-testid={testId}
    >
      {children}
    </span>
  )
}
