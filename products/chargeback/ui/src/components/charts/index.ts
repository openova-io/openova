// TEMPORARY contract stub for the chart library (#6867). The real
// dependency-free SVG implementation lands from the charts lane and replaces
// this file; the exported names and props below ARE the contract the pages
// code against. Rendering here is deliberately minimal (text only).
import { createElement, type ReactNode } from 'react'
import type { ExploreResult } from '../../api/types'

export type Series = { key: string; label: string; values: number[]; color?: string }

const PALETTE = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed', '#0891b2', '#db2777', '#65a30d', '#ea580c', '#4f46e5', '#0d9488', '#9333ea']
export function colorFor(i: number): string {
  return PALETTE[((i % PALETTE.length) + PALETTE.length) % PALETTE.length]
}

export interface StackedBarsProps {
  buckets: string[]
  series: Series[]
  format?: (v: number) => string
  labelFor?: (bucket: string) => string
  height?: number
  forecast?: { fromIndex: number; values: number[] } | null
  missing?: boolean[]
  onBarClick?: (bucketIndex: number, seriesKey?: string) => void
  legend?: boolean
  totalsLine?: boolean
}
export function StackedBars(p: StackedBarsProps): ReactNode {
  return createElement('div', { className: 'muted small' }, `[chart: ${p.series.length} series × ${p.buckets.length} buckets]`)
}

export interface LineChartProps extends StackedBarsProps {
  area?: boolean
  band?: { min: number[]; max: number[] } | null
}
export function LineChart(p: LineChartProps): ReactNode {
  return createElement('div', { className: 'muted small' }, `[line chart: ${p.series.length} series]`)
}

export interface DonutProps {
  slices: Array<{ key: string; label: string; value: number; color?: string }>
  format?: (v: number) => string
  centerLabel?: string
  centerCaption?: string
  onSliceClick?: (key: string) => void
  size?: number
}
export function Donut(p: DonutProps): ReactNode {
  return createElement('div', { className: 'muted small' }, `[donut: ${p.slices.length} slices]`)
}

export interface RankedBarsProps {
  rows: Array<{ key: string; label: string; value: number; share?: number; delta_pct?: number | null; color?: string }>
  format?: (v: number) => string
  onClick?: (key: string) => void
  max?: number
}
export function RankedBars(p: RankedBarsProps): ReactNode {
  return createElement('div', { className: 'muted small' }, `[ranked bars: ${p.rows.length} rows]`)
}

export interface SparklineProps {
  values: number[]
  width?: number
  height?: number
  color?: string
}
export function Sparkline(p: SparklineProps): ReactNode {
  return createElement('span', { className: 'muted tiny' }, `[${p.values.length} pts]`)
}

export type WaterfallStep = { label: string; value: number; kind: 'total' | 'delta' }
export interface WaterfallProps {
  steps: WaterfallStep[]
  format?: (v: number) => string
  height?: number
}
export function waterfallLayout(steps: WaterfallStep[]): Array<{ label: string; start: number; end: number; kind: 'total' | 'delta' }> {
  let run = 0
  return steps.map((s) => {
    if (s.kind === 'total') {
      run = s.value
      return { label: s.label, start: 0, end: s.value, kind: s.kind }
    }
    const start = run
    run += s.value
    return { label: s.label, start, end: run, kind: s.kind }
  })
}
export function Waterfall(p: WaterfallProps): ReactNode {
  return createElement('div', { className: 'muted small' }, `[waterfall: ${p.steps.length} steps]`)
}

export interface ProgressBarProps {
  value: number
  max: number
  markers?: Array<{ label: string; value: number }>
  thresholds?: number[]
  format?: (v: number) => string
}
export function ProgressBar(p: ProgressBarProps): ReactNode {
  const pct = p.max > 0 ? Math.round((p.value / p.max) * 100) : 0
  return createElement('div', { className: 'muted small' }, `${pct} % of ${p.format ? p.format(p.max) : p.max}`)
}

export function EmptyChart({ children }: { children?: ReactNode }): ReactNode {
  return createElement('div', { className: 'empty' }, children ?? 'No data in this window.')
}

/** Maps the explore document into StackedBars/LineChart props. */
export function seriesFromExplore(r: ExploreResult): Pick<StackedBarsProps, 'buckets' | 'series' | 'forecast' | 'missing'> {
  const series: Series[] = r.groups.map((g, i) => ({ key: g.key, label: g.label, values: g.values, color: colorFor(i) }))
  if (r.other) series.push({ key: 'other', label: r.other.label, values: r.other.values, color: '#94a3b8' })
  if (series.length === 0) series.push({ key: 'total', label: 'Total', values: r.totals_by_bucket, color: colorFor(0) })
  return { buckets: r.buckets, series, forecast: null, missing: r.bucket_has_data.map((h) => !h) }
}
