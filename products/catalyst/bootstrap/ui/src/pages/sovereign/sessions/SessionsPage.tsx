/**
 * SessionsPage — EPIC-4 Slice E3 (#1099). Lists all Guacamole shell
 * sessions for the current Sovereign with timestamp / user / pod /
 * duration / recording-available / Replay action.
 *
 * Replay is RBAC-gated server-side (admin or owner tier ⇒
 * `sessions.playback`). UI hides the button when the caller doesn't
 * have it; the server is the authoritative gate.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state) — filter + replay shipped at first cut.
 *   #4 (never hardcode) — every URL via resource.api.ts.
 */

import { useEffect, useMemo, useState } from 'react'

import {
  listSessions,
  getSessionReplay,
  type SessionListItem,
  type SessionListResponse,
  type SessionListFilter,
  type SessionReplayResponse,
} from '@/pages/sovereign/cloud-list/resource.api'

export interface SessionsPageProps {
  deploymentId: string
  /** Whether the operator has `sessions.playback` (admin/owner). */
  canReplay?: boolean
  /** Test seam — substitute the list call. */
  listFn?: typeof listSessions
  /** Test seam — substitute the replay call. */
  replayFn?: typeof getSessionReplay
}

export function SessionsPage({
  deploymentId,
  canReplay = true,
  listFn = listSessions,
  replayFn = getSessionReplay,
}: SessionsPageProps) {
  const [data, setData] = useState<SessionListResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<SessionListFilter>({ page: 1, pageSize: 25 })
  const [replay, setReplay] = useState<SessionReplayResponse | null>(null)
  const [replayErr, setReplayErr] = useState<string | null>(null)

  const filterKey = useMemo(() => JSON.stringify(filter), [filter])

  useEffect(() => {
    if (!deploymentId) return
    let cancelled = false
    const ac = new AbortController()
    setLoading(true)
    listFn(deploymentId, filter, ac.signal)
      .then((d) => {
        if (cancelled) return
        setData(d)
        setError(null)
      })
      .catch((err) => {
        if (cancelled) return
        if (ac.signal.aborted) return
        setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
      ac.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deploymentId, filterKey])

  async function onReplay(sessionId: string) {
    setReplayErr(null)
    setReplay(null)
    try {
      const r = await replayFn(deploymentId, sessionId)
      setReplay(r)
    } catch (e) {
      setReplayErr(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <div className="space-y-4 p-4" data-testid="sessions-page">
      <header className="space-y-1">
        <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">Shell Sessions</h2>
        <p className="text-sm text-[var(--color-text-dim)]">
          Recorded kubectl-exec sessions through Apache Guacamole. Per ADR-0001
          §11 there is one Guacamole per Sovereign — every session here lived
          inside this cluster's namespace boundary.
        </p>
      </header>

      <SessionsFilterBar
        value={filter}
        onChange={(next) => setFilter({ ...next, page: 1, pageSize: filter.pageSize })}
      />

      {loading && (
        <div data-testid="sessions-loading" className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 text-sm text-[var(--color-text-dim)]">
          Loading sessions…
        </div>
      )}
      {error && (
        <div data-testid="sessions-error" className="rounded border border-rose-500 bg-[var(--color-bg-2)] p-4 text-sm text-rose-300">
          {error}
        </div>
      )}
      {!loading && data && (
        <SessionsTable
          items={data.items}
          canReplay={canReplay}
          onReplay={onReplay}
        />
      )}
      {!loading && data && (
        <Pagination
          page={data.page}
          pageSize={data.pageSize}
          total={data.total}
          onPage={(p) => setFilter((f) => ({ ...f, page: p }))}
        />
      )}

      {replay && (
        <ReplayModal
          replay={replay}
          onClose={() => {
            setReplay(null)
            setReplayErr(null)
          }}
        />
      )}
      {replayErr && (
        <div data-testid="sessions-replay-error" className="rounded border border-rose-500 bg-[var(--color-bg-2)] p-3 text-sm text-rose-300">
          Replay failed: {replayErr}
        </div>
      )}
    </div>
  )
}

interface FilterBarProps {
  value: SessionListFilter
  onChange: (next: SessionListFilter) => void
}

function SessionsFilterBar({ value, onChange }: FilterBarProps) {
  const [pod, setPod] = useState(value.pod ?? '')
  const [user, setUser] = useState(value.user ?? '')
  return (
    <form
      data-testid="sessions-filter"
      className="flex flex-wrap gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        onChange({ ...value, pod: pod || undefined, user: user || undefined })
      }}
    >
      <input
        data-testid="sessions-filter-pod"
        value={pod}
        onChange={(e) => setPod(e.target.value)}
        placeholder="Filter by pod"
        className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
      />
      <input
        data-testid="sessions-filter-user"
        value={user}
        onChange={(e) => setUser(e.target.value)}
        placeholder="Filter by user"
        className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)]"
      />
      <button
        type="submit"
        data-testid="sessions-filter-apply"
        className="rounded bg-[var(--color-accent)] px-3 py-1 text-xs font-medium text-[var(--color-accent-text)]"
      >
        Apply
      </button>
    </form>
  )
}

