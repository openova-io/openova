/**
 * StatusPanel — EPIC-6 Slice U-DR-1 (#1101).
 *
 * Renders the live Continuum CR status the K-Cont-2 reconciler patches
 * on every reconcile loop:
 *
 *   - Phase pill (Healthy / Degraded / SwitchingOver / FailedOver / ...)
 *   - Primary region badge
 *   - Lease holder + expiresAt
 *   - Replication lag with progress bar (green <30s, yellow 30-60s, red >60s)
 *   - Switchover-in-progress: "Step N/7 — <name>" live progress widget
 *   - LastSwitchover side panel (at, from, to, result)
 *
 * Pure presentation — receives the parsed Continuum CR's `status` map
 * via prop. Parent (DRSection) refetches via React Query and feeds in
 * fresh data.
 */

import { lagBucket, parseLagSeconds } from '@/lib/continuum.api'

export interface StatusPanelProps {
  /** Parsed Continuum CR status map (from GET /continuums/{name}). */
  status?: Record<string, unknown>
  /** Region claimed primary (carried separately so we can render a placeholder). */
  primaryRegion?: string
  /** Hot-standby regions for context. */
  hotStandbyRegions?: string[]
}

export function StatusPanel({ status, primaryRegion, hotStandbyRegions }: StatusPanelProps) {
  const phase = (status?.['phase'] as string | undefined) ?? 'Pending'
  const leaseHolder = (status?.['leaseHolder'] as string | undefined) ?? ''
  const leaseExpiresAt = (status?.['leaseExpiresAt'] as string | undefined) ?? ''
  const switchoverInProgress = Boolean(status?.['switchoverInProgress'] ?? false)
  const switchoverStep = (status?.['switchoverStep'] as string | undefined) ?? ''
  const lag = parseLagSeconds(status?.['replicationLagSeconds'])
  const last = (status?.['lastSwitchover'] as Record<string, unknown> | undefined) ?? undefined

  const bucket = lagBucket(lag)
  const lagBarColor =
    bucket === 'green'
      ? 'bg-green-500'
      : bucket === 'yellow'
        ? 'bg-yellow-500'
        : bucket === 'red'
          ? 'bg-red-500'
          : 'bg-[var(--color-border)]'
  const lagPct = lag == null ? 0 : Math.min(100, Math.round((lag / 90) * 100))

  return (
    <div className="continuum-status-panel" data-testid="continuum-status-panel">
      <div className="mb-3 grid grid-cols-1 gap-3 md:grid-cols-2">
        <div
          data-testid="continuum-status-phase"
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-sm"
        >
          <div className="mb-1 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Phase</div>
          <span
            className={`inline-flex rounded-md px-2 py-0.5 text-xs font-semibold uppercase ${
              phase === 'Healthy'
                ? 'bg-green-500/10 text-green-400'
                : phase === 'SwitchingOver'
                  ? 'bg-blue-500/10 text-blue-400'
                  : phase === 'Degraded'
                    ? 'bg-yellow-500/10 text-yellow-400'
                    : phase === 'FailedOver'
                      ? 'bg-purple-500/10 text-purple-400'
                      : phase === 'Failed'
                        ? 'bg-red-500/10 text-red-400'
                        : 'bg-[var(--color-border)]/40 text-[var(--color-text-dim)]'
            }`}
            data-testid={`continuum-status-phase-${phase}`}
          >
            {phase}
          </span>
        </div>

        <div
          data-testid="continuum-status-primary"
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-sm"
        >
          <div className="mb-1 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Primary region</div>
          <code className="font-mono text-[var(--color-text)]">{primaryRegion ?? '—'}</code>
        </div>
      </div>

      <div className="mb-3 grid grid-cols-1 gap-3 md:grid-cols-2">
        <div
          data-testid="continuum-status-lease"
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-sm"
        >
          <div className="mb-1 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Lease holder</div>
          <code className="font-mono text-[var(--color-text)]">{leaseHolder || '—'}</code>
          {leaseExpiresAt ? (
            <span className="ml-2 text-xs text-[var(--color-text-dim)]">
              expires {new Date(leaseExpiresAt).toLocaleTimeString()}
            </span>
          ) : null}
        </div>

        <div
          data-testid="continuum-status-lag"
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-sm"
        >
          <div className="mb-1 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Replication lag</div>
          <div className="flex items-baseline gap-2">
            <span className="font-mono text-[var(--color-text)]" data-testid="continuum-status-lag-value">
              {lag == null ? '—' : `${lag}s`}
            </span>
            <span
              className={`inline-flex rounded-md px-1.5 py-0.5 text-[10px] font-semibold uppercase ${
                bucket === 'green'
                  ? 'bg-green-500/10 text-green-400'
                  : bucket === 'yellow'
                    ? 'bg-yellow-500/10 text-yellow-400'
                    : bucket === 'red'
                      ? 'bg-red-500/10 text-red-400'
                      : 'bg-[var(--color-border)]/40 text-[var(--color-text-dim)]'
              }`}
              data-testid={`continuum-status-lag-bucket-${bucket}`}
            >
              {bucket}
            </span>
          </div>
          <div
            className="mt-1.5 h-1.5 w-full overflow-hidden rounded-full bg-[var(--color-border)]/30"
            aria-label="lag-bar"
          >
            <div
              className={`h-full ${lagBarColor}`}
              style={{ width: `${lagPct}%` }}
              data-testid="continuum-status-lag-bar"
            />
          </div>
        </div>
      </div>

      {switchoverInProgress ? (
        <div
          data-testid="continuum-status-switching-over"
          className="mb-3 rounded-md border border-blue-500/40 bg-blue-500/10 p-3 text-sm text-blue-300"
        >
          <div className="mb-1 text-xs uppercase tracking-wide">Switchover in progress</div>
          <div className="font-mono text-xs" data-testid="continuum-status-switching-over-step">
            {switchoverStep || 'step ?/7 — starting'}
          </div>
        </div>
      ) : null}

      {last && Object.keys(last).length > 0 ? (
        <div
          data-testid="continuum-status-last-switchover"
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs text-[var(--color-text-dim)]"
        >
          <div className="mb-1 uppercase tracking-wide text-[var(--color-text-dim)]">Last switchover</div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <strong className="text-[var(--color-text)]">at</strong>:{' '}
              {(last['at'] as string | undefined) ?? '—'}
            </div>
            <div>
              <strong className="text-[var(--color-text)]">result</strong>:{' '}
              <span data-testid="continuum-status-last-switchover-result">
                {(last['result'] as string | undefined) ?? '—'}
              </span>
            </div>
            <div>
              <strong className="text-[var(--color-text)]">from</strong>:{' '}
              <code className="font-mono text-[var(--color-text)]">
                {(last['from'] as string | undefined) ?? '—'}
              </code>
            </div>
            <div>
              <strong className="text-[var(--color-text)]">to</strong>:{' '}
              <code className="font-mono text-[var(--color-text)]">
                {(last['to'] as string | undefined) ?? '—'}
              </code>
            </div>
          </div>
        </div>
      ) : null}

      {hotStandbyRegions && hotStandbyRegions.length > 0 ? (
        <div
          data-testid="continuum-status-hotstandbys"
          className="mt-3 text-xs text-[var(--color-text-dim)]"
        >
          <strong className="text-[var(--color-text)]">Hot standbys</strong>:{' '}
          {hotStandbyRegions.map((r) => (
            <code
              key={r}
              data-testid={`continuum-status-hotstandby-${r}`}
              className="ml-2 inline-block rounded-md border border-[var(--color-border)] px-1.5 py-0.5 font-mono"
            >
              {r}
            </code>
          ))}
        </div>
      ) : null}
    </div>
  )
}
