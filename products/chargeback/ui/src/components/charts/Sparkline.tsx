import { formatNumber } from '../../lib/money'
import { PALETTE, SURFACE } from './palette'

export interface SparklineProps {
  values: number[]
  width?: number
  height?: number
  color?: string
  /** Light fill under the line (default on). */
  area?: boolean
  format?: (v: number) => string
  /** Accessible name prefix, e.g. the resource name. */
  label?: string
}

/**
 * Sparkline — a tiny inline trend for table cells: no axes, no legend, a
 * dot on the last point. Renders nothing for an empty series (a flat line
 * would claim a measurement that was never taken).
 */
export function Sparkline({ values, width = 120, height = 28, color = PALETTE[0], area = true, format, label }: SparklineProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const vals = values.map((v) => (Number.isFinite(v) ? v : NaN))
  const finite = vals.filter((v) => !Number.isNaN(v))
  if (!finite.length) return null
  const hi = Math.max(...finite, 0)
  const lo = Math.min(...finite, 0)
  const span = hi - lo || 1
  const pad = 3
  const n = vals.length
  const x = (i: number) => (n > 1 ? pad + (i * (width - 2 * pad)) / (n - 1) : width / 2)
  const y = (v: number) => height - pad - ((v - lo) / span) * (height - 2 * pad)
  let line = ''
  let pen = false
  let areaD = ''
  let run: string[] = []
  let runStart = 0
  let runEnd = 0
  const flush = () => {
    if (run.length) areaD += `M${x(runStart).toFixed(1)} ${y(0).toFixed(1)} ${run.map((p) => 'L' + p).join(' ')} L${x(runEnd).toFixed(1)} ${y(0).toFixed(1)} Z `
    run = []
  }
  vals.forEach((v, i) => {
    if (Number.isNaN(v)) {
      pen = false
      flush()
      return
    }
    const p = `${x(i).toFixed(1)} ${y(v).toFixed(1)}`
    line += `${pen ? 'L' : 'M'}${p} `
    if (!run.length) runStart = i
    runEnd = i
    run.push(p)
    pen = true
  })
  flush()
  let last = n - 1
  while (last >= 0 && Number.isNaN(vals[last])) last--
  const first = finite[0]
  const peak = Math.max(...finite)
  const aria = `${label ? label + ': ' : ''}trend ${fmt(first)} to ${fmt(vals[last])}, peak ${fmt(peak)}`
  return (
    <svg className="sparkline" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={aria}>
      <title>{aria}</title>
      {area ? <path d={areaD} fill={color} fillOpacity={0.12} stroke="none" /> : null}
      <path d={line} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
      <circle cx={x(last)} cy={y(vals[last])} r={2.5} fill={color} stroke={SURFACE} strokeWidth={1.5} />
    </svg>
  )
}
