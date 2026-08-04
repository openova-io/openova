/**
 * JobsTable — table view of the recursive Job tree.
 *
 * Layout, top-down:
 *   • Toolbar:
 *       - search input that filters across jobName / appId / dependsOn /
 *         status / parent
 *       - filter dropdowns per column for status / app / parent, plus
 *         region on a multi-region Sovereign (2+ region buckets in the
 *         job set, primary counted as its own bucket)
 *   • <table data-testid="jobs-table"> with columns:
 *       name (with child-count badge on parents), app, region (multi-
 *       region only — derived from the backend-stamped region field or
 *       the install-<region>:<chart> jobName), deps, parent, status,
 *       started, duration
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
import type { Job, JobStatus, JobKind } from '@/lib/jobs.types'
import { jobKind, isJobRetryable } from '@/lib/jobs.types'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { RetryJobButton } from './RetryJobButton'

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
  // Unhealthy reconciler kinds rank with failures — the operator wants
  // them surfaced at the top alongside failed installs (issue #3646).
  failing:   0,
  running:   0,
  degraded:  1,
  pending:   1,
  succeeded: 2,
  failed:    3,
  // A healthy recurring reconciler is steady-state — sinks like succeeded.
  healthy:   2,
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
 * Canonical jobName prefixes (mirror the backend's JobNamePrefix in
 * products/catalyst/bootstrap/api/internal/jobs — per Principle #4 the
 * literals live here once, never inline in the parser).
 */
const INSTALL_JOB_PREFIX = 'install-'
const LIFECYCLE_JOB_PREFIX = 'provision-'

/**
 * regionOf — derive the region key from a bare jobName (founder hw126
 * report: the /jobs table must show + filter region per row, and the
 * region is derivable from the row name alone).
 *
 * Canonical jobName shapes (see helmwatch_bridge.go RegionFromComponent
 * and the dependsOn canonicalisation table at its line ~544):
 *   • "install-<region>:<chart>"  — secondary-region install row
 *     (e.g. "install-me-east-215-b-1:harbor")        → "<region>"
 *   • "<region>:<chart>"          — bare componentID form → "<region>"
 *   • "install-<chart>"           — primary-region install → ""
 *   • "provision-*"               — lifecycle rows (tofu plan/apply,
 *     network, …) always run from the primary control plane and are
 *     never region-encoded → "" (the UI labels "" as "primary")
 *
 * Returns "" (primary) when no region key is encoded, so the region
 * filter's "All"/primary semantics match regionFromJob's contract.
 * Exported so the unit test in JobsTable.test.tsx can lock in the
 * secondary / primary / lifecycle shapes.
 */
export function regionOf(jobName: string): string {
  if (!jobName) return ''
  // Lifecycle rows are never region-encoded — even a ":" in their name
  // (a step qualifier) must not be misread as a region separator.
  if (jobName.startsWith(LIFECYCLE_JOB_PREFIX)) return ''
  // Strip the canonical `install-` prefix, then check for the region
  // separator. Anything before `:` is the region.
  const stripped = jobName.startsWith(INSTALL_JOB_PREFIX)
    ? jobName.slice(INSTALL_JOB_PREFIX.length)
    : jobName
  const sep = stripped.indexOf(':')
  return sep > 0 ? stripped.substring(0, sep) : ''
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
 *      `install-<region>:<chart>` `jobName` via {@link regionOf}. The
 *      fallback keeps older payloads (and group rows) rendering
 *      correctly while the wire shape rolls forward.
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
  return regionOf(job.jobName)
}

/**
 * regionUnionOfGroup — distinct, lexically-sorted region keys across a
 * group's direct children (matched by parentId). A parent/group row
 * carries no region of its own, so its Region cell shows the union of
 * its children's regions — or "—" when every child is primary (the
 * union is empty). Nested group rows are skipped: their own children
 * contribute through their own union, never through the grandparent's.
 *
 * Exported so the unit test in JobsTable.test.tsx can lock in the
 * union / all-primary / other-parent cases.
 */
