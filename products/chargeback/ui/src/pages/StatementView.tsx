import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import { useSession } from '../auth/session'
import type { CostSource, RatedLine, Statement } from '../api/types'
import { Waterfall, waterfallLayout, type WaterfallStep } from '../components/charts'
import { Badge, Confirm, EmptyState, Notice, PageHeader, Skeleton } from '../components/ui'
import { num, when } from '../lib/format'
import { formatMoney, formatPct } from '../lib/money'
import { toNumber } from '../lib/num'
import { groupBySource, groupByService } from '../lib/sku'
import { statementPeriod } from '../lib/statements'
import { useQuery } from '../lib/useQuery'

/**
 * Statement view (DESIGN.md §2.9) — what a customer is billed for a period,
 * invoice-grade: list subtotal → discounts → net → tax → total, lines grouped
 * by service with each group's share, a per-source breakdown, printable.
 */

type Dialog = { kind: 'issue' } | { kind: 'delete' } | null

export function StatementView() {
  const { id = '' } = useParams()
  const nav = useNavigate()
  const { me } = useSession()
  const q = useQuery<Statement>(`/statements/${id}`)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const s = q.data
  // The rated lines carry source ids; the customer's sources give them names.
  const srcQ = useQuery<unknown>(s ? `/customers/${s.customer_id}/sources` : null)
  const sourceLabel = useMemo(() => {
    const m = new Map<string, string>()
    for (const src of asList<CostSource>(srcQ.data, 'sources')) m.set(src.id, `${src.kind}${src.project_id ? ' · ' + src.project_id : ''}${src.region ? ' · ' + src.region : ''}`)
    return m
  }, [srcQ.data])
  const lines: RatedLine[] = useMemo(() => s?.lines ?? [], [s])
  const groups = useMemo(() => groupByService(lines), [lines])
  const sources = useMemo(() => groupBySource(lines), [lines])

  if (q.error && !s) return <Notice kind="bad">{q.error}</Notice>
  if (!s) return <Skeleton lines={8} />

  const operator = me?.role === 'operator'
  const cur = s.currency
  const money = (v: number | string | null | undefined) => formatMoney(toNumber(v), cur)
  const subtotal = toNumber(s.subtotal)
  const discount = toNumber(s.discount_total)
  const tax = toNumber(s.tax)
  const total = toNumber(s.total)
  const net = subtotal - discount
  const taxRate = toNumber(s.tax_rate) * 100
  const steps: WaterfallStep[] = [
    { label: 'List subtotal', value: subtotal, kind: 'total' },
    { label: 'Discounts', value: -discount, kind: 'delta' },
    { label: 'Net subtotal', value: net, kind: 'total' },
    { label: `Tax ${formatPct(taxRate, { digits: taxRate % 1 ? 2 : 0 })}`, value: tax, kind: 'delta' },
    { label: 'Total', value: total, kind: 'total' },
  ]
  const layout = waterfallLayout(steps)
  const customer = s.customer_name ?? s.customer_slug ?? s.customer_id
  const detail = s.discount_detail ?? []
  const back = operator ? { to: '/statements', label: 'Statements' } : { to: '/my/statements', label: 'My statements' }

  const act = async (kind: 'issue' | 'delete') => {
    setBusy(true)
    setError('')
    try {
      if (kind === 'issue') {
        await api.post(`/statements/${id}/issue`)
        setDialog(null)
        await q.reload()
      } else {
        await api.del(`/statements/${id}`)
        nav('/statements')
      }
    } catch (e) {
      setError(errorText(e))
      setDialog(null)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack" style={{ maxWidth: 1100 }}>
      <PageHeader
        crumbs={[back, { label: statementPeriod(s) }]}
        title={
          <>
            {customer} · {statementPeriod(s)} <Badge status={s.status} />
          </>
        }
        sub={
          <>
            {s.period_start.slice(0, 10)} → {s.period_end.slice(0, 10)} · {cur} · created {when(s.created_at)}
            {s.issued_at ? ` · issued ${when(s.issued_at)}` : ' · draft, not yet issued'}
            {s.customer_slug ? (
              <>
                {' '}
                · <span className="mono">{s.customer_slug}</span>
              </>
            ) : null}
          </>
        }
        actions={
          <>
            <button onClick={() => window.print()}>Print</button>
            <a href={`${API_BASE}/statements/${s.id}.csv`}>
              <button>CSV</button>
            </a>
            {operator && s.status === 'draft' ? (
              <>
                <button className="primary" onClick={() => setDialog({ kind: 'issue' })}>
                  Issue
                </button>
                <button className="danger" onClick={() => setDialog({ kind: 'delete' })}>
                  Delete draft
                </button>
              </>
            ) : null}
          </>
        }
      />
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {s.status === 'draft' ? <Notice kind="warn">Draft — figures change if the period is run again. Issue it to freeze them.</Notice> : null}

      <div className="grid side">
        <div className="card">
          <div className="card-head">
            <h2>From list price to total</h2>
            <span className="hint">{cur}</span>
          </div>
          {subtotal > 0 || total > 0 ? <Waterfall steps={steps} format={(v) => money(v)} height={220} /> : <EmptyState title="Nothing billed">Every line rated to 0 in this period.</EmptyState>}
        </div>
        <div className="card">
          <h2>Totals</h2>
          <table>
            <tbody>
              {layout.bars.map((step, i) => (
                <tr key={step.label} style={step.kind === 'total' ? { fontWeight: 600 } : undefined}>
                  <td className={step.kind === 'total' ? '' : 'muted'}>{step.label}</td>
                  <td className="num">
                    {step.kind === 'delta' ? (
                      <span className={steps[i].value < 0 ? 'ok' : ''}>
                        {steps[i].value < 0 ? '−' : '+'}
                        {money(Math.abs(steps[i].value))}
                      </span>
                    ) : (
                      money(step.end)
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="muted tiny" style={{ marginBottom: 0 }}>
            {lines.length} rated line{lines.length === 1 ? '' : 's'} across {groups.length} service{groups.length === 1 ? '' : 's'}
            {discount > 0 ? ` · ${detail.length || 'the'} discount${detail.length === 1 ? '' : 's'} applied` : ' · no discount applied'}
          </p>
        </div>
      </div>

      {detail.length ? (
        <div className="card pad-0">
          <table>
            <thead>
              <tr>
                <th>Discount</th>
                <th>Kind</th>
                <th className="num">Value</th>
                <th>Applies to</th>
                <th className="num">Amount</th>
              </tr>
            </thead>
            <tbody>
              {detail.map((d, i) => (
                <tr key={d.id ?? i}>
                  <td>{d.name}</td>
                  <td>{d.kind === 'percent' ? 'percent off' : d.kind === 'fixed' ? 'fixed amount' : d.kind}</td>
                  <td className="num">{d.value === undefined || d.value === null ? '—' : d.kind === 'percent' ? formatPct(toNumber(d.value), { digits: toNumber(d.value) % 1 ? 2 : 0 }) : money(d.value)}</td>
                  <td>{d.sku ? <span className="mono">{d.sku}</span> : <span className="muted">whole bill</span>}</td>
                  <td className="num ok">−{money(d.amount)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr>
                <td colSpan={4}>Discounts</td>
                <td className="num">−{money(discount)}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      ) : null}

      <div className="card pad-0">
        {lines.length === 0 ? (
          <EmptyState title="No rated lines">No usage was collected for {customer} in this period, or none of it carries a rate in the price book.</EmptyState>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>SKU</th>
                  <th>Unit</th>
                  <th className="num">Quantity</th>
                  <th className="num">Unit price</th>
                  <th className="num">Amount</th>
                  <th className="num">Resources</th>
                </tr>
              </thead>
              {groups.map((g) => (
                <tbody key={g.key}>
                  <tr style={{ background: 'var(--panel-2)' }}>
                    <td colSpan={4}>
                      <b>{g.label}</b> <span className="muted small">· {g.lines.length} line{g.lines.length === 1 ? '' : 's'}</span>
                    </td>
                    <td className="num">
                      <b>{money(g.amount)}</b>
                    </td>
                    <td className="num muted">{formatPct(g.share * 100)}</td>
                  </tr>
                  {g.lines.map((l, i) => (
                    <tr key={`${g.key}-${l.sku}-${l.source_id ?? ''}-${i}`}>
                      <td className="mono">{l.sku}</td>
                      <td>{l.unit ?? '—'}</td>
                      <td className="num">{num(l.quantity, 4)}</td>
                      <td className="num">{formatMoney(l.unit_price, cur, { digits: 8 })}</td>
                      <td className="num">{money(l.amount)}</td>
                      <td className="num">{l.resource_count ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              ))}
              <tfoot>
                <tr>
                  <td colSpan={4}>List subtotal</td>
                  <td className="num">{money(subtotal)}</td>
                  <td></td>
                </tr>
              </tfoot>
            </table>
          </div>
        )}
      </div>

      {sources.length ? (
        <div className="card">
          <div className="card-head">
            <h2>By cost source</h2>
            <span className="hint">list prices, before discounts</span>
          </div>
          <table>
            <thead>
              <tr>
                <th>Source</th>
                <th className="num">Lines</th>
                <th className="num">Amount</th>
                <th className="num">Share</th>
              </tr>
            </thead>
            <tbody>
              {sources.map((src) => (
                <tr key={src.source_id || '(none)'}>
                  <td>
                    {src.source_id ? (
                      <>
                        {sourceLabel.get(src.source_id) ?? 'cost source'}
                        <span className="sub mono">{src.source_id}</span>
                      </>
                    ) : (
                      <span className="muted">no source recorded</span>
                    )}
                  </td>
                  <td className="num">{src.lines}</td>
                  <td className="num">{money(src.amount)}</td>
                  <td className="num">{formatPct(src.share * 100)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      <p className="no-print">
        <Link to={back.to}>← {back.label}</Link>
      </p>

      {dialog?.kind === 'issue' ? (
        <Confirm
          title={`Issue ${customer} · ${statementPeriod(s)}?`}
          confirmLabel="Issue statement"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => act('issue')}
          body={<p>An issued statement is final: {money(total)} with its lines and discounts frozen. Re-running the period will not change it.</p>}
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title="Delete this draft?"
          danger
          confirmLabel="Delete draft"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => act('delete')}
          body={<p>Removes the draft only; the usage stays and the period can be run again.</p>}
        />
      ) : null}
    </div>
  )
}
