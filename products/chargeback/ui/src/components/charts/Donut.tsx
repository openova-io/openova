import { useState } from 'react'
import { formatCompact, formatNumber, formatPct } from '../../lib/money'
import { EmptyChart } from './EmptyChart'
import { useWidth } from './measure'
import { OTHER_COLOR, OTHER_KEY, SURFACE, colorForKey } from './palette'
import { ChartTooltip, TipRows, useTooltip } from './tooltip'

export interface DonutSlice {
  key: string
  label: string
  value: number
  color?: string
}

export interface DonutArc {
  key: string
  label: string
  value: number
  share: number
  /** Radians, clockwise from 12 o'clock. */
  start: number
  end: number
  color: string
  /** Keys of the slices folded into this arc (only on the "other" arc). */
  folded: string[]
}

export interface DonutLegendRow {
  key: string
  label: string
  value: number
  share: number
  color: string
  /** Drawn inside the "Other" arc because it is below the fold threshold. */
  folded: boolean
}

export interface DonutLayout {
  total: number
  arcs: DonutArc[]
  legend: DonutLegendRow[]
}

/**
 * donutLayout turns slices into arcs. Slices below `foldBelow` (a share,
 * default 1.5 %) are drawn inside one neutral "Other" arc — a sliver has no
 * readable area — but every slice stays in the legend with its own share.
 * An "other" slice the API already sent joins the same arc.
 */
export function donutLayout(slices: DonutSlice[], foldBelow = 0.015): DonutLayout {
  const clean = slices.map((s) => ({ ...s, value: Number.isFinite(s.value) && s.value > 0 ? s.value : 0 }))
  const total = clean.reduce((a, s) => a + s.value, 0)
  const keys = clean.map((s) => s.key)
  const colorOf = (s: DonutSlice) => s.color ?? colorForKey(s.key, keys)
  const share = (v: number) => (total > 0 ? v / total : 0)
  const isFolded = (s: DonutSlice) => s.key === OTHER_KEY || share(s.value) < foldBelow

  const arcs: DonutArc[] = []
  let angle = -Math.PI / 2
  const push = (key: string, label: string, value: number, color: string, folded: string[]) => {
    const sh = share(value)
    const end = angle + sh * 2 * Math.PI
    arcs.push({ key, label, value, share: sh, start: angle, end, color, folded })
    angle = end
  }
  for (const s of clean) if (s.value > 0 && !isFolded(s)) push(s.key, s.label, s.value, colorOf(s), [])
  const folded = clean.filter((s) => s.value > 0 && isFolded(s))
  if (folded.length) {
    const own = folded.find((s) => s.key === OTHER_KEY)
    push(
      OTHER_KEY,
      own?.label ?? 'Other',
      folded.reduce((a, s) => a + s.value, 0),
      OTHER_COLOR,
      folded.map((s) => s.key),
    )
  }
  const legend: DonutLegendRow[] = clean.map((s) => ({
    key: s.key,
    label: s.label,
    value: s.value,
    share: share(s.value),
    color: isFolded(s) ? OTHER_COLOR : colorOf(s),
    folded: isFolded(s) && s.key !== OTHER_KEY,
  }))
  return { total, arcs, legend }
}

function pt(cx: number, cy: number, r: number, a: number): string {
  return `${(cx + r * Math.cos(a)).toFixed(2)} ${(cy + r * Math.sin(a)).toFixed(2)}`
}

/** An annular sector; a full ring is drawn as two halves (an arc cannot span 360°). */
export function arcPath(cx: number, cy: number, ro: number, ri: number, a0: number, a1: number): string {
  if (a1 - a0 >= 2 * Math.PI - 1e-6) {
    const mid = a0 + Math.PI
    return `${arcPath(cx, cy, ro, ri, a0, mid)} ${arcPath(cx, cy, ro, ri, mid, a1)}`
  }
  const large = a1 - a0 > Math.PI ? 1 : 0
  return `M${pt(cx, cy, ro, a0)} A${ro} ${ro} 0 ${large} 1 ${pt(cx, cy, ro, a1)} L${pt(cx, cy, ri, a1)} A${ri} ${ri} 0 ${large} 0 ${pt(cx, cy, ri, a0)} Z`
}

export interface DonutProps {
  slices: DonutSlice[]
  format?: (v: number) => string
  /** Used for the centre figure when the full format does not fit the hole (default: compact number). */
  compactFormat?: (v: number) => string
  /** Text under the total in the centre (e.g. "MTD cost"). */
  caption?: string
  size?: number
  thickness?: number
  legend?: boolean
  title?: string
  /** Share below which a slice is drawn inside "Other" (default 0.015). */
  foldBelow?: number
  onSliceClick?: (key: string) => void
}

