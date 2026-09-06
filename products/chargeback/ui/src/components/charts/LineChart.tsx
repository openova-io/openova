import { useState, type KeyboardEvent, type ReactNode } from 'react'
import { formatCompact, formatNumber } from '../../lib/money'
import { EmptyChart } from './EmptyChart'
import { Legend, type LegendItem } from './Legend'
import { useWidth } from './measure'
import { FORECAST_COLOR, SURFACE, colorFor } from './palette'
import { CHAR_PX, fitLabel, linearTicks, shortBucket, xLabelEvery } from './scale'
import type { ForecastTail, Series } from './stack'
import { ChartTooltip, TipRows, useTooltip, type TipRow } from './tooltip'

export interface LineChartProps {
  buckets: string[]
  series: Series[]
  format?: (v: number) => string
  axisFormat?: (v: number) => string
  bucketLabel?: (b: string) => string
  height?: number
  /** Fill under each line (a 12 % wash of the series colour). */
  area?: boolean
  /** Dashed projection continuing from the last observed bucket. */
  forecast?: ForecastTail
  /** Expected range behind a single series (expected vs actual); ignored for multi-series. */
  band?: { min: number[]; max: number[]; label?: string }
  /** bucket_has_data=false → a gap in the line, never a dip to zero. */
  missing?: boolean[]
  legend?: boolean
  /**
   * Draw the per-bucket total as a thin ink line. Defaults to on when there
   * are several series AND a forecast, since the forecast continues the
   * total and needs something to continue from.
   */
  totalsLine?: boolean
  title?: string
  onPointClick?: (bucketIndex: number) => void
}

const TOP = 10
const RIGHT = 12
const BOTTOM = 24
const PAD = 8

type Pt = { x: number; y: number } | null

function pathOf(points: Pt[]): string {
  let d = ''
  let pen = false
  for (const p of points) {
    if (!p) {
      pen = false
      continue
    }
    d += `${pen ? 'L' : 'M'}${p.x.toFixed(1)} ${p.y.toFixed(1)} `
    pen = true
  }
  return d
}

/** Closed areas for each contiguous run of points, down to the baseline. */
function areaOf(points: Pt[], base: number): string {
  let d = ''
  let run: { x: number; y: number }[] = []
  const flush = () => {
    if (run.length) {
      d += `M${run[0].x.toFixed(1)} ${base.toFixed(1)} ` + run.map((p) => `L${p.x.toFixed(1)} ${p.y.toFixed(1)} `).join('') + `L${run[run.length - 1].x.toFixed(1)} ${base.toFixed(1)} Z `
    }
    run = []
  }
  for (const p of points) {
    if (!p) flush()
    else run.push(p)
  }
  flush()
  return d
}

/**
 * LineChart — one line per series (not stacked). A crosshair snaps to the
 * nearest bucket and one tooltip lists every series there; arrow keys walk
 * the buckets from the keyboard. The forecast continues from the last
 * observed value (the single series, or the sum when there are several).
 */
