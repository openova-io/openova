/**
 * JobsTable — table view of the recursive Job tree.
 *
 * Layout, top-down:
 *   • Toolbar:
 *       - search input that filters across jobName / appId / dependsOn /
 *         status / parent
 *       - filter dropdowns per column for status / app / parent
 *   • <table data-testid="jobs-table"> with columns:
 *       name (with child-count badge on parents), app, deps, parent,
 *       status, started, duration
 *
 * Rows are CLICKABLE LINKS, not expandable accordions. Clicking a
 * leaf navigates to its own JobDetail page; clicking a parent
 * navigates to that parent's JobDetail (which renders the canvas
 * scoped to the page's host job).
 *
 * Default sort: status priority (running > pending > succeeded > failed)
 * then startedAt DESC. When a job's status transitions from pending
 * to running, the comparator naturally re-sorts it to the top because
 * `running` outranks `pending`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label,
 * column id, status value, and CSS token comes from a typed input or
 * a CSS variable — there is no inlined Job id or status string.
 */

import { useMemo, useState } from 'react'
import { Link, useParams } from '@tanstack/react-router'
import type { Job, JobStatus } from '@/lib/jobs.types'
import { DETECTED_MODE } from '@/shared/lib/detectMode'

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
 * regionFromJob — extract the cloud region key from a Job.
 *
 * Preference order:
 *   1. The first-class `region` field the backend now stamps on every
 *      install row (jobs.Region, Go json:"region"). Unambiguous source
 *      of truth — set only for secondary-region rows, empty for primary.
 *   2. Fallback to the `<region>:<chart>` prefix encoded in `appId`
 *      (the backend's canonical componentID for secondaries), then the
 *      `install-<region>:<chart>` `jobName`. The fallback keeps older
 *      payloads (and group rows) rendering correctly while the wire
 *      shape rolls forward.
 *
 * The encoding is documented in
 * products/catalyst/bootstrap/api/internal/jobs/helmwatch_bridge.go
 * (RegionFromComponent + the three input shapes: bare chart,
 * region-prefixed, install-region-prefixed).
 *
 * Returns the empty string for primary-region rows so the region filter
 * dropdown's "All" option naturally matches them. Day-2 mutation rows
 * and groups have empty appId/region and return ''.
 *
 * Exported so the unit test in JobsTable.test.tsx can lock in the
 * contract.
 */
export function regionFromJob(job: Pick<Job, 'jobName' | 'appId' | 'region'>): string {
  // Prefer the explicit first-class region field — it's the
  // unambiguous backend-stamped source of truth and avoids any
  // string-prefix parsing ambiguity.
  if (job.region) return job.region
  // Fallback: the AppID encoding (backend `componentID` is
  // `<region>:<chart>` for secondaries, bare for primary).
  if (job.appId) {
    const sep = job.appId.indexOf(':')
    if (sep > 0) return job.appId.substring(0, sep)
  }
  // Fallback: parse the jobName when AppID is empty (group rows /
  // pre-bridge legacy rows).
  if (job.jobName) {
    // Strip the canonical `install-` prefix, then check for the
    // region separator. Anything before `:` is the region.
    const stripped = job.jobName.startsWith('install-')
      ? job.jobName.slice('install-'.length)
      : job.jobName
    const sep = stripped.indexOf(':')
    if (sep > 0) return stripped.substring(0, sep)
  }
  return ''
}

/**
 * Search predicate — matches across jobName / appId / dependsOn /
 * status / parentId. Case-insensitive substring match. Exported so
 * unit tests cover edge cases (empty query, query in deps, etc.).
 */
export function matchJob(job: Job, query: string): boolean {
  if (!query.trim()) return true
  const q = query.toLowerCase()
  if (job.jobName.toLowerCase().includes(q)) return true
  if (job.displayName && job.displayName.toLowerCase().includes(q)) return true
  if (job.appId.toLowerCase().includes(q)) return true
  if (job.parentId.toLowerCase().includes(q)) return true
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
  /**
   * Optional pre-filter applied BEFORE search/filter dropdowns. Used
   * by AppDetail's Jobs tab to narrow the list to a single appId.
   */
  appIdFilter?: string
  /**
   * Optional pre-filter pinned to a single parent group. Used by
   * AppDetail / per-group surfaces to surface only the rows that
   * belong to one parent group. The Parent filter dropdown is hidden
   * when this is set, mirroring how `appIdFilter` hides the App
   * dropdown.
   */
  initialParentFilter?: string
}

const STATUS_VALUES: readonly JobStatus[] = ['running', 'pending', 'succeeded', 'failed']

export function JobsTable({ jobs, appIdFilter, initialParentFilter }: JobsTableProps) {
  const [search, setSearch] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<'' | JobStatus>('')
  const [appFilter, setAppFilter] = useState<string>('')
  const [parentFilter, setParentFilter] = useState<string>('')
  // D20 (2026-05-17 t143): region filter dropdown so operators on a
  // multi-region Sovereign can scope the table to one region without
  // typing the region key into the search box. Empty string = "All".
  const [regionFilter, setRegionFilter] = useState<string>('')

  // Resolve parent display labels — used in the Parent column + filter.
  const parentLabelById = useMemo<Map<string, string>>(() => {
    const m = new Map<string, string>()
    for (const j of jobs) {
      if (j.type === 'group') {
        m.set(j.id, j.displayName ?? j.jobName)
      }
    }
    return m
  }, [jobs])

  const appOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const j of jobs) {
      if (j.appId) set.add(j.appId)
    }
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [jobs])
  const parentOptions = useMemo<{ id: string; label: string }[]>(() => {
    const seen = new Map<string, string>()
    for (const j of jobs) {
      if (j.parentId && !seen.has(j.parentId)) {
        seen.set(j.parentId, parentLabelById.get(j.parentId) ?? j.parentId)
      }
    }
    return [...seen.entries()]
      .map(([id, label]) => ({ id, label }))
      .sort((a, b) => a.label.localeCompare(b.label))
  }, [jobs, parentLabelById])

  // D20 (2026-05-17 t143): unique non-empty region keys present in the
  // current job set. Sorted lexically so operators see a stable order
  // (fsn1, hel1-2, nbg1-1, sin-2). These are the secondary-region
  // dropdown options; the primary region (empty key) is always matched
  // by the "All" option.
  const regionOptions = useMemo<string[]>(() => {
    const set = new Set<string>()
    for (const j of jobs) {
      const r = regionFromJob(j)
      if (r) set.add(r)
    }
    return [...set].sort((a, b) => a.localeCompare(b))
  }, [jobs])

  // showRegion gates BOTH the Region filter dropdown and the per-row
  // Region column. The gate must count the PRIMARY region (empty key)
  // as its own bucket — otherwise a 2-region Sovereign (one primary +
  // one secondary) would have only a single non-empty key in
  // regionOptions and the region UI would stay hidden, which is exactly
  // the #3276 symptom (jobs from both regions present but no way to tell
  // them apart). Show the region UI whenever the job set spans more than
  // one distinct region INCLUDING primary. A single-region Sovereign
  // (every row primary) yields one bucket → hidden, so its table renders
  // exactly as before (no extra column, no dropdown).
  const showRegion = useMemo<boolean>(() => {
    const buckets = new Set<string>()
    for (const j of jobs) {
      if (j.type === 'group') continue
      // "" (primary) is a legitimate bucket key alongside secondaries.
      buckets.add(regionFromJob(j))
      if (buckets.size > 1) return true
    }
    return false
  }, [jobs])

  const visibleJobs = useMemo<Job[]>(() => {
    const filtered = jobs.filter((j) => {
      // Hide group rows by default — they appear in the canvas as
      // collapsible parents but in the table they'd dominate the
      // sort. Operators that want to drill into a group click its
      // Parent chip on a leaf row.
      if (j.type === 'group') return false
      if (appIdFilter && j.appId !== appIdFilter) return false
      if (initialParentFilter && j.parentId !== initialParentFilter) return false
      if (statusFilter && j.status !== statusFilter) return false
      if (appFilter && j.appId !== appFilter) return false
      if (parentFilter && j.parentId !== parentFilter) return false
      if (regionFilter && regionFromJob(j) !== regionFilter) return false
      if (!matchJob(j, search)) return false
      return true
    })
    return [...filtered].sort(compareJobs)
  }, [jobs, search, statusFilter, appFilter, parentFilter, regionFilter, appIdFilter, initialParentFilter])

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

          {initialParentFilter ? null : (
            <label className="jobs-filter-label">
              <span className="jobs-filter-caption">Parent</span>
              <select
                value={parentFilter}
                onChange={(e) => setParentFilter(e.target.value)}
                className="jobs-filter-select"
                data-testid="jobs-filter-parent"
                aria-label="Filter by parent group"
              >
                <option value="">All</option>
                {parentOptions.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                  </option>
                ))}
              </select>
            </label>
          )}

          {/*
            D20 region filter — visible only when 2+ regions appear in
            the current job set. A single-region Sovereign sees no
            dropdown (would be a one-option no-op + visual noise).
            Operators on a multi-region cluster get a quick way to
            scope the table to fsn1 / hel1-2 / nbg1-1 / sin-2 without
            typing the region key into the free-text search.
          */}
          {showRegion ? (
            <label className="jobs-filter-label">
              <span className="jobs-filter-caption">Region</span>
              <select
                value={regionFilter}
                onChange={(e) => setRegionFilter(e.target.value)}
                className="jobs-filter-select"
                data-testid="jobs-filter-region"
                aria-label="Filter by region"
              >
                <option value="">All</option>
                {regionOptions.map((r) => (
                  <option key={r} value={r}>
                    {r}
                  </option>
                ))}
              </select>
            </label>
          ) : null}

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
              {/* Region column — shown only on a multi-region Sovereign
                  (2+ distinct regions in the job set), matching the
                  region-filter visibility gate so a single-region
                  Sovereign's table stays unchanged. */}
              {showRegion ? <th data-col="region">Region</th> : null}
              <th data-col="deps">Deps</th>
              <th data-col="parent">Parent</th>
              <th data-col="status">Status</th>
              <th data-col="started">Started</th>
              <th data-col="duration">Duration</th>
            </tr>
          </thead>
          <tbody>
            {visibleJobs.length === 0 ? (
              <tr>
                <td colSpan={showRegion ? 8 : 7} className="jobs-empty" data-testid="jobs-table-empty">
                  No jobs match the current filters.
                </td>
              </tr>
            ) : (
              visibleJobs.map((j) => (
                <JobRow
                  key={j.id}
                  job={j}
                  parentLabel={parentLabelById.get(j.parentId) ?? j.parentId}
                  showRegion={showRegion}
                />
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
  parentLabel: string
  /** Render the Region cell — true only on a multi-region Sovereign so
      a single-region table keeps its original column set. */
  showRegion: boolean
}

/**
 * useJobLinkBuilder — returns a function that builds the chroot-correct
 * Link `to` for a job id.
 *
 * On the mother's monitoring surface (Catalyst-Zero hostname,
 * `/sovereign/provision/$deploymentId/...`) every link MUST stay scoped
 * under `/provision/$deploymentId/jobs/$jobId` — escaping to clean
 * `/jobs/$jobId` would route the operator to either the mother's own
 * Sovereign-Console route (404 here) or the legacy /sovereign/jobs
 * surface (which renders disjointed data).
 *
 * On the Sovereign's adult surface (hostname = `console.<sov-fqdn>`)
 * the deploymentId is implicit from the hostname, so the link uses the
 * clean root form `/jobs/$jobId`.
 */
function useJobLinkBuilder(): (jobId: string) => string {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const isSovereign = DETECTED_MODE.mode === 'sovereign'
  const depId = params.deploymentId ?? ''
  // Strip the "<deploymentId>:" prefix from the FULL canvas JobID form
  // before encoding. The mothership canvas emits ids like
  // "<deploymentId>:install-X" (single-region) and
  // "<deploymentId>:<region>:install-X" (multi-region per
  // flow_snapshot_local.go:410). jobs.Store.GetJob keys by the BARE
  // jobName ("install-X" or "<region>:install-X" for secondary
  // regions) — exact-match URL lookup of the full prefix-bearing form
  // returns 404. FlowPage.handleNodeDoubleClick already strips the
  // first `:` prefix (FlowPage.tsx:355); this JobsTable build path
  // must do the same so a /jobs row click and a canvas drill-down
  // resolve to the SAME backend endpoint.
  //
  // Caught on prov #59 (a43364f11c10cde3, 2026-05-13): clicking a
  // running secondary-region install-* row on /sovereign/provision/
  // <id>/jobs landed on /provision/<id>/jobs/<id>:install-nbg1-1/
  // self-sovereign-cutover → 404 "page not found" at the SPA boundary
  // because no Job ID matches the prefix-bearing form in jobs.Store.
  // Note: keep encodeURIComponent so the `/` inside multi-region bare
  // names ("nbg1-1/self-sovereign-cutover") stays safe in a path
  // segment; jobs.Store.GetJob URL-decodes its lookup key on the BE.
  return (jobId: string) => {
    const bare = jobId.includes(':') ? jobId.slice(jobId.indexOf(':') + 1) : jobId
    const encoded = encodeURIComponent(bare)
    return isSovereign || !depId
      ? `/jobs/${encoded}`
      : `/provision/${depId}/jobs/${encoded}`
  }
}

function JobRow({ job, parentLabel, showRegion }: JobRowProps) {
  const started = formatRelative(job.startedAt)
  const jobLink = useJobLinkBuilder()
  const region = regionFromJob(job)
  return (
    <tr
      className="jobs-row"
      data-testid={`jobs-table-row-${job.id}`}
      data-status={job.status}
    >
      <td className="jobs-cell jobs-cell-name">
        <Link
          to={jobLink(job.id) as never}
          className="jobs-row-link"
          data-testid={`jobs-row-link-${job.id}`}
        >
          {job.displayName ?? job.jobName}
        </Link>
      </td>
      <td className="jobs-cell jobs-cell-app">
        {job.appId ? (
          <Chip text={job.appId} testid={`jobs-cell-app-${job.id}`} kind="app" />
        ) : (
          <span className="jobs-empty-cell">—</span>
        )}
      </td>
      {showRegion ? (
        <td className="jobs-cell jobs-cell-region">
          {region ? (
            <Chip text={region} testid={`jobs-cell-region-${job.id}`} kind="app" />
          ) : (
            // Primary-region rows have no region key — label them
            // explicitly so the operator can tell "primary" apart from
            // "unknown" on a multi-region Sovereign.
            <span className="jobs-region-primary" data-testid={`jobs-cell-region-primary-${job.id}`}>
              primary
            </span>
          )}
        </td>
      ) : null}
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
      <td className="jobs-cell jobs-cell-parent">
        {/* Parent chip — links to that parent group's home page,
            which renders the same canvas + log pane scoped to the
            group as its host job (issue #351). */}
        {job.parentId ? (
          <Link
            to={jobLink(job.parentId) as never}
            className="jobs-chip jobs-chip-parent jobs-chip-link"
            data-testid={`jobs-cell-parent-${job.id}`}
            title={parentLabel}
          >
            {parentLabel}
          </Link>
        ) : (
          <span className="jobs-empty-cell">—</span>
        )}
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
  kind: 'app' | 'dep' | 'parent'
}

function Chip({ text, testid, kind }: ChipProps) {
  return (
    <span className={`jobs-chip jobs-chip-${kind}`} data-testid={testid} title={text}>
      {text}
    </span>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * Styles — keep in lockstep with the canvas family-colour tokens.
 * ────────────────────────────────────────────────────────────────── */

const JOBS_TABLE_CSS = `
.jobs-table-wrap { width: 100%; }

.jobs-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  /* #531 item 3: align all controls (search input + filter dropdowns
     + result count) on the same vertical baseline. Filter labels
     stack the caption *above* the select; aligning to the bottom
     keeps every interactive surface on one row regardless of which
     children carry a caption. */
  align-items: flex-end;
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
  /* Match the filter-select height (0.32rem padding + 0.82rem font ≈
     1.46rem) so the search input and the dropdowns sit on the same
     baseline once .jobs-toolbar aligns them to flex-end. */
  padding: 0.32rem 0.7rem 0.32rem 1.9rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  color: var(--color-text);
  font-size: 0.82rem;
  outline: none;
  transition: border-color 0.15s ease;
}
.jobs-search-input:focus {
  border-color: var(--color-accent);
}

.jobs-filters {
  display: flex;
  gap: 0.6rem;
  align-items: flex-end;
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
.jobs-chip-app    { color: #38BDF8; border-color: rgba(56,189,248,0.25); }
.jobs-chip-parent { color: #C084FC; border-color: rgba(192,132,252,0.25); }
.jobs-chip-dep    { color: var(--color-text-dim); }
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
