import { useCallback, useState, type CSSProperties, type ReactNode, type RefObject } from 'react'

export interface TooltipState {
  /** Position in container pixels. */
  x: number
  y: number
  content: ReactNode
}

export type TipAnchor = { clientX: number; clientY: number } | { x: number; y: number }

export interface TooltipApi {
  tip: TooltipState | null
  /** Show `content` beside a pointer event, or at an anchor in container px (keyboard focus). */
  show: (at: TipAnchor, content: ReactNode) => void
  hide: () => void
}

/**
 * useTooltip drives the one positioned <ChartTooltip/> every chart shares.
 * The container ref is the element the tooltip is positioned inside (the
 * `.chart` wrapper, which is position: relative).
 */
export function useTooltip(container: RefObject<HTMLElement | null>): TooltipApi {
  const [tip, setTip] = useState<TooltipState | null>(null)
  const show = useCallback(
    (at: TipAnchor, content: ReactNode) => {
      if ('clientX' in at) {
        const r = container.current?.getBoundingClientRect()
        setTip({ x: at.clientX - (r?.left ?? 0), y: at.clientY - (r?.top ?? 0), content })
      } else {
        setTip({ x: at.x, y: at.y, content })
      }
    },
    [container],
  )
  const hide = useCallback(() => setTip(null), [])
  return { tip, show, hide }
}

/** ChartTooltip renders the tooltip; it flips away from the nearest edges. */
export function ChartTooltip({ tip, width }: { tip: TooltipState | null; width: number }) {
  if (!tip) return null
  const flipX = tip.x > width / 2
  const flipY = tip.y < 80
  const style: CSSProperties = {
    left: tip.x,
    top: tip.y,
    transform: `translate(${flipX ? 'calc(-100% - 12px)' : '12px'}, ${flipY ? '12px' : 'calc(-100% - 12px)'})`,
  }
  return (
    <div className="chart-tip" role="tooltip" style={style}>
      {tip.content}
    </div>
  )
}

export interface TipRow {
  label: string
  value: string
  color?: string
  /** Draw the key as a hatched swatch (forecast / no-data). */
  hatch?: boolean
  /** Emphasise (the hovered series). */
  strong?: boolean
}

/** TipRows is the shared tooltip anatomy: title, one row per series, optional total and note. */
export function TipRows({ title, rows, total, note }: { title?: string; rows: TipRow[]; total?: TipRow; note?: string }) {
  return (
    <div>
      {title ? <div className="t">{title}</div> : null}
      {rows.map((r, i) => (
        <div key={`${r.label}-${i}`} className={`r${r.strong ? ' strong' : ''}`}>
          <span className="k">
            {r.color || r.hatch ? <span className={`sw${r.hatch ? ' hatch' : ''}`} style={r.color ? { background: r.color } : undefined} /> : null}
            <span className="lbl">{r.label}</span>
          </span>
          <span className="v">{r.value}</span>
        </div>
      ))}
      {total ? (
        <div className="r total">
          <span className="k">
            <span className="lbl">{total.label}</span>
          </span>
          <span className="v">{total.value}</span>
        </div>
      ) : null}
      {note ? <div className="note">{note}</div> : null}
    </div>
  )
}
