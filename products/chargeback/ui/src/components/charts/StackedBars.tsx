import { useState, type KeyboardEvent, type ReactNode } from 'react'
import { formatCompact, formatNumber } from '../../lib/money'
import { EmptyChart } from './EmptyChart'
import { Legend, type LegendItem } from './Legend'
import { HatchDefs, hatchForecast, hatchMissing, useChartId } from './defs'
import { useWidth } from './measure'
import { FORECAST_COLOR, SURFACE, colorFor } from './palette'
import { CHAR_PX, fitLabel, linearTicks, shortBucket, xLabelEvery } from './scale'
import { stackSeries, type ForecastTail, type Series } from './stack'
import { ChartTooltip, TipRows, useTooltip } from './tooltip'

export interface StackedBarsProps {
  buckets: string[]
  series: Series[]
  /** Formats a value for the tooltip and the accessible name. */
  format?: (v: number) => string
  /** Formats a y-axis tick (default: compact number, no currency). */
  axisFormat?: (v: number) => string
  /** Formats a bucket for the x axis (default: "1 Sep" / "Sep 2026"). */
  bucketLabel?: (b: string) => string
  height?: number
  /** Projected buckets, drawn hatched from fromIndex on. */
  forecast?: ForecastTail
  /** bucket_has_data=false → a hatched column and no bars (never a zero bar). */
  missing?: boolean[]
  onBarClick?: (bucketIndex: number, seriesKey: string) => void
  /** Show the legend (default true; a lone single-series item is never shown — the title names it). */
  legend?: boolean
  /** Draw the per-bucket total as a thin line over the bars. */
  totalsLine?: boolean
  /** Accessible name; prefixes the screen-reader summary. */
  title?: string
}

const TOP = 10
const RIGHT = 12
const BOTTOM = 24

/**
 * StackedBars — one column per bucket, one segment per series, in palette
 * order. Hover anywhere in a column for every series' value and the total;
 * hovering a legend item isolates that series. Missing buckets are hatched,
 * not zero; forecast buckets are hatched and dashed.
 */
