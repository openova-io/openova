import { useMemo, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import { useSession } from '../auth/session'
import type { CostSource, RatedLine, Statement } from '../api/types'
import { Waterfall, waterfallLayout, type WaterfallStep } from '../components/charts'
import { Badge, Confirm, EmptyState, Field, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { discountRuleLabel } from '../lib/discountRule'
import { day, num, when } from '../lib/format'
import { formatMoney, formatPct, minorUnitDigits, minorUnitTolerance } from '../lib/money'
import { toNumber } from '../lib/num'
import { groupBySource, groupByService } from '../lib/sku'
import { acceptsPayment, dueLabel, statementBalance, statementPaid, statementPeriod, statementStatus } from '../lib/statements'
import { emptyPaymentForm, hasErrors, paymentBody, validatePayment, type Errors, type PaymentForm } from '../lib/forms'
import { useQuery } from '../lib/useQuery'

/**
 * Statement view (DESIGN.md §2.9) — what a customer is billed for a period,
 * invoice-grade: list subtotal → discounts → net → tax → total, lines grouped
 * by service with each group's share, a per-source breakdown, printable.
 */

// DESIGN.md §8 — the invoice lifecycle, from this page: issue, send it to
// the customer, record what they paid, or void it.
type Dialog = { kind: 'issue' } | { kind: 'delete' } | { kind: 'send' } | { kind: 'pay' } | { kind: 'cancel' } | null

export function StatementView() {
  const { id = '' } = useParams()
  const nav = useNavigate()
  const { me } = useSession()
  const q = useQuery<Statement>(`/statements/${id}`)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [notify, setNotify] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [flash, setFlash] = useState('')
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
  // Wire contract (rating.TotalsWithDiscount): `subtotal` is the NET — list
  // minus discounts, before tax — so that subtotal + tax == total holds for
  // every reader. The list price is reconstructed as net + discount.
  const net = toNumber(s.subtotal)
  const discount = toNumber(s.discount_total)
  const tax = toNumber(s.tax)
  const total = toNumber(s.total)
  const subtotal = net + discount
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
  // DESIGN.md §2.11 — a superseded entry matched the bill but lost to a
  // better discount under the rule; it is shown, not counted.
  const applied = detail.filter((d) => !d.superseded_by)
  const detailID = (d: (typeof detail)[number]) => d.discount_id ?? d.id ?? ''
  const nameOf = (id: string) => detail.find((d) => detailID(d) === id)?.name ?? id
  const ruleLabel = discountRuleLabel(s.discount_rule)
  const back = operator ? { to: '/statements', label: 'Statements' } : { to: '/my/statements', label: 'My statements' }

  const act = async (kind: 'issue' | 'delete' | 'send' | 'cancel', body?: Record<string, unknown>) => {
    setBusy(true)
    setError('')
    setFlash('')
    try {
      if (kind === 'delete') {
        await api.del(`/statements/${id}`)
        nav('/statements')
        return
      }
      if (kind === 'issue') await api.post(`/statements/${id}/issue`, { notify })
      if (kind === 'send') await api.post(`/statements/${id}/send`, { notify: body?.notify === true })
      if (kind === 'cancel') await api.post(`/statements/${id}/cancel`, { reason: String(body?.reason ?? '') })
      setDialog(null)
      setFlash(kind === 'send' ? 'marked as sent to the customer' : kind === 'cancel' ? 'invoice cancelled' : 'issued')
      await q.reload()
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
            {customer} · {statementPeriod(s)} <Badge status={statementStatus(s)} kind={statementStatus(s) === 'overdue' ? 'bad' : undefined} />
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
                <button
                  className="primary"
                  onClick={() => {
                    setNotify(true)
                    setDialog({ kind: 'issue' })
                  }}
                >
                  Issue
                </button>
                <button className="danger" onClick={() => setDialog({ kind: 'delete' })}>
                  Delete draft
                </button>
              </>
            ) : null}
            {/* DESIGN.md §8 — the invoice lifecycle. Send records that the
                customer received it (which the due date is measured from);
                Record payment books what arrived; Cancel voids an invoice
                nobody has been sent yet. */}
            {operator && s.status === 'issued' ? (
              <button className="primary" onClick={() => setDialog({ kind: 'send' })}>
                Mark sent
              </button>
            ) : null}
            {operator && acceptsPayment(s) ? (
              <button className="primary" onClick={() => setDialog({ kind: 'pay' })}>
                Record payment
              </button>
            ) : null}
            {operator && s.status === 'issued' ? (
              <button className="danger" onClick={() => setDialog({ kind: 'cancel' })}>
                Cancel invoice
              </button>
            ) : null}
          </>
        }
      />
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}
      {s.status === 'draft' ? <Notice kind="warn">Draft — figures change if the period is run again. Issue it to freeze them.</Notice> : null}
      {statementStatus(s) === 'overdue' ? (
        <Notice kind="bad">
          Overdue — {money(statementBalance(s))} outstanding, {dueLabel(s)}.
        </Notice>
      ) : null}
      {s.status === 'cancelled' ? <Notice kind="warn">Cancelled{s.cancel_reason ? ` — ${s.cancel_reason}` : ''}. Nothing is collected against it.</Notice> : null}

      {s.invoice_number || s.external_invoice_ref || s.po_reference || s.due_at ? (
        <div className="card">
          <div className="card-head">
            <h2>Invoice</h2>
            <span className="hint">
              {s.invoice_number ? (
                <span className="mono">{s.invoice_number}</span>
              ) : s.external_invoice_ref ? (
                <span className="mono">{s.external_invoice_ref}</span>
              ) : (
                'not yet numbered'
              )}
            </span>
          </div>
          <table>
            <tbody>
              {/* DESIGN.md §8.10 — when the operator's billing system is the
                  system of record it numbers the invoice, and what we hold is
                  the reference it knows this bill by. */}
              {s.external_invoice_ref ? (
                <tr>
                  <td className="muted">Billing system reference</td>
                  <td>
                    <span className="mono">{s.external_invoice_ref}</span>
                    <span className="sub">this invoice is raised and collected in the operator's billing system</span>
                  </td>
                </tr>
              ) : null}
              <tr>
                <td className="muted">Purchase order</td>
                <td>{s.po_reference ? <span className="mono">{s.po_reference}</span> : <span className="muted">none quoted</span>}</td>
              </tr>
              <tr>
                <td className="muted">Payment terms</td>
                <td>{typeof s.payment_terms_days === 'number' ? (s.payment_terms_days === 0 ? 'due on receipt' : `net ${s.payment_terms_days}`) : '—'}</td>
              </tr>
              <tr>
                <td className="muted">Due</td>
                <td>
                  {s.due_at ? (
                    <>
                      {day(s.due_at)} <span className={statementStatus(s) === 'overdue' ? 'bad' : 'muted'}>· {dueLabel(s)}</span>
                    </>
                  ) : (
                    '—'
                  )}
                </td>
              </tr>
              <tr>
                <td className="muted">Paid</td>
                <td>
                  {money(statementPaid(s))} of {money(total)}
                  {statementBalance(s) > 0 ? <span className="muted"> · {money(statementBalance(s))} outstanding</span> : <span className="ok"> · settled</span>}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      ) : null}

      {s.payments && s.payments.length ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>Payments</h2>
            <span className="hint">{s.payments.length} recorded</span>
          </div>
          <table>
            <thead>
              <tr>
                <th>Date</th>
                <th>Reference</th>
                <th>Recorded by</th>
                <th className="num">Amount</th>
              </tr>
            </thead>
            <tbody>
              {s.payments.map((p) => (
                <tr key={p.id} className={p.status && p.status !== 'received' ? 'muted' : undefined}>
                  <td>
                    {day(p.paid_at)}
                    {p.status && p.status !== 'received' ? <span className="sub warn">{p.status} — settles nothing yet</span> : null}
                  </td>
                  <td>
                    {p.reference ? <span className="mono">{p.reference}</span> : <span className="muted">—</span>}
                    {p.method ? <span className="sub">{p.method}</span> : null}
                  </td>
                  <td className="muted">{p.recorded_by || p.gateway || '—'}</td>
                  <td className="num">{money(p.amount)}</td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr>
                <td colSpan={3}>Outstanding</td>
                <td className="num">{money(statementBalance(s))}</td>
              </tr>
            </tfoot>
          </table>
        </div>
      ) : null}

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
            {discount > 0 ? ` · ${applied.length || 'the'} discount${applied.length === 1 ? '' : 's'} applied` : ' · no discount applied'}
            {ruleLabel ? ` · rule: ${ruleLabel}` : ''}
          </p>
        </div>
      </div>

      {detail.length ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>Discounts</h2>
            <span className="hint" title="DESIGN.md §2.11 — how several percent discounts on one line were combined when this statement was rated">
              {ruleLabel ? `Combination rule: ${ruleLabel}` : 'rated before the combination rule existed'}
            </span>
          </div>
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
                <tr key={detailID(d) || i} className={d.superseded_by ? 'muted' : undefined}>
                  <td>
                    {d.name}
                    {d.stackable ? <span className="sub">stackable — added on top of the winner</span> : null}
                    {d.superseded_by ? <span className="sub">not applied: superseded by {nameOf(d.superseded_by)}</span> : null}
                  </td>
                  <td>{d.kind === 'percent' ? 'percent off' : d.kind === 'fixed' ? 'fixed amount' : d.kind}</td>
                  <td className="num">{d.value === undefined || d.value === null ? '—' : d.kind === 'percent' ? formatPct(toNumber(d.value), { digits: toNumber(d.value) % 1 ? 2 : 0 }) : money(d.value)}</td>
                  <td>{d.sku ? <span className="mono">{d.sku}</span> : <span className="muted">whole bill</span>}</td>
                  <td className="num ok">{d.superseded_by ? <span className="muted">—</span> : `−${money(d.amount)}`}</td>
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
          body={
            <div className="stack tight">
              <p>An issued statement is final: {money(total)} with its lines and discounts frozen. Re-running the period will not change it.</p>
              <label className="check">
                <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} /> Email the statement to the customer
              </label>
            </div>
          }
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
      {dialog?.kind === 'send' ? (
        <Confirm
          title={`Mark ${s.invoice_number || statementPeriod(s)} as sent?`}
          confirmLabel="Mark sent"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => act('send', { notify })}
          body={
            <div className="stack tight">
              <p>Records that the customer has the invoice. Its due date{s.due_at ? ` (${day(s.due_at)})` : ''} is measured from there, and it reads overdue once that passes with money outstanding.</p>
              <label className="check">
                <input type="checkbox" checked={notify} onChange={(e) => setNotify(e.target.checked)} /> Also email the invoice to the customer
              </label>
              <p className="muted small" style={{ marginBottom: 0 }}>
                Leave this unticked if you have already sent it yourself — the customer would otherwise receive a second copy.
              </p>
            </div>
          }
        />
      ) : null}
      {dialog?.kind === 'cancel' ? (
        <CancelDialog busy={busy} onClose={() => setDialog(null)} onConfirm={(reason) => act('cancel', { reason })} />
      ) : null}
      {dialog?.kind === 'pay' ? (
        <RecordPaymentModal
          statementId={id}
          balance={statementBalance(s)}
          currency={cur}
          onClose={() => setDialog(null)}
          onDone={async (amount) => {
            setDialog(null)
            setFlash(`payment of ${formatMoney(amount, cur)} recorded`)
            await q.reload()
          }}
        />
      ) : null}
    </div>
  )
}

