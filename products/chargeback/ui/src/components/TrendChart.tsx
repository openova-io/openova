/**
 * TrendChart — a dependency-free SVG time series (#6863).
 *
 * Deliberately no charting library. This app ships three runtime dependencies
 * (react, react-dom, react-router-dom); a chart package would add a tree of
 * transitive dependencies to a service that handles billing data, for a bar
 * chart that is forty lines of SVG. The platform scans its own supply chain,
 * so the cheapest dependency is the one not added.
 *
 * Renders nothing rather than an empty frame when there is no data: an axis
 * with no series reads as "zero spend", which is a different and wrong claim.
 */

export type TrendPoint = { label: string; value: number }

type Props = {
  points: TrendPoint[]
  /** Rendered above the plot; also the accessible name. */
  title?: string
  /** Formats a value for the tooltip and the peak label. */
  format?: (v: number) => string
  height?: number
}

export function TrendChart({ points, title, format, height = 120 }: Props) {
  const fmt = format ?? ((v: number) => String(Math.round(v * 100) / 100))
  if (!points.length) return null

  const max = Math.max(...points.map((p) => p.value), 0)
  // A flat-zero series still deserves a baseline rather than a divide-by-zero.
  const scale = max > 0 ? max : 1
  const w = Math.max(points.length * 8, 240)
  const barW = w / points.length
  const peak = points.reduce((a, b) => (b.value > a.value ? b : a), points[0])

  return (
    <figure className="trend" style={{ margin: 0 }}>
      {title && (
        <figcaption className="muted" style={{ marginBottom: 4 }}>
          {title} · peak {fmt(peak.value)} on {peak.label}
        </figcaption>
      )}
      <svg
        viewBox={`0 0 ${w} ${height}`}
        preserveAspectRatio="none"
        style={{ width: '100%', height, display: 'block' }}
        role="img"
        aria-label={title ? `${title}. Peak ${fmt(peak.value)} on ${peak.label}.` : 'Trend'}
      >
        {points.map((p, i) => {
          const h = (p.value / scale) * (height - 2)
          return (
            <rect
              key={`${p.label}-${i}`}
              x={i * barW}
              y={height - h}
              width={Math.max(barW - 1, 1)}
              height={h}
              fill="currentColor"
              opacity={p.value === peak.value && max > 0 ? 0.95 : 0.55}
            >
              <title>{`${p.label}: ${fmt(p.value)}`}</title>
            </rect>
          )
        })}
      </svg>
      <div className="muted" style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.85em' }}>
        <span>{points[0].label}</span>
        <span>{points[points.length - 1].label}</span>
      </div>
    </figure>
  )
}

/**
 * toDailySeries turns `group_by=day` usage rows into one series.
 *
 * Rows arrive per (day, sku); a day with three SKUs is three rows. Summing by
 * day is what makes the series a spend/usage trend rather than a sawtooth of
 * whichever SKU happened to sort last. Days with no rows are absent from the
 * API, and are left absent here rather than back-filled with zero — inventing
 * a zero would claim a measurement that was never taken.
 */
export function toDailySeries(rows: { day?: string; key?: string; quantity?: string | number }[]): TrendPoint[] {
  const byDay = new Map<string, number>()
  for (const r of rows) {
    // The API names this column `key` on the wire and the client type calls it
    // `day`; accept either so the chart cannot silently render empty because
    // one side was renamed.
    const day = r.day ?? r.key ?? ''
    if (!day) continue
    const v = typeof r.quantity === 'number' ? r.quantity : parseFloat(String(r.quantity ?? '0'))
    if (!Number.isFinite(v)) continue
    byDay.set(day, (byDay.get(day) ?? 0) + v)
  }
  return [...byDay.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([label, value]) => ({ label, value }))
}
