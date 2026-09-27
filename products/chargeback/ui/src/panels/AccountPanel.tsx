import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { AccountDocument, CreditNote, Customer, Payment, PaymentIntent, Statement, Suspension } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, FormRow, KPI, Modal, Notice, Segmented, Skeleton } from '../components/ui'
import { accountFigures, allocationText, balanceWord, canAllocate, creditNoteEffect, entryLabel, entryReference, intentStatus, ledgerRows, openInvoices, paymentStatus, suspensionOutcome, suspensionText, type LedgerRow } from '../lib/account'
import { gatewayLabel } from '../lib/customers'
import { day, when } from '../lib/format'
import { TOP_UP_METHODS, emptyTopUpForm, hasErrors, topUpBody, validateTopUp, type Errors, type TopUpForm } from '../lib/forms'
import { formatMoney, minorUnitDigits, minorUnitTolerance } from '../lib/money'
import { toNumber } from '../lib/num'
import { statementBalance, statementPeriod } from '../lib/statements'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * The Account tab (DESIGN.md §9): the balance and what it is made of, the
 * ledger with a running balance, the payments with where each one went, the
 * credit notes and the platform suspensions. Money in is a top-up (credit on
 * account) or, for a gateway customer, a checkout through the gateway seam;
 * credit reaches invoices only when the operator applies it — explicit,
 * never implicit (§9.5) — and a settled payment is allocated or refunded
 * from its own row (#6946).
 */

type Dialog = { kind: 'topup' } | { kind: 'checkout' } | { kind: 'apply' } | { kind: 'allocate'; payment: Payment } | { kind: 'refund'; payment: Payment } | null

/**
 * The money controls follow the caller's permissions (DESIGN.md §10.9):
 * `canRecord` (billing.collect) records a transfer as a top-up and applies
 * credit — the operator's; `canCheckout` (account.topup, or billing.collect)
 * asks the gateway to collect — the one money write a customer may make on
 * its own account; `canApplyCredit` defaults to `canRecord`. The panel
 * renders read-only without them, which is what a viewer sees.
 */
export function AccountPanel({
  customerId,
  customer,
  currency: fallbackCurrency,
  canRecord = true,
  canCheckout = true,
  canApplyCredit,
  onChanged,
}: {
  customerId: string
  customer: Customer
  currency?: string
  canRecord?: boolean
  canCheckout?: boolean
  canApplyCredit?: boolean
  onChanged?: () => void | Promise<void>
}) {
  const mayApplyCredit = canApplyCredit ?? canRecord
  const acct = useQuery<AccountDocument>(`/customers/${customerId}/account`)
  const susp = useQuery<unknown>(`/customers/${customerId}/suspensions`)
  const gateway = customer.payment_method === 'gateway'
  const intents = useQuery<unknown>(gateway ? `/customers/${customerId}/payment-intents` : null)
  const act = useAction()
  const [dialog, setDialog] = useState<Dialog>(null)
  const [intent, setIntent] = useState<PaymentIntent | null>(null)
  const [applied, setApplied] = useState<number | null>(null)

  const a = acct.data
  const currency = a?.currency || fallbackCurrency || ''
  const money = (v: number | string | null | undefined) => formatMoney(toNumber(v), currency)
  const rows = useMemo(() => ledgerRows(a?.entries ?? []), [a])
  const paymentsById = useMemo(() => new Map((a?.payments ?? []).map((p) => [p.id, p])), [a])
  const suspensions = useMemo(() => asList<Suspension>(susp.data, 'suspensions'), [susp.data])
  const intentRows = useMemo(() => asList<PaymentIntent>(intents.data, 'intents'), [intents.data])

  if (acct.error && !a) return <Notice kind="bad">{acct.error}</Notice>
  if (!a) return <Skeleton lines={5} />

  const f = accountFigures(a)
  const word = balanceWord(a.balance)
  const external = a.account_owner === 'external'
  const canApply = !external && f.credit > 0 && a.open_invoices > 0
  const gatewayName = gatewayLabel(customer.gateway_name) || 'the gateway'

  const reloadAll = async () => {
    await Promise.all([acct.reload(), susp.reload(), gateway ? intents.reload() : Promise.resolve(), onChanged?.()])
  }

  const applyCredit = async () => {
    const ok = await act.run(
      'credit applied to the open invoices, oldest due first',
      async () => {
        const r = await api.post<{ applied?: Record<string, number | string> }>(`/customers/${customerId}/account/apply-credit`, {})
        setApplied(
          Object.values(r?.applied ?? {})
            .map((v) => toNumber(v))
            .reduce((n, v) => n + v, 0),
        )
      },
      reloadAll,
    )
    if (ok) setDialog(null)
  }

  const ledger: Column<LedgerRow>[] = [
    { key: 'date', header: 'Date', value: (r) => r.entry.entered_at, render: (r) => when(r.entry.entered_at) },
    {
      key: 'kind',
      header: 'Type',
      value: (r) => entryLabel(r.entry.kind),
      render: (r) => {
        const p = r.entry.payment_id !== undefined ? paymentsById.get(r.entry.payment_id) : undefined
        return (
          <>
            {entryLabel(r.entry.kind)}
            {p ? <span className="sub">{allocationText(p, currency, (v, cur) => formatMoney(v, cur))}</span> : r.entry.note ? <span className="sub">{r.entry.note}</span> : null}
          </>
        )
      },
    },
    {
      key: 'reference',
      header: 'Reference',
      value: (r) => entryReference(r.entry),
      render: (r) => {
        const ref = entryReference(r.entry)
        if (!ref) return <span className="muted">—</span>
        return r.entry.statement_id ? (
          <Link to={`/statements/${r.entry.statement_id}`} className="mono">
            {ref}
          </Link>
        ) : (
          <span className="mono">{ref}</span>
        )
      },
    },
    { key: 'debit', header: 'Debit', numeric: true, value: (r) => r.debit, render: (r) => (r.debit === null ? <span className="muted">—</span> : money(r.debit)) },
    { key: 'credit', header: 'Credit', numeric: true, value: (r) => r.credit, render: (r) => (r.credit === null ? <span className="muted">—</span> : <span className="ok">{money(r.credit)}</span>) },
    {
      key: 'balance',
      header: 'Balance',
      numeric: true,
      value: (r) => r.running,
      render: (r) => (
        <span className={r.running > 0 ? 'bad' : r.running < 0 ? 'ok' : ''} title={balanceWord(r.running)}>
          {money(Math.abs(r.running))}
          {r.running !== 0 ? <span className="sub">{balanceWord(r.running)}</span> : null}
        </span>
      ),
    },
  ]

  const notes: Column<CreditNote>[] = [
    { key: 'number', header: 'Credit note', value: (n) => n.number, render: (n) => <span className="mono">{n.number}</span> },
    {
      key: 'invoice',
      header: 'Invoice',
      value: (n) => n.invoice_number ?? n.statement_id,
      render: (n) => (
        <Link to={`/statements/${n.statement_id}`} className="mono">
          {n.invoice_number || n.statement_id}
        </Link>
      ),
    },
    { key: 'reason', header: 'Reason', value: (n) => n.reason ?? '', render: (n) => n.reason || <span className="muted">—</span> },
    { key: 'total', header: 'Amount', numeric: true, value: (n) => toNumber(n.total), render: (n) => money(n.total) },
    { key: 'effect', header: 'Effect', value: (n) => creditNoteEffect(n, currency, (v, cur) => formatMoney(v, cur)), sortable: false },
    { key: 'issued', header: 'Issued', value: (n) => n.issued_at, render: (n) => when(n.issued_at) },
  ]

  const suspCols: Column<Suspension>[] = [
    { key: 'at', header: 'When', value: (s) => s.at, render: (s) => when(s.at) },
    {
      key: 'action',
      header: 'Action',
      value: (s) => suspensionOutcome(s).label,
      render: (s) => {
        const o = suspensionOutcome(s)
        return (
          <>
            <Badge status={o.label} kind={o.ok ? (s.action === 'resume' ? 'ok' : 'warn') : 'bad'} />
            {o.detail ? <span className={o.ok ? 'sub' : 'sub bad'}>{o.detail}</span> : null}
          </>
        )
      },
    },
    { key: 'actor', header: 'By', value: (s) => s.actor ?? '', render: (s) => s.actor || <span className="muted">—</span> },
  ]

  // DESIGN.md §9.2 — every payment, what it settled and what is still on
  // account; allocating more of one and refunding one are the operator's
  // billing.collect, from the payment's own row.
  const paymentCols: Column<Payment>[] = [
    { key: 'date', header: 'Received', value: (p) => p.paid_at, render: (p) => day(p.paid_at) },
    {
      key: 'reference',
      header: 'Reference',
      value: (p) => p.reference ?? '',
      render: (p) => (
        <>
          {p.reference ? <span className="mono">{p.reference}</span> : <span className="muted">—</span>}
          <span className="sub">{[p.method, p.purpose].filter(Boolean).join(' · ')}</span>
        </>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      value: (p) => paymentStatus(p),
      render: (p) => (
        <>
          <Badge status={paymentStatus(p)} kind={paymentStatus(p) === 'settled' ? 'ok' : paymentStatus(p) === 'pending' ? 'warn' : 'bad'} />
          {p.refund_reason ? <span className="sub">{p.refund_reason}</span> : null}
        </>
      ),
    },
    { key: 'amount', header: 'Amount', numeric: true, value: (p) => toNumber(p.amount), render: (p) => money(p.amount) },
    { key: 'where', header: 'Where it went', value: (p) => allocationText(p, currency, (v, cur) => formatMoney(v, cur)), sortable: false },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap actions',
      render: (p) => {
        if (!canRecord) return null
        const settled = paymentStatus(p) === 'settled'
        const allocatable = canAllocate(p) && a.open_invoices > 0
        return (
          <span className="btn-row">
            {!external ? (
              <button
                className="small"
                disabled={act.busy || !allocatable}
                title={!settled ? `A ${paymentStatus(p)} payment settles nothing and cannot be allocated` : !canAllocate(p) ? 'The payment is fully allocated' : a.open_invoices === 0 ? 'No open invoice to apply it to' : `Apply up to ${money(p.unallocated)} of this payment to the open invoices`}
                onClick={() => setDialog({ kind: 'allocate', payment: p })}
              >
                Allocate
              </button>
            ) : null}
            <button className="small" disabled={act.busy || !settled} title={settled ? 'Reverse this payment: its allocations are released and the invoices they settled are due again' : `Only a settled payment can be refunded; this one is ${paymentStatus(p)}`} onClick={() => setDialog({ kind: 'refund', payment: p })}>
              Refund
            </button>
          </span>
        )
      },
    },
  ]

  const intentCols: Column<PaymentIntent>[] = [
    { key: 'created', header: 'Requested', value: (i) => i.created_at, render: (i) => when(i.created_at) },
    { key: 'purpose', header: 'Purpose', value: (i) => i.purpose },
    { key: 'amount', header: 'Amount', numeric: true, value: (i) => toNumber(i.amount), render: (i) => formatMoney(i.amount, i.currency || currency) },
    {
      key: 'status',
      header: 'Status',
      value: (i) => intentStatus(i),
      render: (i) => (
        <>
          <Badge status={intentStatus(i)} kind={i.status === 'settled' ? 'ok' : i.status === 'failed' || i.status === 'refused' ? 'bad' : 'warn'} />
          {i.detail ? <span className="sub">{i.detail}</span> : null}
        </>
      ),
    },
    {
      key: 'reference',
      header: 'Reference',
      value: (i) => i.reference ?? '',
      render: (i) => (
        <>
          {i.reference ? <span className="mono">{i.reference}</span> : <span className="muted">—</span>}
          {i.pay_url ? (
            <span className="sub">
              <a href={i.pay_url} target="_blank" rel="noreferrer">
                payment page
              </a>
            </span>
          ) : null}
        </>
      ),
    },
  ]

  return (
    <div className="stack">
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? (
        <Notice kind="ok">
          {act.ok}
          {applied !== null && act.ok.startsWith('credit applied') ? ` — ${money(applied)} applied` : ''}
        </Notice>
      ) : null}
      {intent ? (
        <Notice kind={intent.status === 'settled' ? 'ok' : intent.status === 'failed' || intent.status === 'refused' ? 'bad' : 'info'}>
          Checkout with {gatewayName}: <b>{intentStatus(intent)}</b>
          {intent.reference ? (
            <>
              {' '}
              · reference <span className="mono">{intent.reference}</span>
            </>
          ) : null}
          {intent.pay_url ? (
            <>
              {' '}
              ·{' '}
              <a href={intent.pay_url} target="_blank" rel="noreferrer">
                payment page
              </a>
            </>
          ) : null}
          {intent.detail ? ` · ${intent.detail}` : ''}
        </Notice>
      ) : null}
      {a.suspension ? (
        <Notice kind="bad">
          {customer.name} is {suspensionText(a.suspension)}. <Link to="/collections">Open Collections</Link> to resume once settled.
        </Notice>
      ) : null}
      {external ? (
        <Notice kind="info">
          This account is owned by the operator's billing system: it collects and keeps the balance. Its last reported balance is shown beside ours; credit is applied there, not here.
        </Notice>
      ) : null}

      <div className="kpis">
        <KPI
          label="Balance"
          value={<span className={f.balance > 0 ? 'bad' : f.balance < 0 ? 'ok' : ''}>{money(Math.abs(f.balance))}</span>}
          note={word === 'owes' ? 'owed by the customer' : word === 'in credit' ? 'in credit' : 'settled'}
          tone={f.balance > 0 ? 'bad' : f.balance < 0 ? 'ok' : undefined}
          hint="The ledger sum: invoices less payments, credit notes and write-offs"
        />
        <KPI label="Credit available" value={money(f.credit)} note={f.credit > 0 ? (canApply ? 'can be applied to the open invoices' : 'on account') : 'nothing on account'} />
        <KPI label="Owed" value={money(f.outstanding)} note={`${a.open_invoices} open invoice${a.open_invoices === 1 ? '' : 's'}`} />
        <KPI label="Overdue" value={money(f.overdue)} note={f.overdue > 0 ? 'past its due date' : 'nothing past due'} tone={f.overdue > 0 ? 'bad' : undefined} />
        {external ? (
          <KPI label="Billing system balance" value={a.external_balance === null || a.external_balance === undefined ? '—' : money(a.external_balance)} note={a.external_balance_at ? `reported ${when(a.external_balance_at)}` : 'not yet reported'} />
        ) : null}
      </div>

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Account ledger</h2>
          <span className="btn-row">
            {a.payment_model === 'prepaid' ? (
              <span className="hint">
                prepaid wallet · {a.suspend_at_zero ? 'suspends at zero' : 'stays up at zero'}
                {a.low_balance_threshold !== null && a.low_balance_threshold !== undefined ? ` · alert below ${money(a.low_balance_threshold)}` : ''}
              </span>
            ) : null}
            {canRecord ? (
              <button className="small" onClick={() => setDialog({ kind: 'topup' })} disabled={act.busy} title="Record a transfer or internal recharge as credit on the account">
                Top up
              </button>
            ) : null}
            {canCheckout && gateway ? (
              <button className="small primary" onClick={() => setDialog({ kind: 'checkout' })} disabled={act.busy} title={`Ask ${gatewayName} to collect a top-up`}>
                {canRecord ? `Checkout with ${gatewayName}` : 'Top up'}
              </button>
            ) : null}
            {canCheckout && !canRecord && !gateway ? (
              <span className="hint" title="This account has no payment gateway; the operator records transfers">
                top-ups are recorded by the operator
              </span>
            ) : null}
            {!external && mayApplyCredit ? (
              <button className="small primary" onClick={() => setDialog({ kind: 'apply' })} disabled={act.busy || !canApply} title={canApply ? `Apply ${money(f.credit)} to the open invoices` : f.credit > 0 ? 'No open invoice to apply it to' : 'No credit on account'}>
                Apply credit
              </button>
            ) : null}
          </span>
        </div>
        <DataTable
          columns={ledger}
          rows={rows}
          rowKey={(r) => String(r.entry.id)}
          label="Account ledger"
          csvName={`account-${customer.slug}`}
          emptyTitle="No account activity yet"
          emptyBody="The first issued invoice or top-up opens the ledger."
          footNote={a.auto_apply_credit ? 'credit is applied automatically when an invoice is issued' : 'credit stays on account until it is applied'}
        />
      </div>

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Payments</h2>
          <span className="hint">{a.payments.length} recorded · what each one settled</span>
        </div>
        <DataTable
          columns={paymentCols}
          rows={a.payments}
          rowKey={(p) => String(p.id)}
          label="Payments"
          defaultSort={{ key: 'date', dir: 'desc' }}
          csvName={`payments-${customer.slug}`}
          emptyTitle="No payments yet"
          emptyBody="A top-up, a checkout or a payment recorded against an invoice appears here with where it went."
        />
      </div>

      {gateway ? (
        <div className="card pad-0">
          <div className="card-head" style={{ padding: '12px 12px 0' }}>
            <h2>Checkouts</h2>
            <span className="hint">requests to {gatewayName}</span>
          </div>
          <DataTable columns={intentCols} rows={intentRows} rowKey={(i) => i.id} label="Payment intents" emptyTitle="No checkout requested yet" emptyBody={`A checkout asks ${gatewayName} to collect a top-up from the customer.`} />
        </div>
      ) : null}

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Credit notes</h2>
          <span className="hint">{a.credit_notes.length} issued</span>
        </div>
        <DataTable columns={notes} rows={a.credit_notes} rowKey={(n) => n.id} label="Credit notes" emptyTitle="No credit notes" emptyBody="An issued invoice is only ever reduced by a credit note, issued from its statement page." />
      </div>

      <div className="card pad-0">
        <div className="card-head" style={{ padding: '12px 12px 0' }}>
          <h2>Suspensions</h2>
          <span className="hint">what this product did at the platform</span>
        </div>
        {susp.error ? (
          <Notice kind="bad">{susp.error}</Notice>
        ) : (
          <DataTable columns={suspCols} rows={suspensions} rowKey={(s) => String(s.id)} label="Suspensions" emptyTitle="Never suspended at the platform" emptyBody="A suspension by collections, the prepaid wallet or an operator would be listed here with the platform's answer." />
        )}
      </div>

      {dialog?.kind === 'topup' || dialog?.kind === 'checkout' ? (
        <MoneyInModal
          mode={dialog.kind}
          customerId={customerId}
          currency={currency}
          gatewayName={gatewayName}
          onClose={() => setDialog(null)}
          onDone={async (label, i) => {
            setDialog(null)
            if (i) setIntent(i)
            act.setError('')
            await act.run(label, async () => {}, reloadAll)
          }}
        />
      ) : null}
      {dialog?.kind === 'apply' ? (
        <Confirm
          title={`Apply ${money(f.credit)} of credit?`}
          confirmLabel="Apply credit"
          busy={act.busy}
          onClose={() => setDialog(null)}
          onConfirm={applyCredit}
          body={
            <p>
              Applies the available credit to the {a.open_invoices} open invoice{a.open_invoices === 1 ? '' : 's'}, oldest due first, until the credit or the invoices run out. Nothing moves until you confirm.
            </p>
          }
        />
      ) : null}
      {dialog?.kind === 'allocate' ? (
        <AllocateModal
          key={dialog.payment.id}
          payment={dialog.payment}
          customerId={customerId}
          currency={currency}
          onClose={() => setDialog(null)}
          onDone={async (label) => {
            setDialog(null)
            act.setError('')
            await act.run(label, async () => {}, reloadAll)
          }}
        />
      ) : null}
      {dialog?.kind === 'refund' ? (
        <RefundModal
          key={dialog.payment.id}
          payment={dialog.payment}
          currency={currency}
          onClose={() => setDialog(null)}
          onDone={async (label) => {
            setDialog(null)
            act.setError('')
            await act.run(label, async () => {}, reloadAll)
          }}
        />
      ) : null}
    </div>
  )
}

