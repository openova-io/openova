import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { API_BASE, api, errorText } from '../api/client'
import { useSession } from '../auth/session'
import type { Statement } from '../api/types'
import { Badge, Empty, Notice } from '../components/ui'
import { day, money, num, when } from '../lib/format'

export function StatementView() {
  const { id = '' } = useParams()
  const { me } = useSession()
  const [s, setS] = useState<Statement | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      setS(await api.get<Statement>(`/statements/${id}`))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [id])
  useEffect(() => {
    void load()
  }, [load])

  if (error && !s) return <Notice kind="bad">{error}</Notice>
  if (!s) return <p className="muted">Loading…</p>

  const issue = async () => {
    if (!window.confirm('Issue this statement? An issued statement is final.')) return
    try {
      await api.post(`/statements/${id}/issue`)
      await load()
    } catch (e) {
      setError(errorText(e))
    }
  }

  const back = me?.role === 'operator' ? `/customers/${s.customer_id}?tab=statements` : '/my/statements'
  const lines = s.lines ?? []

  return (
    <div className="stack" style={{ maxWidth: 960 }}>
      <div className="row between">
        <div>
          <h1 style={{ marginBottom: 2 }}>
            Statement {day(s.period_start)} → {day(s.period_end)}
          </h1>
          <div className="muted small">
            {s.customer_name ?? s.customer_slug ?? s.customer_id} · {s.currency} · created {when(s.created_at)}
            {s.issued_at ? ` · issued ${when(s.issued_at)}` : ''}
          </div>
        </div>
        <div className="row">
          <Badge status={s.status} />
          <a href={`${API_BASE}/statements/${s.id}.csv`}>
            <button>Download CSV</button>
          </a>
          {me?.role === 'operator' && s.status === 'draft' ? (
            <button className="primary" onClick={() => void issue()}>
              Issue
            </button>
          ) : null}
        </div>
      </div>
      {error ? <Notice kind="bad">{error}</Notice> : null}

      {lines.length === 0 ? (
        <Empty>No rated lines in this period.</Empty>
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
            <tbody>
              {lines.map((l, i) => (
                <tr key={i}>
                  <td className="mono">{l.sku}</td>
                  <td>{l.unit ?? '—'}</td>
                  <td className="num">{num(l.quantity)}</td>
                  <td className="num">{num(l.unit_price, 8)}</td>
                  <td className="num">{money(l.amount, s.currency)}</td>
                  <td className="num">{l.resource_count ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="card" style={{ maxWidth: 360, marginLeft: 'auto' }}>
        <div className="row between">
          <span className="muted">Subtotal</span>
          <span>{money(s.subtotal, s.currency)}</span>
        </div>
        <div className="row between">
          <span className="muted">Tax ({num(Number(s.tax_rate) * 100, 2)}%)</span>
          <span>{money(s.tax, s.currency)}</span>
        </div>
        <div className="row between" style={{ fontWeight: 600 }}>
          <span>Total</span>
          <span>{money(s.total, s.currency)}</span>
        </div>
      </div>
      <p>
        <Link to={back}>← Back</Link>
      </p>
    </div>
  )
}
