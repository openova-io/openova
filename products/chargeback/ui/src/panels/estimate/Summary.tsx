import type { ReactNode } from 'react'
import type { Estimate } from '../../api/types'
import { EmptyState, KPI, Notice } from '../../components/ui'
import { formatMoney, formatPct } from '../../lib/money'
import { Breakdown } from './Breakdown'
import { familyBreakdown, groupItems, type EstimateItem, type PricedItem } from './model'

/**
 * The estimate summary (DESIGN.md §12.5): the two totals the server
 * returned in large type, the estimate by product family (gauge, bar,
 * legend), then the items grouped by service — each with what its lines
 * came to, its lines on request, and Edit / Remove — and the subtotal and
 * tax under them. `children` is the page's Share and Send controls, which
 * sit at the bottom.
 */
export function Summary({
  items,
  priced,
  currency,
  taxRate,
  totals,
  previewError,
  expanded,
  onToggle,
  onEdit,
  onRemove,
  children,
}: {
  items: EstimateItem[]
  priced: Map<string, PricedItem>
  currency: string
  taxRate: number | string
  totals: Estimate | null
  previewError: string
  expanded: Record<string, boolean>
  onToggle: (id: string) => void
  onEdit: (item: EstimateItem) => void
  onRemove: (id: string) => void
  children?: ReactNode
}) {
  const lineCount = items.reduce((n, it) => n + it.lines.length, 0)
  return (
    <div className="card" data-testid="estimate-summary">
      <div className="card-head">
        <h2>Your estimate</h2>
        {items.length ? (
          <span className="hint">
            {items.length} item{items.length === 1 ? '' : 's'} · {lineCount} line{lineCount === 1 ? '' : 's'} · {currency}
          </span>
        ) : null}
      </div>

      <div className="kpis estimate-kpis" data-testid="estimate-kpis">
        {/* The number carries the card; the currency rides as the small unit so a
            four-figure total never clips behind the KPI's ellipsis. */}
        <KPI
          label="Per month"
          value={
            <span data-testid="total-monthly">
              {totals ? formatMoney(totals.monthly, '') : '—'}
              {totals ? <> <span className="unit">{currency}</span></> : null}
            </span>
          }
          note="incl. tax"
        />
        <KPI
          label="12 months"
          value={
            <span data-testid="total-yearly">
              {totals ? formatMoney(totals.yearly, '') : '—'}
              {totals ? <> <span className="unit">{currency}</span></> : null}
            </span>
          }
          note="incl. tax"
        />
      </div>

      {items.length === 0 ? (
        <EmptyState title="Choose a service on the left to start an estimate">
          <div>Each service opens a short form. Every figure comes back priced as an invoice would be.</div>
        </EmptyState>
      ) : (
        <div className="stack tight" style={{ marginTop: 10 }}>
          <Breakdown shares={familyBreakdown(items, priced)} currency={currency} />
          {groupItems(items).map((g) => (
            <div key={g.service} className="stack tight" data-testid={`group-${g.service}`}>
              <div className="tiny muted" style={{ textTransform: 'uppercase', letterSpacing: '.04em', marginTop: 4 }}>
                {g.familyName} › {g.serviceName}
              </div>
              {g.items.map((it) => {
                const p = priced.get(it.id)
                const open = expanded[it.id] ?? false
                return (
                  <div key={it.id} className="card flat" style={{ padding: '8px 10px' }} data-testid={`item-${it.id}`}>
                    <div className="row between start">
                      <div style={{ minWidth: 0 }}>
                        <strong>{it.serviceName}</strong>
                        <div className="small muted">{it.summary}</div>
                      </div>
                      <div className="right" style={{ whiteSpace: 'nowrap' }}>
                        <strong className="num" data-testid="item-amount">
                          {p?.amount != null ? formatMoney(p.amount, currency) : '—'}
                        </strong>
                        <div className="tiny muted">{p?.monthly === false ? 'for the term' : 'per month'} · before tax</div>
                      </div>
                    </div>
                    <div className="row" style={{ marginTop: 6 }}>
                      <button className="small" onClick={() => onToggle(it.id)} aria-expanded={open} aria-label={`${open ? 'Hide' : 'Show'} lines of ${it.serviceName}`}>
                        {open ? 'Hide lines' : `${it.lines.length} line${it.lines.length === 1 ? '' : 's'}`}
                      </button>
                      <button className="small" onClick={() => onEdit(it)} aria-label={`Edit ${it.serviceName}`}>
                        Edit
                      </button>
                      <button className="small danger" onClick={() => onRemove(it.id)} aria-label={`Remove ${it.serviceName}`}>
                        Remove
                      </button>
                    </div>
                    {open ? (
                      <div className="stack tight small" style={{ marginTop: 8 }} data-testid="item-lines">
                        {(p?.lines ?? it.lines.map((line) => ({ line, priced: null }))).map((l, i) => (
                          <div key={`${l.line.sku ?? l.line.plan}-${i}`} className="row between">
                            <span style={{ minWidth: 0 }}>
                              {l.line.label} <span className="mono muted tiny">{l.line.sku ?? `plan.${l.line.plan}`}</span>
                              <span className="muted"> · {l.line.plan ? `${l.line.quantity} × ${l.line.months} month(s)` : `${l.line.quantity} × ${l.line.hours} h`}</span>
                            </span>
                            <span className="num">{l.priced ? formatMoney(l.priced.amount, currency) : '—'}</span>
                          </div>
                        ))}
                      </div>
                    ) : null}
                  </div>
                )
              })}
            </div>
          ))}
        </div>
      )}

      {previewError ? <Notice kind="bad">{previewError}</Notice> : null}

      <div className="kv" style={{ marginTop: 12, gridTemplateColumns: '1fr max-content' }} data-testid="estimate-totals">
        <span className="muted">Subtotal</span>
        <span className="num right">{totals ? formatMoney(totals.subtotal, currency) : '—'}</span>
        <span className="muted">Tax ({formatPct(Number(taxRate) * 100, { digits: 1 })})</span>
        <span className="num right">{totals ? formatMoney(totals.tax, currency) : '—'}</span>
        {totals && String(totals.total) !== String(totals.monthly) ? (
          <>
            <span className="muted">Total for the term</span>
            <span className="num right">{formatMoney(totals.total, currency)}</span>
          </>
        ) : null}
      </div>

      {children}
    </div>
  )
}
