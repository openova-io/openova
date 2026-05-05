/**
 * ConsoleAppsPage — Sovereign Console /console/apps.
 *
 * Reads /api/v1/sovereign/apps (issue #933). The endpoint joins the
 * embedded Blueprint catalog (same data the wizard's StepComponents
 * renders) with the local cluster's HelmRelease state, so:
 *
 *   - "installed"  → bp-* HR present + Ready=True
 *   - "installing" → bp-* HR present, Ready=False or pending
 *   - "bootstrap"  → bootstrap-kit member (always installed; cannot
 *                    be uninstalled via UI)
 *   - "available"  → listed in catalog, no matching HR yet
 *
 * The page renders the FULL marketplace card grid an operator expects
 * after handover — no need to reach the mothership, no waiting for
 * the gitea-mirror cutover step. This is the "Apps" surface that
 * makes a fresh Sovereign immediately useful.
 */

import { useEffect, useMemo, useState } from 'react'
import { Package, Search, Filter, RefreshCw, AlertCircle, CheckCircle2 } from 'lucide-react'
import { API_BASE } from '@/shared/config/urls'
import { loadTokens } from '@/shared/lib/oidc'

interface AppRow {
  id: string
  slug: string
  title: string
  summary: string
  category?: string
  tagline?: string
  tags?: string[]
  version?: string
  section?: string
  depends?: string[]
  status: 'installed' | 'installing' | 'available' | 'bootstrap'
  bootstrapKit: boolean
}

interface ApiResponse {
  apps: AppRow[]
  generatedAt?: string
  bootstrapKit?: string[]
}

type PageState =
  | { status: 'loading' }
  | { status: 'loaded'; apps: AppRow[]; generatedAt?: string }
  | { status: 'error'; message: string }

async function fetchApps(): Promise<ApiResponse> {
  const tokens = loadTokens()
  const resp = await fetch(`${API_BASE}/v1/sovereign/apps`, {
    headers: {
      Accept: 'application/json',
      ...(tokens ? { Authorization: `Bearer ${tokens.accessToken}` } : {}),
    },
  })
  if (!resp.ok) throw new Error(`status ${resp.status}`)
  return (await resp.json()) as ApiResponse
}

const STATUS_LABEL: Record<AppRow['status'], { color: string; text: string }> = {
  installed: { color: 'text-green-400', text: 'Installed' },
  installing: { color: 'text-blue-400', text: 'Installing' },
  available: { color: 'text-[var(--color-text-dim)]', text: 'Available' },
  bootstrap: { color: 'text-cyan-400', text: 'Bootstrap' },
}

