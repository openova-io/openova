/**
 * JobsTable — table view replacing the legacy expand-in-place JobCard
 * accordion (issue #204 founder spec, items 1/2/4/6/7/8a/10).
 *
 * Layout, top-down:
 *   • BatchProgress strip — one progress bar per batch (item #4).
 *   • Toolbar:
 *       - search input that filters across jobName / appId / dependsOn /
 *         status (item #8a)
 *       - filter dropdowns per column for status / app / batch
 *   • <table data-testid="jobs-table"> with columns:
 *       name, app, deps, batch, status, started, duration  (items #6/#7)
 *
 * Rows are CLICKABLE LINKS, not expandable accordions (item #1: "NEVER
 * use accordions"). Clicking a row navigates to
 *   /sovereign/provision/$deploymentId/jobs/$jobId
 * which is owned by the JobDetail sibling agent on issue #TBD.
 *
 * Default sort: status priority (running > pending > succeeded > failed)
 * then startedAt DESC (item #10). When a job's status transitions from
 * pending to running, the comparator naturally re-sorts it to the top
 * because `running` outranks `pending`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label,
 * column id, status value, and CSS token comes from a typed input or
 * a CSS variable — there is no inlined Job id or status string.
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { Job, JobStatus } from '@/lib/jobs.types'

/* ──────────────────────────────────────────────────────────────────
 * Pure helpers (exported for unit tests)
 * ────────────────────────────────────────────────────────────────── */

/**
 * Status priority for the default comparator. running > pending >
 * succeeded > failed reflects the founder's reading order: the
 * operator wants in-flight + about-to-start work surfaced first; done
 * work and failures sink to the bottom (failures already attract
 * attention via the red badge + the failed batch chip).
 *
 * Lower number = higher in the table. Exported so the unit test in
 * JobsTable.test.tsx can assert without re-deriving.
 */
export const STATUS_PRIORITY: Record<JobStatus, number> = {
  running:   0,
  pending:   1,
  succeeded: 2,
  failed:    3,
}

/**
 * Default comparator for the table. Sort keys, in order:
 *   1. STATUS_PRIORITY (running first, failed last)
 *   2. startedAt DESC (most recently started first; null sorts last
 *      among equals so a never-started pending job follows ones that
 *      did start)
 *   3. id ASC (deterministic tiebreaker — keeps render output stable
 *      across renders even when two jobs share status + startedAt).
 *
 * Pure function. Exported so tests can lock in the contract.
 */
export function compareJobs(a: Job, b: Job): number {
  const pa = STATUS_PRIORITY[a.status] ?? 99
  const pb = STATUS_PRIORITY[b.status] ?? 99
  if (pa !== pb) return pa - pb

  // Started DESC (newer-first). null/empty values sort AFTER real ones
  // so "not yet started" pendings land below "actually started" ones.
  const ta = a.startedAt ? new Date(a.startedAt).getTime() : 0
  const tb = b.startedAt ? new Date(b.startedAt).getTime() : 0
  if (ta !== tb) return tb - ta

  return a.id.localeCompare(b.id)
}

/**
 * Search predicate — matches across jobName / appId / dependsOn /
 * status / batchId. Case-insensitive substring match. Exported so
 * unit tests cover edge cases (empty query, query in deps, etc.).
 */
export function matchJob(job: Job, query: string): boolean {
  if (!query.trim()) return true
  const q = query.toLowerCase()
  if (job.jobName.toLowerCase().includes(q)) return true
  if (job.appId.toLowerCase().includes(q)) return true
  if (job.batchId.toLowerCase().includes(q)) return true
  if (job.status.toLowerCase().includes(q)) return true
  for (const d of job.dependsOn) {
    if (d.toLowerCase().includes(q)) return true
  }
  return false
}

/**
 * Format an integer duration in milliseconds as a human-readable
 * "12s" / "1m 24s" / "2h 5m" string. Mirrors the canonical core/console
 * format used in the legacy JobCard meta line.
 *
 * Returns "—" for 0 / negative values so the table never renders an
 * empty cell for pending jobs.
 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  const totalSec = Math.floor(ms / 1000)
  const h = Math.floor(totalSec / 3600)
  const m = Math.floor((totalSec % 3600) / 60)
  const s = totalSec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

/**
 * Format an ISO timestamp as a relative-time string ("5s ago",
 * "3m ago", "Apr 29"). The full ISO is surfaced as the cell title
 * attribute so hovering reveals the absolute timestamp without
 * pulling in dayjs (it isn't a dep yet).
 */
