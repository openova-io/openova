/**
 * BatchDetail — per-batch detail surface served at
 * `/sovereign/provision/$deploymentId/batches/$batchId`.
 *
 * Founder requirement (epic #204 item #4, verbatim):
 *   "On the jobs page the top 3 cards are not required, the progress
 *    bar needs to be shown only when I click a specific batch and it
 *    shows the batch page along with its batch progress at the top"
 *
 * Layout, top-down:
 *   • Back-link to JobsPage.
 *   • Header: batch label + small breadcrumb tag.
 *   • <BatchProgress singleBatch={batch} /> — ONE full-width card with
 *     the prominent progress bar + per-status counters.
 *   • <JobsTable initialBatchFilter={batchId} /> — the same table view
 *     as JobsPage, pre-pinned to this batch's rows. The Batch filter
 *     dropdown is hidden (already pre-filtered).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the batch
 * label, counts, and filtered job set are all derived from
 * `deriveJobs() → adaptDerivedJobsToFlat() → deriveBatches()`. There
 * is no inlined batch id.
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

interface BatchDetailProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function BatchDetail({ disableStream = false }: BatchDetailProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/batches/$batchId' as never,
  }) as { deploymentId: string; batchId: string }
  const { deploymentId, batchId } = params
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

  // Recompute batch rollups from the flat job set; pick out the one we
  // are inspecting. If the batch id from the URL doesn't match any
  // derived batch (e.g. operator pasted a stale link), surface a small
  // not-found state — the JobsTable filtered to that batchId would also
  // be empty, so the page tells the operator why.
  const batches = useMemo(() => deriveBatches(flatJobs), [flatJobs])
  const batch = useMemo(() => batches.find((b) => b.batchId === batchId), [batches, batchId])

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <style>{BATCH_DETAIL_CSS}</style>

      <div className="batch-detail-page" data-testid={`sov-batch-detail-${batchId}`}>
        <Link
          to="/provision/$deploymentId/jobs"
          params={{ deploymentId }}
          className="batch-detail-back"
          data-testid="sov-batch-back-to-jobs"
        >
          &larr; Back to jobs
        </Link>

        <div className="batch-detail-header">
          <div>
            <div
              className="batch-detail-breadcrumb"
              data-testid="sov-batch-breadcrumb"
            >
              Jobs / Batch
            </div>
            <h1
              className="batch-detail-title"
              data-testid="sov-batch-title"
            >
              {batchId}
            </h1>
            <p className="batch-detail-tagline">
              All jobs in this batch for{' '}
              {sovereignFQDN || `deployment ${deploymentId.slice(0, 8)}`}
            </p>
          </div>
        </div>

        {batch ? (
          <BatchProgress singleBatch={batch} />
        ) : (
          <div
            className="batch-detail-empty"
            data-testid="sov-batch-not-found"
          >
            <p>No jobs found for batch <code>{batchId}</code> yet.</p>
            <p>
              The batch may not have started, or it may have been removed
              from the deployment plan.
            </p>
          </div>
        )}

        <div data-testid="sov-batch-jobs-list">
          <JobsTable
            jobs={flatJobs}
            deploymentId={deploymentId}
            initialBatchFilter={batchId}
          />
        </div>
      </div>
    </PortalShell>
  )
}

const BATCH_DETAIL_CSS = `
.batch-detail-page {
  max-width: 1100px;
  margin: 0 auto;
  padding: 0.5rem 0 4rem;
}
.batch-detail-back {
  display: inline-block;
  margin-bottom: 1rem;
  color: var(--color-text-dim);
  font-size: 0.85rem;
  text-decoration: none;
}
.batch-detail-back:hover {
  color: var(--color-text-strong);
}
.batch-detail-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1rem;
}
.batch-detail-breadcrumb {
  font-size: 0.7rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  font-weight: 600;
}
.batch-detail-title {
  margin: 0.2rem 0 0;
  font-size: 1.6rem;
  font-weight: 700;
  color: var(--color-text-strong);
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
}
.batch-detail-tagline {
  margin: 0.35rem 0 0;
  color: var(--color-text-dim);
  font-size: 0.9rem;
}
.batch-detail-empty {
  padding: 1.4rem 1.2rem;
  background: var(--color-surface);
  border: 1px dashed var(--color-border);
  border-radius: 12px;
  margin-bottom: 1.2rem;
}
.batch-detail-empty p {
  margin: 0;
  color: var(--color-text-dim);
  font-size: 0.88rem;
}
.batch-detail-empty p + p {
  margin-top: 0.4rem;
}
.batch-detail-empty code {
  background: color-mix(in srgb, var(--color-border) 50%, transparent);
  padding: 0.1rem 0.4rem;
  border-radius: 4px;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 0.82rem;
  color: var(--color-text);
}
`
