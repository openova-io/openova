export interface LegendItem {
  key: string
  label: string
  color?: string
  /** rect (bars/areas, default) · line (lines) · dash (forecast line) · hatch (forecast bars) · hatch-light (no data). */
  kind?: 'rect' | 'line' | 'dash' | 'hatch' | 'hatch-light'
  /** Trailing text, e.g. a share or value (donut). */
  detail?: string
}

/**
 * Legend is the identity channel every multi-series chart carries (colour is
 * never the only key). Hovering an item highlights its series when the chart
 * passes `onHover`. Long labels ellipsise with the full text in the title.
 */
export function Legend({
  items,
  active,
  onHover,
}: {
  items: LegendItem[]
  active?: string | null
  onHover?: (key: string | null) => void
}) {
  if (!items.length) return null
  return (
    <div className="chart-legend" role="list" aria-label="Legend">
      {items.map((it) => (
        <span
          key={it.key}
          role="listitem"
          className={`item${active && active !== it.key ? ' dim' : ''}`}
          title={it.label}
          onMouseEnter={onHover ? () => onHover(it.key) : undefined}
          onMouseLeave={onHover ? () => onHover(null) : undefined}
        >
          <span className={`sw ${it.kind ?? 'rect'}`} style={it.color && (it.kind === undefined || it.kind === 'rect' || it.kind === 'line') ? { background: it.color } : undefined} />
          <span className="lbl">{it.label}</span>
          {it.detail ? <span className="detail">{it.detail}</span> : null}
        </span>
      ))}
    </div>
  )
}