export function formatRelative(iso: string | null): { display: string; absolute: string } {
  if (!iso) return { display: '—', absolute: '' }
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t) || t <= 0) return { display: '—', absolute: '' }
  const now = Date.now()
  const dMs = now - t
  const dSec = Math.floor(dMs / 1000)
  const display =
    dSec < 5         ? 'just now' :
    dSec < 60        ? `${dSec}s ago` :
    dSec < 3600      ? `${Math.floor(dSec / 60)}m ago` :
    dSec < 86_400    ? `${Math.floor(dSec / 3600)}h ago` :
    new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  const absolute = new Date(t).toLocaleString()
  return { display, absolute }
}

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

interface JobsTableProps {
  /** Job list. Backend populates; UI sorts/filters in place. */
  jobs: readonly Job[]
  /** Stable deployment id — embedded in the per-row link target. */
  deploymentId: string
  /**
   * Optional pre-filter applied BEFORE search/filter dropdowns. Used
   * by AppDetail's Jobs tab to narrow the list to a single appId
   * (item #8b: AppDetail → Jobs tab filtered to that app's jobs only).
   */
  appIdFilter?: string
  /**
   * Optional pre-filter pinned to a single batchId. Used by the
   * BatchDetail page (epic #204 item #4) to surface only the rows
   * that belong to the batch the operator drilled into. The Batch
   * filter dropdown is hidden when this is set, mirroring how
   * `appIdFilter` hides the App dropdown.
   */
  initialBatchFilter?: string
}

const STATUS_VALUES: readonly JobStatus[] = ['running', 'pending', 'succeeded', 'failed']

