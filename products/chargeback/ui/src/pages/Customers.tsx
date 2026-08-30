import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { Customer, InviteIssued } from '../api/types'
import { Badge, Empty, Notice } from '../components/ui'
import { day, when } from '../lib/format'

export function sourceCount(c: Customer): number | string {
  if (Array.isArray(c.sources)) return c.sources.length
  if (typeof c.sources === 'number') return c.sources
  if (typeof c.source_count === 'number') return c.source_count
  return '—'
}

export function lastStatement(c: Customer): string {
  const s = c.last_statement
  if (!s) return '—'
  if (typeof s === 'string') return s
  return `${day(s.period_start)}${s.status ? ` (${s.status})` : ''}`
}

export function Customers() {
  const [rows, setRows] = useState<Customer[]>([])
  const [error, setError] = useState('')
  const [invite, setInvite] = useState<{ customer: Customer; issued: InviteIssued } | null>(null)

  const load = useCallback(async () => {
    try {
      setRows(asList<Customer>(await api.get<unknown>('/customers'), 'customers'))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const sendInvite = async (c: Customer) => {
    try {
      const issued = await api.post<InviteIssued>(`/customers/${c.id}/invite`)
      setInvite({ customer: c, issued })
    } catch (e) {
      setError(errorText(e))
    }
  }

  return (
    <div className="stack">
      <div className="row between">
        <h1>Customers</h1>
        <div className="row">
          <Link to="/customers/import">
            <button>Import CSV</button>
          </Link>
          <Link to="/customers/new">
            <button className="primary">New customer</button>
          </Link>
        </div>
      </div>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {invite ? (
        <Notice kind="ok">
          Invite for {invite.customer.name} sent to {invite.customer.admin_email} (expires {when(invite.issued.expires_at)}).
          <br />
          <span className="mono">{invite.issued.invite_url}</span>
        </Notice>
      ) : null}
      {rows.length === 0 ? (
        <Empty>No customers yet.</Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Billing</th>
                <th className="num">Sources</th>
                <th>Last collected</th>
                <th>Last statement</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.id}>
                  <td>
                    <Link to={`/customers/${c.id}`}>{c.name}</Link>
                    <div className="muted small mono">{c.slug}</div>
                  </td>
                  <td>
                    <Badge status={c.status} />
                  </td>
                  <td>{c.billing_mode}</td>
                  <td className="num">{sourceCount(c)}</td>
                  <td>{when(c.last_collected_at)}</td>
                  <td>{lastStatement(c)}</td>
                  <td>
                    <button className="link" onClick={() => void sendInvite(c)}>
                      Invite
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
