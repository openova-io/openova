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
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useFlowStream } from '@/lib/openflow-adapter-sse'
import { synthesizeJobFromFlowNode } from '@/lib/synthesizeJobFromFlowNode'
import type { Job } from '@/lib/jobs.types'

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
  // On Sovereign mode the imported snapshot from mother is FROZEN at
  // mother's last reducer state (always streamStatus="completed" by the
  // time handover fires), so the inFlight gate would never poll the
  // live /api/v1/sovereign/jobs endpoint. The Sovereign Console MUST
  // always show ground-truth from the local cluster, so override the
  // gate when we're running on chroot Sovereign mode. Caught on
  // omantel.biz 2026-05-06 — every job rendered "Pending" because
  // mergeJobs fell through to the imported snapshot.
  const isSovereignMode = DETECTED_MODE.mode === 'sovereign'
  const inFlight = streamStatus !== 'completed' && streamStatus !== 'failed'
  const { liveJobs } = useLiveJobsBackfill({
    deploymentId,
    enabled: !disableJobsBackfill,
    disablePolling: disableJobsBackfill || (!inFlight && !isSovereignMode),
  })

  const legacyMerged = useMemo(
    () => mergeJobs(reducerJobs, liveJobs),
    [reducerJobs, liveJobs],
  )

  // OpenovaFlow snapshot merge — TC-035 (2026-05-11). Some Sovereign
  // jobs (notably the openova-flow-server + openova-flow-emitter HRs)
  // exist ONLY in the OpenovaFlow snapshot — the legacy
  // /api/v1/deployments/<id>/jobs endpoint has no entry for those ids.
  // Without this merge the JobsTable misses entire HelmReleases for any
  // Sovereign with an active flow surface. Verified live: GET
  // /v1/flows/<id>/snapshot returns 2 leaf nodes (contabo:bp-openova-
  // flow-server, contabo:bp-openova-flow-emitter) whose ids never
  // appear in the legacy /jobs payload. Reuses the same SSE seam +
  // FlowNode→Job adapter the JobDetail flow-fallback (PR #1412) shipped.
  const flowStream = useFlowStream({ deploymentId, disableStream })
  const flowJobs = useMemo(
    () =>
      Array.from(flowStream.nodes.values()).map(synthesizeJobFromFlowNode),
    [flowStream.nodes],
  )

  // Final merge: legacy (reducer + live backfill) is authoritative on
  // id collisions because it carries real execution timeline / status
  // / appId / parentId — the flow-stream synth job is a minimal stub.
  // Flow rows whose id is NOT present in legacy are appended so the
  // table covers HRs that live only in the OpenovaFlow snapshot.
  // Behavior unchanged when the flow stream is empty (Sovereigns
  // without openova-flow-server deployed) — flowJobs.length === 0 and
  // the dedupe loop returns legacyMerged untouched.
  const flatJobs: Job[] = useMemo(() => {
    if (flowJobs.length === 0) return legacyMerged
    const seen = new Set(legacyMerged.map((j) => j.id))
    const extra: Job[] = []
    for (const fj of flowJobs) {
      if (seen.has(fj.id)) continue
      seen.add(fj.id)
      extra.push(fj)
    }
    if (extra.length === 0) return legacyMerged
    return [...legacyMerged, ...extra]
  }, [legacyMerged, flowJobs])

  const liveBackfillActive = liveJobs.length > 0

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Jobs"
      headerSlotLeft={
        <Link
          to={`/dashboard` as never}
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
        <JobsTable jobs={flatJobs} />
      </div>
    </PortalShell>
  )
}