export function StackedBars({
  buckets,
  series,
  format,
  axisFormat,
  bucketLabel,
  height = 240,
  forecast,
  missing,
  onBarClick,
  legend = true,
  totalsLine = false,
  title,
}: StackedBarsProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const afmt = axisFormat ?? formatCompact
  const blabel = bucketLabel ?? shortBucket
  const { ref, width } = useWidth()
  const { tip, show, hide } = useTooltip(ref)
  const id = useChartId()
  const [hoverKey, setHoverKey] = useState<string | null>(null)
  const [hoverCol, setHoverCol] = useState<number | null>(null)

  const n = buckets.length
  const observed = forecast ? Math.max(0, Math.min(forecast.fromIndex, n)) : n
  const drawn = (i: number) => i < observed && !missing?.[i]
  const hasData = series.length > 0 && buckets.some((_, i) => drawn(i) && series.some((s) => Number.isFinite(s.values[i])))
  if (!n || !hasData) return <EmptyChart height={height} />

  const st = stackSeries(series, observed)
  let hi = st.max
  if (forecast) for (const v of forecast.values) if (Number.isFinite(v) && v > hi) hi = v
  const ticks = linearTicks(st.min, hi, 5)
  const lo = ticks[0]
  hi = ticks[ticks.length - 1]
  const tickText = ticks.map(afmt)
  const left = Math.max(28, Math.max(...tickText.map((t) => t.length)) * 6.7 + 10)
  const plotW = Math.max(10, width - left - RIGHT)
  const plotH = Math.max(10, height - TOP - BOTTOM)
  const y = (v: number) => TOP + plotH - ((v - lo) / (hi - lo)) * plotH
  const slot = plotW / n
  const barW = Math.min(32, Math.max(2, slot * 0.64))
  const bx = (i: number) => left + i * slot + (slot - barW) / 2
  const cx = (i: number) => left + (i + 0.5) * slot
  // Thin labels by the width of the longest one, so thinning happens before truncation.
  const every = xLabelEvery(n, plotW, Math.max(...buckets.map((b) => blabel(b).length)) * CHAR_PX + 12)
  const color = (i: number) => series[i].color ?? colorFor(i)
  const labelW = (i: number) => Math.min(slot * every - 4, 2 * cx(i), 2 * (width - cx(i)))

  const tipFor = (i: number): ReactNode => {
    const t = buckets[i]
    if (i >= observed && forecast) {
      return <TipRows title={t} rows={[{ label: 'Forecast', value: fmt(forecast.values[i - observed] ?? 0), hatch: true }]} />
    }
    if (!drawn(i)) return <TipRows title={t} rows={[]} note="No data collected for this bucket." />
    const rows = series.map((s, si) => ({ label: s.label, value: fmt(Number(s.values[i]) || 0), color: color(si), strong: hoverKey === s.key }))
    return <TipRows title={t} rows={rows} total={series.length > 1 ? { label: 'Total', value: fmt(st.totals[i]) } : undefined} />
  }
  const enter = (i: number) => (e: { clientX: number; clientY: number }) => {
    setHoverCol(i)
    show(e, tipFor(i))
  }
  const leave = () => {
    setHoverCol(null)
    hide()
  }
  const onKey = (i: number, k: string) => (e: KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onBarClick?.(i, k)
    }
  }

  const items: LegendItem[] = series.map((s, i) => ({ key: s.key, label: s.label, color: color(i) }))
  if (forecast && observed < n) items.push({ key: '__forecast', label: 'Forecast', kind: 'hatch' })
  if (missing?.some((m, i) => m && i < observed)) items.push({ key: '__nodata', label: 'No data', kind: 'hatch-light' })

  // The totals line breaks at missing buckets rather than bridging them.
  let totalsPath = ''
  if (totalsLine) {
    let pen = false
    for (let i = 0; i < observed; i++) {
      if (!drawn(i)) {
        pen = false
        continue
      }
      totalsPath += `${pen ? 'L' : 'M'}${cx(i).toFixed(1)} ${y(st.totals[i]).toFixed(1)} `
      pen = true
    }
  }

  const sum = st.totals.reduce((a, b) => a + b, 0)
  const aria =
    `${title ?? 'Stacked bars'}: ${observed} buckets from ${blabel(buckets[0])} to ${blabel(buckets[Math.max(0, observed - 1)])}, ` +
    `${series.length} series, total ${fmt(sum)}${forecast && observed < n ? `, forecast to ${blabel(buckets[n - 1])}` : ''}.`

  return (
    <div className="chart" ref={ref}>
      <svg width="100%" height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={aria}>
        <HatchDefs id={id} />
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
        {buckets.map((b, i) => {
          const isMissing = i < observed && !!missing?.[i]
          const fc = i >= observed && forecast ? forecast.values[i - observed] : undefined
          return (
            <g key={`${b}-${i}`} onPointerMove={enter(i)} onPointerLeave={leave}>
              {isMissing ? (
                <rect x={left + i * slot} y={TOP} width={slot} height={plotH} fill={hatchMissing(id)}>
                  <title>{`${blabel(b)}: no data collected`}</title>
                </rect>
              ) : (
                <rect className="chart-hit" x={left + i * slot} y={TOP} width={slot} height={plotH} />
              )}
              {drawn(i)
                ? series.map((s, si) => {
                    const v = Number(s.values[i])
                    if (!Number.isFinite(v) || v === 0) return null
                    const a = y(st.y0[si][i])
                    const b2 = y(st.y1[si][i])
                    const top = Math.min(a, b2)
                    const h = Math.abs(a - b2)
                    if (h <= 0) return null
                    const dim = hoverKey !== null && hoverKey !== s.key
                    const label = `${s.label}, ${blabel(b)}: ${fmt(v)}`
                    return (
                      <rect
                        key={s.key}
                        className={`chart-mark${onBarClick ? ' clickable' : ''}`}
                        x={bx(i)}
                        y={top}
                        width={barW}
                        height={h > 2 ? h - 1 : h}
                        fill={color(si)}
                        style={{ opacity: dim ? 0.3 : hoverCol === i ? 0.8 : 1 }}
                        tabIndex={onBarClick ? 0 : undefined}
                        role={onBarClick ? 'button' : undefined}
                        aria-label={onBarClick ? label : undefined}
                        onClick={onBarClick ? () => onBarClick(i, s.key) : undefined}
                        onKeyDown={onBarClick ? onKey(i, s.key) : undefined}
                        onFocus={
                          onBarClick
                            ? () => {
                                setHoverCol(i)
                                show({ x: cx(i), y: top }, tipFor(i))
                              }
                            : undefined
                        }
                        onBlur={onBarClick ? leave : undefined}
                      >
                        <title>{label}</title>
                      </rect>
                    )
                  })
                : null}
              {fc !== undefined && Number.isFinite(fc) && fc > 0 ? (
                <rect
                  className="chart-mark"
                  x={bx(i)}
                  y={y(fc)}
                  width={barW}
                  height={y(0) - y(fc)}
                  fill={hatchForecast(id)}
                  stroke={FORECAST_COLOR}
                  strokeWidth={1}
                  strokeDasharray="3 2"
                  style={{ opacity: hoverCol === i ? 0.8 : 1 }}
                >
                  <title>{`Forecast, ${blabel(b)}: ${fmt(fc)}`}</title>
                </rect>
              ) : null}
            </g>
          )
        })}
        {totalsPath ? (
          <g pointerEvents="none">
            <path d={totalsPath} fill="none" stroke="#0f172a" strokeWidth={1.5} strokeOpacity={0.6} strokeLinejoin="round" />
            {buckets.slice(0, observed).map((b, i) =>
              drawn(i) ? <circle key={`${b}-${i}`} cx={cx(i)} cy={y(st.totals[i])} r={2.5} fill="#0f172a" stroke={SURFACE} strokeWidth={1.5} /> : null,
            )}
          </g>
        ) : null}
        <g className="chart-axis">
          {buckets.map((b, i) =>
            i % every === 0 ? (
              <text key={`${b}-${i}`} x={cx(i)} y={height - 6} textAnchor="middle">
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