/**
 * Allocate more of a settled payment (DESIGN.md §9.2, POST
 * /payments/{id}/allocate). Either oldest due first across the open invoices,
 * or the invoices the operator picks with the amount each takes. The rules
 * are the store's, checked here before the round trip and shown in its words
 * after it: every amount above zero, none above what its invoice still owes
 * (judged at the currency's minor unit), and together no more than what is
 * still unallocated on the payment.
 */
function AllocateModal({ payment, customerId, currency, onClose, onDone }: { payment: Payment; customerId: string; currency: string; onClose: () => void; onDone: (label: string) => void | Promise<void> }) {
  const list = useQuery<unknown>(`/customers/${customerId}/statements`)
  const invoices = useMemo(() => openInvoices(asList<Statement>(list.data, 'statements')), [list.data])
  const [mode, setMode] = useState<'auto' | 'pick'>('auto')
  const [amounts, setAmounts] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const digits = minorUnitDigits(currency)
  const tolerance = minorUnitTolerance(currency)
  const remaining = toNumber(payment.unallocated)
  const money = (v: number | string | null | undefined) => formatMoney(toNumber(v), currency, { digits })
  // Ticked rows, whatever their amount says: a ticked invoice with a cleared
  // amount is refused by the check below, never skipped in silence.
  const picked = invoices.filter((s) => amounts[s.id] !== undefined)
  const sum = picked.reduce((n, s) => n + Number(amounts[s.id] || 0), 0)

  // Ticking an invoice offers it what is left of the payment, up to what it
  // owes, at the minor unit; the figure stays editable.
  const toggle = (s: Statement) => {
    setAmounts((cur) => {
      if (cur[s.id] !== undefined) {
        const next = { ...cur }
        delete next[s.id]
        return next
      }
      const left = remaining - Object.values(cur).reduce((n, v) => n + Number(v || 0), 0)
      const offer = Math.max(0, Math.min(statementBalance(s), left))
      return { ...cur, [s.id]: offer.toFixed(digits) }
    })
  }

  const validate = (): string => {
    if (mode === 'auto') return ''
    if (picked.length === 0) return 'pick at least one invoice, or apply oldest due first'
    for (const s of picked) {
      const v = Number(amounts[s.id])
      if (!Number.isFinite(v) || v <= 0) return `an allocation amount must be above zero (${s.invoice_number || statementPeriod(s)})`
      if (v - statementBalance(s) >= tolerance) return `allocation of ${money(v)} exceeds the outstanding balance of ${money(statementBalance(s))} on ${s.invoice_number || statementPeriod(s)}`
    }
    if (sum - remaining >= tolerance) return `the allocations (${money(sum)}) exceed the unallocated ${money(remaining)} of this payment`
    return ''
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const msg = validate()
    if (msg) {
      setError(msg)
      return
    }
    setBusy(true)
    setError('')
    try {
      const body = mode === 'auto' ? { auto: true } : { allocations: picked.map((s) => ({ statement_id: s.id, amount: Number(amounts[s.id]).toFixed(digits) })) }
      const p = await api.post<Payment>(`/payments/${payment.id}/allocate`, body)
      await onDone(`${money(toNumber(p.allocated) - toNumber(payment.allocated))} of ${p.reference || `payment ${p.id}`} applied — ${allocationText(p, currency, (v, cur) => formatMoney(v, cur))}`)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const title = `Allocate ${payment.reference || `payment ${payment.id}`}`
  return (
    <Modal
      title={title}
      onClose={onClose}
      wide
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="allocate-form" disabled={busy || (mode === 'pick' && picked.length === 0)}>
            {busy ? 'Applying…' : mode === 'auto' ? 'Apply oldest due first' : `Apply ${money(sum)}`}
          </button>
        </>
      }
    >
      <form id="allocate-form" onSubmit={(e) => void submit(e)} className="stack tight" aria-label={title}>
        <p className="muted small" style={{ margin: 0 }}>
          {money(remaining)} of this {money(payment.amount)} payment is still on account. Nothing moves until you apply it.
        </p>
        <FormRow>
          <Segmented
            value={mode}
            ariaLabel="How to allocate"
            options={[
              { value: 'auto', label: 'Oldest due first' },
              { value: 'pick', label: 'Choose invoices' },
            ]}
            onChange={setMode}
          />
        </FormRow>
        {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
        {mode === 'pick' ? (
          list.loading && !list.data ? (
            <Skeleton lines={3} />
          ) : invoices.length === 0 ? (
            <Notice kind="info">This customer has no open invoice to apply the payment to.</Notice>
          ) : (
            <table aria-label="Open invoices">
              <thead>
                <tr>
                  <th />
                  <th>Invoice</th>
                  <th>Due</th>
                  <th className="num">Outstanding</th>
                  <th className="num">Apply ({currency})</th>
                </tr>
              </thead>
              <tbody>
                {invoices.map((s) => {
                  const on = amounts[s.id] !== undefined
                  return (
                    <tr key={s.id}>
                      <td>
                        <input type="checkbox" aria-label={`Apply to ${s.invoice_number || statementPeriod(s)}`} checked={on} onChange={() => toggle(s)} />
                      </td>
                      <td>
                        <span className="mono">{s.invoice_number || statementPeriod(s)}</span>
                        <span className="sub">{statementPeriod(s)}</span>
                      </td>
                      <td>{s.due_at ? day(s.due_at) : <span className="muted">—</span>}</td>
                      <td className="num">{money(statementBalance(s))}</td>
                      <td className="num">
                        <input inputMode="decimal" aria-label={`Amount for ${s.invoice_number || statementPeriod(s)}`} value={amounts[s.id] ?? ''} disabled={!on} onChange={(e) => setAmounts({ ...amounts, [s.id]: e.target.value })} style={{ width: 120 }} />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
              <tfoot>
                <tr>
                  <td colSpan={4}>To apply · {money(remaining - sum)} stays on account</td>
                  <td className="num">{money(sum)}</td>
                </tr>
              </tfoot>
            </table>
          )
        ) : (
          <p className="muted small" style={{ margin: 0 }}>
            Applies {money(remaining)} to the open invoices oldest due date first, until the payment or the invoices run out. Anything left stays on account.
          </p>
        )}
        {error ? <Notice kind="bad">{error}</Notice> : null}
      </form>
    </Modal>
  )
}

/**
 * Refund a settled payment (POST /payments/{id}/refund). The store reverses
 * the WHOLE payment — there is no partial refund: its allocations are
 * released, the invoices they settled are due again, and the ledger gets a
 * refund debit against the original credit, which stays. The reason is what
 * the audit trail and the ledger line carry.
 */
function RefundModal({ payment, currency, onClose, onDone }: { payment: Payment; currency: string; onClose: () => void; onDone: (label: string) => void | Promise<void> }) {
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const money = (v: number | string | null | undefined) => formatMoney(toNumber(v), currency)
  const settled = (payment.allocations ?? []).map((al) => al.invoice_number || 'an invoice')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!reason.trim()) {
      setError('say why the payment is refunded — it is written on the ledger line')
      return
    }
    setBusy(true)
    setError('')
    try {
      const p = await api.post<Payment>(`/payments/${payment.id}/refund`, { reason: reason.trim() })
      await onDone(`${money(p.amount)} refunded (${p.reference || `payment ${p.id}`})${settled.length ? `; ${settled.join(', ')} ${settled.length === 1 ? 'is' : 'are'} due again` : ''}`)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const title = `Refund ${payment.reference || `payment ${payment.id}`}`
  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="danger" form="refund-form" disabled={busy}>
            {busy ? 'Refunding…' : `Refund ${money(payment.amount)}`}
          </button>
        </>
      }
    >
      <form id="refund-form" onSubmit={(e) => void submit(e)} className="stack tight" aria-label={title}>
        <p className="muted small" style={{ margin: 0 }}>
          The whole payment of {money(payment.amount)} is reversed — a payment is refunded in full, never in part.
          {settled.length ? ` Its allocation${settled.length === 1 ? '' : 's'} to ${settled.join(', ')} ${settled.length === 1 ? 'is' : 'are'} released and ${settled.length === 1 ? 'that invoice is' : 'those invoices are'} due again.` : ' It settled no invoice; the credit it left on account is withdrawn.'}{' '}
          The original credit stays on the ledger and the refund offsets it.
        </p>
        <Field label="Reason" help="Written on the ledger line and the audit trail.">
          <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="duplicate transfer, returned to the customer" autoFocus />
        </Field>
        {error ? <Notice kind="bad">{error}</Notice> : null}
      </form>
    </Modal>
  )
}

/**
 * Money in. A top-up records a transfer or internal recharge as credit on
 * the account (POST /customers/{id}/payments with no allocations); a
 * checkout asks the gateway to collect it (POST .../payment-intents).
 */
function MoneyInModal({
  mode,
  customerId,
  currency,
  gatewayName,
  onClose,
  onDone,
}: {
  mode: 'topup' | 'checkout'
  customerId: string
  currency: string
  gatewayName: string
  onClose: () => void
  onDone: (label: string, intent?: PaymentIntent) => void | Promise<void>
}) {
  const today = new Date().toISOString().slice(0, 10)
  const [form, setForm] = useState<TopUpForm>(() => emptyTopUpForm(today))
  const [errors, setErrors] = useState<Errors<TopUpForm>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = <K extends keyof TopUpForm>(k: K, v: TopUpForm[K]) => setForm((f) => ({ ...f, [k]: v }))
  const checkout = mode === 'checkout'

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const errs = validateTopUp(checkout ? { ...form, paid_at: today, method: 'transfer' } : form)
    setErrors(errs)
    if (hasErrors(errs)) return
    setBusy(true)
    setError('')
    try {
      if (checkout) {
        const i = await api.post<PaymentIntent>(`/customers/${customerId}/payment-intents`, { purpose: 'checkout', amount: form.amount.trim(), reference: form.reference.trim() })
        await onDone(`checkout of ${formatMoney(Number(form.amount), currency)} requested from ${gatewayName}`, i)
      } else {
        const p = await api.post<Payment>(`/customers/${customerId}/payments`, topUpBody(form))
        await onDone(`top-up of ${formatMoney(toNumber(p.amount), currency)} recorded as credit on account (${paymentStatus(p)})`)
      }
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={checkout ? `Checkout with ${gatewayName}` : 'Top up the account'}
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="money-in-form" disabled={busy}>
            {busy ? 'Working…' : checkout ? 'Request checkout' : 'Record top-up'}
          </button>
        </>
      }
    >
      <form id="money-in-form" onSubmit={(e) => void submit(e)} className="stack tight">
        <p className="muted small" style={{ margin: 0 }}>
          {checkout ? `Asks ${gatewayName} to collect this amount from the customer. A settled checkout lands as credit on the account.` : 'A payment not tied to an invoice: it is held as credit on the account until it is applied.'}
        </p>
        <div className="grid2">
          <Field label={`Amount (${currency})`} error={errors.amount}>
            <input value={form.amount} onChange={(e) => set('amount', e.target.value)} inputMode="decimal" autoFocus />
          </Field>
          {checkout ? (
            <Field label="Reference" error={errors.reference} help="Optional — your reference for this checkout.">
              <input value={form.reference} onChange={(e) => set('reference', e.target.value)} className="mono" placeholder="TOPUP-2026-09" />
            </Field>
          ) : (
            <Field label="Received on" error={errors.paid_at} help="The day the money arrived, as the bank shows it.">
              <input type="date" value={form.paid_at} onChange={(e) => set('paid_at', e.target.value)} />
            </Field>
          )}
        </div>
        {!checkout ? (
          <div className="grid2">
            <Field label="Reference" error={errors.reference} help="The bank or recharge reference. The same reference is never booked twice.">
              <input value={form.reference} onChange={(e) => set('reference', e.target.value)} className="mono" placeholder="TRF-4471" />
            </Field>
            <Field label="Method" error={errors.method}>
              <select value={form.method} onChange={(e) => set('method', e.target.value)}>
                {TOP_UP_METHODS.map((m) => (
                  <option key={m.value} value={m.value}>
                    {m.label}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        ) : null}
        {error ? <Notice kind="bad">{error}</Notice> : null}
      </form>
    </Modal>
  )
}
