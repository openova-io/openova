import { useState } from 'react'
import { Link } from 'react-router-dom'
import { API_BASE, api, asList } from '../api/client'
import type { Statement } from '../api/types'
import { DataTable, type Column } from '../components/DataTable'
import { Badge, Confirm, Notice, Skeleton } from '../components/ui'
import { day, when } from '../lib/format'
import { formatMoney } from '../lib/money'
import { useAction } from '../lib/useAction'
import { useQuery } from '../lib/useQuery'

/** One customer's statements (#6867). `canIssue` = the operator may issue drafts. */
export function StatementsPanel({ customerId, canIssue, showCustomer }: { customerId: string; canIssue: boolean; showCustomer?: boolean }) {
  const q = useQuery<unknown>(`/customers/${customerId}/statements`)
  const rows = asList<Statement>(q.data, 'statements')
  return (
    <div className="stack">
      {q.error ? <Notice kind="bad">{q.error}</Notice> : null}
      {q.loading && !q.data ? <Skeleton lines={3} /> : <StatementTable rows={rows} canIssue={canIssue} onChanged={q.reload} showCustomer={showCustomer} />}
    </div>
  )
}

export function StatementTable({
  rows,
  canIssue,
  onChanged,
  showCustomer,
}: {
  rows: Statement[]
  canIssue: boolean
  onChanged: () => void | Promise<void>
  showCustomer?: boolean
}) {
  const act = useAction()
  const [issuing, setIssuing] = useState<Statement | null>(null)

  const columns: Column<Statement>[] = [
    ...(showCustomer ? [{ key: 'customer', header: 'Customer', value: (s: Statement) => s.customer_name ?? s.customer_slug ?? s.customer_id } as Column<Statement>] : []),
    {
      key: 'period',
      header: 'Period',
      value: (s) => s.period_start,
      render: (s) => (
        <Link to={`/statements/${s.id}`}>
          {day(s.period_start)} → {day(s.period_end)}
        </Link>
      ),
    },
    { key: 'status', header: 'Status', value: (s) => s.status, render: (s) => <Badge status={s.status} /> },
    { key: 'subtotal', header: 'Subtotal', value: (s) => Number(s.subtotal), numeric: true, render: (s) => formatMoney(s.subtotal, s.currency) },
    {
      key: 'discount',
      header: 'Discounts',
      value: (s) => Number(s.discount_total ?? 0),
      numeric: true,
      render: (s) => (Number(s.discount_total ?? 0) > 0 ? <span className="down">−{formatMoney(s.discount_total, s.currency)}</span> : <span className="muted">—</span>),
    },
    { key: 'tax', header: 'Tax', value: (s) => Number(s.tax), numeric: true, render: (s) => formatMoney(s.tax, s.currency) },
    { key: 'total', header: 'Total', value: (s) => Number(s.total), numeric: true, render: (s) => formatMoney(s.total, s.currency), total: (rs) => (rs.length > 1 && new Set(rs.map((r) => r.currency)).size === 1 ? formatMoney(rs.reduce((n, r) => n + Number(r.total || 0), 0), rs[0].currency) : '') },
    { key: 'issued', header: 'Issued', value: (s) => s.issued_at ?? '', render: (s) => when(s.issued_at) },
    {
      key: 'actions',
      header: '',
      value: () => '',
      sortable: false,
      className: 'nowrap',
      render: (s) => (
        <span className="btn-row">
          <a href={`${API_BASE}/statements/${s.id}.csv`} className="small">
            CSV
          </a>
          {canIssue && s.status === 'draft' ? (
            <button className="link small" disabled={act.busy} onClick={() => setIssuing(s)}>
              Issue
            </button>
          ) : null}
        </span>
      ),
    },
  ]

  return (
    <div className="stack">
      {act.error ? <Notice kind="bad">{act.error}</Notice> : null}
      {act.ok ? <Notice kind="ok">{act.ok}</Notice> : null}
      <div className="card pad-0">
        <DataTable columns={columns} rows={rows} rowKey={(s) => s.id} defaultSort={{ key: 'period', dir: 'desc' }} emptyTitle="No statements yet" emptyBody={canIssue ? 'Run a period from Statements to rate collected usage into a draft.' : 'A statement appears here once the operator runs a billing period.'} />
      </div>
      {issuing ? (
        <Confirm
          title="Issue statement"
          confirmLabel="Issue"
          busy={act.busy}
          onClose={() => setIssuing(null)}
          onConfirm={async () => {
            const ok = await act.run(`statement ${day(issuing.period_start)} issued`, () => api.post(`/statements/${issuing.id}/issue`), onChanged)
            if (ok) setIssuing(null)
          }}
          body={
            <>
              Issue the statement for {day(issuing.period_start)} → {day(issuing.period_end)} ({formatMoney(issuing.total, issuing.currency)})? An issued statement is final: its lines and discounts are frozen and it can no longer be re-run or deleted.
            </>
          }
        />
      ) : null}
    </div>
  )
}