/** Voiding an invoice: the reason is kept on the record. */
function CancelDialog({ busy, onClose, onConfirm }: { busy: boolean; onClose: () => void; onConfirm: (reason: string) => void }) {
  const [reason, setReason] = useState('')
  return (
    <Confirm
      title="Cancel this invoice?"
      danger
      confirmLabel="Cancel invoice"
      busy={busy}
      onClose={onClose}
      onConfirm={() => onConfirm(reason)}
      body={
        <div className="stack tight">
          <p>The invoice stays on the record as cancelled — its number is never reused — and nothing is collected against it. An invoice already sent to the customer cannot be cancelled; issue a credit note instead.</p>
          <Field label="Reason">
            <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="raised in error" autoFocus />
          </Field>
        </div>
      }
    />
  )
}

/**
 * Recording what arrived (DESIGN.md §8). Part payment is ordinary: the
 * balance carries and the invoice stays open until the payments reach the
 * total. More than the balance is refused — by this form and by the server —
 * judged at the currency's minor unit: the prefilled amount is the exact
 * outstanding rounded to what a transfer can carry (4.857 for 4.856782 OMR),
 * and paying it settles the invoice.
 */
function RecordPaymentModal({
  statementId,
  balance,
  currency,
  onClose,
  onDone,
}: {
  statementId: string
  balance: number
  currency: string
  onClose: () => void
  onDone: (amount: number) => void | Promise<void>
}) {
  const today = new Date().toISOString().slice(0, 10)
  const digits = minorUnitDigits(currency)
  const [form, setForm] = useState<PaymentForm>(() => emptyPaymentForm(balance, today, digits))
  const [errors, setErrors] = useState<Errors<PaymentForm>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = <K extends keyof PaymentForm>(k: K, v: PaymentForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const remaining = balance - Number(form.amount || 0)
  // Still owed only when what is left is at least half a minor unit — less
  // than that is the settlement the server books as paid.
  const stillOwed = remaining >= minorUnitTolerance(currency)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validatePayment(form, balance, digits)
    setErrors(errs)
    if (hasErrors(errs)) return
    setBusy(true)
    setError('')
    try {
      await api.post(`/statements/${statementId}/payments`, paymentBody(form))
      await onDone(Number(form.amount))
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Record a payment"
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="record-payment-form" disabled={busy}>
            {busy ? 'Recording…' : 'Record payment'}
          </button>
        </>
      }
    >
      <form id="record-payment-form" onSubmit={(e) => void submit(e)} className="stack tight">
        <p className="muted small" style={{ margin: 0 }}>
          {formatMoney(balance, currency, { digits })} outstanding. Part payment is fine — the balance carries and the invoice settles when the payments reach the total.
        </p>
        <div className="grid2">
          <Field label={`Amount (${currency})`} error={errors.amount}>
            <input value={form.amount} onChange={(e) => set('amount', e.target.value)} inputMode="decimal" autoFocus />
          </Field>
          <Field label="Received on" error={errors.paid_at} help="The day the money arrived, as the bank shows it.">
            <input type="date" value={form.paid_at} onChange={(e) => set('paid_at', e.target.value)} />
          </Field>
        </div>
        <Field label="Reference" error={errors.reference} help="The bank or gateway transaction id. Recording the same reference twice is refused, so a duplicate can never be booked.">
          <input value={form.reference} onChange={(e) => set('reference', e.target.value)} className="mono" placeholder="TRF-4471" />
        </Field>
        {!hasErrors(errors) && form.amount && stillOwed ? <Notice kind="warn">{formatMoney(remaining, currency, { digits })} will still be outstanding.</Notice> : null}
        {error ? <Notice kind="bad">{error}</Notice> : null}
      </form>
    </Modal>
  )
}
