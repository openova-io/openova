/**
 * AdminPage — Sovereign Admin landing surface served at
 * `/sovereign/provision/$deploymentId`. Replaces the legacy DAG
 * provision view.
 *
 * Layout (top-down):
 *   • AdminShell top bar (OpenOva logo + Sovereign FQDN + overall
 *     status pill + open-console CTA + theme toggle)
 *   • AdminShell sidebar (deployment metadata + per-family rollup)
 *   • Main:
 *       — Failure card (when stream ended in failure or unreachable)
 *       — PhaseBanners (Hetzner infra + Cluster bootstrap)
 *       — Application card grid (every Application being installed
 *         on this Sovereign — bootstrap-kit + user-selected). Each
 *         card carries a status pill + brand-coloured logo + family
 *         chip + tier chip + a clickable link to the per-Application
 *         page.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall is the contract):
 *   • the card grid renders the FULL set from first paint, even
 *     before any /events arrive — each card starts in `pending` and
 *     flips to `installing` / `installed` as the API emits events.
 *   • the page is the same shape regardless of whether the deployment
 *     is mid-flight or completed an hour ago.
 *
 * Per #2 (never compromise), there is no "MVP" branch where cards
 * render without status pills, no fallback list view, no "loading…"
 * spinner that hides the grid.
 *
 * Per #4 (never hardcode), the application list is computed by
 * `resolveApplications()` from the catalog + selectedComponents — no
 * hand-maintained id list exists in this file.
 */

import { useMemo } from 'react'
import { useParams, useRouter } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { AdminShell } from './AdminShell'
import { ApplicationCard } from './ApplicationCard'
import { PhaseBanners } from './PhaseBanners'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'

interface AdminPageProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
}

export function AdminPage({ disableStream = false }: AdminPageProps = {}) {
  const params = useParams({ from: '/provision/$deploymentId' as never }) as {
    deploymentId: string
  }
  const deploymentId = params.deploymentId
  const router = useRouter()
  const store = useWizardStore()

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )

  const applicationIds = useMemo(
    () => applications.map((a) => a.id),
    [applications],
  )

  const { state, snapshot, streamStatus, streamError, startedAt, finishedAt, retry } =
    useDeploymentEvents({
      deploymentId,
      applicationIds,
      disableStream,
    })

  const isFailed = streamStatus === 'failed' || streamStatus === 'unreachable'
  const failureMessage = streamError ?? snapshot?.error ?? null

  // Group cards by bootstrap-kit then by family for visual scanability
  // — bootstrap-kit Applications are the always-installed core; user-
  // selected Applications come below them in family order.
  const bootstrapApps = applications.filter((a) => a.bootstrapKit)
  const selectedApps = applications.filter((a) => !a.bootstrapKit)

  return (
    <AdminShell
      deploymentId={deploymentId}
      state={state}
      snapshot={snapshot}
      applications={applications}
      startedAt={startedAt}
      finishedAt={finishedAt}
    >
      {isFailed && (
        <FailureCard
          deploymentId={deploymentId}
          status={streamStatus}
          message={failureMessage}
          onRetry={retry}
          onBack={() => router.navigate({ to: '/wizard' })}
        />
      )}

      <PhaseBanners state={state} />

      <div className="sov-sec-head">
        <h2 className="sov-sec-h">Bootstrap kit</h2>
        <span className="sov-sec-meta" data-testid="sov-bootstrap-summary">
          {bootstrapApps.length} components — always installed
        </span>
      </div>
      <div className="sov-grid" data-testid="sov-bootstrap-grid">
        {bootstrapApps.map((app) => (
          <ApplicationCard
            key={app.id}
            app={app}
            status={state.apps[app.id]?.status ?? 'pending'}
            deploymentId={deploymentId}
          />
        ))}
      </div>

      <div className="sov-sec-head">
        <h2 className="sov-sec-h">Applications</h2>
        <span className="sov-sec-meta" data-testid="sov-selected-summary">
          {selectedApps.length} selected — including transitive dependencies
        </span>
      </div>
      <div className="sov-grid" data-testid="sov-selected-grid">
        {selectedApps.map((app) => (
          <ApplicationCard
            key={app.id}
            app={app}
            status={state.apps[app.id]?.status ?? 'pending'}
            deploymentId={deploymentId}
          />
        ))}
      </div>
    </AdminShell>
  )
}

interface FailureCardProps {
  deploymentId: string
  status: 'failed' | 'unreachable'
  message: string | null
  onRetry: () => void
  onBack: () => void
}

function FailureCard({ deploymentId, status, message, onRetry, onBack }: FailureCardProps) {
  const isUnreachable = status === 'unreachable'
  return (
    <div className="sov-failure" role="alert" data-testid="sov-failure-card">
      <h3>{isUnreachable ? 'Couldn’t reach the deployment stream' : 'Provisioning failed'}</h3>
      <p>
        {isUnreachable
          ? `The catalyst-api is unreachable, or deployment ${deploymentId} is unknown to the backend.`
          : `The catalyst-api emitted a terminal failure for deployment ${deploymentId}.`}
      </p>
      {message && (
        <pre data-testid="sov-failure-error">{message}</pre>
      )}
      <div style={{ display: 'flex', gap: '0.5rem' }}>
        <button
          type="button"
          onClick={onRetry}
          data-testid="sov-failure-retry"
          style={{
            padding: '0.4rem 0.85rem',
            borderRadius: 6,
            border: '1px solid rgba(var(--wiz-accent-ch), 1)',
            background: 'rgba(var(--wiz-accent-ch), 1)',
            color: '#fff',
            font: 'inherit',
            fontWeight: 600,
            cursor: 'pointer',
          }}
        >
          Retry stream
        </button>
        <button
          type="button"
          onClick={onBack}
          data-testid="sov-failure-back"
          style={{
            padding: '0.4rem 0.85rem',
            borderRadius: 6,
            border: '1px solid var(--wiz-border-sub)',
            background: 'transparent',
            color: 'var(--wiz-text-md)',
            font: 'inherit',
            cursor: 'pointer',
          }}
        >
          Back to wizard
        </button>
      </div>
    </div>
  )
}