interface TableProps {
  items: SessionListItem[]
  canReplay: boolean
  onReplay: (sessionId: string) => void
}

function SessionsTable({ items, canReplay, onReplay }: TableProps) {
  if (items.length === 0) {
    return (
      <div data-testid="sessions-empty" className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 text-sm text-[var(--color-text-dim)]">
        No sessions yet. Sessions appear after an operator opens a shell from
        any Pod's Exec tab.
      </div>
    )
  }
  return (
    <div className="overflow-x-auto rounded border border-[var(--color-border)]">
      <table className="min-w-full text-sm" data-testid="sessions-table">
        <thead className="bg-[var(--color-bg-2)] text-[var(--color-text-dim)]">
          <tr>
            <th className="px-3 py-2 text-left">Started</th>
            <th className="px-3 py-2 text-left">User</th>
            <th className="px-3 py-2 text-left">Pod</th>
            <th className="px-3 py-2 text-left">Container</th>
            <th className="px-3 py-2 text-right">Duration (s)</th>
            <th className="px-3 py-2 text-left">Recording</th>
            <th className="px-3 py-2 text-left">Action</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--color-border)]">
          {items.map((s) => (
            <tr key={s.sessionId} data-testid={`sessions-row-${s.sessionId}`}>
              <td className="px-3 py-2 font-mono text-xs">{s.started}</td>
              <td className="px-3 py-2">{s.user || '—'}</td>
              <td className="px-3 py-2 font-mono text-xs">{s.pod}</td>
              <td className="px-3 py-2 font-mono text-xs">{s.container}</td>
              <td className="px-3 py-2 text-right">{s.durationSeconds}</td>
              <td className="px-3 py-2">
                {s.recordingAvailable ? (
                  <span className="rounded bg-emerald-500/20 px-2 py-0.5 text-xs text-emerald-300">
                    Available
                  </span>
                ) : (
                  <span className="rounded bg-zinc-500/20 px-2 py-0.5 text-xs text-zinc-300">
                    None
                  </span>
                )}
              </td>
              <td className="px-3 py-2">
                {canReplay ? (
                  <button
                    type="button"
                    data-testid={`sessions-row-replay-${s.sessionId}`}
                    onClick={() => onReplay(s.sessionId)}
                    disabled={!s.recordingAvailable}
                    className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs text-[var(--color-text)] disabled:opacity-50"
                  >
                    Replay
                  </button>
                ) : (
                  <span data-testid={`sessions-row-replay-locked-${s.sessionId}`} className="text-xs text-[var(--color-text-dim)]">
                    Replay restricted
                  </span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

interface PageProps {
  page: number
  pageSize: number
  total: number
  onPage: (next: number) => void
}

function Pagination({ page, pageSize, total, onPage }: PageProps) {
  const lastPage = Math.max(1, Math.ceil(total / pageSize))
  return (
    <div className="flex items-center gap-2 text-xs text-[var(--color-text-dim)]" data-testid="sessions-pagination">
      <span>
        Page {page} of {lastPage} · {total} sessions
      </span>
      <button
        type="button"
        data-testid="sessions-pagination-prev"
        onClick={() => onPage(Math.max(1, page - 1))}
        disabled={page <= 1}
        className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 disabled:opacity-50"
      >
        Prev
      </button>
      <button
        type="button"
        data-testid="sessions-pagination-next"
        onClick={() => onPage(Math.min(lastPage, page + 1))}
        disabled={page >= lastPage}
        className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 disabled:opacity-50"
      >
        Next
      </button>
    </div>
  )
}

interface ReplayModalProps {
  replay: SessionReplayResponse
  onClose: () => void
}

function ReplayModal({ replay, onClose }: ReplayModalProps) {
  return (
    <div
      data-testid="sessions-replay-modal"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-6"
      onClick={onClose}
    >
      <div
        className="max-h-[90vh] w-full max-w-5xl rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-base font-semibold text-[var(--color-text-strong)]">
            Replay session {replay.sessionId}
          </h3>
          <button
            type="button"
            data-testid="sessions-replay-close"
            onClick={onClose}
            className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs"
          >
            Close
          </button>
        </div>
        {replay.available && replay.embedURL ? (
          <iframe
            data-testid="sessions-replay-iframe"
            src={replay.embedURL}
            title={`Replay ${replay.sessionId}`}
            className="h-[70vh] w-full rounded border border-[var(--color-border)] bg-black"
          />
        ) : (
          <div className="rounded bg-amber-500/10 p-4 text-sm text-amber-300">
            Recording unavailable: {replay.reason ?? 'unknown'}
          </div>
        )}
      </div>
    </div>
  )
}
