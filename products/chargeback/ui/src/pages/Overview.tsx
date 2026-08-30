import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, errorText } from '../api/client'
import type { Overview as OverviewData, UsageRow } from '../api/types'
import { Empty, Notice } from '../components/ui'
import { money, num } from '../lib/format'

export function Overview() {
  const [data, setData] = useState<OverviewData | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    api
      .get<OverviewData>('/overview')
      .then(setData)
      .catch((e) => setError(errorText(e)))
  }, [])

  if (error) return <Notice kind="bad">{error}</Notice>
  if (!data) return <p className="muted">Loading…</p>

  const byStatus = data.customers_by_status ?? {}
  const total = Object.values(byStatus).reduce((a, b) => a + (Number(b) || 0), 0)
  const usage = Array.isArray(data.usage_last_30d) ? (data.usage_last_30d as UsageRow[]) : []
  const rated = data.rated_total_last_period

  return (
    <div className="stack">
      <h1>Overview</h1>
      <div className="cards">
        <div className="card kpi">
          <div className="v">{total}</div>
          <div className="k">customers</div>
        </div>
        {['pending', 'active', 'suspended'].map((s) => (
          <div className="card kpi" key={s}>
            <div className="v">{byStatus[s] ?? 0}</div>
            <div className="k">{s}</div>
          </div>
        ))}
        <div className="card kpi">
          <div className="v">
            {rated && typeof rated === 'object' ? money(rated.total, rated.currency) : money(rated as number | string | null)}
          </div>
          <div className="k">rated total{rated && typeof rated === 'object' && rated.period ? ` · ${rated.period}` : ' · last period'}</div>
        </div>
      </div>

      <h2>Usage, last 30 days</h2>
      {usage.length === 0 ? (
        <Empty>No usage recorded in the last 30 days.</Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>SKU</th>
                <th className="num">Quantity</th>
                <th>Unit</th>
                <th className="num">Resources</th>
              </tr>
            </thead>
            <tbody>
              {usage.map((r, i) => (
                <tr key={i}>
                  <td className="mono">{r.sku ?? r.resource_id ?? r.day ?? '—'}</td>
                  <td className="num">{num(r.quantity)}</td>
                  <td>{r.unit ?? '—'}</td>
                  <td className="num">{r.resource_count ?? '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="muted small">
        <Link to="/customers">Customers</Link> · <Link to="/pricebooks">Price books</Link> ·{' '}
        <Link to="/statements">Statements</Link>
      </p>
    </div>
  )
}