export function ConsoleAppsPage() {
  const [state, setState] = useState<PageState>({ status: 'loading' })
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<'all' | AppRow['status']>('all')

  const reload = () => {
    setState({ status: 'loading' })
    fetchApps()
      .then((body) => setState({ status: 'loaded', apps: body.apps ?? [], generatedAt: body.generatedAt }))
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        setState({ status: 'error', message: msg })
      })
  }

  useEffect(() => {
    reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const filteredApps = useMemo(() => {
    if (state.status !== 'loaded') return []
    const q = search.trim().toLowerCase()
    return state.apps.filter((a) => {
      if (filter !== 'all' && a.status !== filter) return false
      if (!q) return true
      return (
        a.title.toLowerCase().includes(q) ||
        a.id.toLowerCase().includes(q) ||
        (a.category ?? '').toLowerCase().includes(q) ||
        (a.tags ?? []).some((t) => t.toLowerCase().includes(q))
      )
    })
  }, [state, search, filter])

  return (
    <div data-testid="console-apps-page">
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Applications</h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Installable Blueprints + currently-installed apps on this Sovereign cluster.
          </p>
        </div>
        <button
          type="button"
          onClick={reload}
          className="flex h-9 items-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 text-sm text-[var(--color-text)] hover:bg-[var(--color-bg-3)]"
          data-testid="apps-refresh"
        >
          <RefreshCw className="h-4 w-4" />
          Refresh
        </button>
      </div>

      {/* toolbar */}
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <div className="relative flex-1 min-w-[200px]">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[var(--color-text-dim)]" />
          <input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search apps…"
            className="h-9 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] pl-9 pr-3 text-sm text-[var(--color-text)] placeholder-[var(--color-text-dim)]"
            data-testid="apps-search"
          />
        </div>
        <div className="flex items-center gap-2">
          <Filter className="h-4 w-4 text-[var(--color-text-dim)]" />
          {(['all', 'installed', 'installing', 'available', 'bootstrap'] as const).map((k) => (
            <button
              key={k}
              type="button"
              onClick={() => setFilter(k)}
              className={`h-9 rounded-lg border px-3 text-xs uppercase transition-colors ${
                filter === k
                  ? 'border-[var(--color-accent)] bg-[var(--color-accent)]/10 text-[var(--color-accent)]'
                  : 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-dim)] hover:bg-[var(--color-bg-3)]'
              }`}
              data-testid={`apps-filter-${k}`}
            >
              {k}
            </button>
          ))}
        </div>
      </div>

      {state.status === 'loading' ? (
        <div className="flex items-center gap-2 text-sm text-[var(--color-text-dim)]" data-testid="apps-loading">
          <RefreshCw className="h-4 w-4 animate-spin" />
          Loading apps…
        </div>
      ) : state.status === 'error' ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="apps-error"
        >
          <AlertCircle className="mx-auto mb-3 h-10 w-10 text-red-400" />
          <p className="text-sm font-medium text-[var(--color-text)]">Couldn’t load apps</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">{state.message}</p>
        </div>
      ) : filteredApps.length === 0 ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="apps-empty"
        >
          <Package className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">No matching apps</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            {search ? 'Try a different search term.' : 'No apps in this filter.'}
          </p>
        </div>
      ) : (
        <div
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
          data-testid="apps-grid"
        >
          {filteredApps.map((app) => {
            const badge = STATUS_LABEL[app.status]
            return (
              <div
                key={app.id}
                className="flex h-full flex-col rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
                data-testid={`app-card-${app.id}`}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="truncate text-sm font-semibold text-[var(--color-text-strong)]">
                      {app.title}
                    </p>
                    <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
                      {app.id}
                      {app.version ? ` · v${app.version}` : ''}
                    </p>
                  </div>
                  <span
                    className={`shrink-0 text-xs font-semibold uppercase ${badge.color}`}
                    data-testid={`app-status-${app.id}`}
                  >
                    <span className="inline-flex items-center gap-1">
                      {(app.status === 'installed' || app.status === 'bootstrap') && (
                        <CheckCircle2 className="h-3.5 w-3.5" />
                      )}
                      {badge.text}
                    </span>
                  </span>
                </div>
                {app.summary ? (
                  <p className="mt-2 line-clamp-3 text-xs text-[var(--color-text-dim)]">{app.summary}</p>
                ) : null}
                <div className="mt-auto pt-3 flex flex-wrap items-center gap-2">
                  {app.category ? (
                    <span className="rounded-full border border-[var(--color-border)] px-2 py-0.5 text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]">
                      {app.category}
                    </span>
                  ) : null}
                  {app.status === 'available' ? (
                    <button
                      type="button"
                      className="ml-auto h-7 rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)]/10 px-3 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent)]/20"
                      data-testid={`app-install-${app.id}`}
                    >
                      Install
                    </button>
                  ) : null}
                </div>
              </div>
            )
          })}
        </div>
      )}

      {state.status === 'loaded' && state.generatedAt ? (
        <p className="mt-6 text-xs text-[var(--color-text-dim)]" data-testid="apps-catalog-meta">
          Catalog snapshot generated {new Date(state.generatedAt).toLocaleString()}.
        </p>
      ) : null}
    </div>
  )
}
