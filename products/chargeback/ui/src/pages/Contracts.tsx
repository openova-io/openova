import { useMemo, useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { Contract, Customer } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, EmptyState, Field, KPI, Modal, Notice, PageHeader, Skeleton } from '../components/ui'
import { useSession } from '../auth/session'
import { can } from '../lib/access'
import { CONTRACT_STATUSES, daysToEnd, renewalDue, termEnd, termText } from '../lib/contracts'
import { today } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { useQuery } from '../lib/useQuery'

/**
 * Configure → Contracts (DESIGN.md §15.9). The directory of agreements with
 * the renewals due INSIDE THEIR NOTICE WINDOW at the top, because that is
 * the only thing on this page anybody has to act on: a contract is otherwise
 * a record, and a renewal is a deadline.
 *
 * Writing needs `customers.manage`; reading is `metering.read` at the scope,
 * so a customer principal that reaches this path sees its own and nothing
 * else — the server decides that, not this page.
 */

/** The status badge tone: active is good, expired and cancelled are not. */
function statusKind(status: string): 'ok' | 'warn' | 'bad' | undefined {
  if (status === 'active') return 'ok'
  if (status === 'draft') return 'warn'
  if (status === 'expired' || status === 'cancelled') return 'bad'
  return undefined
}

export function Contracts() {
  const { me } = useSession()
  const canManage = can(me, 'customers.manage')
  const list = useQuery<unknown>('/contracts')
  const customers = useQuery<unknown>('/customers')
  const [creating, setCreating] = useState(false)
  const [flash, setFlash] = useState('')
  const nav = useNavigate()

  const contracts = useMemo(() => asList<Contract>(list.data, 'contracts'), [list.data])
  const customerRows = useMemo(() => asList<Customer>(customers.data, 'customers'), [customers.data])
  const on = today()
  const due = useMemo(() => contracts.filter((c) => renewalDue(c, on)), [contracts, on])
  const active = contracts.filter((c) => c.status === 'active')
  const minimums = active.reduce((n, c) => n + toNumber(c.minimum_commitment), 0)
  const currency = active.find((c) => c.currency)?.currency ?? ''
  const mixed = new Set(active.map((c) => c.currency)).size > 1

  const cols: Column<Contract>[] = [
    {
      key: 'name',
      header: 'Contract',
      value: (c) => c.name,
      render: (c) => (
        <span>
          <Link to={`/contracts/${c.id}`}>{c.name}</Link>
          {c.po_reference ? <span className="sub">PO {c.po_reference}</span> : null}
        </span>
      ),
    },
    {
      key: 'customer',
      header: 'Customer',
      value: (c) => c.customer_name ?? '',
      render: (c) => <Link to={`/customers/${c.customer_id}?tab=contract`}>{c.customer_name || c.customer_id}</Link>,
    },
    { key: 'term', header: 'Term', value: (c) => c.starts_on, render: (c) => <span className="nowrap">{termText(c)}</span> },
    {
      key: 'minimum',
      header: 'Monthly minimum',
      value: (c) => toNumber(c.minimum_commitment),
      numeric: true,
      render: (c) =>
        toNumber(c.minimum_commitment) > 0 ? (
          <span title="A period below this carries a true-up line for the shortfall">{formatMoney(toNumber(c.minimum_commitment), c.currency)}</span>
        ) : (
          <span className="muted">none</span>
        ),
    },
    { key: 'status', header: 'Status', value: (c) => c.status, render: (c) => <Badge status={c.status} kind={statusKind(c.status)} /> },
    {
      key: 'renewal',
      header: 'Renews',
      value: (c) => c.ends_on,
      render: (c) => {
        const days = daysToEnd(c, on)
        return (
          <span className="nowrap">
            {c.auto_renew ? c.renewal_date || '—' : <span className="muted">ends {c.ends_on}</span>}
            {days !== null && c.status === 'active' ? <span className="sub">{days >= 0 ? `in ${days} day${days === 1 ? '' : 's'}` : `${-days} day${days === -1 ? '' : 's'} ago`}</span> : null}
          </span>
        )
      },
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Contracts"
        sub="The agreement a customer's commercial terms hang on: the term, the monthly minimum, and the committed-use and allowance lines the rating engine applies."
        actions={
          canManage ? (
            <button className="primary" onClick={() => setCreating(true)}>
              New contract
            </button>
          ) : null
        }
      />
      {flash ? <Notice kind="ok">{flash}</Notice> : null}
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}

      <div className="kpis">
        <KPI label="Active contracts" value={active.length} note={contracts.length === active.length ? 'every contract on the book' : `of ${contracts.length} on the book`} />
        <KPI
          label="Renewals due"
          value={due.length}
          note={due.length ? 'inside the notice window — act before the term ends' : 'nothing inside a notice window today'}
          tone={due.length ? 'warn' : undefined}
        />
        <KPI
          label="Committed monthly"
          value={mixed ? '—' : formatMoney(minimums, currency)}
          note={mixed ? 'the active contracts are in more than one currency' : 'the minimums a period is measured against'}
        />
      </div>

      {/* The renewals-due list: the deadline, called out. */}
      {due.length ? (
        <div className="card">
          <div className="card-head">
            <h2>Renewals due</h2>
            <span className="hint">inside the notice window on {on}</span>
          </div>
          <ul className="stack tight" style={{ margin: 0, paddingLeft: 18 }}>
            {due.map((c) => {
              const days = daysToEnd(c, on)
              return (
                <li key={c.id}>
                  <Link to={`/contracts/${c.id}`}>{c.name}</Link> · {c.customer_name} · ends {c.ends_on}
                  {days !== null ? ` (${days} day${days === 1 ? '' : 's'})` : ''} ·{' '}
                  {c.auto_renew ? (
                    <span className="muted">renews automatically for another {c.term_months} months on {c.renewal_date}</span>
                  ) : (
                    <b>expires unless it is renewed</b>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
      ) : null}

      <div className="card">
        <div className="card-head">
          <h2>All contracts</h2>
          <span className="hint">soonest to renew first</span>
        </div>
        {list.loading && !contracts.length ? (
          <Skeleton lines={4} />
        ) : contracts.length === 0 ? (
          <EmptyState title="No contracts">
            A contract carries the term, the monthly minimum whose shortfall is invoiced as a true-up line, and the committed-use and allowance lines the rating engine applies. Without one a customer is
            rated at its price books alone.
          </EmptyState>
        ) : (
          <DataTable columns={cols} rows={contracts} rowKey={(c) => c.id} defaultSort={{ key: 'renewal', dir: 'asc' }} pageSize={20} emptyTitle="No contracts" />
        )}
      </div>

      {creating ? (
        <NewContractModal
          customers={customerRows}
          onClose={() => setCreating(false)}
          onCreated={(c) => {
            setCreating(false)
            setFlash(`${c.name} created`)
            nav(`/contracts/${c.id}`)
          }}
        />
      ) : null}
    </div>
  )
}

/** Create a contract. The end date follows the term unless it is typed. */
export function NewContractModal({ customers, customerId, onClose, onCreated }: { customers: Customer[]; customerId?: string; onClose: () => void; onCreated: (c: Contract) => void }) {
  const [form, setForm] = useState({
    customer_id: customerId ?? customers[0]?.id ?? '',
    name: '',
    starts_on: today(),
    term_months: '12',
    minimum_commitment: '',
    currency: 'OMR',
    status: 'draft',
    auto_renew: true,
    renewal_notice_days: '30',
    po_reference: '',
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }))
  const ends = termEnd(form.starts_on, Number(form.term_months))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!form.customer_id) {
      setError('Choose the customer this contract is with')
      return
    }
    if (!form.name.trim()) {
      setError('A contract needs a name')
      return
    }
    setBusy(true)
    setError('')
    try {
      const created = await api.post<Contract>('/contracts', {
        customer_id: form.customer_id,
        name: form.name.trim(),
        starts_on: form.starts_on,
        term_months: Number(form.term_months) || 12,
        minimum_commitment: form.minimum_commitment.trim() === '' ? null : form.minimum_commitment.trim(),
        currency: form.currency.trim().toUpperCase(),
        status: form.status,
        auto_renew: form.auto_renew,
        renewal_notice_days: Number(form.renewal_notice_days) || 0,
        po_reference: form.po_reference.trim(),
      })
      onCreated(created)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="New contract"
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="new-contract" disabled={busy}>
            {busy ? 'Creating…' : 'Create contract'}
          </button>
        </>
      }
    >
      <form id="new-contract" onSubmit={(e) => void submit(e)} className="stack tight">
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <div className="grid2">
          <Field label="Customer">
            <select value={form.customer_id} onChange={(e) => set({ customer_id: e.target.value })} aria-label="Customer" disabled={Boolean(customerId)}>
              {customers.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Name" help="What the agreement is called in your records">
            <input value={form.name} onChange={(e) => set({ name: e.target.value })} placeholder="e.g. ACME 2026" autoFocus />
          </Field>
          <Field label="Starts on">
            <input type="date" value={form.starts_on} onChange={(e) => set({ starts_on: e.target.value })} />
          </Field>
          <Field label="Term (months)" help={ends ? `Ends ${ends} — the day before the anniversary, so the term never overlaps its own renewal.` : undefined}>
            <input type="number" min={1} step={1} value={form.term_months} onChange={(e) => set({ term_months: e.target.value })} />
          </Field>
          <Field label="Monthly minimum" help="A period whose net falls below this carries a true-up line for the shortfall. Leave empty for none.">
            <input type="number" step="any" min={0} value={form.minimum_commitment} onChange={(e) => set({ minimum_commitment: e.target.value })} placeholder="none" />
          </Field>
          <Field label="Currency">
            <input value={form.currency} maxLength={3} onChange={(e) => set({ currency: e.target.value.toUpperCase() })} />
          </Field>
          <Field label="Status" help="Only an ACTIVE contract covering the first day of a period rates that period.">
            <select value={form.status} onChange={(e) => set({ status: e.target.value })} aria-label="Status">
              {CONTRACT_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Renewal notice (days)" help="How long before the end date it appears in the renewals-due list.">
            <input type="number" min={0} step={1} value={form.renewal_notice_days} onChange={(e) => set({ renewal_notice_days: e.target.value })} />
          </Field>
          <Field label="Purchase order">
            <input value={form.po_reference} onChange={(e) => set({ po_reference: e.target.value })} placeholder="optional" />
          </Field>
          <Field label="Auto-renew">
            <label className="check">
              <input type="checkbox" checked={form.auto_renew} onChange={(e) => set({ auto_renew: e.target.checked })} aria-label="Auto-renew" /> Renew for another term at the end date
            </label>
          </Field>
        </div>
      </form>
    </Modal>
  )
}
