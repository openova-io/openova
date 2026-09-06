import { formatNumber, formatPct } from '../../lib/money'
import { EmptyChart } from './EmptyChart'
import { PALETTE } from './palette'

export interface RankedRow {
  key: string
  label: string
  value: number
  /** Share of the whole, 0..1 (as the API sends it). */
  share?: number | null
  delta_pct?: number | null
  color?: string
}

export interface RankedBarsProps {
  rows: RankedRow[]
  format?: (v: number) => string
  /** Fixes the scale so two lists stay comparable (default: the largest row). */
  max?: number
  onClick?: (key: string) => void
  /** Cost goes up = bad (default). Set for revenue / savings lists. */
  upIsGood?: boolean
  showShare?: boolean
  showDelta?: boolean
  /** One colour for every bar (a single series is one colour); rows may override. */
  color?: string
  barHeight?: number
  title?: string
}

/** DeltaChip renders a signed percentage with direction colouring; null → "—". */
export function DeltaChip({ pct, upIsGood = false, title }: { pct: number | null | undefined; upIsGood?: boolean; title?: string }) {
  const text = formatPct(pct, { sign: true })
  const dir = pct === null || pct === undefined || !Number.isFinite(pct) || Math.abs(pct) < 0.05 ? 'flat' : pct > 0 ? 'up' : 'down'
  const cls = dir === 'flat' ? '' : (dir === 'up') === upIsGood ? ' good' : ' bad'
  const arrow = dir === 'up' ? '▲ ' : dir === 'down' ? '▼ ' : ''
  return (
    <span className={`delta${cls}`} title={title ?? (dir === 'flat' && text === '—' ? 'No previous period to compare' : `${text} vs previous period`)}>
      {arrow}
      {text}
    </span>
  )
}

/**
 * RankedBars — horizontal ranked bars with value, share and a delta chip.
 * Labels ellipsise (full text in the title); bars scale to `max`.
 */
export function RankedBars({ rows, format, max, onClick, upIsGood = false, showShare = true, showDelta = true, color, barHeight = 10, title }: RankedBarsProps) {
  const fmt = format ?? ((v: number) => formatNumber(v))
  const scale = max ?? Math.max(0, ...rows.map((r) => (Number.isFinite(r.value) ? r.value : 0)))
  if (!rows.length || scale <= 0) return <EmptyChart />
  const fill = color ?? PALETTE[0]
  const h = barHeight
  return (
    <div className="ranked" role="list" aria-label={title ?? 'Ranked bars'}>
      {rows.map((r) => {
        const v = Number.isFinite(r.value) ? r.value : 0
        const w = Math.max(0, Math.min(100, (v / scale) * 100))
        const share = r.share !== null && r.share !== undefined && Number.isFinite(r.share) ? formatPct(r.share * 100) : null
        const aria = `${r.label}: ${fmt(v)}${share ? ` (${share})` : ''}`
        const body = (
          <>
            <span className="lbl" title={r.label}>
              {r.label}
            </span>
            <svg height={h + 2} viewBox={`0 0 100 ${h + 2}`} preserveAspectRatio="none" role="img" aria-label={aria}>
              <rect x={0} y={1} width={100} height={h} fill="#f1f5f9" />
              {w > 0 ? <rect className="chart-mark" x={0} y={1} width={w} height={h} fill={r.color ?? fill} /> : null}
              <title>{aria}</title>
            </svg>
            <span className="val">
              <span>{fmt(v)}</span>
              {showShare ? <span className="share">{share ?? ''}</span> : null}
              {showDelta ? <DeltaChip pct={r.delta_pct} upIsGood={upIsGood} /> : null}
            </span>
          </>
        )
        return onClick ? (
          <button key={r.key} type="button" className="ranked-row" role="listitem" onClick={() => onClick(r.key)}>
            {body}
          </button>
        ) : (
          <div key={r.key} className="ranked-row" role="listitem">
            {body}
          </div>
        )
      })}
    </div>
  )
}
