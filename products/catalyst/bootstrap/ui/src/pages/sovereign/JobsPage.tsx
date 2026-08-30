/**
 * JobsPage — table-view of the recursive Job tree (issue #3646 §5b).
 *
 * Layout, top-down:
 *   • Header: <h1>Jobs</h1> + tagline + back-to-apps link.
 *   • <JobsTable /> — table view with search/sort/filter + per-row Retry.
 *
 * # Which rows render here (#3996 follow-up, install lens per #5019)
 *
 * /jobs lists work with a completion semantics — things that start, run,
 * and reach a result: provision steps, cutover steps, batch Jobs, CronJob
 * runs, one-shot Day-2 mutations, AND the bootstrap-kit HelmRelease
 * install rows (issue #5019 — the ~65 `install-*` leaves are a BOUNDED
 * catalog set with a real Ready result, so "install-openbao green" is
 * walkable here; the Kind filter offers the `install` lens). The truly
 * OPEN-ENDED reconcilers — Flux Kustomization reconciles and long-running
 * reconciler Deployments — run forever by design and stay EXCLUDED from
 * `/jobs` by the catalyst-api `ListJobs` handler (`jobs.FilterFiniteJobs`);
 * they surface ONLY on the Cloud surface's Reconciliation lens + the
 * ArgoCD-like reconciler-management surface (#3996), which reads them LIVE
 * from the cluster. So this page still never drowns in an ever-growing
 * wall of "running" reconcilers.
 *
 * # One honest list — no client-side mashup (issue #3646)
 *
 * This page renders ONE backend list: the catalyst-api `/jobs` REST
 * payload (`liveJobs`), now narrowed to the finite-work set described
 * above (recurring CronJobs + batch Jobs + the provision/cutover steps),
 * each with a backend-derived, honest status. The previous four-feed
 * mashup is GONE:
 *
 *   ✗ `flowJobs` (`synthesizeJobFromFlowNode` as a list source) — the
 *     openova-flow rows are now written into the jobs Store by the
 *     all-reconcilers ingestion, so they appear in `/jobs` directly.
 *   ✗ `mergeJobs(reducerJobs, liveJobs)` + the dedupe loop — the backend
 *     list is complete; there is nothing to stitch on the client.
 *   ✗ `applyHandoverStageOverride` — the client no longer fabricates a
 *     `succeeded` for a contentless lifecycle group; the backend owns the
 *     status, so an empty Apps/Handover/Cutover phase never renders as a
 *     phantom Pending the client must rewrite.
 *
 * The SSE reducer (`reducerJobs`) is kept ONLY as the waterfall
 * first-paint tail (Principle #1): before the live `/jobs` fetch lands, it
 * provides provisional rows so the operator sees the activity immediately;
 * the instant `liveJobs` arrives it becomes the single source of truth and
 * the reducer rows are dropped. This is a strict "prefer the backend list"
 * selection, never a merge — `liveJobs` always wins outright once present.
 *
 * # No provisioning-progress widget here (#4704)
 *
 * The #4221 graft mounted the 16-phase `BootstrapProgress` timeline on
 * top of this table. That mixed a transient boot sequence into the
 * structured finite-jobs surface. Provisioning progress lives on the
 * provision Dashboard (`/provision/$id/dashboard` — Dashboard.tsx), whose
 * Progress ⇄ Treemap pane auto-flips to the FleetTreemap on
 * `status==ready`. This page is a PURE finite-jobs table.
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { sovereignPathOrDeployments } from '@/shared/lib/sovereignPaths'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobsTable } from './JobsTable'
import { JobsGraphView } from './JobsGraphView'
import { JobKindChips } from './jobs-list/JobKindChips'
import {
  DEFAULT_JOB_KIND,
  isValidJobKind,
  readPersistedJobKind,
  writePersistedJobKind,
  type JobChipKind,
} from './jobs-list/jobKinds'
import { deriveJobKindCounts } from './jobs-list/jobKindCounts'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill } from './useLiveJobsBackfill'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { Job } from '@/lib/jobs.types'
import { jobKind } from '@/lib/jobs.types'
import { HandoverRedirectBanner } from './HandoverRedirectBanner'
import { HANDOVER_REDIRECT_BANNER_CSS } from './HandoverRedirectBanner.css'

/* ── View mode (List ⇄ Graph toggle, P1b Refs #6703) ─────────────── */

export type JobsView = 'list' | 'graph'
const DEFAULT_JOBS_VIEW: JobsView = 'list'
const JOBS_VIEW_STORAGE_KEY = 'sov-jobs-view'

