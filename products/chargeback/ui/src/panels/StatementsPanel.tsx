import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { API_BASE, api, asList, errorText } from '../api/client'
import type { Statement } from '../api/types'
import { Badge, Confirm, Empty, Notice } from '../components/ui'
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
  const [pending, setPending] = useState<{ kind: 'issue' | 'delete'; s: Statement } | null>(null)
  const [busy, setBusy] = useState(false)
  const act = async () => {
    if (!pending) return
    setBusy(true)
    try {
      if (pending.kind === 'issue') await api.post(`/statements/${pending.s.id}/issue`)
      else await api.del(`/statements/${pending.s.id}`)
      setPending(null)
      setError('')
      await onChanged()
    } catch (e) {
      setError(errorText(e))
      setPending(null)
    } finally {
      setBusy(false)
    }
  }
  if (rows.length === 0) return <Empty>No statements yet. Run a period from Statements to rate collected usage into a draft.</Empty>
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
            <th className="num">Discount</th>
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
              <td className="num">{Number(s.discount_total ?? 0) > 0 ? `−${money(s.discount_total, s.currency)}` : '—'}</td>
              <td className="num">{money(s.tax, s.currency)}</td>
              <td className="num">{money(s.total, s.currency)}</td>
              <td>{when(s.issued_at)}</td>
              <td className="row">
                <a href={`${API_BASE}/statements/${s.id}.csv`}>CSV</a>
                {canIssue && s.status === 'draft' ? (
                  <>
                    <button className="link" onClick={() => setPending({ kind: 'issue', s })}>
                      Issue
                    </button>
                    <button className="link danger" onClick={() => setPending({ kind: 'delete', s })}>
                      Delete
                    </button>
                  </>
                ) : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {pending ? (
        <Confirm
          title={pending.kind === 'issue' ? 'Issue this statement?' : 'Delete this draft?'}
          danger={pending.kind === 'delete'}
          confirmLabel={pending.kind === 'issue' ? 'Issue' : 'Delete draft'}
          busy={busy}
          onClose={() => setPending(null)}
          onConfirm={act}
          body={
            pending.kind === 'issue' ? (
              <p>An issued statement is final: {money(pending.s.total, pending.s.currency)} with its lines and discounts frozen.</p>
            ) : (
              <p>Removes the draft only; the usage stays and the period can be run again.</p>
            )
          }
        />
      ) : null}
    </div>
  )
}
