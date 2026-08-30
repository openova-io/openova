import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { AuditEntry, CostSource, Customer, CustomerUser, InviteIssued } from '../api/types'
import { Badge, Details, Empty, Field, Notice, Tabs } from '../components/ui'
import { day, when } from '../lib/format'
import { SourcesPanel } from '../panels/SourcesPanel'
import { StatementsPanel } from '../panels/StatementsPanel'
import { UsagePanel } from '../panels/UsagePanel'

const TABS = ['Sources', 'Usage', 'Statements', 'Users', 'Audit']

export function CustomerDetail() {
  const { id = '' } = useParams()
  const [params] = useSearchParams()
  const tab = (params.get('tab') ?? 'sources').toLowerCase()
  const [c, setC] = useState<Customer | null>(null)
  const [error, setError] = useState('')
  const [invite, setInvite] = useState<InviteIssued | null>(null)

  const load = useCallback(async () => {
    try {
      setC(await api.get<Customer>(`/customers/${id}`))
      setError('')
    } catch (e) {
      setError(errorText(e))
    }
  }, [id])

  useEffect(() => {
    void load()
  }, [load])

  if (error && !c) return <Notice kind="bad">{error}</Notice>
  if (!c) return <p className="muted">Loading…</p>

  const sources: CostSource[] = Array.isArray(c.sources) ? c.sources : []
  const users: CustomerUser[] = Array.isArray(c.users) ? c.users : []

  const setStatus = async (status: string) => {
    try {
      await api.patch(`/customers/${id}`, { status })
      await load()
    } catch (e) {
      setError(errorText(e))
    }
  }

  const sendInvite = async () => {
    try {
      setInvite(await api.post<InviteIssued>(`/customers/${id}/invite`))
    } catch (e) {
      setError(errorText(e))
    }
  }

  return (
    <div>
      <div className="row between">
        <div>
          <h1 style={{ marginBottom: 2 }}>{c.name}</h1>
          <div className="muted small">
            <span className="mono">{c.slug}</span> · {c.admin_email} · {c.billing_mode}
            {c.kind ? ` · ${c.kind}` : ''}
            {c.start_date ? ` · from ${day(c.start_date)}` : ''}
          </div>
        </div>
        <div className="row">
          <Badge status={c.status} />
          <button onClick={() => void sendInvite()}>Invite</button>
          {c.status === 'suspended' ? (
            <button onClick={() => void setStatus('active')}>Resume</button>
          ) : (
            <button className="danger" onClick={() => window.confirm(`Suspend ${c.name}?`) && void setStatus('suspended')}>
              Suspend
            </button>
          )}
        </div>
      </div>
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {invite ? (
        <Notice kind="ok">
          Invite sent to {c.admin_email} (expires {when(invite.expires_at)}). <span className="mono">{invite.invite_url}</span>
        </Notice>
      ) : null}

      <Tabs base={`/customers/${id}`} tabs={TABS} current={tab} />

      {tab === 'sources' ? (
        <SourcesPanel customerId={id} sources={sources} canManage canRotate onChanged={load} />
      ) : null}
      {tab === 'usage' ? <UsagePanel customerId={id} /> : null}
      {tab === 'statements' ? <StatementsPanel customerId={id} canIssue /> : null}
      {tab === 'users' ? <UsersTab customerId={id} users={users} onChanged={load} /> : null}
      {tab === 'audit' ? <AuditTab customerId={id} /> : null}
    </div>
  )
}

function UsersTab({
  customerId,
  users,
  onChanged,
}: {
  customerId: string
  users: CustomerUser[]
  onChanged: () => Promise<void>
}) {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<'admin' | 'viewer'>('viewer')
  const [error, setError] = useState('')

  const add = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    try {
      await api.post(`/customers/${customerId}/users`, { email: email.trim().toLowerCase(), role })
      setEmail('')
      await onChanged()
    } catch (err) {
      setError(errorText(err))
    }
  }
  const remove = async (u: CustomerUser) => {
    if (!window.confirm(`Remove ${u.email}?`)) return
    try {
      await api.del(`/customers/${customerId}/users/${encodeURIComponent(u.email)}`)
      await onChanged()
    } catch (err) {
      setError(errorText(err))
    }
  }
  return (
    <div className="stack">
      {error ? <Notice kind="bad">{error}</Notice> : null}
      {users.length === 0 ? (
        <Empty>No users beyond the admin email.</Empty>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Email</th>
              <th>Role</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.email}>
                <td>{u.email}</td>
                <td>{u.role}</td>
                <td>
                  <button className="link" onClick={() => void remove(u)}>
                    Remove
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <form className="card inline" onSubmit={add}>
        <Field label="Email">
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
        </Field>
        <Field label="Role">
          <select value={role} onChange={(e) => setRole(e.target.value as 'admin' | 'viewer')}>
            <option value="viewer">viewer</option>
            <option value="admin">admin</option>
          </select>
        </Field>
        <button className="primary">Add user</button>
      </form>
    </div>
  )
}

function AuditTab({ customerId }: { customerId: string }) {
  const [rows, setRows] = useState<AuditEntry[]>([])
  const [error, setError] = useState('')
  useEffect(() => {
    api
      .get<unknown>(`/customers/${customerId}/audit`)
      .then((r) => setRows(asList<AuditEntry>(r, 'audit', 'entries')))
      .catch((e) => setError(errorText(e)))
  }, [customerId])
  if (error) return <Notice kind="bad">{error}</Notice>
  if (rows.length === 0) return <Empty>No audit entries.</Empty>
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>At</th>
            <th>Actor</th>
            <th>Action</th>
            <th>Details</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a, i) => (
            <tr key={a.id ?? i}>
              <td>{when(a.at)}</td>
              <td>{a.actor}</td>
              <td className="mono">{a.action}</td>
              <td>
                <Details value={a.details} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