/**
 * Donut — part-to-whole at a glance. The centre carries the total (the
 * hovered slice while hovering); the legend carries share and value for
 * every slice, including the ones folded into "Other".
 */
export function Donut({ slices, format, compactFormat, caption, size = 168, thickness = 24, legend = true, title, foldBelow, onSliceClick }: DonutProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const cfmt = compactFormat ?? formatCompact
  const { ref, width } = useWidth()
  const { tip, show, hide } = useTooltip(ref)
  const [hover, setHover] = useState<string | null>(null)

  const layout = donutLayout(slices, foldBelow)
  if (!slices.length || layout.total <= 0) return <EmptyChart height={size} />

  const c = size / 2
  const ro = c - 2
  const ri = ro - thickness
  const innerW = 2 * ri - 12
  const fit = (s: string, n: number) => (s.length * 8.6 > innerW ? cfmt(n) : s)
  const hovered = layout.arcs.find((a) => a.key === hover)
  const centre = hovered ? { v: fit(fmt(hovered.value), hovered.value), c: hovered.label } : { v: fit(fmt(layout.total), layout.total), c: caption ?? 'Total' }
  const arcKeyFor = (k: string) => (layout.legend.find((r) => r.key === k)?.folded ? OTHER_KEY : k)

  const tipFor = (a: DonutArc) => (
    <TipRows
      title={a.label}
      rows={[
        { label: 'Value', value: fmt(a.value) },
        { label: 'Share', value: formatPct(a.share * 100) },
      ]}
      note={a.folded.length > 1 || (a.folded.length === 1 && a.folded[0] !== OTHER_KEY) ? `${a.folded.length} small slices folded in` : undefined}
    />
  )

  const aria = `${title ?? 'Donut'}: total ${fmt(layout.total)} across ${layout.legend.length} slices; ` + layout.legend.slice(0, 4).map((r) => `${r.label} ${formatPct(r.share * 100)}`).join(', ') + '.'

  return (
    <div className="chart donut" ref={ref}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={aria} style={{ width: size, flex: 'none' }}>
        {layout.arcs.map((a) => (
          <path
            key={a.key}
            className={`chart-mark${onSliceClick ? ' clickable' : ''}`}
            d={arcPath(c, c, ro, ri, a.start, a.end)}
            fill={a.color}
            stroke={SURFACE}
            strokeWidth={2}
            style={{ opacity: hover !== null && hover !== a.key ? 0.4 : 1 }}
            tabIndex={onSliceClick ? 0 : undefined}
            role={onSliceClick ? 'button' : undefined}
            aria-label={`${a.label}: ${fmt(a.value)} (${formatPct(a.share * 100)})`}
            onPointerMove={(e) => {
              setHover(a.key)
              show(e, tipFor(a))
            }}
            onPointerLeave={() => {
              setHover(null)
              hide()
            }}
            onFocus={() => setHover(a.key)}
            onBlur={() => setHover(null)}
            onClick={onSliceClick ? () => onSliceClick(a.key) : undefined}
            onKeyDown={
              onSliceClick
                ? (e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      onSliceClick(a.key)
                    }
                  }
                : undefined
            }
          >
            <title>{`${a.label}: ${fmt(a.value)} (${formatPct(a.share * 100)})`}</title>
          </path>
        ))}
        <text className="center-v" x={c} y={c - 3} textAnchor="middle" dominantBaseline="auto">
          {centre.v}
        </text>
        <text className="center-c" x={c} y={c + 14} textAnchor="middle">
          {centre.c.length * 6.7 > innerW ? centre.c.slice(0, Math.max(1, Math.floor(innerW / 6.7) - 1)) + '…' : centre.c}
        </text>
      </svg>
      <ChartTooltip tip={tip} width={width} />
      {legend ? (
        <div className="chart-legend" role="list" aria-label="Legend">
          {layout.legend.map((r) => (
            <span
              key={r.key}
              role="listitem"
              className={`item${hover !== null && hover !== arcKeyFor(r.key) ? ' dim' : ''}${r.folded ? ' folded' : ''}${onSliceClick ? ' clickable' : ''}`}
              title={r.folded ? `${r.label} (drawn inside Other)` : r.label}
              onMouseEnter={() => setHover(arcKeyFor(r.key))}
              onMouseLeave={() => setHover(null)}
              onClick={onSliceClick ? () => onSliceClick(r.key) : undefined}
            >
              <span className="sw" style={{ background: r.color }} />
              <span className="lbl">{r.label}</span>
              <span className="detail">
                {formatPct(r.share * 100)} · {fmt(r.value)}
              </span>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  )
}
