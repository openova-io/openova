import { useState } from 'react'
import { formatCompact, formatNumber } from '../../lib/money'
import { EmptyChart } from './EmptyChart'
import { Legend } from './Legend'
import { useWidth } from './measure'
import { fitLabel, linearTicks } from './scale'
import { ChartTooltip, TipRows, useTooltip } from './tooltip'

export interface WaterfallStep {
  label: string
  value: number
  /** total: a bar from zero (list, net, total) · delta: a change from the running total (discount, tax). */
  kind: 'total' | 'delta'
}

export interface WaterfallBar extends WaterfallStep {
  index: number
  /** Running total before / after the step. */
  start: number
  end: number
  /** Bar extent, low to high. */
  y0: number
  y1: number
}

export interface WaterfallLayoutResult {
  bars: WaterfallBar[]
  min: number
  max: number
}

/**
 * waterfallLayout computes the running totals: a 'total' step is drawn from
 * zero and resets the running total to its value; a 'delta' step is drawn
 * from the running total and moves it by the step's value.
 */
export function waterfallLayout(steps: WaterfallStep[]): WaterfallLayoutResult {
  let running = 0
  let min = 0
  let max = 0
  const bars = steps.map((s, index) => {
    const value = Number.isFinite(s.value) ? s.value : 0
    const start = s.kind === 'total' ? 0 : running
    const end = s.kind === 'total' ? value : running + value
    running = end
    const y0 = Math.min(start, end)
    const y1 = Math.max(start, end)
    if (y0 < min) min = y0
    if (y1 > max) max = y1
    return { ...s, value, index, start, end, y0, y1 }
  })
  return { bars, min, max }
}

export interface WaterfallProps {
  steps: WaterfallStep[]
  format?: (v: number) => string
  axisFormat?: (v: number) => string
  height?: number
  legend?: boolean
  title?: string
  colors?: { total?: string; up?: string; down?: string }
}

const TOP = 22
const RIGHT = 12
const BOTTOM = 24

/**
 * Waterfall — list → discounts → net → tax → total. Totals stand on the
 * baseline; deltas hang off the running total, connected by hairlines, with
 * the value on every cap (few bars, so each label earns its place).
 */
export function Waterfall({ steps, format, axisFormat, height = 220, legend = true, title, colors }: WaterfallProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const afmt = axisFormat ?? formatCompact
  const { ref, width } = useWidth()
  const { tip, show, hide } = useTooltip(ref)
  const [hover, setHover] = useState<number | null>(null)
  if (!steps.length) return <EmptyChart height={height} />

  const cTotal = colors?.total ?? '#1d4ed8'
  const cUp = colors?.up ?? '#b45309'
  const cDown = colors?.down ?? '#15803d'
  const { bars, min, max } = waterfallLayout(steps)
  const ticks = linearTicks(min, max, 5)
  const lo = ticks[0]
  const hi = ticks[ticks.length - 1]
  const tickText = ticks.map(afmt)
  const left = Math.max(28, Math.max(...tickText.map((t) => t.length)) * 6.7 + 10)
  const plotW = Math.max(10, width - left - RIGHT)
  const plotH = Math.max(10, height - TOP - BOTTOM)
  const y = (v: number) => TOP + plotH - ((v - lo) / (hi - lo)) * plotH
  const n = bars.length
  const slot = plotW / n
  const barW = Math.min(48, Math.max(4, slot * 0.6))
  const bx = (i: number) => left + i * slot + (slot - barW) / 2
  const cx = (i: number) => left + (i + 0.5) * slot
  const colorOf = (b: WaterfallBar) => (b.kind === 'total' ? cTotal : b.value >= 0 ? cUp : cDown)

  const aria = `${title ?? 'Waterfall'}: ` + bars.map((b) => `${b.label} ${fmt(b.value)}`).join(', ') + `; ends at ${fmt(bars[n - 1].end)}.`

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
        {bars.map((b, i) =>
          i < n - 1 ? <line key={`c${i}`} x1={bx(i) + barW} x2={bx(i + 1)} y1={y(b.end)} y2={y(b.end)} stroke="#94a3b8" strokeWidth={1} /> : null,
        )}
        {bars.map((b, i) => {
          const top = y(b.y1)
          const h = Math.max(1, y(b.y0) - top)
          const label = `${b.label}: ${fmt(b.value)}${b.kind === 'delta' ? ` (running ${fmt(b.end)})` : ''}`
          return (
            <g
              key={i}
              onPointerMove={(e) => {
                setHover(i)
                show(
                  e,
                  <TipRows
                    title={b.label}
                    rows={[{ label: b.kind === 'total' ? 'Amount' : b.value >= 0 ? 'Adds' : 'Removes', value: fmt(b.value), color: colorOf(b) }]}
                    total={b.kind === 'delta' ? { label: 'Running total', value: fmt(b.end) } : undefined}
                  />,
                )
              }}
              onPointerLeave={() => {
                setHover(null)
                hide()
              }}
            >
              <rect className="chart-hit" x={left + i * slot} y={TOP} width={slot} height={plotH} />
              <rect className="chart-mark" x={bx(i)} y={top} width={barW} height={h} fill={colorOf(b)} style={{ opacity: hover === null || hover === i ? 1 : 0.5 }}>
                <title>{label}</title>
              </rect>
              <text className="chart-label" x={cx(i)} y={top - 5} textAnchor="middle">
                {fitLabel(fmt(b.value), slot - 4)}
              </text>
              <text className="chart-axis" x={cx(i)} y={height - 6} textAnchor="middle" fill="#64748b">
                {fitLabel(b.label, slot - 4)}
              </text>
            </g>
          )
        })}
      </svg>
      <ChartTooltip tip={tip} width={width} />
      {legend ? (
        <Legend
          items={[
            { key: 'total', label: 'Total', color: cTotal },
            { key: 'up', label: 'Increase', color: cUp },
            { key: 'down', label: 'Decrease', color: cDown },
          ]}
        />
      ) : null}
    </div>
  )
}