/**
 * Sentinel `activeKind` for the graph-view chip strip meaning "no chip is
 * highlighted" (P2). JobKindChips single-selects a real JobChipKind; passing
 * an id that matches no chip renders every chip inactive, which is exactly
 * the graph-view default (no dimming) — a chip becomes a HIGHLIGHT toggle
 * rather than a filter. Empty string can never collide with a real kind.
 */
const NO_GRAPH_HIGHLIGHT = '' as JobChipKind

function isValidJobsView(value: unknown): value is JobsView {
  return value === 'list' || value === 'graph'
}

function writePersistedJobsView(view: JobsView): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(JOBS_VIEW_STORAGE_KEY, view)
  } catch {
    /* noop */
  }
}

interface JobsPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
  /**
   * Test seam — disables the auto-redirect timer + window.location.assign()
   * on the handover banner. The banner DOM still renders so tests can
   * assert visibility + cancel-button behavior without faking timers
   * or window.location. Production call sites never set this.
   */
  disableHandoverAutoRedirect?: boolean
  /** Test seam — force the view irrespective of URL/storage (highest
   *  precedence, mirroring CloudPage.viewOverride). */
  viewOverride?: JobsView
}

export function JobsPage({
  disableStream = false,
  disableJobsBackfill = false,
  disableHandoverAutoRedirect = false,
  viewOverride,
}: JobsPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
  const store = useWizardStore()
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { view?: string; kind?: string }

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot, streamStatus, handoverReady } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  // Reducer-derived rows — the SSE tail of the SAME activity model. Used
  // ONLY as the waterfall first-paint until the live /jobs list lands; the
  // backend list supersedes them entirely once present (never merged).
  const derivedJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const reducerJobs = useMemo(
    () => markProvisional(adaptDerivedJobsToFlat(derivedJobs)),
    [derivedJobs],
  )

  // The single backend list. After §5a ingestion this is COMPLETE — every
  // reconciler activity is here with a backend-derived honest status.
  // On Sovereign chroot mode the imported snapshot from mother is frozen,
  // so always poll the live local-cluster /jobs endpoint there.
  const isSovereignMode = DETECTED_MODE.mode === 'sovereign'
  const inFlight = streamStatus !== 'completed' && streamStatus !== 'failed'
  const { liveJobs } = useLiveJobsBackfill({
    deploymentId,
    enabled: !disableJobsBackfill,
    disablePolling: disableJobsBackfill || (!inFlight && !isSovereignMode),
  })

  // ONE honest list: the backend payload wins outright. The reducer tail
  // is shown only before the first live fetch returns (waterfall paint),
  // then dropped — a strict prefer-live selection, NOT a merge/coercion.
  const flatJobs: Job[] = useMemo(
    () => (liveJobs.length > 0 ? liveJobs : reducerJobs),
    [liveJobs, reducerJobs],
  )

  const liveBackfillActive = liveJobs.length > 0

  // Gap D — auto-redirect to the Sovereign Console after handover.
  const handoverURL = handoverReady?.handoverURL ?? ''
  const handoverActive =
    handoverReady !== null && handoverURL !== '' && !isSovereignMode

  /* ── View + kind resolution (P1b — mirror /cloud UX) ───────────── */
  // Chroot-aware nav target: on the mothership monitor surface every link
  // MUST stay scoped under /provision/<id>/jobs; on the Sovereign's adult
  // hostname (DETECTED_MODE === 'sovereign') the id is implicit → /jobs.
  const jobsPath = (id: string) =>
    DETECTED_MODE.mode === 'sovereign' || !id ? '/jobs' : `/provision/${id}/jobs`

  // Precedence: viewOverride → URL ?view= → default 'list'. The persisted
  // value is WRITTEN on change but (like CloudPage) NOT consulted for the
  // default, so a fresh /jobs load lands on List, never silently on Graph.
  const activeView: JobsView = useMemo(() => {
    if (viewOverride) return viewOverride
    if (isValidJobsView(search.view)) return search.view
    return DEFAULT_JOBS_VIEW
  }, [viewOverride, search.view])

  useEffect(() => {
    writePersistedJobsView(activeView)
  }, [activeView])

  // Active kind: URL ?kind= → persisted → DEFAULT_JOB_KIND (mirror cloud).
  const activeKind: JobChipKind = useMemo(() => {
    if (isValidJobKind(search.kind)) return search.kind
    return readPersistedJobKind() ?? DEFAULT_JOB_KIND
  }, [search.kind])

  useEffect(() => {
    writePersistedJobKind(activeKind)
  }, [activeKind])

  function setView(next: JobsView) {
    if (next === activeView) return
    const target: { view: JobsView; kind?: string } = { view: next }
    if (next === 'list' && typeof search.kind === 'string') target.kind = search.kind
    navigate({
      to: jobsPath(deploymentId) as never,
      params: { deploymentId } as never,
      search: target as never,
      replace: false,
    })
  }

  const setKind = useCallback(
    (next: JobChipKind) => {
      navigate({
        to: jobsPath(deploymentId) as never,
        params: { deploymentId } as never,
        search: { view: 'list', kind: next } as never,
        replace: false,
      })
    },
    [navigate, deploymentId],
  )

  // Per-kind count map behind the chip badges — derived from the SAME
  // flat list the table/graph render (production path, not a literal).
  const kindCounts = useMemo(() => deriveJobKindCounts(flatJobs), [flatJobs])

  // List view shows exactly one kind at a time (the /cloud contract).
  // `jobKind()` reads the backend-stamped kind, never a name prefix.
  const filteredByKind = useMemo(
    () => flatJobs.filter((j) => jobKind(j) === activeKind),
    [flatJobs, activeKind],
  )

  // Graph-view chip HIGHLIGHT (P2, Refs #6703). Deliberately a separate
  // local state from the list's `?kind=` filter: list=filter removes rows,
  // graph=highlight only dims (removing graph nodes would sever mid-chain
  // dependsOn edges). null = no highlight, the default on entering graph
  // view. Clicking a chip toggles it; clicking the active chip clears it.
  const [graphHighlightKind, setGraphHighlightKind] = useState<JobChipKind | null>(null)
  const toggleGraphHighlight = useCallback((next: JobChipKind) => {
    setGraphHighlightKind((cur) => (cur === next ? null : next))
  }, [])

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Jobs"
      headerSlotLeft={
        <Link
          // #4704 Task B — mode-aware, id-safe. The previous bare
          // '/dashboard' landed a mothership operator on the id-less
          // console-layout dashboard instead of this deployment's apps
          // surface (/provision/$id).
          to={sovereignPathOrDeployments('', { deploymentId }) as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-jobs-back-to-apps"
        >
          ← Back to apps
        </Link>
      }
    >
      <style>{HANDOVER_REDIRECT_BANNER_CSS}</style>
      <style>{JOBS_PAGE_TOOLBAR_CSS}</style>

      <HandoverRedirectBanner
        handoverURL={handoverURL}
        active={handoverActive}
        sovereignFQDN={sovereignFQDN}
        disableAutoRedirect={disableHandoverAutoRedirect}
      />

      {liveBackfillActive ? (
        <div
          role="status"
          data-testid="sov-jobs-backfill-banner"
          className="mt-3 rounded-lg border border-[var(--color-accent)]/35 bg-[var(--color-accent)]/10 p-2 text-xs text-[var(--color-text-dim)]"
        >
          <span className="text-[var(--color-accent)] font-semibold">Live state stream re-attached.</span>{' '}
          Refreshing from the catalyst-api every 5s.
        </div>
      ) : null}

      {/* Toolbar — segmented List/Graph toggle + (list view) the job-kind
          chip strip. Mirrors CloudPage's toolbar (issue #366 / #3978). */}
      <div className="jobs-page-toolbar mt-6" data-testid="jobs-page-toolbar">
        <div
          role="tablist"
          aria-label="Jobs view"
          className="jobs-page-view-toggle"
          data-testid="jobs-page-view-toggle"
        >
          <button
            type="button"
            role="tab"
            data-testid="jobs-page-view-list"
            data-active={activeView === 'list' ? 'true' : 'false'}
            aria-selected={activeView === 'list'}
            aria-controls="jobs-page-content"
            onClick={() => setView('list')}
            className={`jobs-page-view-tab ${activeView === 'list' ? 'jobs-page-view-tab-active' : ''}`}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
              <line x1="4" y1="6" x2="20" y2="6" strokeLinecap="round" />
              <line x1="4" y1="12" x2="20" y2="12" strokeLinecap="round" />
              <line x1="4" y1="18" x2="20" y2="18" strokeLinecap="round" />
            </svg>
            <span>List</span>
          </button>
          <button
            type="button"
            role="tab"
            data-testid="jobs-page-view-graph"
            data-active={activeView === 'graph' ? 'true' : 'false'}
            aria-selected={activeView === 'graph'}
            aria-controls="jobs-page-content"
            onClick={() => setView('graph')}
            className={`jobs-page-view-tab ${activeView === 'graph' ? 'jobs-page-view-tab-active' : ''}`}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
              <circle cx="6" cy="6" r="2" />
              <circle cx="18" cy="6" r="2" />
              <circle cx="12" cy="18" r="2" />
              <line x1="7.5" y1="7" x2="11" y2="16.5" strokeLinecap="round" />
              <line x1="16.5" y1="7" x2="13" y2="16.5" strokeLinecap="round" />
              <line x1="8" y1="6" x2="16" y2="6" strokeLinecap="round" />
            </svg>
            <span>Graph</span>
          </button>
        </div>

        {activeView === 'list' ? (
          <JobKindChips activeKind={activeKind} counts={kindCounts} onChange={setKind} />
        ) : (
          // Graph view keeps the SAME chip strip (founder: "the graph view
          // same as cloud graph, it should still contain the chips"). Here a
          // chip HIGHLIGHTS (dims the rest) rather than filters — the active
          // kind is the local graph-highlight state, NO_GRAPH_HIGHLIGHT when
          // none is selected so no chip reads active by default.
          <JobKindChips
            activeKind={graphHighlightKind ?? NO_GRAPH_HIGHLIGHT}
            counts={kindCounts}
            onChange={toggleGraphHighlight}
          />
        )}

        {/* Schedule affordance (P3, Refs #6703). A contextual "View schedule"
            link — shown ONLY when the CronJob chip is active in list view —
            routes to the consolidated 24h Schedule timeline. Chosen over a
            third List/Graph/Schedule toggle value because a time-of-day
            schedule is meaningful ONLY for CronJobs; a segmented value that is
            empty for the other 7 kinds would be noise. It mirrors how the
            "Jobs timeline" retrospective is also a dedicated route, not a
            view-toggle value. */}
        {activeView === 'list' && activeKind === 'cron' ? (
          <Link
            to={sovereignPathOrDeployments('jobs', { deploymentId, sub: 'schedule' }) as never}
            className="jobs-view-schedule-link"
            data-testid="jobs-cron-view-schedule"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.6} aria-hidden>
              <circle cx="12" cy="12" r="9" />
              <path d="M12 7v5l3 2" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
            <span>View schedule →</span>
          </Link>
        ) : null}
      </div>

      <div id="jobs-page-content" className="mt-4" data-testid="sov-jobs-list" data-view={activeView}>
        {activeView === 'list' ? (
          <JobsTable jobs={filteredByKind} deploymentId={deploymentId} kindScope={activeKind} />
        ) : (
          <JobsGraphView
            jobs={flatJobs}
            deploymentId={deploymentId}
            highlightKind={graphHighlightKind}
          />
        )}
      </div>
    </PortalShell>
  )
}

