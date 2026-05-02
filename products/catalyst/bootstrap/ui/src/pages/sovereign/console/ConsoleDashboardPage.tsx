/**
 * ConsoleDashboardPage — Sovereign Console /console/dashboard
 *
 * Overview cards: HelmReleases Ready, Pods Running, cert expiry.
 * In Sovereign mode there is no deploymentId — the cluster is identified
 * by the hostname. API calls target /api/v1/sovereign/* (Agent C's
 * server-side endpoints).
 *
 * For Phase 8b, this renders placeholder cards with wiring stubs.
 * The API integration is tracked in the epic's Phase-4 tickets.
 *
 * Related: GitHub issue #607
 */

import { useEffect, useState } from 'react'
import { Activity, CheckCircle, Shield } from 'lucide-react'
import { API_BASE } from '@/shared/config/urls'
import { loadTokens } from '@/shared/lib/oidc'

interface SovereignStatus {
  helmReleasesReady: number
  helmReleasesTotal: number
  podsRunning: number
  podsTotal: number
  certExpirySoonCount: number
}

type StatusState =
  | { status: 'loading' }
  | { status: 'loaded'; data: SovereignStatus }
  | { status: 'error'; message: string }

async function fetchSovereignStatus(): Promise<SovereignStatus> {
  const tokens = loadTokens()
  const resp = await fetch(`${API_BASE}/v1/sovereign/status`, {
    headers: {
      Accept: 'application/json',
      ...(tokens ? { Authorization: `Bearer ${tokens.accessToken}` } : {}),
    },
  })
  if (!resp.ok) throw new Error(`status ${resp.status}`)
  return resp.json() as Promise<SovereignStatus>
}

export function ConsoleDashboardPage() {
  const [state, setState] = useState<StatusState>({ status: 'loading' })

  useEffect(() => {
    fetchSovereignStatus()
      .then((data) => setState({ status: 'loaded', data }))
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        // Surface a placeholder state on API error — the console is still
        // usable for navigation even if the status endpoint is unavailable.
        setState({ status: 'error', message: msg })
      })
  }, [])

  return (
    <div data-testid="console-dashboard-page">
      <div className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Dashboard</h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Overview of your Sovereign cluster health.
        </p>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatusCard
          icon={<CheckCircle className="h-5 w-5 text-green-400" />}
          label="HelmReleases Ready"
          value={
            state.status === 'loaded'
              ? `${state.data.helmReleasesReady} / ${state.data.helmReleasesTotal}`
              : state.status === 'loading'
                ? '…'
                : '—'
          }
          testId="dashboard-hr-card"
        />
        <StatusCard
          icon={<Activity className="h-5 w-5 text-blue-400" />}
          label="Pods Running"
          value={
            state.status === 'loaded'
              ? `${state.data.podsRunning} / ${state.data.podsTotal}`
              : state.status === 'loading'
                ? '…'
                : '—'
          }
          testId="dashboard-pods-card"
        />
        <StatusCard
          icon={<Shield className="h-5 w-5 text-amber-400" />}
          label="Certs Expiring Soon"
          value={
            state.status === 'loaded'
              ? String(state.data.certExpirySoonCount)
              : state.status === 'loading'
                ? '…'
                : '—'
          }
          testId="dashboard-certs-card"
        />
      </div>

      {state.status === 'error' ? (
        <p
          className="mt-4 text-xs text-[var(--color-text-dim)]"
          data-testid="dashboard-api-note"
        >
          Live status unavailable ({state.message}) — API integration pending.
        </p>
      ) : null}
    </div>
  )
}

function StatusCard({
  icon,
  label,
  value,
  testId,
}: {
  icon: React.ReactNode
  label: string
  value: string
  testId: string
}) {
  return (
    <div
      className="flex items-center gap-4 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
      data-testid={testId}
    >
      <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-[var(--color-bg)]">
        {icon}
      </div>
      <div>
        <p className="text-xs text-[var(--color-text-dim)]">{label}</p>
        <p className="mt-0.5 text-2xl font-bold tabular-nums text-[var(--color-text-strong)]">
          {value}
        </p>
      </div>
    </div>
  )
}
