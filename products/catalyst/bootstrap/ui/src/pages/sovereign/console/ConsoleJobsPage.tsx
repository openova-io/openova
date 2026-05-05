/**
 * ConsoleJobsPage — Sovereign Console /console/jobs.
 *
 * Reads /api/v1/sovereign/jobs (issue #933) which surfaces THREE
 * signal sources from the local cluster:
 *
 *   1. Flux HelmRelease Ready/NotReady transitions (the bootstrap-kit
 *      install path the operator just lived through)
 *   2. K8s Jobs across all namespaces (Helm post-install hooks, any
 *      operator-authored Job/CronJob runs)
 *   3. K8s Warning Events (operator-visible cluster anomalies)
 *
 * Sorted started-DESC. Each row carries kind / status / message so the
 * operator sees their cluster's full activity feed without round-tripping
 * to the mothership.
 *
 * Phase 8b shipped this page as a placeholder; #933 wires the actual
 * data source. The PortalShell + sidebar surrounding this view stays
 * unchanged.
 */

import { useEffect, useState } from 'react'
import { Clipboard, AlertCircle, CheckCircle2, RefreshCw } from 'lucide-react'
import { API_BASE } from '@/shared/config/urls'
import { loadTokens } from '@/shared/lib/oidc'

interface SovereignJob {
  id: string
  name: string
  namespace: string
  kind: string
  status: string
  message?: string
  startedAt: string
  finishedAt?: string
}

type PageState =
  | { status: 'loading' }
  | { status: 'loaded'; jobs: SovereignJob[] }
  | { status: 'error'; message: string }

async function fetchJobs(): Promise<SovereignJob[]> {
  const tokens = loadTokens()
  const resp = await fetch(`${API_BASE}/v1/sovereign/jobs`, {
    headers: {
      Accept: 'application/json',
      ...(tokens ? { Authorization: `Bearer ${tokens.accessToken}` } : {}),
    },
  })
  if (!resp.ok) throw new Error(`status ${resp.status}`)
  const body = (await resp.json()) as { jobs?: SovereignJob[] }
  return body.jobs ?? []
}

const STATUS_BADGE: Record<string, { color: string; label: string }> = {
  succeeded: { color: 'text-green-400', label: 'Succeeded' },
  installed: { color: 'text-green-400', label: 'Installed' },
  failed: { color: 'text-red-400', label: 'Failed' },
  running: { color: 'text-blue-400', label: 'Running' },
  pending: { color: 'text-amber-400', label: 'Pending' },
  warning: { color: 'text-amber-400', label: 'Warning' },
}

function formatTimestamp(iso?: string): string {
  if (!iso) return '—'
  try {
    const t = new Date(iso)
    if (Number.isNaN(t.getTime())) return '—'
    return t.toLocaleString()
  } catch {
    return iso
  }
}

export function ConsoleJobsPage() {
  const [state, setState] = useState<PageState>({ status: 'loading' })

  const reload = () => {
    setState({ status: 'loading' })
    fetchJobs()
      .then((jobs) => setState({ status: 'loaded', jobs }))
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        setState({ status: 'error', message: msg })
      })
  }

  useEffect(() => {
    reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div data-testid="console-jobs-page">
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Jobs</h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Provisioning + maintenance + warning events from the local cluster.
          </p>
        </div>
        <button
          type="button"
          onClick={reload}
          className="flex h-9 items-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 text-sm text-[var(--color-text)] hover:bg-[var(--color-bg-3)]"
          data-testid="jobs-refresh"
        >
          <RefreshCw className="h-4 w-4" />
          Refresh
        </button>
      </div>

      {state.status === 'loading' ? (
        <div className="flex items-center gap-2 text-sm text-[var(--color-text-dim)]" data-testid="jobs-loading">
          <RefreshCw className="h-4 w-4 animate-spin" />
          Loading jobs…
        </div>
      ) : state.status === 'error' ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="jobs-error"
        >
          <AlertCircle className="mx-auto mb-3 h-10 w-10 text-red-400" />
          <p className="text-sm font-medium text-[var(--color-text)]">Couldn’t load jobs</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">{state.message}</p>
        </div>
      ) : state.jobs.length === 0 ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="jobs-empty"
        >
          <Clipboard className="mx-auto mb-3 h-10 w-10 text-[var(--color-text-dim)]" />
          <p className="text-sm font-medium text-[var(--color-text)]">No jobs to display</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">
            Provisioning + maintenance activity from the local cluster will appear here.
          </p>
        </div>
      ) : (
        <div
          className="overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)]"
          data-testid="jobs-table"
        >
          <table className="w-full text-sm">
            <thead className="bg-[var(--color-bg)] text-left text-xs uppercase text-[var(--color-text-dim)]">
              <tr>
                <th className="px-4 py-2">Name</th>
                <th className="px-4 py-2">Kind</th>
                <th className="px-4 py-2">Namespace</th>
                <th className="px-4 py-2">Status</th>
                <th className="px-4 py-2">Started</th>
                <th className="px-4 py-2">Message</th>
              </tr>
            </thead>
            <tbody>
              {state.jobs.map((j) => {
                const badge = STATUS_BADGE[j.status] ?? { color: 'text-[var(--color-text)]', label: j.status }
                return (
                  <tr
                    key={j.id}
                    className="border-t border-[var(--color-border)]"
                    data-testid={`job-row-${j.id}`}
                  >
                    <td className="px-4 py-2 font-medium text-[var(--color-text-strong)]">{j.name}</td>
                    <td className="px-4 py-2 text-[var(--color-text-dim)]">{j.kind}</td>
                    <td className="px-4 py-2 text-[var(--color-text-dim)]">{j.namespace || '—'}</td>
                    <td className={`px-4 py-2 font-semibold uppercase ${badge.color}`}>
                      <span className="inline-flex items-center gap-1">
                        {j.status === 'succeeded' || j.status === 'installed' ? (
                          <CheckCircle2 className="h-3.5 w-3.5" />
                        ) : null}
                        {badge.label}
                      </span>
                    </td>
                    <td className="px-4 py-2 text-[var(--color-text-dim)]">{formatTimestamp(j.startedAt)}</td>
                    <td className="px-4 py-2 text-xs text-[var(--color-text-dim)]">
                      <div className="max-w-md truncate" title={j.message}>
                        {j.message ?? ''}
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