export function regionUnionOfGroup(
  jobs: readonly Pick<Job, 'jobName' | 'appId' | 'region' | 'parentId' | 'type'>[],
  groupId: string,
): string[] {
  const set = new Set<string>()
  for (const j of jobs) {
    if (j.parentId !== groupId) continue
    if (j.type === 'group') continue
    const r = regionFromJob(j)
    if (r) set.add(r)
  }
  return [...set].sort((a, b) => a.localeCompare(b))
}

/**
 * Search predicate — matches across jobName / appId / dependsOn /
 * status / parentId. Case-insensitive substring match. Exported so
 * unit tests cover edge cases (empty query, query in deps, etc.).
 *
 * Every field access tolerates undefined/null: handover-imported rows
 * (POST /api/v1/internal/jobs/import) carry only what the mothership
 * export stamped, so the wire shape can omit any of these despite the
 * Job type — TS types don't survive res.json().
 */
export function matchJob(job: Job, query: string): boolean {
  if (!query.trim()) return true
  const q = query.toLowerCase()
  const fields = [job.jobName, job.displayName, job.appId, job.parentId, job.status]
  for (const f of fields) {
    if (typeof f === 'string' && f.toLowerCase().includes(q)) return true
  }
  for (const d of job.dependsOn ?? []) {
    if (typeof d === 'string' && d.toLowerCase().includes(q)) return true
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
  /**
   * The deployment id the rows belong to — threaded to the per-row Retry
   * control so it can POST the §5c remediation endpoint
   * (/deployments/{depId}/jobs/{jobId}/retry). When absent the Retry
   * control is hidden (e.g. a preview surface with no live cluster).
   */
  deploymentId?: string
}

const STATUS_VALUES: readonly JobStatus[] = [
  'running', 'pending', 'succeeded', 'failed', 'healthy', 'degraded', 'failing',
]

export function JobsTable({ jobs, appIdFilter, initialParentFilter, deploymentId }: JobsTableProps) {
  const [search, setSearch] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<'' | JobStatus>('')
  const [kindFilter, setKindFilter] = useState<'' | JobKind>('')
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

  // Region union per group id — a parent/group row has no region of its
  // own, so its Region cell (and the Parent chip's hover title on a
  // multi-region Sovereign) surfaces the union of its children's
  // regions, never a bogus "primary" label (founder hw126 report).
  const regionUnionByGroupId = useMemo<Map<string, string[]>>(() => {
    const m = new Map<string, string[]>()
    for (const j of jobs) {
      if (j.type === 'group') m.set(j.id, regionUnionOfGroup(jobs, j.id))
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
  // Kind options — the typed activity kinds actually present (issue
  // #3646). Group rows are excluded since the table hides them; the
  // remaining leaf kinds drive the Kind filter dropdown generically.
  const kindOptions = useMemo<JobKind[]>(() => {
    const set = new Set<JobKind>()
    for (const j of jobs) {
      if (j.type === 'group') continue
      set.add(jobKind(j))
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
      if (kindFilter && jobKind(j) !== kindFilter) return false
      if (appFilter && j.appId !== appFilter) return false
      if (parentFilter && j.parentId !== parentFilter) return false
      if (regionFilter && regionFromJob(j) !== regionFilter) return false
      if (!matchJob(j, search)) return false
      return true
    })
    return [...filtered].sort(compareJobs)
  }, [jobs, search, statusFilter, kindFilter, appFilter, parentFilter, regionFilter, appIdFilter, initialParentFilter])

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

          {/* Kind filter (issue #3646) — scope the canvas to a single
              activity kind (cron / reconcile / install / task / …). Only
              the kinds actually present render as options. */}
          <label className="jobs-filter-label">
            <span className="jobs-filter-caption">Kind</span>
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value as '' | JobKind)}
              className="jobs-filter-select"
              data-testid="jobs-filter-kind"
              aria-label="Filter by kind"
            >
              <option value="">All</option>
              {kindOptions.map((k) => (
                <option key={k} value={k}>
                  {k}
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
              {/* Kind column (issue #3646) — the typed activity kind, read
                  from job.kind (never a JobName-prefix string-match). */}
              <th data-col="kind">Kind</th>
              <th data-col="app">App</th>
              {/* Region column — shown only on a multi-region Sovereign
                  (2+ distinct regions in the job set), matching the
                  region-filter visibility gate so a single-region
                  Sovereign's table stays unchanged. */}
              {showRegion ? <th data-col="region">Region</th> : null}
              <th data-col="deps">Deps</th>
              <th data-col="parent">Parent</th>
              <th data-col="status">Status</th>
              {/* Runs column (#3925) — run-history depth. A recurring job
                  that collapses to ONE identity row (e.g. a trivy/syft
                  scan) shows its full run count here; a one-shot shows 1. */}
              <th data-col="runs">Runs</th>
              <th data-col="started">Started</th>
              <th data-col="duration">Duration</th>
              {/* Actions column (issue #3646 §5c) — per-row Retry control
                  for Failed/degraded rows. Hidden when no deploymentId is
                  threaded (preview surfaces with no live cluster). */}
              {deploymentId ? <th data-col="actions">Actions</th> : null}
            </tr>
          </thead>
          <tbody>
            {visibleJobs.length === 0 ? (
              <tr>
                {/* base 8 cols (name,app,deps,parent,status,runs,started,
                    duration) + Kind (+1) + Region (showRegion) + Actions
                    (deploymentId). */}
                <td
                  colSpan={9 + (showRegion ? 1 : 0) + (deploymentId ? 1 : 0)}
                  className="jobs-empty"
                  data-testid="jobs-table-empty"
                >
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
                  regionUnionByGroupId={regionUnionByGroupId}
                  deploymentId={deploymentId}
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
  /** Distinct region keys per group id (regionUnionOfGroup output) —
      drives the group-row Region cell union and the Parent chip's
      hover title on a multi-region Sovereign. */
  regionUnionByGroupId: Map<string, string[]>
  /** Deployment id for the per-row Retry POST (issue #3646 §5c). When
      undefined the Actions cell is omitted (no live cluster to act on). */
  deploymentId?: string
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

function JobRow({ job, parentLabel, showRegion, regionUnionByGroupId, deploymentId }: JobRowProps) {
  const started = formatRelative(job.startedAt)
  const jobLink = useJobLinkBuilder()
  const region = regionFromJob(job)
  const kind = jobKind(job)
  // Union of the row's OWN children's regions (group rows) + union of
  // the row's PARENT group's children (drives the Parent chip title).
  const ownGroupRegions = regionUnionByGroupId.get(job.id) ?? []
  const parentGroupRegions = regionUnionByGroupId.get(job.parentId) ?? []
  // On a multi-region Sovereign the Parent chip's hover title carries
  // the group's region union so the operator can see at a glance which
  // regions a parent group spans without opening it.
  const parentTitle =
    showRegion && parentGroupRegions.length > 0
      ? `${parentLabel} — regions: ${parentGroupRegions.join(', ')}`
      : parentLabel
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
      <td className="jobs-cell jobs-cell-kind">
        {/* Kind chip — read from job.kind (issue #3646), never inferred
            from a JobName prefix. */}
        <span
          className={`jobs-kind-chip jobs-kind-${kind}`}
          data-testid={`jobs-cell-kind-${job.id}`}
          data-kind={kind}
        >
          {kind}
        </span>
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
          {job.type === 'group' ? (
            // Parent/group rows have no region of their own — show the
            // union of their children's regions, or "—" when every
            // child is primary. Never label a group "primary": a group
            // spanning region-b children is not a primary-region row.
            ownGroupRegions.length > 0 ? (
              <div className="jobs-chip-row">
                {ownGroupRegions.map((r) => (
                  <Chip key={r} text={r} testid={`jobs-cell-region-${job.id}-${r}`} kind="app" />
                ))}
              </div>
            ) : (
              <span className="jobs-empty-cell" data-testid={`jobs-cell-region-empty-${job.id}`}>—</span>
            )
          ) : region ? (
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
            title={parentTitle}
          >
            {parentLabel}
          </Link>
        ) : (
          <span className="jobs-empty-cell">—</span>
        )}
      </td>
      <td className="jobs-cell jobs-cell-status">
        <StatusBadge status={job.status} jobId={job.id} provisional={job.provisional === true} />
      </td>
      <td className="jobs-cell jobs-cell-runs">
        {/* Runs (#3925) — run-history depth. A collapsed recurring row
            (scan/backup) shows its run count ("600"); a one-shot shows 1.
            A pending row with no run yet (undefined/0) shows "—". */}
        {job.runCount && job.runCount > 0 ? (
          <span
            className="jobs-runs-count"
            data-testid={`jobs-cell-runs-${job.id}`}
            title={`${job.runCount} run${job.runCount === 1 ? '' : 's'} recorded`}
          >
            {job.runCount}
          </span>
        ) : (
          <span className="jobs-empty-cell" data-testid={`jobs-cell-runs-empty-${job.id}`}>—</span>
        )}
      </td>
      <td className="jobs-cell jobs-cell-started" title={started.absolute}>
        <span data-testid={`jobs-cell-started-${job.id}`}>{started.display}</span>
      </td>
      <td className="jobs-cell jobs-cell-duration">
        <span data-testid={`jobs-cell-duration-${job.id}`}>{formatDuration(job.durationMs)}</span>
      </td>
      {deploymentId ? (
        <td className="jobs-cell jobs-cell-actions">
          {/* Retry control (issue #3646 §5c) — visible only for a
              retryable (failed / degraded / failing) row. Re-drives the
              reconcile via the backend's Flux-native annotation; the
              backend RBAC-gates it (403 for a non-operator). */}
          {isJobRetryable(job.status) ? (
            <RetryJobButton
              deploymentId={deploymentId}
              jobId={job.jobName}
              kind={kind}
            />
          ) : (
            <span className="jobs-empty-cell" data-testid={`jobs-cell-actions-empty-${job.id}`}>—</span>
          )}
        </td>
      ) : null}
    </tr>
  )
}

interface StatusBadgeProps {
  status: JobStatus
  jobId: string
  /**
   * #3656 (founder #6) — the row's status is reducer-derived and NOT yet
   * confirmed by the live source. Render a distinct "Confirming…" badge
   * instead of a definitive Pending/Running, so a job that actually
   * completed is never shown non-terminal-then-flipped.
   */
  provisional?: boolean
}

function StatusBadge({ status, jobId, provisional = false }: StatusBadgeProps) {
  // #3656 — a provisional (live-unconfirmed) row never shows a definitive
  // status. It renders "Confirming…" with its own tone + a spinner so the
  // operator reads it as "settling", not as a committed state to flip.
  if (provisional) {
    return (
      <span
        className="jobs-badge jobs-badge-confirming"
        data-testid={`jobs-cell-status-${jobId}`}
        data-status="confirming"
        // Preserve the underlying reducer-derived status for diagnostics +
        // the sort comparator's transparency — the visible label stays
        // provisional until the live source confirms.
        data-underlying-status={status}
        title="Awaiting confirmation from the live cluster state"
      >
        <span className="jobs-badge-spinner" aria-hidden />
        <span className="jobs-badge-text">Confirming…</span>
      </span>
    )
  }
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
  // HEALTH axis (issue #3646 §4c) — recurring/reconciler kinds. Distinct
  // labels so "Healthy (running forever, correct)" never reads as the
  // one-shot "Succeeded", and a hang reads as "Degraded"/"Failing".
  healthy:   { label: 'Healthy' },
  degraded:  { label: 'Degraded' },
  failing:   { label: 'Failing' },
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
.jobs-cell-runs { text-align: right; min-width: 56px; }
.jobs-runs-count {
  font-variant-numeric: tabular-nums;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.8rem;
  color: var(--color-text);
}
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
/* HEALTH axis (issue #3646) — recurring/reconciler kinds. Healthy = green
 * (steady-state, distinct from one-shot Succeeded by label); degraded =
 * amber; failing = red (same urgency as failed). */
.jobs-badge-healthy   { background: rgba(74,222,128,0.08);  color: #4ADE80; border-color: rgba(74,222,128,0.30); }
.jobs-badge-degraded  { background: rgba(251,191,36,0.10);  color: #FBBF24; border-color: rgba(251,191,36,0.40); }
.jobs-badge-failing   { background: rgba(248,113,113,0.12); color: #F87171; border-color: rgba(248,113,113,0.40); }
/* Kind chip — a neutral pill carrying the typed activity kind. */
.jobs-kind-chip {
  display: inline-block; padding: 1px 7px; border-radius: 999px;
  font-size: 10px; font-weight: 600; letter-spacing: 0.03em;
  text-transform: uppercase;
  background: rgba(148,163,184,0.10); color: var(--color-text-dim);
  border: 1px solid rgba(148,163,184,0.25);
}
.jobs-kind-cron       { color: #C084FC; border-color: rgba(192,132,252,0.35); background: rgba(192,132,252,0.08); }
.jobs-kind-install    { color: #818CF8; border-color: rgba(129,140,248,0.35); background: rgba(129,140,248,0.08); }
.jobs-kind-reconcile  { color: #38BDF8; border-color: rgba(56,189,248,0.35);  background: rgba(56,189,248,0.08); }
.jobs-kind-reconciler { color: #2DD4BF; border-color: rgba(45,212,191,0.35);  background: rgba(45,212,191,0.08); }
.jobs-kind-step       { color: #FBBF24; border-color: rgba(251,191,36,0.35);  background: rgba(251,191,36,0.08); }
/* Retry control (issue #3646 §5c). */
.jobs-retry-wrap { display: inline-flex; align-items: center; gap: 6px; }
.jobs-retry-btn {
  cursor: pointer; padding: 2px 9px; border-radius: 6px; font-size: 11px;
  font-weight: 600; border: 1px solid rgba(56,189,248,0.40); color: #38BDF8;
  background: rgba(56,189,248,0.08);
}
.jobs-retry-btn:hover:not(:disabled) { background: rgba(56,189,248,0.18); }
.jobs-retry-btn:disabled { opacity: 0.6; cursor: default; }
.jobs-retry-result { font-size: 11px; font-weight: 600; }
.jobs-retry-done  { color: #4ADE80; }
/* The error text is the SERVER's own detail (#3379), which can be a full
 * sentence — clamp it to one line so a long 422/409 reason cannot widen the
 * Actions column and push the table into a horizontal scroll. The element
 * carries the untruncated message in its title for hover/copy. */
.jobs-retry-error {
  color: #F87171; max-width: 28ch; overflow: hidden;
  text-overflow: ellipsis; white-space: nowrap;
}
/* #3656 (founder #6) — provisional "Confirming…" badge: a reducer-derived
 * row the live cluster state has not confirmed yet. Amber + DASHED border so
 * it reads as a distinct "settling, not committed" state — unmistakable from
 * the four definitive statuses; it resolves to the confirmed status the
 * moment the live /jobs fetch lands. text-transform:none keeps the ellipsis
 * label readable. */
.jobs-badge-confirming {
  background: rgba(251,191,36,0.10);
  color: #FBBF24;
  border-color: rgba(251,191,36,0.45);
  border-style: dashed;
  text-transform: none;
  letter-spacing: 0.02em;
}
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
