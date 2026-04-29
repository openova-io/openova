/**
 * JobsPage — table-view replacement for the legacy expand-in-place
 * accordion (issue #204). The founder rejected the accordion pattern
 * verbatim ("NEVER use accordions anywhere"); every job is now a row in
 * <JobsTable />, and the row is a navigation link to JobDetail (owned
 * by a sibling agent on the JobDetail+ExecutionLogs scope).
 *
 * Layout, top-down:
 *   • Header: <h1>Jobs</h1> + tagline + back-to-apps link.
 *   • <BatchProgress /> — one progress bar per batch (item #4).
 *   • <JobsTable /> — table view with search/sort/filter (items #2,
 *     #6, #7, #8a).
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
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobsTable } from './JobsTable'
import { BatchProgress } from './BatchProgress'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { deriveBatches } from '@/test/fixtures/jobs.fixture'

interface JobsPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function JobsPage({ disableStream = false }: JobsPageProps = {}) {
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

  const { state, snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const derivedJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const flatJobs = useMemo(() => adaptDerivedJobsToFlat(derivedJobs), [derivedJobs])
  const batches = useMemo(() => deriveBatches(flatJobs), [flatJobs])

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

      <div className="mt-6" data-testid="sov-jobs-list">
        <BatchProgress batches={batches} />
        <JobsTable jobs={flatJobs} deploymentId={deploymentId} />
      </div>
    </PortalShell>
  )
}