const JOBS_PAGE_TOOLBAR_CSS = `
.jobs-page-toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.jobs-page-toolbar-spacer { flex: 1; }
.jobs-page-toolbar > [data-testid="jobs-kind-chips"] {
  flex: 1;
  overflow-x: auto;
}
.jobs-page-view-toggle {
  display: inline-flex;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--color-bg-2);
  flex-shrink: 0;
}
.jobs-page-view-tab {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.4rem 0.85rem;
  font-size: 0.82rem;
  color: var(--color-text-dim);
  background: transparent;
  border: 0;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}
.jobs-page-view-tab:hover { color: var(--color-text); background: var(--color-surface-hover); }
.jobs-page-view-tab svg { width: 16px; height: 16px; }
.jobs-page-view-tab + .jobs-page-view-tab { border-left: 1px solid var(--color-border); }
.jobs-page-view-tab-active {
  background: color-mix(in srgb, var(--color-accent) 16%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}
.jobs-view-schedule-link {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  flex-shrink: 0;
  padding: 0.4rem 0.7rem;
  font-size: 0.8rem;
  font-weight: 500;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  color: var(--color-text-dim);
  background: var(--color-bg-2);
  text-decoration: none;
  white-space: nowrap;
  transition: color 0.12s ease, border-color 0.12s ease, background 0.12s ease;
}
.jobs-view-schedule-link:hover {
  color: var(--color-accent);
  border-color: var(--color-accent);
  background: color-mix(in srgb, var(--color-accent) 12%, var(--color-bg-2));
}
.jobs-view-schedule-link svg { width: 15px; height: 15px; }
`

/**
 * markProvisional flags reducer-derived rows whose status is still
 * unconfirmed (pending/running) so the StatusBadge renders the
 * "Confirming…" tone until the live source lands (#3656). Terminal rows
 * are passed through. The reducer is only ever the first-paint tail, so
 * this is the sole place a provisional flag is set.
 */
function markProvisional(jobs: Job[]): Job[] {
  return jobs.map((j) =>
    j.status === 'pending' || j.status === 'running'
      ? { ...j, provisional: true }
      : j,
  )
}
