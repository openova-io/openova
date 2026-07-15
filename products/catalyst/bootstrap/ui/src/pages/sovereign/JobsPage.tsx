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

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { sovereignPathOrDeployments } from '@/shared/lib/sovereignPaths'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { JobsTable } from './JobsTable'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs } from './jobs'
import { adaptDerivedJobsToFlat } from './jobsAdapter'
import { useLiveJobsBackfill } from './useLiveJobsBackfill'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { Job } from '@/lib/jobs.types'
import { HandoverRedirectBanner } from './HandoverRedirectBanner'
import { HANDOVER_REDIRECT_BANNER_CSS } from './HandoverRedirectBanner.css'

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
}

export function JobsPage({
  disableStream = false,
  disableJobsBackfill = false,
  disableHandoverAutoRedirect = false,
}: JobsPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''
  const store = useWizardStore()

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

      <div className="mt-6" data-testid="sov-jobs-list">
        <JobsTable jobs={flatJobs} deploymentId={deploymentId} />
      </div>
    </PortalShell>
  )
}

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