export function JobsTable({ jobs, deploymentId, appIdFilter, initialBatchFilter }: JobsTableProps) {
  const [search, setSearch] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<'' | JobStatus>('')
  const [appFilter, setAppFilter] = useState<string>('')
  const [batchFilter, setBatchFilter] = useState<string>('')

  // Distinct app/batch lists for the filter dropdowns. Recomputed when
  // jobs change; cheap because the row count is bounded.
  const appOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const j of jobs) set.add(j.appId)
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [jobs])
  const batchOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const j of jobs) set.add(j.batchId)
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [jobs])

  const visibleJobs = useMemo<Job[]>(() => {
    const filtered = jobs.filter((j) => {
      if (appIdFilter && j.appId !== appIdFilter) return false
      if (initialBatchFilter && j.batchId !== initialBatchFilter) return false
      if (statusFilter && j.status !== statusFilter) return false
      if (appFilter && j.appId !== appFilter) return false
      if (batchFilter && j.batchId !== batchFilter) return false
      if (!matchJob(j, search)) return false
      return true
    })
    // Spread to a mutable copy before sort — `jobs` is readonly.
    return [...filtered].sort(compareJobs)
  }, [jobs, search, statusFilter, appFilter, batchFilter, appIdFilter, initialBatchFilter])

  return (
    <div className="jobs-table-wrap" data-testid="jobs-table-wrap">
      <style>{JOBS_TABLE_CSS}</style>

      <div className="jobs-toolbar" data-testid="jobs-toolbar">
        <div className="jobs-search-wrap">
          <svg className="jobs-search-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} aria-hidden>
            <circle cx="11" cy="11" r="7" />
            <path d="M21 21l-4.35-4.35" strokeLinecap="round" />
          </svg>
          <input
            type="search"
            placeholder="Search jobs by name, app, batch, dependency, or status…"
            className="jobs-search-input"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            data-testid="jobs-search"
            aria-label="Search jobs"
          />
        </div>

        <div className="jobs-filters">
          <label className="jobs-filter-label">
            <span className="jobs-filter-caption">Status</span>
            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as '' | JobStatus)}
              className="jobs-filter-select"
              data-testid="jobs-filter-status"
              aria-label="Filter by status"
            >
              <option value="">All</option>
              {STATUS_VALUES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>

          {appIdFilter ? null : (
            <label className="jobs-filter-label">
              <span className="jobs-filter-caption">App</span>
              <select
                value={appFilter}
                onChange={(e) => setAppFilter(e.target.value)}
                className="jobs-filter-select"
                data-testid="jobs-filter-app"
                aria-label="Filter by app"
              >
                <option value="">All</option>
                {appOptions.map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </select>
            </label>
          )}

          {initialBatchFilter ? null : (
            <label className="jobs-filter-label">
              <span className="jobs-filter-caption">Batch</span>
              <select
                value={batchFilter}
                onChange={(e) => setBatchFilter(e.target.value)}
                className="jobs-filter-select"
                data-testid="jobs-filter-batch"
                aria-label="Filter by batch"
              >
                <option value="">All</option>
                {batchOptions.map((b) => (
                  <option key={b} value={b}>
                    {b}
                  </option>
                ))}
              </select>
            </label>
          )}

          <span
            className="jobs-result-count"
            data-testid="jobs-result-count"
            aria-live="polite"
          >
            {visibleJobs.length}/{jobs.length}
          </span>
        </div>
      </div>

      <div className="jobs-table-scroll">
        <table className="jobs-table" data-testid="jobs-table">
          <thead>
            <tr>
              <th data-col="name">Name</th>
              <th data-col="app">App</th>
              <th data-col="deps">Deps</th>
              <th data-col="batch">Batch</th>
              <th data-col="status">Status</th>
              <th data-col="started">Started</th>
              <th data-col="duration">Duration</th>
            </tr>
          </thead>
          <tbody>
            {visibleJobs.length === 0 ? (
              <tr>
                <td colSpan={7} className="jobs-empty" data-testid="jobs-table-empty">
                  No jobs match the current filters.
                </td>
              </tr>
            ) : (
              visibleJobs.map((j) => (
                <JobRow key={j.id} job={j} deploymentId={deploymentId} />
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

interface JobRowProps {
  job: Job
  deploymentId: string
}

function JobRow({ job, deploymentId }: JobRowProps) {
  const started = formatRelative(job.startedAt)
  return (
    <tr
      className="jobs-row"
      data-testid={`jobs-table-row-${job.id}`}
      data-status={job.status}
    >
      <td className="jobs-cell jobs-cell-name">
        <Link
          to="/provision/$deploymentId/jobs/$jobId"
          params={{ deploymentId, jobId: job.id }}
          className="jobs-row-link"
          data-testid={`jobs-row-link-${job.id}`}
        >
          {job.jobName}
        </Link>
      </td>
      <td className="jobs-cell jobs-cell-app">
        <Chip text={job.appId} testid={`jobs-cell-app-${job.id}`} kind="app" />
      </td>
      <td className="jobs-cell jobs-cell-deps">
        {job.dependsOn.length === 0 ? (
          <span className="jobs-empty-cell" data-testid={`jobs-cell-deps-empty-${job.id}`}>—</span>
        ) : (
          <div className="jobs-chip-row">
            {job.dependsOn.map((d) => (
              <Chip key={d} text={d} testid={`jobs-cell-dep-${job.id}-${d}`} kind="dep" />
            ))}
          </div>
        )}
      </td>
      <td className="jobs-cell jobs-cell-batch">
        {/* Batch chip → BatchDetail (per-batch detail page).
            2026-04-30: standalone /flow route removed; batch chip now
            points back to the canonical batch-detail page where the
            embedded flow canvas is scoped to that batch. */}
        <Link
          to="/provision/$deploymentId/batches/$batchId"
          params={{ deploymentId, batchId: job.batchId }}
          className="jobs-chip jobs-chip-batch jobs-chip-link"
          data-testid={`jobs-cell-batch-${job.id}`}
          title={job.batchId}
        >
          {job.batchId}
        </Link>
      </td>
      <td className="jobs-cell jobs-cell-status">
        <StatusBadge status={job.status} jobId={job.id} />
      </td>
      <td className="jobs-cell jobs-cell-started" title={started.absolute}>
        <span data-testid={`jobs-cell-started-${job.id}`}>{started.display}</span>
      </td>
      <td className="jobs-cell jobs-cell-duration">
        <span data-testid={`jobs-cell-duration-${job.id}`}>{formatDuration(job.durationMs)}</span>
      </td>
    </tr>
  )
}

interface StatusBadgeProps {
  status: JobStatus
  jobId: string
}

function StatusBadge({ status, jobId }: StatusBadgeProps) {
  const tone = STATUS_TONE[status]
  return (
    <span
      className={`jobs-badge jobs-badge-${status}`}
      data-testid={`jobs-cell-status-${jobId}`}
      data-status={status}
    >
      {status === 'running' ? <span className="jobs-badge-spinner" aria-hidden /> : null}
      <span className="jobs-badge-text">{tone.label}</span>
    </span>
  )
}

const STATUS_TONE: Record<JobStatus, { label: string }> = {
  pending:   { label: 'Pending' },
  running:   { label: 'Running' },
  succeeded: { label: 'Succeeded' },
  failed:    { label: 'Failed' },
}

interface ChipProps {
  text: string
  testid: string
  kind: 'app' | 'dep' | 'batch'
}

function Chip({ text, testid, kind }: ChipProps) {
  return (
    <span className={`jobs-chip jobs-chip-${kind}`} data-testid={testid} title={text}>
      {text}
    </span>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Styles — keep in lockstep with BatchProgress.tsx tokens.
 * ────────────────────────────────────────────────────────────────── */

const JOBS_TABLE_CSS = `
.jobs-table-wrap { width: 100%; }

.jobs-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
  margin-bottom: 0.75rem;
}

.jobs-search-wrap {
  position: relative;
  flex: 1 1 280px;
  min-width: 240px;
  max-width: 480px;
}
.jobs-search-icon {
  position: absolute;
  left: 0.6rem;
  top: 50%;
  transform: translateY(-50%);
  width: 14px;
  height: 14px;
  color: var(--color-text-dim);
}
.jobs-search-input {
  width: 100%;
  padding: 0.45rem 0.7rem 0.45rem 1.9rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  color: var(--color-text);
  font-size: 0.85rem;
  outline: none;
  transition: border-color 0.15s ease;
}
.jobs-search-input:focus {
  border-color: var(--color-accent);
}

.jobs-filters {
  display: flex;
  gap: 0.6rem;
  align-items: center;
  flex-wrap: wrap;
}
.jobs-filter-label {
  display: inline-flex;
  flex-direction: column;
  gap: 0.15rem;
}
.jobs-filter-caption {
  font-size: 0.62rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.jobs-filter-select {
  padding: 0.32rem 0.5rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.82rem;
  cursor: pointer;
}
.jobs-result-count {
  font-size: 0.72rem;
  color: var(--color-text-dim);
  align-self: flex-end;
  margin-left: auto;
  padding-bottom: 0.32rem;
  font-variant-numeric: tabular-nums;
}

.jobs-table-scroll {
  width: 100%;
  overflow-x: auto;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
}
.jobs-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}
.jobs-table thead th {
  padding: 0.55rem 0.8rem;
  text-align: left;
  background: color-mix(in srgb, var(--color-border) 35%, transparent);
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-dim);
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-weight: 600;
  white-space: nowrap;
}
.jobs-row {
  border-bottom: 1px solid var(--color-border);
  transition: background-color 0.12s ease;
}
.jobs-row:last-of-type {
  border-bottom: none;
}
.jobs-row:hover {
  background: color-mix(in srgb, var(--color-accent) 5%, transparent);
}
.jobs-cell {
  padding: 0.55rem 0.8rem;
  vertical-align: middle;
  color: var(--color-text);
}
.jobs-cell-name { min-width: 220px; max-width: 360px; }
.jobs-cell-deps { min-width: 120px; }
.jobs-row-link {
  display: block;
  width: 100%;
  text-decoration: none;
  color: var(--color-text-strong);
  font-weight: 500;
  cursor: pointer;
}
.jobs-row-link:hover {
  color: var(--color-accent);
}
.jobs-empty-cell {
  color: var(--color-text-dim);
  font-size: 0.78rem;
}
.jobs-empty {
  padding: 2rem 1rem;
  text-align: center;
  color: var(--color-text-dim);
  font-size: 0.85rem;
}

.jobs-chip {
  display: inline-flex;
  align-items: center;
  padding: 0.12rem 0.55rem;
  border-radius: 999px;
  font-size: 0.7rem;
  font-weight: 500;
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
  white-space: nowrap;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  color: var(--color-text-dim);
  max-width: 14rem;
  overflow: hidden;
  text-overflow: ellipsis;
}
.jobs-chip-app   { color: #38BDF8; border-color: rgba(56,189,248,0.25); }
.jobs-chip-batch { color: #C084FC; border-color: rgba(192,132,252,0.25); }
.jobs-chip-dep   { color: var(--color-text-dim); }
.jobs-chip-link {
  text-decoration: none;
  cursor: pointer;
  transition: background-color 0.12s ease, border-color 0.12s ease;
}
.jobs-chip-link:hover {
  text-decoration: underline;
  background: color-mix(in srgb, currentColor 8%, transparent);
  border-color: currentColor;
}
.jobs-chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem;
}

.jobs-badge {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  padding: 0.12rem 0.55rem;
  border-radius: 999px;
  font-size: 0.66rem;
  font-weight: 700;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  white-space: nowrap;
  border: 1px solid transparent;
}
.jobs-badge-pending   { background: rgba(148,163,184,0.10); color: var(--color-text-dim); border-color: rgba(148,163,184,0.30); }
.jobs-badge-running   { background: rgba(56,189,248,0.10);  color: #38BDF8; border-color: rgba(56,189,248,0.35); }
.jobs-badge-succeeded { background: rgba(74,222,128,0.10);  color: #4ADE80; border-color: rgba(74,222,128,0.35); }
.jobs-badge-failed    { background: rgba(248,113,113,0.10); color: #F87171; border-color: rgba(248,113,113,0.35); }
.jobs-badge-spinner {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  border: 1.5px solid currentColor;
  border-top-color: transparent;
  animation: sov-spin 0.7s linear infinite;
}
@keyframes sov-spin { to { transform: rotate(360deg); } }
@keyframes sov-pulse { 0%,100%{opacity:1;transform:scale(1)} 50%{opacity:.4;transform:scale(.7)} }
`
