/**
 * ConsoleAppsPage — Sovereign Console /console/apps
 *
 * Lists installed apps (HTTPRoutes → backing services) on the Sovereign.
 * Reuses the same visual shape as AppsPage.tsx but without a deploymentId
 * param — in Sovereign mode the cluster is implicit from the hostname.
 *
 * Phase 8b: renders a placeholder card grid until the
 * /api/v1/sovereign/apps endpoint (Agent C) is implemented.
 *
 * Related: GitHub issue #607
 */

import { useEffect, useState } from 'react'
import { Package } from 'lucide-react'
import { API_BASE } from '@/shared/config/urls'
import { loadTokens } from '@/shared/lib/oidc'

interface AppRow {
  id: string
  name: string
  version: string
  status: 'ready' | 'degraded' | 'pending' | 'failed'
  url?: string
}

type PageState =
  | { status: 'loading' }
  | { status: 'loaded'; apps: AppRow[] }
  | { status: 'error'; message: string }

async function fetchApps(): Promise<AppRow[]> {
  const tokens = loadTokens()
  const resp = await fetch(`${API_BASE}/v1/sovereign/apps`, {
    headers: {
      Accept: 'application/json',
      ...(tokens ? { Authorization: `Bearer ${tokens.accessToken}` } : {}),
    },
  })
  if (!resp.ok) throw new Error(`status ${resp.status}`)
  return resp.json() as Promise<AppRow[]>
}

const STATUS_COLORS: Record<AppRow['status'], string> = {
  ready: 'text-green-400',
  degraded: 'text-amber-400',
  pending: 'text-blue-400',
  failed: 'text-red-400',
}

export function ConsoleAppsPage() {
  const [state, setState] = useState<PageState>({ status: 'loading' })

  useEffect(() => {
    fetchApps()
      .then((apps) => setState({ status: 'loaded', apps }))
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        setState({ status: 'error', message: msg })
      })
  }, [])

  return (
    <div data-testid="console-apps-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Applications</h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Installed apps on this Sovereign cluster.
        </p>
      </div>

      {state.status === 'loading' ? (
        <div className="flex items-center gap-2 text-sm text-[var(--color-text-dim)]" data-testid="apps-loading">
          <svg className="h-4 w-4 animate-spin" viewBox="0 0 24 24" fill="none" aria-hidden>
            <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25" />
            <path fill="currentColor" opacity="0.8" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
          </svg>
          Loading apps…
        </div>
      ) : state.status === 'error' ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="apps-api-placeholder"
        >
          <Package className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">Apps API not yet available</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            {state.message} — /api/v1/sovereign/apps integration pending.
          </p>
        </div>
      ) : state.apps.length === 0 ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="apps-empty"
        >
          <Package className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">No apps installed</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            Apps installed via the Wizard will appear here.
          </p>
        </div>
      ) : (
        <div
          className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3"
          data-testid="apps-grid"
        >
          {state.apps.map((app) => (
            <div
              key={app.id}
              className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
              data-testid={`app-card-${app.id}`}
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <p className="truncate text-sm font-semibold text-[var(--color-text-strong)]">
                    {app.name}
                  </p>
                  <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">{app.version}</p>
                </div>
                <span
                  className={`shrink-0 text-xs font-semibold uppercase ${STATUS_COLORS[app.status]}`}
                  data-testid={`app-status-${app.id}`}
                >
                  {app.status}
                </span>
              </div>
              {app.url ? (
                <a
                  href={app.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="mt-3 block truncate text-xs text-[var(--color-accent)] hover:underline"
                >
                  {app.url}
                </a>
              ) : null}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
