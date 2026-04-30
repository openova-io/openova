/**
 * JobsPage — table-view replacement for the legacy expand-in-place
 * accordion (issue #204). The founder rejected the accordion pattern
 * verbatim ("NEVER use accordions"); every job is now a row in
 * <JobsTable />, and the row is a navigation link to JobDetail.
 *
 * Layout, top-down:
 *   • Header: <h1>Jobs</h1> + tagline + back-to-apps link + a
 *     "Show as Flow" button that navigates to /flow?scope=all.
 *   • <JobsTable /> — table view with search/sort/filter (items #2,
 *     #6, #7, #8a). Each batch chip is now a Link to /flow?scope=batch:
 *     <id> (per the v3 routing model — was previously a Link to the
 *     BatchDetail page).
 *
 * History note (PR #242 was rejected):
 *   The previous PR added a `?view=table|flow` Tab strip on this page.
 *   The founder rejected it; the Flow surface now lives at its own
 *   /flow route. The tab strip / setView / resolveJobsView helpers
 *   have been removed in this commit.
 *
 * Per founder feedback for epic #204 item #4 (verbatim):
 *   "On the jobs page the top 3 cards are not required, the progress
 *    bar needs to be shown only when I click a specific batch and it
 *    shows the batch page along with its batch progress at the top"
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full list), every Job is rendered from mount, even pending ones with
 * no events yet. Per #4 (never hardcode), the job set is computed by
 * deriveJobs() — adding a Blueprint to the catalog automatically adds
 * a row.
 */

import { useMemo } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobsTable } from './JobsTable'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill, mergeJobs } from './useLiveJobsBackfill'

interface JobsPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — disables the live-jobs backfill polling. */
  disableJobsBackfill?: boolean
}

export function JobsPage({
  disableStream = false,
  disableJobsBackfill = false,
}: JobsPageProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/jobs' as never,
  }) as { deploymentId: string }
  const { deploymentId } = params
  const store = useWizardStore()

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
  // automatically when the deployment reaches a terminal state.
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
        <div className="flex items-center gap-3">
          <Link
            to="/provision/$deploymentId/flow"
            params={{ deploymentId }}
            search={{ scope: 'all' }}
            className="inline-flex items-center gap-2 rounded-lg border border-[var(--color-accent)]/45 bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs font-semibold text-[var(--color-accent)] transition-colors hover:bg-[var(--color-accent)]/20 no-underline"
            data-testid="sov-jobs-show-as-flow"
            aria-label="Show jobs as flow canvas"
          >
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth={1.5} aria-hidden>
              <circle cx="3"  cy="7" r="1.6" />
              <circle cx="11" cy="3" r="1.6" />
              <circle cx="11" cy="11" r="1.6" />
              <path d="M4.4 6.2 L9.6 3.6" strokeLinecap="round" />
              <path d="M4.4 7.8 L9.6 10.4" strokeLinecap="round" />
            </svg>
            Show as Flow
          </Link>
          <Link
            to="/provision/$deploymentId"
            params={{ deploymentId }}
            className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
            data-testid="sov-jobs-back-to-apps"
          >
            ← Back to apps
          </Link>
        </div>
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

      <div className="mt-6" data-testid="sov-jobs-list">
        <JobsTable jobs={flatJobs} deploymentId={deploymentId} />
      </div>
    </PortalShell>
  )
}
