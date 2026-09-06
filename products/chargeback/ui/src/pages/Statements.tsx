import { useMemo, useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import type { Customer, RunResult, Statement } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Field, KPI, Modal, Notice, PageHeader, Segmented, Skeleton } from '../components/ui'
import { lastMonth, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { toNumber } from '../lib/num'
import { statementPeriod } from '../lib/statements'
import { customerName, useCustomers } from '../lib/useCustomers'
import { useQuery } from '../lib/useQuery'

/**
 * Statements (DESIGN.md §2.9) — one period at a time across every customer,
 * from GET /statements?period=. Filters live in the URL so a period can be
 * shared; drafts can be issued or deleted here, issued ones only opened.
 */

type StatusFilter = 'all' | 'draft' | 'issued'
type Dialog = { kind: 'run' } | { kind: 'issue'; s: Statement } | { kind: 'delete'; s: Statement } | null

const PERIOD_RE = /^\d{4}-(0[1-9]|1[0-2])$/

export function Statements() {
  const [params, setParams] = useSearchParams()
  const period = PERIOD_RE.test(params.get('period') ?? '') ? params.get('period')! : lastMonth()
  const customerId = params.get('customer') ?? ''
  const statusParam = params.get('status')
  const status: StatusFilter = statusParam === 'draft' || statusParam === 'issued' ? statusParam : 'all'
  const setParam = (k: string, v: string) => {
    const p = new URLSearchParams(params)
    if (v) p.set(k, v)
    else p.delete(k)
    setParams(p, { replace: true })
  }

  const { customers } = useCustomers()
  const list = useQuery<unknown>(`/statements?period=${encodeURIComponent(period)}`)
  const all = useMemo(() => asList<Statement>(list.data, 'statements'), [list.data])
  const rows = useMemo(() => all.filter((s) => (!customerId || s.customer_id === customerId) && (status === 'all' || s.status === status)), [all, customerId, status])
  const [dialog, setDialog] = useState<Dialog>(null)
  const [error, setError] = useState('')
  const [flash, setFlash] = useState('')
  const [busy, setBusy] = useState(false)

  const nameOf = (s: Statement) => s.customer_name ?? customerName(customers, s.customer_id, s.customer_slug)
  const drafts = rows.filter((s) => s.status === 'draft').length
  const issued = rows.filter((s) => s.status === 'issued').length
  const currencies = Array.from(new Set(rows.map((s) => s.currency)))
  const total = rows.reduce((n, s) => n + toNumber(s.total), 0)

  const act = async (kind: 'issue' | 'delete', s: Statement) => {
    setBusy(true)
    setError('')
    try {
      if (kind === 'issue') await api.post(`/statements/${s.id}/issue`)
      else await api.del(`/statements/${s.id}`)
      setDialog(null)
      setFlash(kind === 'issue' ? `${nameOf(s)} · ${statementPeriod(s)} issued` : `draft for ${nameOf(s)} · ${statementPeriod(s)} deleted`)
      await list.reload()
    } catch (e) {
      setError(errorText(e))
      setDialog(null)
    } finally {
      setBusy(false)
    }
  }

  const columns: Column<Statement>[] = [
    {
      key: 'customer',
      header: 'Customer',
      value: (s) => nameOf(s),
      render: (s) => (
        <>
          <Link to={`/customers/${s.customer_id}?tab=statements`}>{nameOf(s)}</Link>
          {s.customer_slug ? <span className="sub mono">{s.customer_slug}</span> : null}
        </>
      ),
    },
    { key: 'period', header: 'Period', value: (s) => s.period_start, render: (s) => <Link to={`/statements/${s.id}`}>{statementPeriod(s)}</Link> },
    { key: 'status', header: 'Status', value: (s) => s.status, render: (s) => <Badge status={s.status} /> },
    { key: 'subtotal', header: 'List subtotal', value: (s) => toNumber(s.subtotal), numeric: true, render: (s) => formatMoney(s.subtotal, s.currency) },
    {
      key: 'discount',
      header: 'Discount',
      value: (s) => toNumber(s.discount_total),
      numeric: true,
      render: (s) => (toNumber(s.discount_total) > 0 ? <span className="ok">−{formatMoney(s.discount_total, s.currency)}</span> : <span className="muted">—</span>),
    },
    { key: 'tax', header: 'Tax', value: (s) => toNumber(s.tax), numeric: true, render: (s) => formatMoney(s.tax, s.currency) },
    {
      key: 'total',
      header: 'Total',
      value: (s) => toNumber(s.total),
      numeric: true,
      render: (s) => <b>{formatMoney(s.total, s.currency)}</b>,
      total: (rs) => (currencies.length === 1 ? formatMoney(rs.reduce((n, s) => n + toNumber(s.total), 0), currencies[0]) : 'mixed currencies'),
    },
    { key: 'issued_at', header: 'Issued', value: (s) => s.issued_at ?? '', render: (s) => (s.issued_at ? when(s.issued_at) : <span className="muted">—</span>) },
    {
      key: 'actions',
      className: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      render: (s) => (
        <span className="btn-row">
          <Link to={`/statements/${s.id}`}>
            <button className="small">Open</button>
          </Link>
          <a href={`${API_BASE}/statements/${s.id}.csv`}>
            <button className="small">CSV</button>
          </a>
          {s.status === 'draft' ? (
            <>
              <button className="small primary" onClick={() => setDialog({ kind: 'issue', s })}>
                Issue
              </button>
              <button className="small danger" onClick={() => setDialog({ kind: 'delete', s })}>
                Delete
              </button>
            </>
          ) : null}
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      <PageHeader
        title="Statements"
        sub={`${period} · ${all.length} statement${all.length === 1 ? '' : 's'} in the period${customerId ? ` · ${customerName(customers, customerId)}` : ''}`}
        actions={
          <button className="primary" onClick={() => setDialog({ kind: 'run' })}>
            Run period
          </button>
        }
      />
      {list.error ? <Notice kind="bad">{list.error}</Notice> : null}
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {flash ? <Notice kind="ok">{flash}</Notice> : null}

      <div className="toolbar" role="region" aria-label="Statement filters">
        <Field label="Period">
          <input type="month" value={period} onChange={(e) => PERIOD_RE.test(e.target.value) && setParam('period', e.target.value)} aria-label="Period" />
        </Field>
        <Field label="Customer">
          <select value={customerId} onChange={(e) => setParam('customer', e.target.value)} aria-label="Customer">
            <option value="">all customers</option>
            {customers.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Status">
          <Segmented<StatusFilter>
            value={status}
            onChange={(v) => setParam('status', v === 'all' ? '' : v)}
            options={[
              { value: 'all', label: 'All' },
              { value: 'draft', label: 'Draft' },
              { value: 'issued', label: 'Issued' },
            ]}
            ariaLabel="Status"
          />
        </Field>
      </div>

      <div className="kpis">
        <KPI label="Drafts" value={drafts} note={drafts ? 'not yet final — re-running the period replaces them' : 'nothing awaiting issue'} tone={drafts ? 'warn' : undefined} />
        <KPI label="Issued" value={issued} note="final; kept as issued even if the period is re-run" />
        <KPI
          label="Total of the selection"
          value={currencies.length <= 1 ? formatMoney(total, currencies[0], { compact: true }) : 'mixed'}
          note={currencies.length > 1 ? `${currencies.join(', ')} — totals are not summed across currencies` : `${rows.length} statement${rows.length === 1 ? '' : 's'} · list subtotal − discounts + tax`}
        />
      </div>

      <div className="card pad-0">
        {list.loading && !list.data ? (
          <div style={{ padding: 16 }}>
            <Skeleton lines={4} />
          </div>
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(s) => s.id}
            defaultSort={{ key: 'customer', dir: 'asc' }}
            csvName={`statements-${period}`}
            emptyTitle={all.length ? 'No statement matches the filter' : `No statements for ${period}`}
            emptyBody={
              all.length ? 'Widen the customer or status filter.' : 'The period has not been run: usage is collected continuously, but a statement only exists once "Run period" rates it into drafts.'
            }
          />
        )}
      </div>

      {dialog?.kind === 'run' ? (
        <RunModal
          period={period}
          customerId={customerId}
          customers={customers}
          onClose={() => setDialog(null)}
          onDone={(r, p, c) => {
            setDialog(null)
            const n = typeof r.created === 'number' ? r.created : Array.isArray(r.statements) ? r.statements.length : null
            setFlash(`${r.period ?? p} rated${n !== null ? ` — ${n} draft statement${n === 1 ? '' : 's'}` : ''}`)
            const q = new URLSearchParams(params)
            q.set('period', p)
            if (c) q.set('customer', c)
            else q.delete('customer')
            setParams(q, { replace: true })
            void list.reload()
          }}
        />
      ) : null}
      {dialog?.kind === 'issue' ? (
        <Confirm
          title={`Issue ${nameOf(dialog.s)} · ${statementPeriod(dialog.s)}?`}
          confirmLabel="Issue statement"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => act('issue', dialog.s)}
          body={
            <p>
              An issued statement is final: {formatMoney(dialog.s.total, dialog.s.currency)} with its lines and discounts frozen. Re-running the period will not change it.
            </p>
          }
        />
      ) : null}
      {dialog?.kind === 'delete' ? (
        <Confirm
          title={`Delete the draft for ${nameOf(dialog.s)} · ${statementPeriod(dialog.s)}?`}
          danger
          confirmLabel="Delete draft"
          busy={busy}
          onClose={() => setDialog(null)}
          onConfirm={() => act('delete', dialog.s)}
          body={<p>Removes the draft only; the usage stays and the period can be run again.</p>}
        />
      ) : null}
    </div>
  )
}

function RunModal({
  period,
  customerId,
  customers,
  onClose,
  onDone,
}: {
  period: string
  customerId: string
  customers: Customer[]
  onClose: () => void
  onDone: (r: RunResult, period: string, customerId: string) => void
}) {
  const [p, setP] = useState(period)
  const [c, setC] = useState(customerId)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const run = async (e: FormEvent) => {
    e.preventDefault()
    if (!PERIOD_RE.test(p)) {
      setError('Period must be a month')
      return
    }
    setBusy(true)
    setError('')
    try {
      const body: Record<string, string> = { period: p }
      if (c) body.customer_id = c
      onDone(await api.post<RunResult>('/statements/run', body), p, c)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Run a period"
      onClose={onClose}
      footer={
        <>
          <button type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" form="run-period-form" disabled={busy}>
            {busy ? 'Rating…' : 'Run period'}
          </button>
        </>
      }
    >
      <form id="run-period-form" onSubmit={run} className="stack tight">
        <p className="muted small" style={{ margin: 0 }}>
          Rates the month's collected usage at each customer's price book, applies active discounts and tax, and writes one draft statement per customer.
        </p>
        <div className="grid2">
          <Field label="Period">
            <input type="month" value={p} onChange={(e) => setP(e.target.value)} required autoFocus />
          </Field>
          <Field label="Customer">
            <select value={c} onChange={(e) => setC(e.target.value)}>
              <option value="">all customers</option>
              {customers.map((x) => (
                <option key={x.id} value={x.id}>
                  {x.name}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Notice kind="warn">Existing drafts for {p || 'the period'} are replaced by the new rating. Issued statements are never touched.</Notice>
        {error ? <Notice kind="bad">{error}</Notice> : null}
      </form>
    </Modal>
  )
}
