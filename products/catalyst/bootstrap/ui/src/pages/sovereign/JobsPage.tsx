/**
 * JobsPage — table-view of the recursive Job tree.
 *
 * Layout, top-down:
 *   • Header: <h1>Jobs</h1> + tagline + back-to-apps link + a
 *     "Show as Flow" button that navigates to /flow.
 *   • <JobsTable /> — table view with search/sort/filter. Each parent
 *     chip in a row is a Link to that parent group's own home page
 *     (/jobs/$parentId) — group jobs are first-class citizens of the
 *     tree (issue #351), no separate BatchDetail page.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full list), every Job is rendered from mount, even pending ones with
 * no events yet. Per #4 (never hardcode), the job set is computed by
 * deriveJobs() — adding a Blueprint to the catalog automatically adds
 * a row.
 */

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
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
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
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
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Jobs"
      headerSlotLeft={
        <Link
          to={`/dashboard` as never}
          params={{ deploymentId }}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-jobs-back-to-apps"
        >
          ← Back to apps
        </Link>
      }
    >
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
