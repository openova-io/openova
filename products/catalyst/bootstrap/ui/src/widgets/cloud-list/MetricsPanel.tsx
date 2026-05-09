/**
 * MetricsPanel — EPIC-4 Slice R5 (#1099) — sparkline + key-figure summary
 * for the focused resource's CPU / memory.
 *
 * Reuses Recharts (already a top-level dep — see ComplianceTreemap.tsx)
 * and the render-callback pattern from slice U1+U2.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #4 (never hardcode) — colour palette + sparkline width are exported
 *      constants for tests.
 */

import { useEffect, useState } from 'react'
import { Area, AreaChart, ResponsiveContainer, YAxis } from 'recharts'

import {
  getResourceMetrics,
  type MetricsResponse,
} from '@/pages/sovereign/cloud-list/resource.api'

// Module-private to keep this file component-only (react-refresh).
const SPARKLINE_HEIGHT_PX = 32
const SPARKLINE_COLOR = '#10b981' // emerald-500

export interface MetricsPanelProps {
  deploymentId: string
  kind: string
  ns: string | undefined
  name: string
  /** Test seam — when supplied, skip the live fetch + render this. */
  initial?: MetricsResponse
}

function formatCPU(milli?: number): string {
  if (milli == null) return '—'
  if (milli < 1000) return `${milli}m`
  return `${(milli / 1000).toFixed(2)} cores`
}

function formatMemory(bytes?: number): string {
  if (bytes == null) return '—'
  const KB = 1024
  const MB = KB * 1024
  const GB = MB * 1024
  if (bytes >= GB) return `${(bytes / GB).toFixed(2)} Gi`
  if (bytes >= MB) return `${(bytes / MB).toFixed(2)} Mi`
  if (bytes >= KB) return `${(bytes / KB).toFixed(2)} Ki`
  return `${bytes} B`
}

export function MetricsPanel({ deploymentId, kind, ns, name, initial }: MetricsPanelProps) {
  const [data, setData] = useState<MetricsResponse | null>(initial ?? null)
  const [error, setError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState<boolean>(!initial)

  useEffect(() => {
    if (initial) return
    let cancelled = false
    const ac = new AbortController()
    getResourceMetrics(deploymentId, kind, ns, name, '1h', ac.signal)
      .then((d) => {
        if (cancelled) return
        setData(d)
        setError(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false)
      })
    return () => {
      cancelled = true
      ac.abort()
    }
  }, [deploymentId, kind, ns, name, initial])

  if (isLoading) {
    return (
      <div data-testid="metrics-panel-loading" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Loading metrics…
      </div>
    )
  }
  if (error) {
    return (
      <div data-testid="metrics-panel-error" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Metrics temporarily unreachable.
      </div>
    )
  }
  if (!data || data.source !== 'metrics.k8s.io') {
    return (
      <div data-testid="metrics-panel-unavailable" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Metrics unavailable for this cluster (metrics-server not installed).
      </div>
    )
  }

  const series = data.series.length > 0 ? data.series : [data.current]
  const cpuSeries = series.map((s, i) => ({ x: i, v: s.cpuMilli ?? 0 }))
  const memSeries = series.map((s, i) => ({ x: i, v: s.memBytes ?? 0 }))

  return (
    <div data-testid="metrics-panel" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <KeyFigure
          label="CPU"
          value={formatCPU(data.current.cpuMilli)}
          dataPoints={cpuSeries}
          testId="metrics-panel-cpu"
        />
        <KeyFigure
          label="Memory"
          value={formatMemory(data.current.memBytes)}
          dataPoints={memSeries}
          testId="metrics-panel-memory"
        />
      </div>
      {data.current.podCount != null && (
        <div data-testid="metrics-panel-podcount" className="mt-3 text-xs text-[var(--color-text-dim)]">
          Aggregated across {data.current.podCount} pod{data.current.podCount === 1 ? '' : 's'}.
        </div>
      )}
    </div>
  )
}

interface KeyFigureProps {
  label: string
  value: string
  dataPoints: Array<{ x: number; v: number }>
  testId: string
}

function KeyFigure({ label, value, dataPoints, testId }: KeyFigureProps) {
  return (
    <div data-testid={testId}>
      <div className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">{label}</div>
      <div className="mt-1 text-2xl font-semibold text-[var(--color-text-strong)]">{value}</div>
      <div style={{ height: SPARKLINE_HEIGHT_PX }} className="mt-2">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={dataPoints} margin={{ top: 4, right: 4, left: 4, bottom: 0 }}>
            <YAxis hide domain={[0, 'dataMax+1']} />
            <Area
              type="monotone"
              dataKey="v"
              stroke={SPARKLINE_COLOR}
              fill={SPARKLINE_COLOR}
              fillOpacity={0.18}
              isAnimationActive={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </div>
  )
}
