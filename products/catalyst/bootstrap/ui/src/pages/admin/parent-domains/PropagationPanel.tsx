/**
 * PropagationPanel — live DNS propagation status for a parent domain
 * (issue #829, parent epic #825).
 *
 * Renders one row per public DNS resolver the catalyst-api queries. Each
 * row has:
 *   - resolver name + geo (e.g. "Google · US")
 *   - status pill (green=converged / amber=diverged / red=error)
 *   - returned NS records (truncated at 2 lines)
 *   - latency
 *
 * Polls GET /api/v1/sovereign/parent-domains/<name>/propagation every
 * 60s. Per-page poll, not per-row, so we do NOT hammer public resolvers
 * (the catalyst-api fans out 5 lookups per request).
 *
 * Consumed by ParentDomainsPage.tsx as a collapsible per-row drawer.
 */

import { useEffect, useState } from 'react'
import { getPropagation, type PropagationResponse, type ResolverStatus } from './parentDomains.api'

const POLL_INTERVAL_MS = 60_000

export interface PropagationPanelProps {
  domainName: string
  /** Test seam — supplies the initial payload synchronously without firing fetch. */
  initialData?: PropagationResponse
  /** Test seam — disables both the fetch effect and the interval. */
  disablePolling?: boolean
}

export function PropagationPanel({
  domainName,
  initialData,
  disablePolling = false,
}: PropagationPanelProps) {
  const [data, setData] = useState<PropagationResponse | null>(initialData ?? null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState<boolean>(!initialData && !disablePolling)
  const [lastFetchedAt, setLastFetchedAt] = useState<Date | null>(null)

  useEffect(() => {
    if (disablePolling) return
    let cancelled = false

    async function poll() {
      try {
        const resp = await getPropagation(domainName)
        if (cancelled) return
        setData(resp)
        setError(null)
        setLastFetchedAt(new Date())
      } catch (err) {
        if (!cancelled) setError((err as Error).message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    poll()
    const id = setInterval(poll, POLL_INTERVAL_MS)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [domainName, disablePolling])

  if (loading) {
    return (
      <div
        data-testid={`propagation-loading-${domainName}`}
        className="px-4 py-3 text-xs text-[var(--color-text-dim)]"
      >
        Querying public DNS resolvers…
      </div>
    )
  }

  if (error && !data) {
    return (
      <div
        data-testid={`propagation-error-${domainName}`}
        className="m-3 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-300"
      >
        {error}
      </div>
    )
  }

  if (!data) {
    return null
  }

  const pctTone =
    data.percentage >= 80 ? 'green' : data.percentage >= 30 ? 'amber' : 'red'

  return (
    <div data-testid={`propagation-panel-${domainName}`} className="px-4 py-3">
      <div className="mb-3 flex items-center justify-between">
        <div>
          <div className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
            DNS Propagation
          </div>
          <div className="mt-1 text-sm text-[var(--color-text)]">
            <span className="font-mono">{domainName}</span> querying{' '}
            <span className="font-medium">{data.total}</span> public resolvers
          </div>
        </div>
        <div
          data-testid={`propagation-pct-${domainName}`}
          className={`rounded-full px-3 py-1 text-sm font-semibold ${
            pctTone === 'green'
              ? 'bg-emerald-500/15 text-emerald-300'
              : pctTone === 'amber'
                ? 'bg-amber-500/15 text-amber-300'
                : 'bg-red-500/15 text-red-300'
          }`}
        >
          {data.percentage}% propagated
        </div>
      </div>

      {data.expectedNs.length > 0 && (
        <div className="mb-2 text-xs text-[var(--color-text-dim)]">
          Expected NS:{' '}
          {data.expectedNs.map((n) => (
            <code
              key={n}
              className="ml-1 rounded bg-[var(--color-bg-2)] px-1.5 py-0.5 text-[11px]"
            >
              {n}
            </code>
          ))}
        </div>
      )}

      <table
        data-testid={`propagation-table-${domainName}`}
        className="w-full border-collapse text-xs"
      >
        <thead>
          <tr className="border-b border-[var(--color-border)] text-left text-[10px] uppercase text-[var(--color-text-dim)]">
            <th className="py-1.5 pr-2">Resolver</th>
            <th className="py-1.5 pr-2">IP</th>
            <th className="py-1.5 pr-2">Status</th>
            <th className="py-1.5 pr-2">Returned NS</th>
            <th className="py-1.5 pr-2 text-right">Latency</th>
          </tr>
        </thead>
        <tbody>
          {data.resolvers.map((r) => (
            <tr
              key={r.resolver.ip}
              data-testid={`propagation-row-${r.resolver.ip}`}
              data-status={r.status}
              className="border-b border-[var(--color-border)]"
            >
              <td className="py-1.5 pr-2 text-[var(--color-text)]">
                {r.resolver.name}{' '}
                <span className="text-[10px] text-[var(--color-text-dim)]">· {r.resolver.geo}</span>
              </td>
              <td className="py-1.5 pr-2 font-mono text-[11px] text-[var(--color-text-dim)]">
                {r.resolver.ip}
              </td>
              <td className="py-1.5 pr-2">
                <StatusPill status={r.status} />
              </td>
              <td className="py-1.5 pr-2 font-mono text-[10px] text-[var(--color-text-dim)]">
                {r.error ? (
                  <span className="text-red-400">{r.error}</span>
                ) : r.ns.length === 0 ? (
                  <span className="italic">no NS records</span>
                ) : (
                  r.ns.slice(0, 2).join(', ') + (r.ns.length > 2 ? ` +${r.ns.length - 2}` : '')
                )}
              </td>
              <td className="py-1.5 pr-2 text-right text-[var(--color-text-dim)]">
                {r.latencyMs}ms
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <div className="mt-2 flex items-center justify-between text-[10px] text-[var(--color-text-dim)]">
        <span>
          Auto-refreshes every {POLL_INTERVAL_MS / 1000}s. gTLD NS TTL is 48h — full convergence
          takes time.
        </span>
        {lastFetchedAt && (
          <span data-testid={`propagation-last-${domainName}`}>
            Updated {lastFetchedAt.toLocaleTimeString()}
          </span>
        )}
      </div>
    </div>
  )
}

function StatusPill({ status }: { status: ResolverStatus }) {
  const tone =
    status === 'converged' ? 'green' : status === 'diverged' ? 'amber' : 'red'
  const label =
    status === 'converged'
      ? 'Converged'
      : status === 'diverged'
        ? 'Pending'
        : 'Error'
  return (
    <span
      data-testid={`status-pill-${status}`}
      className={`inline-block rounded-full px-2 py-0.5 text-[10px] font-semibold ${
        tone === 'green'
          ? 'bg-emerald-500/15 text-emerald-300'
          : tone === 'amber'
            ? 'bg-amber-500/15 text-amber-300'
            : 'bg-red-500/15 text-red-300'
      }`}
    >
      {label}
    </span>
  )
}
