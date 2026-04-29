/**
 * JobsPage — pixel-port of core/console/src/components/JobsPage.svelte.
 *
 * Layout (top-down, byte-identical to canonical):
 *   • Header: <h1>Jobs</h1> + tagline.
 *   • Vertical stack of `<JobCard />` rows. Each row is a `<button>`
 *     toggling inline expansion to show ordered steps. NO `/job/$jobId`
 *     route — clicking the app-name on a per-component row navigates
 *     to that component's AppDetail page; clicking anywhere else on
 *     the row toggles expansion.
 *
 * Job order is stable (Phase 0 → cluster-bootstrap → per-component, in
 * catalog order) so the operator always sees Hetzner / Flux / install
 * jobs in the same place across reloads.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall — first paint is the
 * full list), every Job renders from the moment the page mounts; rows
 * with no events yet show as `pending` with an empty step list.
 *
 * Per #4 (never hardcode), the job set is computed by `deriveJobs()`
 * from the catalog + reducer state. Adding a Blueprint to the catalog
 * automatically adds a row.
 */

import { useMemo } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobCard } from './JobCard'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'

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

  const jobs = useMemo(() => deriveJobs(state, applications), [state, applications])

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

      {jobs.length === 0 ? (
        <div className="mt-12 text-center" data-testid="sov-jobs-empty">
          <p className="text-[var(--color-text-dim)]">No jobs yet for this deployment.</p>
        </div>
      ) : (
        <div className="mt-6 flex flex-col gap-3" data-testid="sov-jobs-list">
          {jobs.map((job) => (
            <JobCard
              key={job.id}
              job={job}
              deploymentId={deploymentId}
              defaultExpanded={job.status === 'running'}
            />
          ))}
        </div>
      )}
    </PortalShell>
  )
}
