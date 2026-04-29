/**
 * JobApps — chip list of Applications this job belongs to.
 *
 * Founder requirement (epic #204 item 5 + 6): the Job Detail page has
 * an Apps tab; the table view exposes apps as a column. Each chip links
 * to the AppDetail page for that application.
 *
 * Multiple apps per job are supported — a single job (e.g. a shared
 * post-install hook) can be attributed to several Applications. The
 * current Catalyst data model attaches a single `app` to each Job, but
 * the founder spec calls out plural "Apps" as a tab; we accept an
 * `appIds` array so this component is forward-compatible the moment the
 * data model evolves to many-apps-per-job.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), each chip's
 * label is sourced from the application descriptor lookup; chips for
 * unknown apps fall back to the bare id so the UI never silently swallows
 * a stale reference.
 */

import { Link } from '@tanstack/react-router'
import type { ApplicationDescriptor } from '@/pages/sovereign/applicationCatalog'

interface JobAppsProps {
  /** App ids this job belongs to (may be a single-element array). */
  appIds: string[]
  /** Lookup keyed by Blueprint id ("bp-<slug>"). */
  appsById: Record<string, ApplicationDescriptor>
  /** Stable deployment id — needed for the AppDetail link. */
  deploymentId: string
}

export function JobApps({ appIds, appsById, deploymentId }: JobAppsProps) {
  // The Phase 0 / cluster-bootstrap pseudo-apps don't have an AppDetail
  // page — surface them as plain (unlinked) chips so the operator still
  // sees the attribution but can't navigate to a 404.
  const SYSTEM_APPS = new Set(['infrastructure', 'cluster-bootstrap'])

  if (appIds.length === 0) {
    return (
      <div
        data-testid="job-apps-empty"
        className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-4 text-sm text-[var(--color-text-dim)]"
      >
        This job is not attributed to any application — it is a deployment-
        level step (Phase 0 infrastructure or cluster bootstrap).
      </div>
    )
  }

  return (
    <div data-testid="job-apps-list" className="flex flex-col gap-3">
      <p className="text-xs text-[var(--color-text-dim)]">
        Attributed to {appIds.length} application{appIds.length === 1 ? '' : 's'} —
        click a chip to open its detail page.
      </p>
      <div className="flex flex-wrap gap-2">
        {appIds.map((appId) => {
          const app = appsById[appId]
          const label = app?.title ?? appId
          if (SYSTEM_APPS.has(appId)) {
            return (
              <span
                key={appId}
                data-testid={`job-app-chip-${appId}`}
                data-system="true"
                className="rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1 text-xs font-medium text-[var(--color-text-dim)]"
              >
                {label}
              </span>
            )
          }
          return (
            <Link
              key={appId}
              to="/provision/$deploymentId/app/$componentId"
              params={{ deploymentId, componentId: appId }}
              data-testid={`job-app-chip-${appId}`}
              className="inline-flex items-center gap-2 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1 text-xs font-semibold text-[var(--color-text-strong)] no-underline hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
            >
              {app?.logoUrl ? (
                <img
                  src={app.logoUrl}
                  alt=""
                  className="h-4 w-4 rounded"
                  aria-hidden
                />
              ) : null}
              <span>{label}</span>
              <span className="text-[10px] font-normal text-[var(--color-text-dim)]">
                ↗
              </span>
            </Link>
          )
        })}
      </div>
    </div>
  )
}
