import { useMemo, useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import { useSession } from '../auth/session'
import { can, isSovereign } from '../lib/access'
import type { CostSource, CreditNote, Dispute, RatedLine, Statement } from '../api/types'
import { Waterfall, waterfallLayout, type WaterfallStep } from '../components/charts'
import { Badge, Confirm, EmptyState, Field, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { acceptsCreditNote, allocationText, creditNoteEffect, creditRoom } from '../lib/account'
import { discountRuleLabel } from '../lib/discountRule'
import { DISPUTE_OUTCOMES, canDispute, disputeState, invoiceDownloadURL, openDispute } from '../lib/selfservice'
import { day, num, when } from '../lib/format'
import { formatMoney, formatPct, minorUnitDigits, minorUnitTolerance } from '../lib/money'
import { toNumber } from '../lib/num'
import { groupBySource, groupByService } from '../lib/sku'
import {
  distinctRates,
  einvoiceDocumentURL,
  einvoiceStateHelp,
  einvoiceStateLabel,
  einvoiceTone,
  einvoiceXMLURL,
  hasArchivedXML,
  kindTone,
  rateText,
  taxBaseTotal,
  taxKindLabel,
  taxLinesTotal,
} from '../lib/tax'
import { acceptsPayment, dueLabel, statementBalance, statementPaid, statementPeriod, statementStatus } from '../lib/statements'
import { creditNoteBody, emptyCreditNoteForm, emptyPaymentForm, hasErrors, paymentBody, validateCreditNote, validatePayment, type CreditNoteForm, type Errors, type PaymentForm } from '../lib/forms'
import { useQuery } from '../lib/useQuery'

/**
 * Statement view (DESIGN.md §2.9) — what a customer is billed for a period,
 * invoice-grade: list subtotal → discounts → net → tax → total, lines grouped
 * by service with each group's share, a per-source breakdown, printable.
 */

// DESIGN.md §8 — the invoice lifecycle, from this page: issue, send it to
// the customer, record what they paid, or void it. §9.3 — reduce an issued
// invoice with a credit note, the only way one is ever reduced.
type Dialog = { kind: 'issue' } | { kind: 'delete' } | { kind: 'send' } | { kind: 'pay' } | { kind: 'cancel' } | { kind: 'credit' } | { kind: 'dispute' } | { kind: 'resolve' } | null

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
  // DESIGN.md §16 — the disputes raised on this invoice; the open one is
  // what an operator resolves.
  const disQ = useQuery<unknown>(`/statements/${id}/disputes`)
  const disputes = useMemo(() => asList<Dispute>(disQ.data, 'disputes'), [disQ.data])
  const lines: RatedLine[] = useMemo(() => s?.lines ?? [], [s])
  const groups = useMemo(() => groupByService(lines), [lines])
  const sources = useMemo(() => groupBySource(lines), [lines])

  if (q.error && !s) return <Notice kind="bad">{q.error}</Notice>
  if (!s) return <Skeleton lines={8} />

  // The lens decides where "back" goes; the permissions decide the actions
  // (DESIGN.md §10.9): issuing, sending, cancelling and credit notes are
  // billing.issue, recording a payment is billing.collect — never the
  // customer's, whatever its role.
  const operator = isSovereign(me)
  const canIssue = can(me, 'billing.issue', s.customer_id)
  const canCollect = can(me, 'billing.collect', s.customer_id)
  // DESIGN.md §16 — raising a dispute is the customer's own account.topup
  // (an owner or a billing user); resolving one is the operator's collect.
  const canRaiseDispute = can(me, 'account.topup', s.customer_id)
  const current = openDispute(disputes)
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
  // DESIGN.md §9.2 — what reached THIS invoice from the account, per payment.
  const allocations = (s.payments ?? []).flatMap((p) => (p.allocations ?? []).filter((a) => a.statement_id === s.id).map((a) => ({ a, p })))
  // DESIGN.md §17 — the per-rule tax summary. `tax_lines` is the frozen
  // summary; a statement issued before it existed carries the same rows
  // inside its tax snapshot, and either is the same block to read.
  const taxLines = s.tax_lines?.length ? s.tax_lines : (s.tax_snapshot?.lines ?? [])
  const audit = s.tax_snapshot?.audit ?? []
  const taxSummaryAgrees = Math.abs(taxLinesTotal(taxLines) - tax) < minorUnitTolerance(cur)

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
            {/* DESIGN.md §16 — the invoice in the customer's hands: the
                same scope-checked read, rendered as a document and named
                after its invoice number. */}
            <a href={invoiceDownloadURL(s.id)}>
              <button>Download PDF</button>
            </a>
            <a href={`${API_BASE}/statements/${s.id}.csv`}>
              <button>CSV</button>
            </a>
            {canRaiseDispute && canDispute(s) ? (
              <button onClick={() => setDialog({ kind: 'dispute' })} title="Tell the operator what is wrong with this invoice">
                Dispute
              </button>
            ) : null}
            {canCollect && current ? (
              <button className="primary" onClick={() => setDialog({ kind: 'resolve' })}>
                Resolve dispute
              </button>
            ) : null}
            {canIssue && s.status === 'draft' ? (
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
            {canIssue && s.status === 'issued' ? (
              <button className="primary" onClick={() => setDialog({ kind: 'send' })}>
                Mark sent
              </button>
            ) : null}
            {canCollect && acceptsPayment(s) ? (
              <button className="primary" onClick={() => setDialog({ kind: 'pay' })}>
                Record payment
              </button>
            ) : null}
            {canIssue && acceptsCreditNote(s) ? (
              <button onClick={() => setDialog({ kind: 'credit' })} title={`Up to ${money(creditRoom(s))} can still be credited`}>
                Credit note
              </button>
            ) : null}
            {canIssue && s.status === 'issued' ? (
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
      {/* DESIGN.md §16 — the dispute, with its reason and where it stands.
          The amount is still owed; it is simply not chased. */}
      {s.disputed_at ? (
        <Notice kind="warn">
          <strong>Disputed</strong>
          {s.dispute_reason ? ` — ${s.dispute_reason}` : ''}. {money(current ? toNumber(current.amount) : statementBalance(s))} stays on the balance and is not chased while it is open.
          {current ? ` Raised ${when(current.opened_at)}${current.opened_by ? ` by ${current.opened_by}` : ''}.` : ''}
        </Notice>
      ) : null}
      {!s.disputed_at && disputes.length > 0 ? (
        <Notice kind="info">
          {disputes.length === 1 ? 'A dispute on this invoice was' : `${disputes.length} disputes on this invoice were`} resolved: {disputes.map((d) => `${d.status} — ${disputeState(d)}`).join('; ')}.
        </Notice>
      ) : null}

      {s.invoice_number || s.external_invoice_ref || s.po_reference || s.due_at || s.tax_snapshot ? (
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
              {/* DESIGN.md §9.4 — what the invoice carries about tax,
                  frozen at issue: the rate applied and both parties'
                  registrations, or the exemption and its reason. */}
              {s.tax_snapshot ? (
                <tr>
                  <td className="muted">Tax</td>
                  <td>
                    {s.tax_snapshot.exempt ? (
                      <>
                        exempt{s.tax_snapshot.exempt_reason ? ` — ${s.tax_snapshot.exempt_reason}` : ''}
                      </>
                    ) : (
                      <>
                        {formatPct(toNumber(s.tax_snapshot.rate) * 100, { digits: (toNumber(s.tax_snapshot.rate) * 100) % 1 ? 2 : 0 })} on the net subtotal · {money(tax)}
                      </>
                    )}
                    <span className="sub">
                      {s.tax_snapshot.customer_tax_registration_number ? `customer registration ${s.tax_snapshot.customer_tax_registration_number}` : 'customer not tax-registered'}
                      {s.tax_snapshot.seller_legal_name || s.tax_snapshot.seller_tax_registration_number
                        ? ` · seller ${[s.tax_snapshot.seller_legal_name, s.tax_snapshot.seller_tax_registration_number].filter(Boolean).join(' ')}`
                        : ''}
                    </span>
                  </td>
                </tr>
              ) : null}
              {toNumber(s.credited_total) > 0 ? (
                <tr>
                  <td className="muted">Credited</td>
                  <td>
                    −{money(s.credited_total)} <span className="muted">· {s.credit_notes?.length ?? 0} credit note{(s.credit_notes?.length ?? 0) === 1 ? '' : 's'}</span>
                  </td>
                </tr>
              ) : null}
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

      {/* DESIGN.md §17 — the tax summary BY RATE. An invoice with several
          rates has to show one row per rule that applied: what was taxed,
          at what rate, under which rule, and the sentence that rule makes
          the invoice carry. Frozen at issue and never recomputed. */}
      {taxLines.length ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>Tax summary</h2>
            <span className="hint">
              {taxLines.length} rule{taxLines.length === 1 ? '' : 's'} applied · {distinctRates(taxLines)} rate{distinctRates(taxLines) === 1 ? '' : 's'}
            </span>
          </div>
          <table aria-label="Tax summary by rate">
            <thead>
              <tr>
                <th>Rate</th>
                <th>Kind</th>
                <th>Category</th>
                <th className="num">Taxable base</th>
                <th className="num">Tax</th>
              </tr>
            </thead>
            <tbody>
              {taxLines.map((t, i) => (
                <tr key={`${t.rule_id ?? 'rule'}-${i}`}>
                  <td className="nowrap">
                    {rateText(t.rate)}
                    {t.rule_name ? <span className="sub">{t.rule_name}</span> : null}
                  </td>
                  <td>
                    <Badge status={taxKindLabel(t.kind)} kind={kindTone(t.kind)} />
                  </td>
                  <td>{(t.category ?? '').trim() ? <span className="mono">{t.category}</span> : <span className="muted">default</span>}</td>
                  <td className="num">{money(t.base)}</td>
                  <td className="num">
                    {money(t.tax)}
                    {t.note ? <span className="sub">{t.note}</span> : null}
                  </td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr>
                <td colSpan={3}>Total</td>
                <td className="num">{money(taxBaseTotal(taxLines))}</td>
                <td className="num">{money(taxLinesTotal(taxLines))}</td>
              </tr>
            </tfoot>
          </table>
          <p className={`${taxSummaryAgrees ? 'muted' : 'bad'} small`} style={{ padding: '0 12px 12px', margin: 0 }}>
            {taxSummaryAgrees
              ? `These rows add up to the ${money(tax)} of tax on this invoice.`
              : `These rows add up to ${money(taxLinesTotal(taxLines))}, and the invoice carries ${money(tax)} of tax — the summary and the total disagree.`}
          </p>
          {audit.length ? (
            <div style={{ padding: '0 12px 12px' }}>
              <div className="muted small">Why, where the rules alone did not decide it:</div>
              <ul className="muted small" style={{ margin: '4px 0 0', paddingLeft: 18 }}>
                {audit.map((line, i) => (
                  <li key={i}>{line}</li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}

      {/* DESIGN.md §17 — the e-invoice: where the signed document has got
          to, and how to take it away. The signing KEY is never shown and
          never asked for; the hash and the algorithm are what make the
          archived copy verifiable. */}
      {s.einvoice ? (
        <div className="card">
          <div className="card-head">
            <h2>E-invoice</h2>
            <span className="hint">
              <Badge status={einvoiceStateLabel(s.einvoice.state)} kind={einvoiceTone(s.einvoice.state)} />
            </span>
          </div>
          <p className="muted small">{einvoiceStateHelp(s.einvoice.state)}</p>
          {s.einvoice.state === 'not_submitted' ? (
            <Notice kind="warn">
              Not submitted: {s.einvoice.submit_reason?.trim() || 'the profile gave no reason.'}
            </Notice>
          ) : null}
          <table>
            <tbody>
              <tr>
                <td className="muted">Profile</td>
                <td>
                  <span className="mono">{s.einvoice.profile || '—'}</span>
                  {s.einvoice.invoice_number ? <span className="sub">invoice {s.einvoice.invoice_number}</span> : null}
                </td>
              </tr>
              <tr>
                <td className="muted">Built</td>
                <td>
                  {when(s.einvoice.built_at)}
                  {s.einvoice.submitted_at ? <span className="sub">submitted {when(s.einvoice.submitted_at)}</span> : null}
                </td>
              </tr>
              {s.einvoice.submit_reference ? (
                <tr>
                  <td className="muted">Authority reference</td>
                  <td>
                    <span className="mono">{s.einvoice.submit_reference}</span>
                  </td>
                </tr>
              ) : null}
              <tr>
                <td className="muted">Hash</td>
                <td>
                  <span className="mono small" style={{ overflowWrap: 'anywhere' }}>{s.einvoice.hash || '—'}</span>
                  <span className="sub">of the document that was signed — recompute it over the archived XML to prove the copy is the one issued</span>
                </td>
              </tr>
              <tr>
                <td className="muted">Signature</td>
                <td>
                  {s.einvoice.signature_algorithm ? <span className="mono">{s.einvoice.signature_algorithm}</span> : <span className="muted">not signed</span>}
                  {s.einvoice.key_id ? <span className="sub">key {s.einvoice.key_id}</span> : null}
                  <span className="sub">the signing key stays on the server; it is never shown here and never asked for</span>
                </td>
              </tr>
              {s.einvoice.qr_payload ? (
                <tr>
                  <td className="muted">QR payload</td>
                  <td>
                    <span className="mono small" style={{ overflowWrap: 'anywhere' }}>{s.einvoice.qr_payload}</span>
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
          <div className="btn-row">
            <a href={einvoiceDocumentURL(s.id)} target="_blank" rel="noreferrer">
              Open the structured document
            </a>
            {hasArchivedXML(s.einvoice) ? (
              <a href={einvoiceXMLURL(s.id)}>Download the signed XML</a>
            ) : (
              <span className="muted small">No archival copy yet — the XML exists once the document is archived.</span>
            )}
          </div>
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
                    {/* DESIGN.md §9.2 — where the payment went: this invoice, others, or credit left on account. */}
                    {p.allocations && p.allocations.length ? <span className="sub">{allocationText(p, cur, (v, c) => formatMoney(v, c))}</span> : null}
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

      {allocations.length ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>Allocations</h2>
            <span className="hint">payments applied to this invoice from the account</span>
          </div>
          <table aria-label="Allocations">
            <thead>
              <tr>
                <th>Applied</th>
                <th>Payment</th>
                <th>By</th>
                <th className="num">Amount</th>
              </tr>
            </thead>
            <tbody>
              {allocations.map(({ a, p }) => (
                <tr key={a.id}>
                  <td>{when(a.allocated_at)}</td>
                  <td>
                    {p.reference ? <span className="mono">{p.reference}</span> : `payment ${p.id}`}
                    <span className="sub">
                      {day(p.paid_at)}
                      {p.method ? ` · ${p.method}` : ''}
                    </span>
                  </td>
                  <td className="muted">{a.allocated_by || '—'}</td>
                  <td className="num">{money(a.amount)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {s.credit_notes && s.credit_notes.length ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>Credit notes</h2>
            <span className="hint">
              −{money(s.credited_total)} off this invoice · {money(creditRoom(s))} can still be credited
            </span>
          </div>
          <table aria-label="Credit notes">
            <thead>
              <tr>
                <th>Credit note</th>
                <th>Reason</th>
                <th>Effect</th>
                <th>Issued</th>
                <th className="num">Amount</th>
              </tr>
            </thead>
            <tbody>
              {s.credit_notes.map((n: CreditNote) => (
                <tr key={n.id}>
                  <td>
                    <span className="mono">{n.number}</span>
                    <span className="sub">{n.kind === 'full' ? 'whole invoice' : n.kind === 'write_off' ? 'write-off' : 'partial'}</span>
                  </td>
                  <td>
                    {n.reason || <span className="muted">—</span>}
                    {/* DESIGN.md §15.5 — an SLA credit says what it answers. */}
                    {n.sla_pct ? (
                      <span className="sub">
                        SLA credit{n.contract_name ? ` under ${n.contract_name}` : ''} · {n.sla_pct} % of the period
                        {n.measured_availability ? ` · availability measured ${n.measured_availability} %` : ''}
                      </span>
                    ) : null}
                  </td>
                  <td>{creditNoteEffect(n, cur, (v, c) => formatMoney(v, c))}</td>
                  <td>
                    {when(n.issued_at)}
                    {n.issued_by ? <span className="sub">{n.issued_by}</span> : null}
                  </td>
                  <td className="num ok">−{money(n.total)}</td>
                </tr>
              ))}
            </tbody>
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

      {/* DESIGN.md §11 — the partner block: what the partner pays us and what
          it keeps. The server sends buy_total and margin_total to Sovereign
          roles and to that partner's roles only, so the block is simply
          absent for anyone else. */}
      {s.buy_total !== undefined && s.buy_total !== null ? (
        <div className="card">
          <h2>Partner</h2>
          <p className="muted">
            {s.statement_kind === 'wholesale'
              ? 'This is the wholesale statement of a reseller: its customers’ usage at the price it pays us, grouped by end customer.'
              : s.statement_kind === 'commission'
                ? 'This is an agent’s commission statement: what we owe the partner on the invoices we issued to its customers.'
                : `Rated through ${s.partner_name || 'a partner'}: the customer pays the net below, the partner pays us the buy price, and the difference is its margin.`}
          </p>
          <table aria-label="Partner block">
            <tbody>
              <tr>
                <td>{s.statement_kind === 'commission' ? 'Customer net' : 'Customer net'}</td>
                <td className="num">{money(net)}</td>
              </tr>
              <tr>
                <td>Partner buy</td>
                <td className="num">{money(toNumber(s.buy_total))}</td>
              </tr>
              <tr style={{ fontWeight: 600 }}>
                <td>Margin</td>
                <td className={`num ${toNumber(s.margin_total ?? 0) < 0 ? 'bad' : 'ok'}`}>
                  {money(toNumber(s.margin_total ?? 0))}
                  {net > 0 ? <span className="sub">{formatPct((toNumber(s.margin_total ?? 0) / net) * 100)} of the net</span> : null}
                </td>
              </tr>
            </tbody>
          </table>
          {s.partner_id ? (
            <p className="muted tiny">
              <Link to={`/partners/${s.partner_id}`}>{s.partner_name || 'the partner'}</Link> — margin is derived per line from the list price, the customer's discounts and the partner's tier; it is never entered.
            </p>
          ) : null}
        </div>
      ) : null}

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
                      {/* DESIGN.md §15.4 — the true-up is a LINE, named and
                          explained on the invoice: a customer charged for
                          usage it did not have has to be able to read why. */}
                      <td className="mono">
                        {l.sku}
                        {l.sku === 'true-up' ? <span className="sub">the shortfall against the contract's monthly minimum</span> : null}
                      </td>
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
      {dialog?.kind === 'dispute' ? (
        <DisputeModal
          statementId={id}
          onClose={() => setDialog(null)}
          onDone={async (d) => {
            setDialog(null)
            setFlash(`dispute raised for ${money(toNumber(d.amount))}; this invoice is not chased while the operator reviews it`)
            await Promise.all([q.reload(), disQ.reload()])
          }}
        />
      ) : null}
      {dialog?.kind === 'resolve' && current ? (
        <ResolveDisputeModal
          dispute={current}
          currency={cur}
          onClose={() => setDialog(null)}
          onDone={async (d) => {
            setDialog(null)
            setFlash(d.status === 'upheld' ? `dispute upheld; a credit note was issued for ${money(toNumber(d.amount))}` : 'dispute rejected; collections resume on this invoice')
            await Promise.all([q.reload(), disQ.reload()])
          }}
        />
      ) : null}
      {dialog?.kind === 'credit' ? (
        <CreditNoteModal
          statementId={id}
          outstanding={statementBalance(s)}
          room={creditRoom(s)}
          total={total}
          currency={cur}
          onClose={() => setDialog(null)}
          onDone={async (n) => {
            setDialog(null)
            setFlash(`credit note ${n.number} issued for ${formatMoney(toNumber(n.total), cur)} — ${creditNoteEffect(n, cur, (v, c) => formatMoney(v, c))}`)
            await q.reload()
          }}
        />
      ) : null}
    </div>
  )
}

/**
 * A credit note (DESIGN.md §9.3): the amount comes off the invoice's
 * outstanding first; anything beyond what is outstanding — a paid invoice,
 * or an over-credit — becomes credit on the customer's account. The
 * server refuses a note that would exceed the invoice total less the notes
 * already issued; this form refuses the same before the round trip.
 */
function CreditNoteModal({
  statementId,
  outstanding,
  room,
  total,
  currency,
  onClose,
  onDone,
}: {
  statementId: string
  outstanding: number
  room: number
  total: number
  currency: string
  onClose: () => void
  onDone: (note: CreditNote) => void | Promise<void>
}) {
  const [form, setForm] = useState<CreditNoteForm>(() => emptyCreditNoteForm(Math.min(outstanding, room)))
  const [errors, setErrors] = useState<Errors<CreditNoteForm>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = <K extends keyof CreditNoteForm>(k: K, v: CreditNoteForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const amount = form.full ? room : Number(form.amount || 0)
  const toAccount = Math.max(0, amount - outstanding)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateCreditNote(form, room)
    setErrors(errs)
    if (hasErrors(errs)) return
    setBusy(true)
    setError('')
    try {
      await onDone(await api.post<CreditNote>(`/statements/${statementId}/credit-notes`, creditNoteBody(form)))
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Issue a credit note"
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="credit-note-form" disabled={busy}>
            {busy ? 'Issuing…' : 'Issue credit note'}
          </button>
        </>
      }
    >
      <form id="credit-note-form" onSubmit={(e) => void submit(e)} className="stack tight">
        <p className="muted small" style={{ margin: 0 }}>
          {formatMoney(outstanding, currency)} outstanding of {formatMoney(total, currency)}; {formatMoney(room, currency)} can still be credited. The note reduces the outstanding first — anything beyond it becomes credit on the account.
        </p>
        <label className="check">
          <input type="checkbox" checked={form.full} onChange={(e) => set('full', e.target.checked)} /> Credit the whole invoice ({formatMoney(total, currency)})
        </label>
        {!form.full ? (
          <Field label={`Amount (${currency})`} error={errors.amount} help="Including tax, at the rate the invoice was issued with.">
            <input value={form.amount} onChange={(e) => set('amount', e.target.value)} inputMode="decimal" autoFocus />
          </Field>
        ) : null}
        <Field label="Reason" error={errors.reason} help="Printed on the credit note and kept on the record.">
          <input value={form.reason} onChange={(e) => set('reason', e.target.value)} placeholder="service credit for the outage of 12 August" />
        </Field>
        {!hasErrors(errors) && amount > 0 && toAccount > 0 ? <Notice kind="warn">{formatMoney(toAccount, currency)} exceeds what is outstanding and will be held as credit on the account.</Notice> : null}
        {error ? <Notice kind="bad">{error}</Notice> : null}
      </form>
    </Modal>
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

/**
 * DisputeModal (DESIGN.md §16) — the customer says what is wrong. Naming
 * lines is optional: with none, the whole outstanding balance is disputed;
 * with some, the share of the invoice those lines represent.
 */
function DisputeModal({ statementId, onClose, onDone }: { statementId: string; onClose: () => void; onDone: (d: Dispute) => void | Promise<void> }) {
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!reason.trim()) {
      setError('say what is wrong with the invoice')
      return
    }
    setBusy(true)
    setError('')
    try {
      const d = await api.post<Dispute>(`/statements/${statementId}/disputes`, { reason: reason.trim() })
      await onDone(d)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Dispute this invoice"
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" onClick={(e) => void submit(e)} disabled={busy}>
            Raise the dispute
          </button>
        </>
      }
    >
      <form className="stack tight" onSubmit={(e) => void submit(e)}>
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <p>The amount stays owed while the operator reviews it — it is simply not chased. If the dispute is upheld you receive a credit note for it.</p>
        <Field label="What is wrong?" help="The operator reads this; be specific about the line or the period.">
          <textarea value={reason} onChange={(ev) => setReason(ev.target.value)} rows={4} placeholder="The storage line is not ours — that volume belongs to another account." />
        </Field>
      </form>
    </Modal>
  )
}

/**
 * ResolveDisputeModal (DESIGN.md §16) — the operator's answer. UPHELD issues
 * a credit note for the disputed amount through the product's own credit-note
 * machinery; REJECTED clears the flag and collections resume.
 */
function ResolveDisputeModal({ dispute, currency, onClose, onDone }: { dispute: Dispute; currency: string; onClose: () => void; onDone: (d: Dispute) => void | Promise<void> }) {
  const [outcome, setOutcome] = useState<'upheld' | 'rejected'>('upheld')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const amount = formatMoney(toNumber(dispute.amount), currency)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const d = await api.post<Dispute>(`/disputes/${dispute.id}/resolve`, { outcome, note: note.trim() })
      await onDone(d)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      title="Resolve this dispute"
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" onClick={(e) => void submit(e)} disabled={busy}>
            {outcome === 'upheld' ? `Uphold and credit ${amount}` : 'Reject and resume collections'}
          </button>
        </>
      }
    >
      <form className="stack tight" onSubmit={(e) => void submit(e)}>
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <p>
          <strong>{amount}</strong> disputed — {dispute.reason}
        </p>
        {DISPUTE_OUTCOMES.map((o) => (
          <label className="check" key={o.outcome}>
            <input type="radio" name="outcome" checked={outcome === o.outcome} onChange={() => setOutcome(o.outcome)} /> {o.label} — {o.help}
          </label>
        ))}
        <Field label="Note" help="Recorded on the dispute and, when upheld, on the credit note.">
          <textarea value={note} onChange={(ev) => setNote(ev.target.value)} rows={3} />
        </Field>
      </form>
    </Modal>
  )
}
