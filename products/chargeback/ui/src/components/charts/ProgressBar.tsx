import { formatCompact, formatNumber, formatPct } from '../../lib/money'
import { EmptyChart } from './EmptyChart'
import { useWidth } from './measure'
import { fitLabel } from './scale'

export type ProgressState = 'ok' | 'warn' | 'bad'

/**
 * progressState: over the whole (≥ 100 %) is bad; past any threshold is a
 * warning; otherwise fine. Thresholds are percentages.
 */
export function progressState(pct: number, thresholds: number[] = []): ProgressState {
  if (!Number.isFinite(pct)) return 'ok'
  if (pct >= 100) return 'bad'
  return thresholds.some((t) => Number.isFinite(t) && pct >= t) ? 'warn' : 'ok'
}

export interface ProgressMarker {
  label: string
  value: number
}

export interface ProgressBarProps {
  value: number
  max: number
  /** Vertical markers with a label above, e.g. the forecast. */
  markers?: ProgressMarker[]
  /** Percent thresholds drawn as ticks; the fill colour shifts when one is crossed. */
  thresholds?: number[]
  format?: (v: number) => string
  /** Text at the left of the value line (e.g. the budget name). */
  label?: string
  height?: number
  title?: string
}

const COLOR: Record<ProgressState, string> = { ok: '#1d4ed8', warn: '#b45309', bad: '#b91c1c' }
const TRACK: Record<ProgressState, string> = { ok: '#dbeafe', warn: '#fef3c7', bad: '#fee2e2' }

/**
 * ProgressBar — a budget meter. The track is the budget; when the actual or a
 * marker exceeds it the scale extends and the budget line stays visible, so
 * an overrun is drawn as an overrun rather than clipped at 100 %.
 */
export function ProgressBar({ value, max, markers = [], thresholds = [], format, label, height = 10, title }: ProgressBarProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const { ref, width } = useWidth()
  if (!Number.isFinite(max) || max <= 0) return <EmptyChart message="No budget amount." />

  const v = Number.isFinite(value) ? Math.max(0, value) : 0
  const pct = (v / max) * 100
  const state = progressState(pct, thresholds)
  const marks = markers.filter((m) => Number.isFinite(m.value))
  // A marker far beyond the amount (a runaway forecast) would squash the bar
  // and its threshold ticks into a sliver; cap the scale at 2× the amount and
  // pin such markers to the right edge with their real value in the label.
  const rawScale = Math.max(max, v, ...marks.map((m) => m.value)) * (marks.length || v > max ? 1.04 : 1)
  const scale = Math.min(rawScale, max * 2)
  const above = marks.length ? 16 : 0
  const below = thresholds.length ? 14 : 0
  const svgH = above + height + below
  const x = (val: number) => (Math.max(0, Math.min(scale, val)) / scale) * width
  const barY = above

  const aria =
    `${title ?? label ?? 'Budget'}: ${formatPct(pct)} used, ${fmt(v)} of ${fmt(max)}` +
    (marks.length ? '; ' + marks.map((m) => `${m.label} ${fmt(m.value)}`).join(', ') : '') +
    (state === 'bad' ? '; over budget' : state === 'warn' ? '; threshold crossed' : '') +
    '.'

  return (
    <div className="chart progress" ref={ref}>
      <svg width="100%" height={svgH} viewBox={`0 0 ${width} ${svgH}`} role="img" aria-label={aria}>
        <title>{aria}</title>
        <rect x={0} y={barY} width={x(max)} height={height} fill={TRACK[state]} />
        {scale > max ? <rect x={x(max)} y={barY} width={width - x(max)} height={height} fill="#f1f5f9" /> : null}
        {v > 0 ? <rect className="chart-mark" x={0} y={barY} width={Math.max(1, x(v))} height={height} fill={COLOR[state]} /> : null}
        {scale > max ? <line x1={x(max)} x2={x(max)} y1={barY - 3} y2={barY + height + 3} stroke="#0f172a" strokeWidth={1.5} /> : null}
        {thresholds.map((t, i) => {
          const tx = x((t / 100) * max)
          // Skip a label that would land on the previous one (keep the tick).
          const prevX = i > 0 ? x((thresholds[i - 1] / 100) * max) : -Infinity
          const showLabel = tx - prevX >= 30
          return (
            <g key={t} className="chart-axis">
              <line x1={tx} x2={tx} y1={barY} y2={barY + height + 3} stroke="#64748b" strokeWidth={1} />
              {showLabel ? (
                <text x={tx} y={svgH - 2} textAnchor={tx > width - 24 ? 'end' : tx < 24 ? 'start' : 'middle'}>
                  {`${t} %`}
                </text>
              ) : null}
            </g>
          )
        })}
        {marks.map((m) => {
          const mx = x(m.value)
          const text = `${m.label} ${formatCompact(m.value)}${m.value > scale ? ' ▸' : ''}`
          const anchor = mx > width - 60 ? 'end' : 'start'
          return (
            <g key={m.label} className="chart-axis">
              <line x1={mx} x2={mx} y1={barY - 4} y2={barY + height + 1} stroke="#0f172a" strokeWidth={2} />
              <text x={mx + (anchor === 'start' ? 4 : -4)} y={barY - 6} textAnchor={anchor} fill="#0f172a">
                {fitLabel(text, anchor === 'start' ? width - mx - 4 : mx - 4)}
              </text>
              <title>{`${m.label}: ${fmt(m.value)}`}</title>
            </g>
          )
        })}
      </svg>
      <div className="txt">
        <span className="lbl" title={label}>
          {label ?? ''}
        </span>
        <span>
          <span className="pct">{formatPct(pct)}</span> · {fmt(v)} / {fmt(max)}
        </span>
      </div>
    </div>
  )
}
