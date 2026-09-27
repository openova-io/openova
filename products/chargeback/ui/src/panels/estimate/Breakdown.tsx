import { formatMoney, formatPct } from '../../lib/money'
import type { FamilyShare } from './model'

/**
 * The estimate by product family (DESIGN.md §12.5): a gauge of the largest
 * family's share, a stacked bar of every family in proportion, and a legend
 * with each family's figure. CSS only — the ring is a conic gradient, the
 * bar a flex row — in the chart palette's colour for the family.
 */
export function Breakdown({ shares, currency }: { shares: FamilyShare[]; currency: string }) {
  if (!shares.length) return null
  const top = shares[0]
  const pct = (s: number) => `${(s * 100).toFixed(2)}%`
  const topPct = Math.round(top.share * 100)
  return (
    <div className="breakdown" data-testid="breakdown">
      <div className="gauge" role="img" aria-label={`${top.familyName} is ${topPct} % of the estimate`} style={{ background: `conic-gradient(${top.color} 0 ${pct(top.share)}, var(--line) 0)` }}>
        <span>{topPct} %</span>
      </div>
      <div className="breakdown-body">
        <div className="tiny muted">
          {top.familyName} is the largest share{shares.length > 1 ? ` of ${shares.length} families` : ''}
        </div>
        <div className="breakdown-bar" aria-hidden>
          {shares.map((s) => (
            <i key={s.family} style={{ width: pct(s.share), background: s.color }} title={`${s.familyName} · ${formatMoney(s.amount, currency)}`} />
          ))}
        </div>
        <div className="breakdown-legend">
          {shares.map((s) => (
            <span key={s.family} className="legend-item" data-testid="breakdown-family">
              <i style={{ background: s.color }} />
              {s.familyName} <span className="num">{formatMoney(s.amount, currency)}</span> <span className="muted">{formatPct(s.share * 100, { digits: 0 })}</span>
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}
