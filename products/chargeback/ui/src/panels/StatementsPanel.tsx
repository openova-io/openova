import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import type { Statement } from '../api/types'
import { Badge, Empty, Notice } from '../components/ui'
import { day, money, when } from '../lib/format'

export function StatementsPanel({
  customerId,
  canIssue,
  showCustomer,
}: {
  customerId: string
  canIssue: boolean
  showCustomer?: boolean
}) {
  const [rows, setRows] = useState<Statement[]>([])
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const res = await api.get<unknown>(`/customers/${customerId}/statements`)
      setRows(asList<Statement>(res, 'statements'))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [customerId])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <div className="stack">
      {error ? <Notice kind="bad">{error}</Notice> : null}
      <StatementTable rows={rows} canIssue={canIssue} onChanged={load} showCustomer={showCustomer} />
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
  const [error, setError] = useState('')
  const issue = async (id: string) => {
    if (!window.confirm('Issue this statement? An issued statement is final.')) return
    try {
      await api.post(`/statements/${id}/issue`)
      await onChanged()
    } catch (e) {
      setError(errorText(e))
    }
  }
  if (rows.length === 0) return <Empty>No statements yet.</Empty>
  return (
    <div className="table-wrap">
      {error ? <Notice kind="bad">{error}</Notice> : null}
      <table>
        <thead>
          <tr>
            {showCustomer ? <th>Customer</th> : null}
            <th>Period</th>
            <th>Status</th>
            <th className="num">Subtotal</th>
            <th className="num">Tax</th>
            <th className="num">Total</th>
            <th>Issued</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((s) => (
            <tr key={s.id}>
              {showCustomer ? <td>{s.customer_name ?? s.customer_slug ?? s.customer_id}</td> : null}
              <td>
                <Link to={`/statements/${s.id}`}>
                  {day(s.period_start)} → {day(s.period_end)}
                </Link>
              </td>
              <td>
                <Badge status={s.status} />
              </td>
              <td className="num">{money(s.subtotal, s.currency)}</td>
              <td className="num">{money(s.tax, s.currency)}</td>
              <td className="num">{money(s.total, s.currency)}</td>
              <td>{when(s.issued_at)}</td>
              <td className="row">
                <a href={`${API_BASE}/statements/${s.id}.csv`}>CSV</a>
                {canIssue && s.status === 'draft' ? (
                  <button className="link" onClick={() => void issue(s.id)}>
                    Issue
                  </button>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
