/**
 * JobsPage — table-view replacement for the legacy expand-in-place
 * accordion (issue #204). The founder rejected the accordion pattern
 * verbatim ("NEVER use accordions anywhere"); every job is now a row in
 * <JobsTable />, and the row is a navigation link to JobDetail (owned
 * by a sibling agent on the JobDetail+ExecutionLogs scope).
 *
 * Layout, top-down:
 *   • Header: <h1>Jobs</h1> + tagline + back-to-apps link.
 *   • <JobsTable /> — table view with search/sort/filter (items #2,
 *     #6, #7, #8a). Each batch chip is a link to the BatchDetail page
 *     (item #4: progress bar moves to per-batch detail view).
 *
 * Per founder feedback for epic #204 item #4 (verbatim):
 *   "On the jobs page the top 3 cards are not required, the progress
 *    bar needs to be shown only when I click a specific batch and it
 *    shows the batch page along with its batch progress at the top"
 *
 * Consequently the top BatchProgress strip is intentionally OMITTED on
 * this surface. Per-batch progress lives at /batches/$batchId only.
 *
 * Data flow:
 *   1. Live SSE events (via useDeploymentEvents) populate the legacy
 *      reducer state (eventReducer.ts) which deriveJobs() folds into
 *      the per-row JobsTable inputs through `jobsAdapter.ts`.
 *   2. Until the catalyst-api jobs endpoint (#205) ships, the live
 *      stream IS the source of truth — every Job listed in
 *      `deriveJobs()` is mapped 1:1 into the new flat shape.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full list), every Job is rendered from mount, even pending ones with
 * no events yet. Per #4 (never hardcode), the job set is computed by
 * deriveJobs() — adding a Blueprint to the catalog automatically adds
 * a row.
 */

import { useMemo } from 'react'
import { useParams, Link, useSearch, useNavigate } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobsTable } from './JobsTable'
import { JobsFlowView } from './JobsFlowView'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'

/** Canonical tabs for the JobsPage view-mode. */
export type JobsViewKey = 'table' | 'flow'

export const JOBS_VIEW_TABS: ReadonlyArray<{ key: JobsViewKey; label: string }> = [
  { key: 'table', label: 'Table' },
  { key: 'flow',  label: 'Flow'  },
]

/** Resolve a free-form `?view=...` URL value to a known JobsViewKey. */
export function resolveJobsView(raw: unknown): JobsViewKey {
  if (raw === 'flow') return 'flow'
  return 'table'
}

interface JobsPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
  /** Test seam — force the active view (bypasses ?view= URL parsing). */
  initialView?: JobsViewKey
}

export function JobsPage({
  disableStream = false,
  disableJobsBackfill = false,
  initialView,
}: JobsPageProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/jobs' as never,
  }) as { deploymentId: string }
  const { deploymentId } = params
  const store = useWizardStore()

  // ?view=table|flow drives the active tab. We read the search params
  // tolerantly — the Jobs route doesn't declare validateSearch (kept
  // backward-compatible with existing deep links), so the value is
  // typed as `unknown` and resolveJobsView() coerces it. The initial
  // view prop overrides the URL for unit tests / Storybook embeds.
  const search = useSearch({ strict: false }) as { view?: unknown }
  const activeView: JobsViewKey = initialView ?? resolveJobsView(search?.view)
  const navigate = useNavigate()
  const setView = (next: JobsViewKey) => {
    navigate({
      to: '/provision/$deploymentId/jobs' as never,
      params: { deploymentId } as never,
      // `table` is the implicit default — keep the URL clean by
      // dropping the param when the user picks the default.
      search: (next === 'table' ? {} : { view: next }) as never,
    })
  }

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot, streamStatus } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const derivedJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const reducerJobs = useMemo(() => adaptDerivedJobsToFlat(derivedJobs), [derivedJobs])

  // Backfill from the catalyst-api Jobs endpoint while the deployment
  // is in flight. helmwatch only fires on transitions, so a HelmRelease
  // that's already Ready=True at watch-attach time emits no SSE event;
  // the backend's Jobs API gives us the current ground-truth list and
  // the merge below ensures live data wins on conflict. Polling stops
  // automatically when the deployment reaches a terminal state — by
  // then `componentStates` on the snapshot already seeded every card.
  const inFlight = streamStatus !== 'completed' && streamStatus !== 'failed'
  const { liveJobs } = useLiveJobsBackfill({
    deploymentId,
    enabled: !disableJobsBackfill,
    disablePolling: disableJobsBackfill || !inFlight,
  })

  const flatJobs = useMemo(
    () => mergeJobs(reducerJobs, liveJobs),
    [reducerJobs, liveJobs],
  )

  // Surface a small banner the first time live data arrives — operators
  // viewing a stalled-looking page need to know the table is being
  // refreshed from the backend, not pulled from the local SSE replay.
  const liveBackfillActive = liveJobs.length > 0

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold text-[var(--color-text-strong)]">Jobs</h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Provisioning, infrastructure, and per-application installs for{' '}
            {sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`}
          </p>
        </div>
        <Link
          to="/provision/$deploymentId"
          params={{ deploymentId }}
          className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-jobs-back-to-apps"
        >
          ← Back to apps
        </Link>
      </div>

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

      <nav
        className="jobs-view-tabs"
        role="tablist"
        aria-label="Jobs view"
        data-testid="jobs-view-tabs"
      >
        <style>{JOBS_VIEW_TABS_CSS}</style>
        {JOBS_VIEW_TABS.map((tab) => {
          const isActive = tab.key === activeView
          return (
            <button
              key={tab.key}
              type="button"
              role="tab"
              aria-selected={isActive}
              aria-current={isActive ? 'page' : undefined}
              className={`jobs-view-tab${isActive ? ' active' : ''}`}
              data-testid={`jobs-view-tab-${tab.key}`}
              onClick={() => setView(tab.key)}
            >
              {tab.label}
            </button>
          )
        })}
      </nav>

      <div className="mt-6" data-testid="sov-jobs-list">
        {activeView === 'flow' ? (
          <JobsFlowView jobs={flatJobs} deploymentId={deploymentId} />
        ) : (
          <JobsTable jobs={flatJobs} deploymentId={deploymentId} />
        )}
      </div>
    </PortalShell>
  )
}

/* Tab strip CSS — mirrors the InfrastructurePage `.tabs` rhythm so
 * the visual feel is consistent across Sovereign-portal surfaces. */
const JOBS_VIEW_TABS_CSS = `
.jobs-view-tabs {
  display: inline-flex;
  gap: 0;
  margin-top: 0.85rem;
  border-bottom: 1px solid var(--color-border);
  padding-bottom: 0;
  width: 100%;
}
.jobs-view-tab {
  appearance: none;
  background: transparent;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--color-text-dim);
  cursor: pointer;
  padding: 0.55rem 1rem;
  font-size: 0.85rem;
  font-weight: 500;
  letter-spacing: 0.01em;
  transition: color 0.12s ease, border-color 0.12s ease;
  margin-bottom: -1px;
}
.jobs-view-tab:hover {
  color: var(--color-text);
}
.jobs-view-tab.active {
  color: var(--color-accent);
  border-bottom-color: var(--color-accent);
}
`