export function LineChart({
  buckets,
  series,
  format,
  axisFormat,
  bucketLabel,
  height = 220,
  area = false,
  forecast,
  band,
  missing,
  legend = true,
  totalsLine,
  title,
  onPointClick,
}: LineChartProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const afmt = axisFormat ?? formatCompact
  const blabel = bucketLabel ?? shortBucket
  const { ref, width } = useWidth()
  const { tip, show, hide } = useTooltip(ref)
  const [hover, setHover] = useState<number | null>(null)
  const [hoverKey, setHoverKey] = useState<string | null>(null)

  const n = buckets.length
  const observed = forecast ? Math.max(0, Math.min(forecast.fromIndex, n)) : n
  const drawn = (i: number) => i < observed && !missing?.[i]
  const val = (s: Series, i: number): number | undefined => {
    if (!drawn(i)) return undefined
    const v = Number(s.values[i])
    return Number.isFinite(v) ? v : undefined
  }
  const hasData = series.length > 0 && buckets.some((_, i) => series.some((s) => val(s, i) !== undefined))
  if (!n || !hasData) return <EmptyChart height={height} />

  const useBand = band && series.length === 1 ? band : undefined
  let hi = 0
  let lo = 0
  for (const s of series) for (let i = 0; i < observed; i++) {
    const v = val(s, i)
    if (v !== undefined) {
      if (v > hi) hi = v
      if (v < lo) lo = v
    }
  }
  if (forecast) for (const v of forecast.values) if (Number.isFinite(v)) hi = Math.max(hi, v)
  if (totalsLine ?? (series.length > 1 && !!forecast)) {
    for (let i = 0; i < observed; i++) if (drawn(i)) hi = Math.max(hi, series.reduce((a, s) => a + (val(s, i) ?? 0), 0))
  }
  if (useBand) {
    for (const v of useBand.max) if (Number.isFinite(v)) hi = Math.max(hi, v)
    for (const v of useBand.min) if (Number.isFinite(v)) lo = Math.min(lo, v)
  }
  const ticks = linearTicks(lo, hi, 5)
  lo = ticks[0]
  hi = ticks[ticks.length - 1]
  const tickText = ticks.map(afmt)
  const left = Math.max(28, Math.max(...tickText.map((t) => t.length)) * 6.7 + 10)
  const plotW = Math.max(10, width - left - RIGHT)
  const plotH = Math.max(10, height - TOP - BOTTOM)
  const y = (v: number) => TOP + plotH - ((v - lo) / (hi - lo)) * plotH
  const step = n > 1 ? (plotW - 2 * PAD) / (n - 1) : 0
  const x = (i: number) => (n > 1 ? left + PAD + i * step : left + plotW / 2)
  // Thin labels by the width of the longest one, so thinning happens before truncation.
  const every = xLabelEvery(n, plotW, Math.max(...buckets.map((b) => blabel(b).length)) * CHAR_PX + 12)
  const color = (i: number) => series[i].color ?? colorFor(i)
  const labelW = (i: number) => Math.min(Math.max(step, 1) * every - 4, 2 * x(i), 2 * (width - x(i)))

  const pts = series.map((s) => buckets.map((_, i): Pt => (i < observed && val(s, i) !== undefined ? { x: x(i), y: y(val(s, i) as number) } : null)))
  const drawTotals = totalsLine ?? (series.length > 1 && !!forecast && observed < n)
  const totalPts: Pt[] = drawTotals ? buckets.map((_, i): Pt => (drawn(i) ? { x: x(i), y: y(series.reduce((a, s) => a + (val(s, i) ?? 0), 0)) } : null)) : []

  // Forecast starts where the data ends: the last drawn bucket's value (or sum).
  let fcPath = ''
  let fcPts: Pt[] = []
  if (forecast && observed < n) {
    let j = observed - 1
    while (j >= 0 && !drawn(j)) j--
    const start = j >= 0 ? series.reduce((a, s) => a + (val(s, j) ?? 0), 0) : undefined
    const pointsF: Pt[] = []
    if (start !== undefined) pointsF.push({ x: x(j), y: y(start) })
    for (let i = observed; i < n; i++) {
      const v = forecast.values[i - observed]
      pointsF.push(Number.isFinite(v) ? { x: x(i), y: y(v) } : null)
    }
    fcPath = pathOf(pointsF)
    fcPts = pointsF
  }

  let bandPath = ''
  if (useBand) {
    let run: number[] = []
    const flush = () => {
      if (run.length) {
        const top = run.map((i) => `${x(i).toFixed(1)} ${y(useBand.max[i]).toFixed(1)}`)
        const bottom = run.slice().reverse().map((i) => `${x(i).toFixed(1)} ${y(useBand.min[i]).toFixed(1)}`)
        bandPath += `M${top.join(' L')} L${bottom.join(' L')} Z `
      }
      run = []
    }
    for (let i = 0; i < observed; i++) {
      if (Number.isFinite(useBand.min[i]) && Number.isFinite(useBand.max[i])) run.push(i)
      else flush()
    }
    flush()
  }

  const tipFor = (i: number): ReactNode => {
    const t = buckets[i]
    if (i >= observed && forecast) {
      return <TipRows title={t} rows={[{ label: 'Forecast', value: fmt(forecast.values[i - observed] ?? 0), hatch: true }]} />
    }
    if (!drawn(i)) return <TipRows title={t} rows={[]} note="No data collected for this bucket." />
    const rows: TipRow[] = series.map((s, si) => {
      const v = val(s, i)
      return { label: s.label, value: v === undefined ? '—' : fmt(v), color: color(si), strong: hoverKey === s.key }
    })
    if (useBand && Number.isFinite(useBand.min[i]) && Number.isFinite(useBand.max[i])) {
      rows.push({ label: useBand.label ?? 'Expected', value: `${fmt(useBand.min[i])} – ${fmt(useBand.max[i])}` })
    }
    const total = series.length > 1 ? { label: 'Total', value: fmt(series.reduce((a, s) => a + (val(s, i) ?? 0), 0)) } : undefined
    return <TipRows title={t} rows={rows} total={total} />
  }

  const nearest = (clientX: number): number => {
    const r = ref.current?.getBoundingClientRect()
    const px = clientX - (r?.left ?? 0)
    if (n <= 1 || step <= 0) return 0
    return Math.max(0, Math.min(n - 1, Math.round((px - left - PAD) / step)))
  }
  const focusAt = (i: number) => {
    setHover(i)
    const ys = pts.map((p) => p[i]?.y).filter((v): v is number => v !== undefined)
    show({ x: x(i), y: ys.length ? Math.min(...ys) : TOP }, tipFor(i))
  }
  const onKey = (e: KeyboardEvent) => {
    const cur = hover ?? observed - 1
    if (e.key === 'ArrowRight') focusAt(Math.min(n - 1, cur + 1))
    else if (e.key === 'ArrowLeft') focusAt(Math.max(0, cur - 1))
    else if (e.key === 'Home') focusAt(0)
    else if (e.key === 'End') focusAt(n - 1)
    else if (e.key === 'Escape') leave()
    else if ((e.key === 'Enter' || e.key === ' ') && onPointClick && hover !== null) onPointClick(hover)
    else return
    e.preventDefault()
  }
  const leave = () => {
    setHover(null)
    hide()
  }

  const items: LegendItem[] = series.map((s, i) => ({ key: s.key, label: s.label, color: color(i), kind: area ? 'rect' : 'line' }))
  if (useBand) items.push({ key: '__band', label: useBand.label ?? 'Expected range', color: color(0), kind: 'rect' })
  if (drawTotals) items.push({ key: '__total', label: 'Total', color: '#0f172a', kind: 'line' })
  if (fcPath) items.push({ key: '__forecast', label: series.length > 1 ? 'Forecast (total)' : 'Forecast', kind: 'dash' })
  if (missing?.some((m, i) => m && i < observed)) items.push({ key: '__nodata', label: 'No data (gap)', kind: 'hatch-light' })

  const aria =
    `${title ?? 'Line chart'}: ${observed} buckets from ${blabel(buckets[0])} to ${blabel(buckets[Math.max(0, observed - 1)])}, ` +
    `${series.length} series${fcPath ? `, forecast to ${blabel(buckets[n - 1])}` : ''}.`

  return (
    <div className="chart" ref={ref}>
      <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={aria}>
        <g className="chart-grid">
          {ticks.map((t) => (
            <line key={t} className={t === 0 ? 'zero' : ''} x1={left} x2={left + plotW} y1={y(t)} y2={y(t)} />
          ))}
        </g>
        <g className="chart-axis">
          {ticks.map((t, i) => (
            <text key={t} x={left - 6} y={y(t)} textAnchor="end" dominantBaseline="middle">
              {tickText[i]}
            </text>
          ))}
        </g>
        {bandPath ? <path d={bandPath} fill={color(0)} fillOpacity={0.12} stroke="none" /> : null}
        {series.map((s, si) => {
          const dim = hoverKey !== null && hoverKey !== s.key
          return (
            <g key={s.key} className="chart-mark" style={{ opacity: dim ? 0.25 : 1 }}>
              {area ? <path d={areaOf(pts[si], y(0))} fill={color(si)} fillOpacity={0.12} stroke="none" /> : null}
              <path d={pathOf(pts[si])} fill="none" stroke={color(si)} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round">
                <title>{s.label}</title>
              </path>
              {pts[si].map((p, i) =>
                p && (n === 1 || (i > 0 && !pts[si][i - 1] && i < n - 1 && !pts[si][i + 1]) || (i === 0 && !pts[si][1]) || (i === n - 1 && !pts[si][n - 2])) ? (
                  <circle key={i} cx={p.x} cy={p.y} r={3} fill={color(si)} />
                ) : null,
              )}
            </g>
          )
        })}
        {drawTotals ? (
          <path d={pathOf(totalPts)} fill="none" stroke="#0f172a" strokeWidth={1.5} strokeOpacity={0.6} strokeLinejoin="round" strokeLinecap="round" pointerEvents="none">
            <title>Total</title>
          </path>
        ) : null}
        {fcPath ? <path d={fcPath} fill="none" stroke={FORECAST_COLOR} strokeWidth={2} strokeDasharray="5 4" strokeLinecap="round" strokeLinejoin="round" /> : null}
        {hover !== null ? (
          <g pointerEvents="none">
            <line x1={x(hover)} x2={x(hover)} y1={TOP} y2={TOP + plotH} stroke="#94a3b8" strokeWidth={1} />
            {hover < observed
              ? pts.map((p, si) => (p[hover] ? <circle key={si} cx={p[hover]!.x} cy={p[hover]!.y} r={4} fill={color(si)} stroke={SURFACE} strokeWidth={2} /> : null))
              : (() => {
                  const p = fcPts[hover - observed + (fcPts.length - (n - observed))]
                  return p ? <circle cx={p.x} cy={p.y} r={4} fill={FORECAST_COLOR} stroke={SURFACE} strokeWidth={2} /> : null
                })()}
          </g>
        ) : null}
        <rect
          className="chart-hit chart-overlay"
          x={left}
          y={TOP}
          width={plotW}
          height={plotH}
          tabIndex={0}
          aria-label="Chart values; use arrow keys to move between buckets"
          style={{ cursor: onPointClick ? 'pointer' : 'crosshair' }}
          onPointerMove={(e) => {
            const i = nearest(e.clientX)
            setHover(i)
            show(e, tipFor(i))
          }}
          onPointerLeave={leave}
          onFocus={() => focusAt(Math.max(0, observed - 1))}
          onBlur={leave}
          onKeyDown={onKey}
          onClick={onPointClick ? (e) => onPointClick(nearest(e.clientX)) : undefined}
        />
        <g className="chart-axis">
          {buckets.map((b, i) =>
            i % every === 0 ? (
              <text key={`${b}-${i}`} x={x(i)} y={height - 6} textAnchor="middle">
                {fitLabel(blabel(b), labelW(i))}
              </text>
            ) : null,
          )}
        </g>
      </svg>
      <ChartTooltip tip={tip} width={width} />
      {legend && items.length > 1 ? <Legend items={items} active={hoverKey} onHover={setHoverKey} /> : null}
    </div>
  )
}
