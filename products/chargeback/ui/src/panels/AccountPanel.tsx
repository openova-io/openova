import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { AccountDocument, CreditNote, Customer, Payment, PaymentIntent, Suspension } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, KPI, Modal, Notice, Skeleton } from '../components/ui'
import { accountFigures, allocationText, balanceWord, creditNoteEffect, entryLabel, entryReference, intentStatus, ledgerRows, paymentStatus, suspensionOutcome, suspensionText, type LedgerRow } from '../lib/account'
import { gatewayLabel } from '../lib/customers'
import { when } from '../lib/format'
import { TOP_UP_METHODS, emptyTopUpForm, hasErrors, topUpBody, validateTopUp, type Errors, type TopUpForm } from '../lib/forms'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/**
 * The Account tab (DESIGN.md §9): the balance and what it is made of, the
 * ledger with a running balance, the credit notes and the platform
 * suspensions. Money in is a top-up (credit on account) or, for a gateway
 * customer, a checkout through the gateway seam; credit reaches invoices
 * only when the operator applies it — explicit, never implicit (§9.5).
 */

type Dialog = { kind: 'topup' } | { kind: 'checkout' } | { kind: 'apply' } | null

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
    </div>
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
